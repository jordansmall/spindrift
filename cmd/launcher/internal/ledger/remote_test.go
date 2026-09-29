package ledger_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
)

// remoteHarness is the ledgertest.Harness for Remote: the "remote" is a bare
// repo shared by Backend and Rival, each of which is a Remote over its own
// scratch clone (t.TempDir()) pointing at that one bare repo's path. t is
// captured at construction so Backend/Rival (no *testing.T param, per the
// Harness interface) can still call t.TempDir()/t.Fatalf.
type remoteHarness struct {
	t      *testing.T
	remote string
}

func (h remoteHarness) newRemote() ledger.Remote {
	h.t.Helper()
	r, err := ledger.NewRemote(h.t.TempDir(), h.remote)
	if err != nil {
		h.t.Fatalf("NewRemote: %v", err)
	}
	return r
}

func (h remoteHarness) Backend() ledger.Backend { return h.newRemote() }
func (h remoteHarness) Rival() ledger.Backend   { return h.newRemote() }

func (h remoteHarness) Branches(t *testing.T) map[string]string {
	t.Helper()
	out, err := exec.Command("git", "-C", h.remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/").Output()
	if err != nil {
		t.Fatalf("for-each-ref refs/heads/: %v", err)
	}
	branches := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		branches[fields[0]] = fields[1]
	}
	return branches
}

func newRemoteHarness(t *testing.T) ledgertest.Harness {
	t.Helper()
	setGitIdentityEnv(t)
	return remoteHarness{t: t, remote: ledgertest.NewRepo(t)}
}

func TestRemoteLedgerContract(t *testing.T) {
	ledgertest.RunContract(t, newRemoteHarness)
}

// TestNewRemoteUnreachableNotLostRace asserts that NewRemote's own sync
// failing (the remote URL is unreachable) surfaces as a plain error, never
// ErrLostRace: NewRemote now owns the routine fetch, so an unreachable
// remote fails there rather than ever reaching Append's push.
func TestNewRemoteUnreachableNotLostRace(t *testing.T) {
	setGitIdentityEnv(t)
	scratch := t.TempDir()
	// A path with no repo at all: git fails to even connect.
	unreachable := t.TempDir() + "/does-not-exist.git"
	_, err := ledger.NewRemote(scratch, unreachable)
	if err == nil {
		t.Fatal("NewRemote against an unreachable remote: got nil error, want one")
	}
	if errors.Is(err, ledger.ErrLostRace) {
		t.Fatalf("NewRemote against an unreachable remote: got ErrLostRace, want a plain error: %v", err)
	}
}

// TestRemoteAppendAppliedDespitePushError asserts that a push the remote
// applies while the client still sees an error (e.g. the connection drops
// after receive-pack updates the ref) returns the new commit, not
// ErrLostRace: the remote now holds our own commit, so the claim won.
func TestRemoteAppendAppliedDespitePushError(t *testing.T) {
	setGitIdentityEnv(t)
	bare := ledgertest.NewRepo(t)

	// A git-receive-pack on --exec-path that runs the real one, so the ref
	// update lands, then exits non-zero so the client's push fails anyway.
	execDir := t.TempDir()
	script := "#!/bin/sh\ngit receive-pack \"$@\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(execDir, "git-receive-pack"), []byte(script), 0o755); err != nil {
		t.Fatalf("write git-receive-pack wrapper: %v", err)
	}

	r, err := ledger.NewRemote(t.TempDir(), bare, "--exec-path="+execDir)
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}

	got, err := r.Append("chore-applied", "", ledger.State{Phase: ledger.Claimed}, time.Now())
	if err != nil {
		t.Fatalf("Append whose push applied despite an error: %v", err)
	}
	out, err := exec.Command("git", "-C", bare, "rev-parse", ledger.RefPrefix+"chore-applied").Output()
	if err != nil {
		t.Fatalf("rev-parse remote Ledger ref: %v", err)
	}
	if tip := strings.TrimSpace(string(out)); tip != got {
		t.Fatalf("remote Ledger tip = %s, want Append's commit %s", tip, got)
	}
}

// TestRemoteRedactsCredentials asserts that NewRemote's own sync error never
// leaks a token carried as URL userinfo (the Forgejo tokened URL shape).
func TestRemoteRedactsCredentials(t *testing.T) {
	setGitIdentityEnv(t)
	scratch := t.TempDir()
	// Port 1 on loopback: nothing listens there, so git fails to connect
	// (not a DNS failure, which some environments intercept), and the
	// userinfo is included right in the connection error text if unredacted.
	tokened := "https://user:secret@127.0.0.1:1/x.git"
	_, err := ledger.NewRemote(scratch, tokened)
	if err == nil {
		t.Fatal("NewRemote against an unreachable remote: got nil error, want one")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("NewRemote error leaked the URL's credential: %v", err)
	}
}

