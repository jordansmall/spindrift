package local

import (
	"fmt"
	"os"
	"os/exec"
)

// SeedAccumulationRepo creates the bare Accumulation repo at repoPath if it
// doesn't already exist, then updates its baseBranch ref to match pwd's
// local checkout — entirely host-side (a filesystem push into repoPath), no
// forge/tracker network call (ADR 0033). Re-running is idempotent: an
// existing repo is not recreated, and only baseBranch's ref is written, so
// agent branches and Integration branches already in repoPath are left
// untouched. pwd needs no configured remote — its own baseBranch ref is the
// only source SeedAccumulationRepo reads from.
//
// repoPath must be absolute: it's resolved both directly (Stat, init
// --bare) and as the push destination from within pwd, so a relative value
// would resolve against two different working directories.
//
// Unlike forge/git's adapter, these git subprocesses run with no
// context/timeout: every path here is a local filesystem operation (init,
// and a push between two paths on disk), never a network remote, so there
// is no hung-connection failure mode to bound.
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

	// git init --bare points HEAD at init.defaultBranch, which need not
	// match baseBranch; a later `git clone` of repoPath (issue #1697) would
	// check out nothing if HEAD referred to a branch that doesn't exist.
	headRef := "refs/heads/" + baseBranch
	if out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "HEAD", headRef).CombinedOutput(); err != nil {
		return fmt.Errorf("set %s HEAD to %s: %w: %s", repoPath, headRef, err, out)
	}
	return nil
}
