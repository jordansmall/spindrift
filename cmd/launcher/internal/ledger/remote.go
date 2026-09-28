package ledger

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// Remote implements Backend against a hosted forge's Ledger ref by keeping a
// host-side scratch bare repo (the working clone the state commits are built
// in) in sync with a remote over push/fetch, rather than writing directly
// into a shared bare repo the way Local does. Read and History each sync
// before reading, so every call sees the remote's current state; Snapshot
// instead syncs once and hands back the scratch repo for a batch of reads.
type Remote struct {
	// Repo is the scratch bare repo's filesystem path. Read/History/ref/commit
	// delegate to a Local built from it via local() (never embedded: embedding
	// would promote Local.Append, a reachable path that writes the scratch ref
	// directly and never pushes).
	Repo string
	// URL is the remote to fetch from and push to: a temp bare repo path in
	// tests, https://<GH_HOST or github.com>/<slug>.git or a Forgejo tokened
	// URL in production. It may carry a token as userinfo, so it must never
	// reach an error string unredacted.
	URL string
	// GitArgs are extra leading git args applied to every command that talks
	// to URL (e.g. -c credential.helper=... -c credential.helper=!gh auth
	// git-credential), before the subcommand.
	GitArgs []string
}

var _ Backend = Remote{}

// butlerRefspec is the fetch refspec that mirrors every Ledger ref (and only
// Ledger refs) from URL into the scratch repo; sync fetches it with --prune
// so a ref gone on the remote is dropped locally.
const butlerRefspec = "+" + RefPrefix + "*:" + RefPrefix + "*"

// NewRemote returns a Remote whose scratch repo is scratch, git-init'ing it
// bare if it is not already a repo.
func NewRemote(scratch, url string, gitArgs ...string) (Remote, error) {
	if err := exec.Command("git", "-C", scratch, "rev-parse", "--is-bare-repository").Run(); err != nil {
		if out, err := exec.Command("git", "init", "--bare", "-q", scratch).CombinedOutput(); err != nil {
			return Remote{}, fmt.Errorf("ledger: init scratch repo %s: %w: %s", scratch, err, out)
		}
		// Same rationale as the bare repo test fixtures: a detached `git gc
		// --auto` racing a later cleanup of scratch is a spurious failure.
		if out, err := exec.Command("git", "-C", scratch, "config", "gc.auto", "0").CombinedOutput(); err != nil {
			return Remote{}, fmt.Errorf("ledger: disable gc.auto in scratch repo %s: %w: %s", scratch, err, out)
		}
	}
	return Remote{Repo: scratch, URL: url, GitArgs: gitArgs}, nil
}

// local returns the Local backend over r's scratch repo, for delegating
// Read/History/ref/commit once the scratch repo is synced from URL.
func (r Remote) local() Local { return Local{Repo: r.Repo} }

// remoteCmd builds a git command against URL: GitArgs first (leading
// options like -c credential.helper=...), then args. GIT_TERMINAL_PROMPT=0
// so a missing credential fails fast instead of hanging the caller.
func (r Remote) remoteCmd(args ...string) *exec.Cmd {
	full := append(append([]string{}, r.GitArgs...), args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}

// sync fetches every Ledger ref from URL into the scratch repo, so a
// subsequent Local read/history/CAS sees the remote's current state. A ref
// gone on the remote is pruned locally; a ref never on the remote is simply
// absent (Local.Read/History already treat that as a zero Tip).
func (r Remote) sync() error {
	out, err := r.remoteCmd("-C", r.Repo, "fetch", "--prune", "-q", r.URL, butlerRefspec).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ledger: fetch %s: %w: %s", forge.RedactURLCredentials(r.URL), err, forge.RedactURLCredentials(string(out)))
	}
	return nil
}

// Read syncs the scratch repo from URL, then delegates to Local.
func (r Remote) Read(chore string) (Tip, error) {
	if err := r.sync(); err != nil {
		return Tip{}, err
	}
	return r.local().Read(chore)
}

// History syncs the scratch repo from URL, then delegates to Local.
func (r Remote) History(chore string, since time.Time) ([]Entry, error) {
	if err := r.sync(); err != nil {
		return nil, err
	}
	return r.local().History(chore, since)
}

// lsRemote queries URL directly (no scratch repo involved) for ref's current
// value, "" if absent, so Append can tell a lost race from a real push error.
func (r Remote) lsRemote(ref string) (string, error) {
	out, err := r.remoteCmd("ls-remote", r.URL, ref).Output()
	if err != nil {
		return "", fmt.Errorf("ledger: ls-remote %s %s: %w", forge.RedactURLCredentials(r.URL), ref, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// Append syncs the scratch repo from URL, builds the state commit with the
// same plumbing Local.Append uses, and pushes it under a --force-with-lease
// keyed on old, so the compare-and-swap happens on the remote rather than a
// local ref. On a lost lease it returns ErrLostRace, exactly like Local.
func (r Remote) Append(chore, old string, s State, at time.Time) (string, error) {
	if err := r.sync(); err != nil {
		return "", err
	}
	l := r.local()
	ref, err := l.ref(chore)
	if err != nil {
		return "", err
	}

	newCommit, err := l.commit(chore, old, s, at)
	if err != nil {
		return "", err
	}

	lease := fmt.Sprintf("--force-with-lease=%s:%s", ref, old)
	refspec := fmt.Sprintf("%s:%s", newCommit, ref)
	pushOut, err := r.remoteCmd("-C", r.Repo, "push", "-q", r.URL, lease, refspec).CombinedOutput()
	if err != nil {
		// The lease lost only if the remote ref has since moved off old
		// (absent counts as "") to something other than our own newCommit;
		// the remote can end up at newCommit despite the client seeing a
		// push error (e.g. the connection drops after the server applies
		// the update), and that is our own win, not a lost race. Any other
		// push failure, including a failed re-query, is a real error.
		cur, lsErr := r.lsRemote(ref)
		if lsErr != nil {
			return "", fmt.Errorf("ledger: push %s: check remote value: %w", ref, lsErr)
		}
		if cur == newCommit {
			return newCommit, nil
		}
		if cur != old {
			return "", fmt.Errorf("%w: %s", ErrLostRace, ref)
		}
		return "", fmt.Errorf("ledger: push %s: %w: %s", ref, err, forge.RedactURLCredentials(string(pushOut)))
	}
	return newCommit, nil
}

// FetchBranch fetches branch's current head from URL into the scratch repo
// (shallow: the butler command only needs the tip tree, not history), so
// butler.Head/butler.TrackedFiles can be computed against a local repo.
func (r Remote) FetchBranch(branch string) error {
	ref := "refs/heads/" + branch
	out, err := r.remoteCmd("-C", r.Repo, "fetch", "--depth=1", "-q", r.URL, "+"+ref+":"+ref).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ledger: fetch branch %s from %s: %w: %s", branch, forge.RedactURLCredentials(r.URL), err, forge.RedactURLCredentials(string(out)))
	}
	return nil
}