// TestRemoteAppendPushFailureRedactsCredentials asserts that a push that
// fails for a real reason (a rejecting pre-receive hook, not a lost lease)
// still redacts the URL's credential in the returned error.
// TestRemoteRedactsCredentials above never reaches push at all: it fails at
// the earlier fetch, so it never exercises redaction on the push-error path.
func TestRemoteAppendPushFailureRedactsCredentials(t *testing.T) {
	setGitIdentityEnv(t)
	bare := ledgertest.NewRepo(t)
	tokened := "https://user:secret@example.invalid/x.git"

	// Reject every push, echoing the tokened URL to stderr (git relays a
	// hook's stderr as "remote: ..." lines) the way a real forge's error
	// page might, so the push failure's combined output carries the
	// credential unless Remote.Append redacts it.
	hook := filepath.Join(bare, "hooks", "pre-receive")
	script := "#!/bin/sh\necho \"remote: rejecting push to " + tokened + "\" >&2\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}

	scratch := t.TempDir()
	r, err := ledger.NewRemote(scratch, tokened, "-c", "url."+bare+".insteadOf="+tokened)
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}

	_, err = r.Append("chore-push-redact", "", ledger.State{Phase: ledger.Claimed}, time.Now())
	if err == nil {
		t.Fatal("Append against a rejecting pre-receive hook: got nil error, want one")
	}
	if errors.Is(err, ledger.ErrLostRace) {
		t.Fatalf("Append against a rejecting pre-receive hook: got ErrLostRace, want a plain error: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("Append error leaked the URL's credential: %v", err)
	}

	// The mirror committed before the push was rejected; that phantom commit
	// must not outlive the Append, or a later Read reports a claim the
	// remote never received.
	tip, err := r.Read("chore-push-redact")
	if err != nil {
		t.Fatalf("Read after rejected push: %v", err)
	}
	if tip.Commit != "" {
		t.Fatalf("Read after rejected push = %+v, want the remote's zero Tip", tip)
	}
}

// TestRemoteReadHistoryNoFetch asserts that the only routine fetch is
// NewRemote's own construction-time sync: Read and History afterwards serve
// from the mirror with no network round-trip at all.
func TestRemoteReadHistoryNoFetch(t *testing.T) {
	setGitIdentityEnv(t)
	bare := ledgertest.NewRepo(t)

	seed, err := ledger.NewRemote(t.TempDir(), bare)
	if err != nil {
		t.Fatalf("NewRemote (seed): %v", err)
	}
	if _, err := seed.Append("chore-no-refetch", "", ledger.State{Phase: ledger.Claimed}, time.Now()); err != nil {
		t.Fatalf("Append (seed): %v", err)
	}

	trace := filepath.Join(t.TempDir(), "trace2.log")
	t.Setenv("GIT_TRACE2_EVENT", trace)

	r, err := ledger.NewRemote(t.TempDir(), bare)
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	if got := ledgertest.CmdCount(t, trace, "fetch"); got != 1 {
		t.Fatalf("fetch count after NewRemote = %d, want exactly 1", got)
	}

	if _, err := r.Read("chore-no-refetch"); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := r.History("chore-no-refetch", time.Time{}); err != nil {
		t.Fatalf("History: %v", err)
	}
	if got := ledgertest.CmdCount(t, trace, "fetch"); got != 1 {
		t.Errorf("fetch count after Read/History = %d, want still 1 (no re-fetch)", got)
	}
}

// TestRemoteStaleMirrorInvisibleUntilRace asserts that an already-open
// Remote's mirror does not pick up a rival's push just because the rival
// pushed: only losing its own compare-and-swap against the remote triggers
// the mirror's next sync.
func TestRemoteStaleMirrorInvisibleUntilRace(t *testing.T) {
	setGitIdentityEnv(t)
	bare := ledgertest.NewRepo(t)

	r, err := ledger.NewRemote(t.TempDir(), bare)
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	rival, err := ledger.NewRemote(t.TempDir(), bare)
	if err != nil {
		t.Fatalf("NewRemote (rival): %v", err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rivalTip, err := ledger.Claim(rival, "chore-stale-mirror", ledger.Tip{}, ledger.ClaimedBy{Host: "rival", Start: start})
	if err != nil {
		t.Fatalf("Claim (rival): %v", err)
	}

	// r's mirror was synced (empty) before rival's push landed, so it still
	// sees an empty tip.
	tip, err := r.Read("chore-stale-mirror")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.Commit != "" {
		t.Fatalf("Read before losing a race = %q, want \"\" (mirror unchanged by rival's push)", tip.Commit)
	}

	// r's own Claim, built against its stale (empty) tip, loses the race;
	// only that failure resyncs the mirror. A distinct ClaimedBy.Host from
	// rival's keeps the two commits from coincidentally hashing the same.
	_, err = ledger.Claim(r, "chore-stale-mirror", ledger.Tip{}, ledger.ClaimedBy{Host: "loser", Start: start})
	if !errors.Is(err, ledger.ErrLostRace) {
		t.Fatalf("Claim against a stale mirror: got err %v, want errors.Is(err, ledger.ErrLostRace)", err)
	}

	tip, err = r.Read("chore-stale-mirror")
	if err != nil {
		t.Fatalf("Read after losing the race: %v", err)
	}
	if tip.Commit != rivalTip.Commit {
		t.Fatalf("Read after losing the race = %q, want the rival's commit %q (lost race resynced the mirror)", tip.Commit, rivalTip.Commit)
	}
}
