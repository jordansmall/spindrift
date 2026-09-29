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
	return remoteHarness{t: t, remote: newBareRepo(t)}
}

func TestRemoteLedgerContract(t *testing.T) {
	ledgertest.RunContract(t, newRemoteHarness)
}

// TestRemotePushFailureNotLostRace asserts that a push failing for a reason
// other than the lease losing (the remote URL is unreachable) surfaces as a
// plain error, never ErrLostRace: the discrimination in Remote.Append must
// query the remote's actual ref value, not just infer a lost race from any
// push failure.
func TestRemotePushFailureNotLostRace(t *testing.T) {
	setGitIdentityEnv(t)
	scratch := t.TempDir()
	// A path with no repo at all: git fails to even connect, well before it
	// could evaluate the lease against the ref's current value.
	unreachable := t.TempDir() + "/does-not-exist.git"
	r, err := ledger.NewRemote(scratch, unreachable)
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}

	_, err = r.Append("chore-unreachable", "", ledger.State{Phase: ledger.Claimed}, time.Now())
	if err == nil {
		t.Fatal("Append against an unreachable remote: got nil error, want one")
	}
	if errors.Is(err, ledger.ErrLostRace) {
		t.Fatalf("Append against an unreachable remote: got ErrLostRace, want a plain error: %v", err)
	}
}

// TestRemoteAppendAppliedDespitePushError asserts that a push the remote
// applies while the client still sees an error (e.g. the connection drops
// after receive-pack updates the ref) returns the new commit, not
// ErrLostRace: the remote now holds our own commit, so the claim won.
func TestRemoteAppendAppliedDespitePushError(t *testing.T) {
	setGitIdentityEnv(t)
	bare := newBareRepo(t)

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

// TestRemoteRedactsCredentials asserts that a Remote error never leaks a
// token carried as URL userinfo (the Forgejo tokened URL shape).
func TestRemoteRedactsCredentials(t *testing.T) {
	setGitIdentityEnv(t)
	scratch := t.TempDir()
	// Port 1 on loopback: nothing listens there, so git fails to connect
	// (not a DNS failure, which some environments intercept), and the
	// userinfo is included right in the connection error text if unredacted.
	tokened := "https://user:secret@127.0.0.1:1/x.git"
	r, err := ledger.NewRemote(scratch, tokened)
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}

	_, err = r.Append("chore-redact", "", ledger.State{Phase: ledger.Claimed}, time.Now())
	if err == nil {
		t.Fatal("Append against an unreachable remote: got nil error, want one")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("Append error leaked the URL's credential: %v", err)
	}
}

// TestRemoteAppendPushFailureRedactsCredentials asserts that a push that
// fails for a real reason (a rejecting pre-receive hook, not a lost lease)
// still redacts the URL's credential in the returned error.
// TestRemoteRedactsCredentials above never reaches push at all: it fails at
// the earlier fetch, so it never exercises redaction on the push-error path.
func TestRemoteAppendPushFailureRedactsCredentials(t *testing.T) {
	setGitIdentityEnv(t)
	bare := newBareRepo(t)
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
}

// TestRemoteSnapshotDoesNotReFetch asserts that ledger.Snapshot syncs a
// Remote exactly once: reads through the returned Reader keep seeing the tip
// as of Snapshot even after a rival Append moves the remote ref, while a
// fresh Remote.Read (which syncs per call) sees the new tip.
func TestRemoteSnapshotDoesNotReFetch(t *testing.T) {
	setGitIdentityEnv(t)
	bare := newBareRepo(t)

	r, err := ledger.NewRemote(t.TempDir(), bare)
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	oldCommit, err := r.Append("chore-snapshot", "", ledger.State{Phase: ledger.Claimed}, time.Now())
	if err != nil {
		t.Fatalf("Append (seed old tip): %v", err)
	}

	snap, err := ledger.Snapshot(r)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// A rival Remote over its own scratch clone moves the remote ref behind
	// the snapshot.
	rival, err := ledger.NewRemote(t.TempDir(), bare)
	if err != nil {
		t.Fatalf("NewRemote (rival): %v", err)
	}
	newCommit, err := rival.Append("chore-snapshot", oldCommit, ledger.State{Phase: ledger.Done}, time.Now())
	if err != nil {
		t.Fatalf("Append (rival, advance remote): %v", err)
	}

	tip, err := snap.Read("chore-snapshot")
	if err != nil {
		t.Fatalf("snapshot.Read: %v", err)
	}
	if tip.Commit != oldCommit {
		t.Fatalf("snapshot.Read after remote advanced = %s, want the pre-Snapshot tip %s (no re-fetch)", tip.Commit, oldCommit)
	}

	entries, err := snap.History("chore-snapshot", time.Time{})
	if err != nil {
		t.Fatalf("snapshot.History: %v", err)
	}
	if len(entries) != 1 || entries[0].Commit != oldCommit {
		t.Fatalf("snapshot.History after remote advanced = %+v, want only the pre-Snapshot tip %s", entries, oldCommit)
	}

	freshTip, err := r.Read("chore-snapshot")
	if err != nil {
		t.Fatalf("r.Read (fresh, syncs per call): %v", err)
	}
	if freshTip.Commit != newCommit {
		t.Fatalf("r.Read after remote advanced = %s, want the new tip %s", freshTip.Commit, newCommit)
	}
}
