package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// RefPrefix is prepended to a Chore's name to get its Ledger ref, e.g.
// "refs/spindrift/butler/sweep-issues".
const RefPrefix = "refs/spindrift/butler/"

// stateFile is the path, within a Ledger commit's tree, of the one blob that
// holds its State.
const stateFile = "state.json"

// Local implements Backend directly against a bare Accumulation repo on the
// local forge (ADR 0056): the Ledger ref lives alongside refs/heads/ in the
// same repo, but on its own refs/spindrift/butler/ namespace so it is never
// mistaken for a branch.
type Local struct {
	// Repo is the bare Accumulation repo's filesystem path.
	Repo string
}

// ref validates chore and returns its Ledger ref. Without the check, rev-parse
// would read a name like "x~1" as the parent of Ledger x's tip.
func (l Local) ref(chore string) (string, error) {
	ref := RefPrefix + chore
	if err := exec.Command("git", "check-ref-format", ref).Run(); err != nil {
		return "", fmt.Errorf("ledger: invalid chore name %q", chore)
	}
	return ref, nil
}

// outputErr wraps err from a git command run with Output(), keeping the
// stderr Output() captured on *exec.ExitError.
func outputErr(err error, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return fmt.Errorf("ledger: %s: %w: %s", msg, err, bytes.TrimSpace(exitErr.Stderr))
	}
	return fmt.Errorf("ledger: %s: %w", msg, err)
}

// resolve returns ref's commit sha, or ok == false if the ref does not exist
// yet.
func (l Local) resolve(ref string) (sha string, ok bool, err error) {
	out, err := exec.Command("git", "-C", l.Repo, "rev-parse", "-q", "--verify", ref+"^{commit}").Output()
	if err != nil {
		// rev-parse -q exits 1 with no output for a ref that does not
		// resolve; anything else (a real git failure) is unexpected here.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, outputErr(err, "resolve %s", ref)
	}
	return strings.TrimSpace(string(out)), true, nil
}

// state reads and decodes the state.json blob committed at sha.
func (l Local) state(sha string) (State, error) {
	blob, err := exec.Command("git", "-C", l.Repo, "cat-file", "blob", sha+":"+stateFile).Output()
	if err != nil {
		return State{}, outputErr(err, "read %s at %s", stateFile, sha)
	}
	var s State
	if err := json.Unmarshal(blob, &s); err != nil {
		return State{}, fmt.Errorf("ledger: decode %s at %s: %w", stateFile, sha, err)
	}
	return s, nil
}

// Read returns chore's current tip, or a zero Tip if its Ledger ref does not
// exist yet.
func (l Local) Read(chore string) (Tip, error) {
	ref, err := l.ref(chore)
	if err != nil {
		return Tip{}, err
	}
	sha, ok, err := l.resolve(ref)
	if err != nil {
		return Tip{}, err
	}
	if !ok {
		return Tip{}, nil
	}

	s, err := l.state(sha)
	if err != nil {
		return Tip{}, err
	}
	return Tip{Commit: sha, State: s}, nil
}

// History returns every state commit in chore's chain whose commit date is
// at or after since, newest first.
func (l Local) History(chore string, since time.Time) ([]Entry, error) {
	ref, err := l.ref(chore)
	if err != nil {
		return nil, err
	}
	if _, ok, err := l.resolve(ref); err != nil {
		return nil, err
	} else if !ok {
		return nil, nil
	}

	logOut, err := exec.Command("git", "-C", l.Repo, "log", "--format=%H %ct", ref).Output()
	if err != nil {
		return nil, outputErr(err, "log %s", ref)
	}

	var entries []Entry
	for _, line := range strings.Split(strings.TrimSpace(string(logOut)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sha, ctStr := fields[0], fields[1]
		ct, err := strconv.ParseInt(ctStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("ledger: parse commit date for %s: %w", sha, err)
		}
		// Never stop at the first commit older than since: clocks across
		// hosts may be non-monotonic, so an older entry can still sit ahead
		// of a newer one in the chain.
		if ct < since.Unix() {
			continue
		}

		s, err := l.state(sha)
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Commit: sha, At: time.Unix(ct, 0), State: s})
	}
	return entries, nil
}

// Append commits s as chore's new tip, parented on old (root commit if old ==
// ""), and moves the Ledger ref from old to the new commit only if it still
// equals old.
func (l Local) Append(chore, old string, s State, at time.Time) (string, error) {
	ref, err := l.ref(chore)
	if err != nil {
		return "", err
	}

	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", fmt.Errorf("ledger: marshal state: %w", err)
	}

	hashCmd := exec.Command("git", "-C", l.Repo, "hash-object", "-w", "--stdin")
	hashCmd.Stdin = bytes.NewReader(body)
	blobOut, err := hashCmd.Output()
	if err != nil {
		return "", outputErr(err, "hash-object")
	}
	blob := strings.TrimSpace(string(blobOut))

	mktreeCmd := exec.Command("git", "-C", l.Repo, "mktree")
	mktreeCmd.Stdin = strings.NewReader(fmt.Sprintf("100644 blob %s\t%s\n", blob, stateFile))
	treeOut, err := mktreeCmd.Output()
	if err != nil {
		return "", outputErr(err, "mktree")
	}
	tree := strings.TrimSpace(string(treeOut))

	commitArgs := []string{"-C", l.Repo, "commit-tree", tree}
	if old != "" {
		commitArgs = append(commitArgs, "-p", old)
	}
	commitArgs = append(commitArgs, "-m", fmt.Sprintf("%s: %s", chore, s.Phase))
	commitCmd := exec.Command("git", commitArgs...)
	commitCmd.Env = commitIdentityEnv(at)
	commitOut, err := commitCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ledger: commit-tree: %w: %s", err, commitOut)
	}
	newCommit := strings.TrimSpace(string(commitOut))

	updateOut, err := exec.Command("git", "-C", l.Repo, "update-ref", ref, newCommit, old).CombinedOutput()
	if err != nil {
		// The compare-and-swap lost only if the ref has since moved off old;
		// a failed re-read is a real error, never a lost race.
		cur, _, resolveErr := l.resolve(ref)
		if resolveErr != nil {
			return "", fmt.Errorf("ledger: update-ref %s: check current value: %w", ref, resolveErr)
		}
		if cur != old {
			return "", fmt.Errorf("%w: %s", ErrLostRace, ref)
		}
		return "", fmt.Errorf("ledger: update-ref %s: %w: %s", ref, err, updateOut)
	}
	return newCommit, nil
}

// commitIdentityEnv pins the commit's author/committer identity and date so
// a Ledger commit never depends on the host's ambient git config, and its
// timestamp reflects the caller's logical "at" rather than wall-clock time.
func commitIdentityEnv(at time.Time) []string {
	date := fmt.Sprintf("@%d +0000", at.Unix())
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=spindrift",
		"GIT_AUTHOR_EMAIL=spindrift@localhost",
		"GIT_COMMITTER_NAME=spindrift",
		"GIT_COMMITTER_EMAIL=spindrift@localhost",
		"GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_DATE="+date,
	)
}
