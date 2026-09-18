// Package github implements forge's IssueTracker, CodeForge, and PRForge
// interfaces by running the gh CLI. GH_TOKEN comes from the ambient
// environment.
package github

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/gitplumbing"
)

// ghCommandErrStderrCap caps the gh stderr folded into an error message, so a
// pathological failure cannot dump unbounded output into the logs that carry
// it.
const ghCommandErrStderrCap = 4096

// ghCommandErrText folds an already-captured stderr string into the error, for
// a call site that wired its own buffer to inspect gh's output before deciding
// on a failure. Wrap that failure here or with ghCommandErr, never both, or the
// same stderr text is reported twice.
func ghCommandErrText(description string, err error, stderr string) error {
	var base error
	if msg := strings.TrimSpace(stderr); msg != "" {
		if len(msg) > ghCommandErrStderrCap {
			cut := ghCommandErrStderrCap
			for cut > 0 && !utf8.RuneStart(msg[cut]) {
				cut--
			}
			msg = msg[:cut] + "...(truncated)"
		}
		base = fmt.Errorf("%s: %w: %s", description, err, msg)
	} else {
		base = fmt.Errorf("%s: %w", description, err)
	}
	if isRateLimited(stderr) {
		return fmt.Errorf("%w: %w", forge.ErrRateLimit, base)
	}
	return base
}

// ghCommandErr wraps an error from cmd.Output() with gh's own stderr
// diagnostic. It recovers that stderr from the *exec.ExitError, which
// cmd.Output fills in only when the caller left cmd.Stderr nil. Any other
// error, such as a missing gh binary, falls back to "description: err".
func ghCommandErr(description string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ghCommandErrText(description, err, string(exitErr.Stderr))
	}
	return fmt.Errorf("%s: %w", description, err)
}

// rateLimitMarkers is GitHub's fixed rate-limit vocabulary as it appears in
// gh's stderr.
var rateLimitMarkers = []string{
	"api rate limit exceeded",
	"already exceeded",
	"secondary rate limit",
	"abuse detection",
}

// isRateLimited reports whether gh's stderr names the primary hourly quota or
// the secondary/abuse-detection limit, as opposed to an auth, not-found, or
// network failure.
func isRateLimited(stderr string) bool {
	return gitplumbing.MatchesAnyMarker(stderr, rateLimitMarkers)
}

type execClient struct {
	repo          string // owner/repo slug
	labels        forge.DispatchLabels
	verdictLabels forge.VerdictLabels
	branchPrefix  string
	mergeMethod   string // "", "merge", "squash", or "rebase"; "" behaves as "rebase" (mergeMethodFlag)
	syncMethod    string // "", "rebase", or "merge"; "" behaves as "rebase"
}

// ExecOption sets one optional execClient field. NewExecClient and
// NewReadOnlyCodeForge apply these in order.
type ExecOption func(*execClient)

// WithVerdictLabels configures CompleteVerdict, the research dispatch kind's
// Complete transition. Work-kind call sites omit it.
func WithVerdictLabels(vl forge.VerdictLabels) ExecOption {
	return func(e *execClient) { e.verdictLabels = vl }
}

// WithMergeMethod sets the `gh pr merge` method ("merge", "squash", or
// "rebase") Merge and EnqueueAutoMerge use. Omitted, it stays on --rebase.
func WithMergeMethod(method string) ExecOption {
	return func(e *execClient) { e.mergeMethod = method }
}

// WithSyncMethod sets the git verb ("rebase" or "merge") Rebase uses to bring
// a PR branch up to date with its base. Omitted, it stays on rebase.
func WithSyncMethod(method string) ExecOption {
	return func(e *execClient) { e.syncMethod = method }
}

// NewExecClient returns the gh-exec adapter for the given repo slug. One
// instance implements IssueTracker, CodeForge, and PRForge, so a caller may
// use it for every seam it needs or construct one per seam. labels maps
// canonical DispatchState values to GitHub label names.
func NewExecClient(repo string, labels forge.DispatchLabels, branchPrefix string, opts ...ExecOption) *execClient {
	e := &execClient{repo: repo, labels: labels, branchPrefix: branchPrefix}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// AgentBranch returns branchPrefix + num.
func (e *execClient) AgentBranch(num string) string {
	return e.branchPrefix + num
}

// IsGithubTracker implements the optional forge.GithubTracker marker (#2341),
// which tells settle's ensureClosesReference that this adapter owns the GitHub
// Closes-keyword convention. Other trackers, such as forgejo, number their
// issues in a separate namespace, so the keyword does not carry over.
func (e *execClient) IsGithubTracker() bool { return true }
