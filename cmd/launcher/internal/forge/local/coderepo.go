package local

import (
	"fmt"
	"os"
	"os/exec"
)

// SeedAccumulationRepo creates the bare Accumulation repo at repoPath if it is
// missing, then pushes pwd's baseBranch ref into it, host-side with no forge or
// tracker call (ADR 0033). Re-running leaves the agent and Integration branches
// in repoPath untouched. repoPath must be absolute: the push runs from inside
// pwd, so a relative path would resolve against two working directories.
func SeedAccumulationRepo(repoPath, pwd, baseBranch string) error {
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		if out, err := exec.Command("git", "init", "--bare", repoPath).CombinedOutput(); err != nil {
			return fmt.Errorf("init bare accumulation repo %s: %w: %s", repoPath, err, out)
		}
	} else if err != nil {
		return fmt.Errorf("stat accumulation repo %s: %w", repoPath, err)
	}

	refspec := fmt.Sprintf("+refs/heads/%s:refs/heads/%s", baseBranch, baseBranch)
	if out, err := exec.Command("git", "-C", pwd, "push", repoPath, refspec).CombinedOutput(); err != nil {
		return fmt.Errorf("seed %s base %s from %s: %w: %s", repoPath, baseBranch, pwd, err, out)
	}

	// git init --bare points HEAD at init.defaultBranch, which need not match
	// baseBranch; a later clone of repoPath (issue #1697) would check out
	// nothing if HEAD referred to a branch that doesn't exist.
	headRef := "refs/heads/" + baseBranch
	if out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "HEAD", headRef).CombinedOutput(); err != nil {
		return fmt.Errorf("set %s HEAD to %s: %w: %s", repoPath, headRef, err, out)
	}
	return nil
}
