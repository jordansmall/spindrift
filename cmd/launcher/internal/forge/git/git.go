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

// defaultCloneTimeout bounds cloneToTemp's git clone invocation when the
// caller doesn't override it via WithCloneTimeout. A hung remote (accepts
// the connection, never completes the handshake) would otherwise block
// cloneToTemp forever, since git itself applies no timeout of its own.
const defaultCloneTimeout = 5 * time.Minute

// defaultOpTimeout bounds every git subprocess gitClient runs after the
// initial clone (Probe's ls-remote, and Merge/Rebase's checkout, fetch,
// merge, and push) when the caller doesn't override it via WithOpTimeout.
// Without it, the same hung-remote failure mode defaultCloneTimeout guards
// against for cloneToTemp could block these calls forever too. Shares
// defaultCloneTimeout's value rather than a separate literal so the two
// don't silently drift apart; WithOpTimeout/WithCloneTimeout still let a
// caller diverge them deliberately.
const defaultOpTimeout = defaultCloneTimeout

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
	cloneTimeout time.Duration
	opTimeout    time.Duration
}

// Option configures optional gitClient behavior beyond NewGitClient's
// required parameters.
type Option func(*gitClient)

// WithCloneTimeout overrides defaultCloneTimeout, the deadline bounding
// cloneToTemp's git clone invocation. Mainly for tests exercising timeout
// behavior against a remote that hangs rather than fails fast.
func WithCloneTimeout(d time.Duration) Option {
	return func(g *gitClient) { g.cloneTimeout = d }
}

// WithOpTimeout overrides defaultOpTimeout, the deadline bounding each git
// subprocess gitClient runs after the initial clone (Probe's ls-remote, and
// Merge/Rebase's checkout, fetch, merge, and push). The deadline applies
// per subprocess, not to the whole Merge/Rebase call — a sequence of several
// calls can take a small multiple of it in the worst case. Mainly for tests
// exercising timeout behavior against a remote that hangs rather than fails
// fast.
func WithOpTimeout(d time.Duration) Option {
	return func(g *gitClient) { g.opTimeout = d }
}

// NewGitClient returns a forge.CodeForge backed by a plain git remote URL.
// baseBranch is the target branch Merge pushes onto for MERGE_MODE=immediate.
// userName/userEmail configure the commit identity on Merge's throwaway
// clone (a merge commit needs a committer) instead of depending on ambient
// host git config, which may be unset on a bare CI runner. branchPrefix is
// baked into AgentBranch's output.
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
// the caller must defer. Shared scaffold for Merge and Rebase. The clone is
// bounded by timeout so a remote that hangs mid-handshake fails instead of
// blocking cloneToTemp forever.
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

// runGit runs `git <args...>` against gitIn's clone, bounded by g.opTimeout,
// and reports a timeout distinctly from any other git failure — the same
// hung-remote failure mode cloneToTemp already guards the clone itself
// against.
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

// setCommitIdentity configures the launcher-supplied commit identity on a
// throwaway clone so Merge/Rebase don't depend on ambient host git config,
// which may be unset on a bare CI runner.
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
// and pushing the result — the MERGE_MODE=immediate mapping for a push-only
// forge. Returns forge.ErrMergeConflict when the merge cannot be completed
// automatically, so callers can retry via Rebase exactly as they do for the
// github adapter.
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

// Rebase rebases branch onto baseBranch and force-pushes it back to the
// remote. Returns forge.ErrMergeConflict when the rebase cannot be completed
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
