package ledger

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitexec"
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
	// tests, https://<GH_HOST or github.com>/<slug>.git or the Forgejo clone
	// URL in production. Callers pass it credential-free (cred authenticates);
	// errors still redact it as defence in depth.
	url string
	// cred authenticates every command that talks to url, as an env-only
	// credential helper: the secret is in no argv, URL, or persisted config.
	cred gitexec.Credential
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
// bare if needed, then syncs its mirror from url: the run's one routine fetch.
// cred (zero for none) authenticates every git call against url.
func NewRemote(scratch, url string, cred gitexec.Credential, gitArgs ...string) (Remote, error) {
	if err := exec.Command("git", "-C", scratch, "rev-parse", "--is-bare-repository").Run(); err != nil {
		if out, err := exec.Command("git", "init", "--bare", "-q", scratch).CombinedOutput(); err != nil {
			return Remote{}, fmt.Errorf("ledger: init scratch repo %s: %w: %s", scratch, err, out)
		}
		// A detached `git gc --auto` or maintenance run racing a later
		// cleanup of scratch is a spurious failure. Persist each knob in the
		// repo, since later commands here carry no -c guard.
		for _, kv := range gitexec.NoAutoMaintenanceConfig() {
			if out, err := exec.Command("git", "-C", scratch, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
				return Remote{}, fmt.Errorf("ledger: set %s in scratch repo %s: %w: %s", kv[0], scratch, err, out)
			}
		}
	}
	r := Remote{repo: scratch, url: url, cred: cred, gitArgs: gitArgs, mirror: Local{Repo: scratch}}
	if err := r.sync(); err != nil {
		return Remote{}, err
	}
	return r, nil
}

// sync fetches every Ledger ref from url into the scratch repo, pruning any
// gone on the remote; a ref never pushed stays absent, which Local's Read
// already treats as a zero Tip. Called only from NewRemote and from Append
// after a lost race or failed push, never routinely.
func (r Remote) sync() error {
	out, err := r.cred.Apply(gitexec.Cmd(r.gitArgs, "-C", r.repo, "fetch", "--prune", "-q", r.url, butlerRefspec)).CombinedOutput()
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
	out, err := r.cred.Apply(gitexec.Cmd(r.gitArgs, "ls-remote", r.url, ref)).Output()
	if err != nil {
		return "", fmt.Errorf("ledger: ls-remote %s %s: %w", forge.RedactURLCredentials(r.url), ref, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// Append builds the state commit on the mirror and, if that wins, pushes it
// under a --force-with-lease keyed on old, so the compare-and-swap that counts
// happens on the remote. After a lost race or failed push it re-syncs the
// mirror so a later Read sees where the remote stands; the "one refs fetch
// per run" (issue #3995) thus holds only for a race-free run. If that re-sync
// fails, the phantom commit mirror.Append wrote stays in the mirror until the
// next Append loses its lease and repairs it.
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
	pushOut, err := r.cred.Apply(gitexec.Cmd(r.gitArgs, "-C", r.repo, "push", "-q", r.url, lease, refspec)).CombinedOutput()
	if err != nil {
		// The remote can hold newCommit despite a push error (the connection
		// dropped after the server applied it): that is our win. The lease lost
		// only if the ref moved off old (absent counts as "") to something
		// else; any other failure, including a failed re-query, is a real
		// error.
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
