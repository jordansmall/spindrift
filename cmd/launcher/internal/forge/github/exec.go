// Package github is the gh-exec adapter: it satisfies the parent forge
// package's IssueTracker, CodeForge, and PRForge interfaces using the gh
// CLI. GH_TOKEN is read from the ambient environment; the repo slug and
// dispatch label mapping are fixed at construction time.
package github

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/forge"
)

// ghCommandErrStderrCap bounds how much of a failed gh invocation's captured
// stderr ghCommandErr folds into the returned error's message, so a
// pathological gh failure can't dump unbounded output into an error message
// (and, transitively, whatever logs it).
const ghCommandErrStderrCap = 4096

// ghCommandErrText is ghCommandErr's counterpart for a call site that wires
// cmd.Stderr to its own buffer (so it can classify the failure itself) instead
// of leaving it nil for cmd.Output to auto-populate *exec.ExitError.Stderr. It
// folds that already-captured text into the message, so the diagnostic reaches
// the caller exactly once — never both here and via a second ghCommandErr call
// on the same failure.
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

// ghCommandErr turns a failed gh invocation's error into one that also
// surfaces gh's own stderr diagnostic, when available. description names the
// operation (e.g. "gh issue list"); err is whatever `cmd.Output()` returned.
//
// exec.Cmd.Output populates (*exec.ExitError).Stderr whenever the command's
// Stderr field was left nil, so this needs no caller-wired buffer — it recovers
// the diagnostic from err via errors.As. When err isn't an *exec.ExitError
// (e.g. gh missing from PATH) or its Stderr is blank, this degrades to the
// plain "description: err" form, never a dangling ": " with nothing after it.
func ghCommandErr(description string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ghCommandErrText(description, err, string(exitErr.Stderr))
	}
	return fmt.Errorf("%s: %w", description, err)
}

// rateLimitMarkers is the fixed GitHub rate-limit vocabulary isRateLimited
// looks for in gh's stderr.
var rateLimitMarkers = []string{
	"api rate limit exceeded",
	"already exceeded",
	"secondary rate limit",
	"abuse detection",
}

// isRateLimited reports whether gh's stderr indicates GitHub is rate-limiting
// the caller — the primary hourly API quota or the secondary/abuse-detection
// limit — as opposed to an unrelated auth, not-found, or network failure.
func isRateLimited(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, marker := range rateLimitMarkers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// execClient is the gh-exec adapter.
type execClient struct {
	repo          string // owner/repo slug
	labels        forge.DispatchLabels
	verdictLabels forge.VerdictLabels
	branchPrefix  string
	mergeMethod   string // "", "merge", "squash", or "rebase"; "" behaves as "rebase" (mergeMethodFlag)
	syncMethod    string // "", "rebase", or "merge"; "" behaves as "rebase"
}

// ExecOption configures an optional, construction-site-specific field on
// execClient beyond the required positional arguments every call site shares.
// Both NewExecClient and NewReadOnlyCodeForge accept a variadic list of these,
// applied in order.
type ExecOption func(*execClient)

// WithVerdictLabels configures CompleteVerdict (the research dispatch kind's
// Complete transition); omitted for work-kind construction sites.
func WithVerdictLabels(vl forge.VerdictLabels) ExecOption {
	return func(e *execClient) { e.verdictLabels = vl }
}

// WithMergeMethod sets the native `gh pr merge` method ("merge", "squash", or
// "rebase") used by both Merge and EnqueueAutoMerge. Empty means --rebase.
func WithMergeMethod(method string) ExecOption {
	return func(e *execClient) { e.mergeMethod = method }
}

// WithSyncMethod sets the git verb ("rebase" or "merge") Rebase uses to bring a
// PR branch up to date with its base. Empty means rebase.
func WithSyncMethod(method string) ExecOption {
	return func(e *execClient) { e.syncMethod = method }
}

// NewExecClient returns the gh-exec adapter for the given repo slug. It
// implements IssueTracker, CodeForge, and PRForge, so callers assign it to
// whichever seam(s) they need — the same concrete instance may be constructed
// twice (once per seam) or once and used for both. labels maps canonical
// DispatchState values to GitHub label names; branchPrefix is baked into
// AgentBranch's output; most call sites pass no opts.
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

// IsGithubTracker implements the optional forge.GithubTracker marker, letting
// settle's ensureClosesReference discover that this adapter — and not e.g.
// forgejo, whose issue numbers are a foreign namespace from GitHub's — owns
// the GitHub Closes-keyword convention.
func (e *execClient) IsGithubTracker() bool { return true }
