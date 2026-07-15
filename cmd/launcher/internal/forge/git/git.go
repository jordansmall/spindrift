package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/gitplumbing"
)

// gitClient is the push-only Code Forge adapter for a plain git remote
// (self-hosted git, gitea, GitLab-without-MRs, a bare server repo). It has no
// PR or CI concept — it implements forge.CodeForge only, never PRForge — and
// Merge/Rebase land code by pushing directly to the remote instead of
// merging a pull request.
type gitClient struct {
	remoteURL    string
	baseBranch   string
	userName     string
	userEmail    string
	branchPrefix string
}

// NewGitClient returns a forge.CodeForge backed by a plain git remote URL.
// baseBranch is the target branch Merge pushes onto for MERGE_MODE=immediate.
// userName/userEmail configure the commit identity on Merge's throwaway
// clone (a merge commit needs a committer) instead of depending on ambient
// host git config, which may be unset on a bare CI runner. branchPrefix is
// baked into AgentBranch's output.
func NewGitClient(remoteURL, baseBranch, userName, userEmail, branchPrefix string) forge.CodeForge {
	return &gitClient{
		remoteURL:    remoteURL,
		baseBranch:   baseBranch,
		userName:     userName,
		userEmail:    userEmail,
		branchPrefix: branchPrefix,
	}
}

// AgentBranch returns branchPrefix + num.
func (g *gitClient) AgentBranch(num string) string {
	return g.branchPrefix + num
}

// validateGitRef rejects a ref that git would parse as an option rather than
// a ref (anything starting with "-"). branch/pr values passed to Merge and
// Rebase originate from the Box's SPINDRIFT_OUTCOME line, which is untrusted
// input (comment-injection trust boundary, CLAUDE.md) — without this check a
// crafted value like "--upload-pack=<cmd>" would run arbitrary commands on
// the launcher host via `git fetch`/`git checkout`.
func validateGitRef(ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid git ref %q", ref)
	}
	return nil
}

// cloneToTemp clones remoteURL into a fresh temp directory named per prefix
// and returns a helper that runs git -C <dir> <args...>, plus a cleanup func
// the caller must defer. Shared scaffold for Merge and Rebase.
func cloneToTemp(remoteURL, prefix string) (dir string, gitIn func(args ...string) *exec.Cmd, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", prefix)
	if err != nil {
		return "", nil, nil, fmt.Errorf("mkdtemp: %w", err)
	}
	cleanup = func() { os.RemoveAll(dir) }
	if err := exec.Command("git", "clone", remoteURL, dir).Run(); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("git clone %s: %w", remoteURL, err)
	}
	gitIn = func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}
	return dir, gitIn, cleanup, nil
}

// setCommitIdentity configures the launcher-supplied commit identity on a
// throwaway clone so Merge/Rebase don't depend on ambient host git config,
// which may be unset on a bare CI runner.
func (g *gitClient) setCommitIdentity(gitIn func(args ...string) *exec.Cmd) error {
	if err := gitIn("config", "user.name", g.userName).Run(); err != nil {
		return fmt.Errorf("git config user.name: %w", err)
	}
	if err := gitIn("config", "user.email", g.userEmail).Run(); err != nil {
		return fmt.Errorf("git config user.email: %w", err)
	}
	return nil
}

// Merge lands branch onto baseBranch by cloning the remote, merging branch in,
// and pushing the result — the MERGE_MODE=immediate mapping for a push-only
// forge. Returns forge.ErrMergeConflict when the merge cannot be completed
// automatically, so callers can retry via Rebase exactly as they do for the
// github adapter.
func (g *gitClient) Merge(branch string) error {
	if err := validateGitRef(branch); err != nil {
		return err
	}
	_, gitIn, cleanup, err := cloneToTemp(g.remoteURL, "spindrift-git-forge-merge-*")
	if err != nil {
		return err
	}
	defer cleanup()

	if err := g.setCommitIdentity(gitIn); err != nil {
		return err
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
		if gitplumbing.IsMergeConflict(out.String()) {
			return forge.ErrMergeConflict
		}
		return fmt.Errorf("git merge %s: %w: %s", branch, err, strings.TrimSpace(out.String()))
	}
	if err := gitIn("push", "origin", "HEAD:"+g.baseBranch).Run(); err != nil {
		return fmt.Errorf("git push origin HEAD:%s: %w", g.baseBranch, err)
	}
	return nil
}

// Rebase rebases branch onto baseBranch and force-pushes it back to the
// remote. Returns forge.ErrMergeConflict when the rebase cannot be completed
// automatically.
func (g *gitClient) Rebase(branch string) error {
	if err := validateGitRef(branch); err != nil {
		return err
	}
	dir, gitIn, cleanup, err := cloneToTemp(g.remoteURL, "spindrift-git-forge-rebase-*")
	if err != nil {
		return err
	}
	defer cleanup()

	if err := g.setCommitIdentity(gitIn); err != nil {
		return err
	}
	if err := gitIn("checkout", branch).Run(); err != nil {
		return fmt.Errorf("git checkout %s: %w", branch, err)
	}
	if err := gitIn("rebase", "origin/"+g.baseBranch).Run(); err != nil {
		_ = gitIn("rebase", "--abort").Run()
		return forge.ErrMergeConflict
	}
	return gitplumbing.GitForcePush(dir)
}

// Probe checks that the configured remote is reachable.
func (g *gitClient) Probe() (string, error) {
	if err := exec.Command("git", "ls-remote", g.remoteURL).Run(); err != nil {
		return "", fmt.Errorf("%w: %s", forge.ErrRepoNotFound, g.remoteURL)
	}
	return g.remoteURL, nil
}
