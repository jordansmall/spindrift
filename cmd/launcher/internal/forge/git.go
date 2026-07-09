package forge

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// gitClient is the push-only Code Forge adapter for a plain git remote
// (self-hosted git, gitea, GitLab-without-MRs, a bare server repo). It has no
// PR or CI concept: OpenPRForBranch/PRForBranch always report "not found",
// and Merge/Rebase land code by pushing directly to the remote instead of
// merging a pull request.
type gitClient struct {
	remoteURL  string
	baseBranch string
}

// NewGitClient returns a CodeForge backed by a plain git remote URL.
// baseBranch is the target branch Merge pushes onto for MERGE_MODE=immediate.
func NewGitClient(remoteURL, baseBranch string) CodeForge {
	return &gitClient{remoteURL: remoteURL, baseBranch: baseBranch}
}

func (g *gitClient) OpenPRForBranch(branch string) (PR, bool, error) {
	return PR{}, false, nil
}

func (g *gitClient) PRForBranch(branch string) (string, bool, error) {
	return "", false, nil
}

func (g *gitClient) PRState(url string) (string, error) {
	return "", fmt.Errorf("PRState: not supported by the git Code Forge (push-only, no PR concept)")
}

func (g *gitClient) CheckState(url string) (RollupState, error) {
	return StateNone, nil
}

func (g *gitClient) ListPRFiles(url string) ([]string, error) {
	return nil, fmt.Errorf("ListPRFiles: not supported by the git Code Forge (MERGE_GUARD_PATHS applies to the github Code Forge only)")
}

// Merge lands branch onto baseBranch by cloning the remote, merging branch in,
// and pushing the result — the MERGE_MODE=immediate mapping for a push-only
// forge. Returns ErrMergeConflict when the merge cannot be completed
// automatically, so callers can retry via Rebase exactly as they do for the
// github adapter.
func (g *gitClient) Merge(branch string) error {
	dir, err := os.MkdirTemp("", "spindrift-git-forge-merge-*")
	if err != nil {
		return fmt.Errorf("mkdtemp: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := exec.Command("git", "clone", g.remoteURL, dir).Run(); err != nil {
		return fmt.Errorf("git clone %s: %w", g.remoteURL, err)
	}
	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}
	if err := gitIn("checkout", g.baseBranch).Run(); err != nil {
		return fmt.Errorf("git checkout %s: %w", g.baseBranch, err)
	}
	if err := gitIn("fetch", "origin", branch).Run(); err != nil {
		return fmt.Errorf("git fetch origin %s: %w", branch, err)
	}
	var out bytes.Buffer
	mergeCmd := gitIn("merge", "--no-ff", "FETCH_HEAD")
	mergeCmd.Stdout = &out
	mergeCmd.Stderr = &out
	if err := mergeCmd.Run(); err != nil {
		_ = gitIn("merge", "--abort").Run()
		if isMergeConflict(out.String()) {
			return ErrMergeConflict
		}
		return fmt.Errorf("git merge %s: %w: %s", branch, err, strings.TrimSpace(out.String()))
	}
	if err := gitIn("push", "origin", "HEAD:"+g.baseBranch).Run(); err != nil {
		return fmt.Errorf("git push origin HEAD:%s: %w", g.baseBranch, err)
	}
	return nil
}

// Rebase rebases branch onto baseBranch and force-pushes it back to the
// remote. Returns ErrMergeConflict when the rebase cannot be completed
// automatically.
func (g *gitClient) Rebase(branch string) error {
	dir, err := os.MkdirTemp("", "spindrift-git-forge-rebase-*")
	if err != nil {
		return fmt.Errorf("mkdtemp: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := exec.Command("git", "clone", g.remoteURL, dir).Run(); err != nil {
		return fmt.Errorf("git clone %s: %w", g.remoteURL, err)
	}
	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}
	if err := gitIn("checkout", branch).Run(); err != nil {
		return fmt.Errorf("git checkout %s: %w", branch, err)
	}
	if err := gitIn("rebase", "origin/"+g.baseBranch).Run(); err != nil {
		_ = gitIn("rebase", "--abort").Run()
		return ErrMergeConflict
	}
	return gitForcePush(dir)
}

func (g *gitClient) CanAutoMerge() (bool, error) {
	return false, nil
}

func (g *gitClient) EnqueueAutoMerge(branch string) error {
	return fmt.Errorf("EnqueueAutoMerge: not supported by the git Code Forge — MERGE_MODE=auto requires CODE_FORGE=github")
}

// Probe checks that the configured remote is reachable.
func (g *gitClient) Probe() (string, error) {
	if err := exec.Command("git", "ls-remote", g.remoteURL).Run(); err != nil {
		return "", fmt.Errorf("%w: %s", ErrRepoNotFound, g.remoteURL)
	}
	return g.remoteURL, nil
}
