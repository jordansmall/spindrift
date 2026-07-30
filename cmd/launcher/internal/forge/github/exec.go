// Package github is the gh-exec adapter: it satisfies the parent forge
// package's IssueTracker, CodeForge, and PRForge interfaces using the gh
// CLI. GH_TOKEN is read from the ambient environment; the repo slug and
// dispatch label mapping are fixed at construction time.
package github

import "spindrift.dev/launcher/internal/forge"

// execClient is the gh-exec adapter.
type execClient struct {
	repo          string // owner/repo slug
	labels        forge.DispatchLabels
	verdictLabels forge.VerdictLabels
	branchPrefix  string
	mergeMethod   string // "", "merge", "squash", or "rebase"; "" behaves as "rebase" (mergeMethodFlag)
}

// ExecOption configures an optional, construction-site-specific field on
// execClient beyond the three required positional arguments every call site
// shares. Both NewExecClient and NewReadOnlyCodeForge accept a variadic list
// of these, applied in order.
type ExecOption func(*execClient)

// WithVerdictLabels configures CompleteVerdict (the research dispatch kind's
// Complete transition); omitted for work-kind construction sites, matching
// NewFake's variadic convention for an optional, test/kind-specific config
// value.
func WithVerdictLabels(vl forge.VerdictLabels) ExecOption {
	return func(e *execClient) { e.verdictLabels = vl }
}

// WithMergeMethod sets the native `gh pr merge` method ("merge", "squash",
// or "rebase") used by both Merge and EnqueueAutoMerge. Omitted (or an empty
// string), it preserves today's --rebase default byte-for-byte
// (mergeMethodFlag).
func WithMergeMethod(method string) ExecOption {
	return func(e *execClient) { e.mergeMethod = method }
}

// NewExecClient returns the gh-exec adapter for the given repo slug, backed
// by the gh CLI. It implements IssueTracker, CodeForge, and PRForge, so
// callers assign it to whichever seam(s) they need — the same concrete
// instance may be constructed twice (once per seam) or once and used for
// both. labels maps canonical DispatchState values to GitHub label names.
// branchPrefix is baked into AgentBranch's output. opts configures optional,
// construction-site-specific fields (WithVerdictLabels, WithMergeMethod);
// most call sites pass none.
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
