package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/gitplumbing"
)

// defaultCloneTimeout bounds cloneToTemp's clone. A hung remote that accepts
// the connection but never completes the handshake would otherwise block
// forever, since git applies no timeout of its own.
const defaultCloneTimeout = 5 * time.Minute

// defaultOpTimeout bounds every git subprocess run after the initial clone,
// guarding against the same hung remote. It shares defaultCloneTimeout's value
// so the two don't drift apart; WithOpTimeout and WithCloneTimeout let a caller
// diverge them deliberately.
const defaultOpTimeout = defaultCloneTimeout

// gitClient is the push-only Code Forge adapter for a plain git remote
// (self-hosted git, gitea, GitLab-without-MRs, a bare server repo). It has no
// PR or CI concept, so it implements forge.CodeForge only, never PRForge, and
// Merge/Rebase land code by pushing directly to the remote.
type gitClient struct {
	remoteURL    string
	baseBranch   string
	userName     string
	userEmail    string
	branchPrefix string
	cloneTimeout time.Duration
	opTimeout    time.Duration
}

// Option configures optional gitClient behavior.
type Option func(*gitClient)

// WithCloneTimeout overrides defaultCloneTimeout.
func WithCloneTimeout(d time.Duration) Option {
	return func(g *gitClient) { g.cloneTimeout = d }
}

// WithOpTimeout overrides defaultOpTimeout. The deadline applies per
// subprocess, not to the whole Merge or Rebase call, so a sequence of several
// calls can take a small multiple of it in the worst case.
func WithOpTimeout(d time.Duration) Option {
	return func(g *gitClient) { g.opTimeout = d }
}

// NewGitClient returns a forge.CodeForge backed by a plain git remote URL.
// baseBranch is the target branch Merge pushes onto for MERGE_MODE=immediate.
// userName and userEmail set the commit identity on Merge's throwaway clone,
// because ambient host git config may be unset on a bare CI runner and a merge
// commit needs a committer.
func NewGitClient(remoteURL, baseBranch, userName, userEmail, branchPrefix string, opts ...Option) forge.CodeForge {
	g := &gitClient{
		remoteURL:    remoteURL,
		baseBranch:   baseBranch,
		userName:     userName,
		userEmail:    userEmail,
		branchPrefix: branchPrefix,
		cloneTimeout: defaultCloneTimeout,
		opTimeout:    defaultOpTimeout,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// AgentBranch returns branchPrefix + num.
func (g *gitClient) AgentBranch(num string) string {
	return g.branchPrefix + num
}

// validateGitRef rejects a ref git would parse as an option. Merge and Rebase
// take branch names from the Box's untrusted SPINDRIFT_OUTCOME line, so without
// this check a value like "--upload-pack=<cmd>" runs arbitrary commands on the
// launcher host via git fetch or git checkout.
func validateGitRef(ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid git ref %q", ref)
	}
	return nil
}

// cloneToTemp clones remoteURL into a fresh temp directory and returns a helper
// that runs git -C <dir> <args...>, plus a cleanup func the caller must defer.
func cloneToTemp(remoteURL, prefix string, timeout time.Duration) (dir string, gitIn func(ctx context.Context, args ...string) *exec.Cmd, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", prefix)
	if err != nil {
		return "", nil, nil, fmt.Errorf("mkdtemp: %w", err)
	}
	cleanup = func() { os.RemoveAll(dir) }

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "git", "clone", remoteURL, dir).Run(); err != nil {
		cleanup()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", nil, nil, fmt.Errorf("git clone %s: timed out after %s: %w", forge.RedactURLCredentials(remoteURL), timeout, ctx.Err())
		}
		return "", nil, nil, fmt.Errorf("git clone %s: %w", forge.RedactURLCredentials(remoteURL), err)
	}
	gitIn = func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	}
	return dir, gitIn, cleanup, nil
}

