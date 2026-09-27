package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// RefPrefix is prepended to a Chore's name to get its Ledger ref, e.g.
// "refs/spindrift/butler/sweep-issues".
const RefPrefix = "refs/spindrift/butler/"

// Local implements Backend directly against a bare Accumulation repo on the
// local forge (ADR 0056): the Ledger ref lives alongside refs/heads/ in the
// same repo, but on its own refs/spindrift/butler/ namespace so it is never
// mistaken for a branch.
type Local struct {
	// Repo is the bare Accumulation repo's filesystem path.
	Repo string
}

// Read returns chore's current tip, or a zero Tip if its Ledger ref does not
// exist yet.
func (l Local) Read(chore string) (Tip, error) {
	ref := RefPrefix + chore
	out, err := exec.Command("git", "-C", l.Repo, "rev-parse", "-q", "--verify", ref+"^{commit}").Output()
	if err != nil {
		// rev-parse -q exits 1 with no output for a ref that does not
		// resolve; anything else (a real git failure) is unexpected here.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return Tip{}, nil
		}
		return Tip{}, fmt.Errorf("ledger: resolve %s: %w", ref, err)
	}
	sha := strings.TrimSpace(string(out))

	blob, err := exec.Command("git", "-C", l.Repo, "cat-file", "blob", sha+":state.json").Output()
	if err != nil {
		return Tip{}, fmt.Errorf("ledger: read state.json at %s: %w", sha, err)
	}
	var s State
	if err := json.Unmarshal(blob, &s); err != nil {
		return Tip{}, fmt.Errorf("ledger: decode state.json at %s: %w", sha, err)
	}
	return Tip{Commit: sha, State: s}, nil
}

// Append commits s as chore's new tip, parented on old (root commit if old ==
// ""), and moves the Ledger ref from old to the new commit only if it still
// equals old.
func (l Local) Append(chore, old string, s State, at time.Time) (string, error) {
	ref := RefPrefix + chore

	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", fmt.Errorf("ledger: marshal state: %w", err)
	}

	hashCmd := exec.Command("git", "-C", l.Repo, "hash-object", "-w", "--stdin")
	hashCmd.Stdin = bytes.NewReader(body)
	blobOut, err := hashCmd.Output()
	if err != nil {
		return "", fmt.Errorf("ledger: hash-object: %w", err)
	}
	blob := strings.TrimSpace(string(blobOut))

	mktreeCmd := exec.Command("git", "-C", l.Repo, "mktree")
	mktreeCmd.Stdin = strings.NewReader(fmt.Sprintf("100644 blob %s\tstate.json\n", blob))
	treeOut, err := mktreeCmd.Output()
	if err != nil {
		return "", fmt.Errorf("ledger: mktree: %w", err)
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
		// re-read it to tell that apart from any other update-ref failure.
		cur, readErr := exec.Command("git", "-C", l.Repo, "rev-parse", "-q", "--verify", ref).Output()
		curSHA := strings.TrimSpace(string(cur))
		if readErr != nil {
			curSHA = ""
		}
		if curSHA != old {
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
