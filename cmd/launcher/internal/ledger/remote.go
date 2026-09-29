package ledger

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// Remote implements Backend against a hosted forge's Ledger refs. It keeps a
// host-side scratch bare repo as a mirror (a Local, used only through the
// Backend interface), fetched from the remote once in NewRemote: Read and
// History serve from the mirror, and Append pushes each commit under a
// lease, so a mirror gone stale since the fetch just loses the race.
type Remote struct {
	// repo is the scratch bare repo's filesystem path.
	repo string
	// url is the remote to fetch from and push to: a temp bare repo path in
	// tests, https://<GH_HOST or github.com>/<slug>.git or a Forgejo tokened
	// URL in production. It may carry a token as userinfo, so it must never
	// reach an error string unredacted.
	url string
	// gitArgs are extra leading git args applied to every command that talks
	// to url (e.g. -c credential.helper=... -c credential.helper=!gh auth
	// git-credential), before the subcommand.
	gitArgs []string
	mirror  Backend
}

var _ Backend = Remote{}

// butlerRefspec is the fetch refspec that mirrors every Ledger ref (and only
// Ledger refs) from url into the scratch repo; sync fetches it with --prune
// so a ref gone on the remote is dropped locally.
const butlerRefspec = "+" + RefPrefix + "*:" + RefPrefix + "*"

// NewRemote returns a Remote whose scratch repo is scratch, git-init'ing it
// bare if it is not already a repo, then syncing its mirror from url once --
// the routine fetch, so nothing later needs to repeat it.
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
	r := Remote{repo: scratch, url: url, gitArgs: gitArgs, mirror: Local{Repo: scratch}}
	if err := r.sync(); err != nil {
		return Remote{}, err
	}
	return r, nil
}

// remoteCmd builds a git command against url: gitArgs first (leading
// options like -c credential.helper=...), then args. GIT_TERMINAL_PROMPT=0
// so a missing credential fails fast instead of hanging the caller.
func (r Remote) remoteCmd(args ...string) *exec.Cmd {
	full := append(append([]string{}, r.gitArgs...), args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}

// sync fetches every Ledger ref from url into the scratch repo, so the
// mirror picks up the remote's current state. A ref gone on the remote is
// pruned locally; a ref never on the remote is simply absent (Local's
// Read/History already treat that as a zero Tip). Called only from
// NewRemote and from Append after a lost race, never routinely.
func (r Remote) sync() error {
	out, err := r.remoteCmd("-C", r.repo, "fetch", "--prune", "-q", r.url, butlerRefspec).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ledger: fetch %s: %w: %s", forge.RedactURLCredentials(r.url), err, forge.RedactURLCredentials(string(out)))
	}
	return nil
}

// Read delegates to the mirror, synced once at construction: no fetch.
func (r Remote) Read(chore string) (Tip, error) { return r.mirror.Read(chore) }

// History delegates to the mirror, synced once at construction: no fetch.
func (r Remote) History(chore string, since time.Time) ([]Entry, error) {
	return r.mirror.History(chore, since)
}

// lsRemote queries url directly (no scratch repo involved) for ref's current
// value, "" if absent, so Append can tell a lost race from a real push error.
func (r Remote) lsRemote(ref string) (string, error) {
	out, err := r.remoteCmd("ls-remote", r.url, ref).Output()
	if err != nil {
		return "", fmt.Errorf("ledger: ls-remote %s %s: %w", forge.RedactURLCredentials(r.url), ref, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// Append builds the state commit on the mirror (mirror.Append's own
// compare-and-swap) and, if that wins, pushes it under a --force-with-lease
// keyed on old, so the compare-and-swap that counts happens on the remote.
// Whenever the mirror and the remote may now disagree -- a lost race on
// either side, or a failed push -- Append re-syncs the mirror before
// returning, so a later Read sees where the remote actually stands. The
// "one refs fetch per run" a caller may assume (issue #3995) holds only for
// a race-free run; a lost race or failed push costs an extra re-sync. If
// that re-sync itself fails (e.g. the network is down), the phantom commit
// mirror.Append already wrote stays in the mirror until the next Append,
// which loses its lease against the stale mirror and repairs it then.
func (r Remote) Append(chore, old string, s State, at time.Time) (string, error) {
	newCommit, err := r.mirror.Append(chore, old, s, at)
	if err != nil {
		if errors.Is(err, ErrLostRace) {
			_ = r.sync() // best effort; the lost race is the error that matters
		}
		return "", err
	}

	// mirror.Append already rejected an invalid chore name.
	ref := RefPrefix + chore
	lease := fmt.Sprintf("--force-with-lease=%s:%s", ref, old)
	refspec := fmt.Sprintf("%s:%s", newCommit, ref)
	pushOut, err := r.remoteCmd("-C", r.repo, "push", "-q", r.url, lease, refspec).CombinedOutput()
	if err != nil {
		// The lease lost only if the remote ref has since moved off old
		// (absent counts as "") to something other than our own newCommit;
		// the remote can end up at newCommit despite the client seeing a
		// push error (e.g. the connection drops after the server applies
		// the update), and that is our own win, not a lost race. Any other
		// push failure, including a failed re-query, is a real error.
		cur, lsErr := r.lsRemote(ref)
		if lsErr == nil && cur == newCommit {
			return newCommit, nil
		}
		// The mirror now holds newCommit, a commit the remote never got: put
		// it back in step with the remote before reporting.
		_ = r.sync()
		if lsErr != nil {
			return "", fmt.Errorf("ledger: push %s: check remote value: %w", ref, lsErr)
		}
		if cur != old {
			return "", fmt.Errorf("%w: %s", ErrLostRace, ref)
		}
		return "", fmt.Errorf("ledger: push %s: %w: %s", ref, err, forge.RedactURLCredentials(string(pushOut)))
	}
	return newCommit, nil
}