// runGit runs git against gitIn's clone, bounded by g.opTimeout, and reports a
// timeout distinctly from any other git failure.
func (g *gitClient) runGit(gitIn func(ctx context.Context, args ...string) *exec.Cmd, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), g.opTimeout)
	defer cancel()
	if err := gitIn(ctx, args...).Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("git %s: timed out after %s: %w", strings.Join(args, " "), g.opTimeout, ctx.Err())
		}
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// setCommitIdentity keeps Merge and Rebase off ambient host git config, which
// may be unset on a bare CI runner.
func (g *gitClient) setCommitIdentity(gitIn func(ctx context.Context, args ...string) *exec.Cmd) error {
	if err := g.runGit(gitIn, "config", "user.name", g.userName); err != nil {
		return err
	}
	if err := g.runGit(gitIn, "config", "user.email", g.userEmail); err != nil {
		return err
	}
	return nil
}

// Merge lands branch onto baseBranch by cloning the remote, merging branch in,
// and pushing the result. It returns forge.ErrMergeConflict when the merge
// cannot complete automatically, so callers retry via Rebase as they do for
// the github adapter.
func (g *gitClient) Merge(branch string) error {
	if err := validateGitRef(branch); err != nil {
		return err
	}
	_, gitIn, cleanup, err := cloneToTemp(g.remoteURL, "spindrift-git-forge-merge-*", g.cloneTimeout)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := g.setCommitIdentity(gitIn); err != nil {
		return err
	}
	if err := g.runGit(gitIn, "checkout", g.baseBranch); err != nil {
		return err
	}
	if err := g.runGit(gitIn, "fetch", "origin", branch); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.opTimeout)
	defer cancel()
	var out bytes.Buffer
	mergeCmd := gitIn(ctx, "merge", "--no-ff", "FETCH_HEAD")
	mergeCmd.Stdout = &out
	mergeCmd.Stderr = &out
	if err := mergeCmd.Run(); err != nil {
		_ = g.runGit(gitIn, "merge", "--abort")
		if gitplumbing.IsMergeConflict(out.String()) {
			return forge.ErrMergeConflict
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("git merge %s: timed out after %s: %w", branch, g.opTimeout, ctx.Err())
		}
		return fmt.Errorf("git merge %s: %w: %s", branch, err, forge.RedactURLCredentials(strings.TrimSpace(out.String())))
	}
	return g.runGit(gitIn, "push", "origin", "HEAD:"+g.baseBranch)
}

// Rebase rebases branch onto baseBranch and force-pushes it back to the remote.
// It returns forge.ErrMergeConflict when the rebase cannot complete
// automatically.
func (g *gitClient) Rebase(branch string) error {
	if err := validateGitRef(branch); err != nil {
		return err
	}
	dir, gitIn, cleanup, err := cloneToTemp(g.remoteURL, "spindrift-git-forge-rebase-*", g.cloneTimeout)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := g.setCommitIdentity(gitIn); err != nil {
		return err
	}
	if err := g.runGit(gitIn, "checkout", branch); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.opTimeout)
	defer cancel()
	if err := gitIn(ctx, "rebase", "origin/"+g.baseBranch).Run(); err != nil {
		_ = g.runGit(gitIn, "rebase", "--abort")
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("git rebase origin/%s: timed out after %s: %w", g.baseBranch, g.opTimeout, ctx.Err())
		}
		return forge.ErrMergeConflict
	}
	pushCtx, pushCancel := context.WithTimeout(context.Background(), g.opTimeout)
	defer pushCancel()
	return gitplumbing.GitForcePush(pushCtx, dir)
}

// BranchExists reports whether branch exists on the remote. Under
// `git ls-remote --exit-code`, exit code 2 means no matching ref rather than a
// failure; any other non-zero exit is a real error.
func (g *gitClient) BranchExists(branch string) (bool, error) {
	if err := validateGitRef(branch); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.opTimeout)
	defer cancel()
	err := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", "--heads", g.remoteURL, branch).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
		return false, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false, fmt.Errorf("git ls-remote %s: timed out after %s: %w", branch, g.opTimeout, ctx.Err())
	}
	return false, fmt.Errorf("git ls-remote %s: %w", branch, err)
}

// Probe checks that the configured remote is reachable.
func (g *gitClient) Probe() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), g.opTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "git", "ls-remote", g.remoteURL).Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%w: timed out after %s: %s", forge.ErrRepoNotFound, g.opTimeout, forge.RedactURLCredentials(g.remoteURL))
		}
		return "", fmt.Errorf("%w: %s", forge.ErrRepoNotFound, forge.RedactURLCredentials(g.remoteURL))
	}
	return g.remoteURL, nil
}
