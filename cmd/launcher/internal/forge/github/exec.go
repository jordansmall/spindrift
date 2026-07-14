// Package github is the gh-exec adapter: it satisfies the parent forge
// package's IssueTracker, CodeForge, and PRForge interfaces using the gh
// CLI. GH_TOKEN is read from the ambient environment; the repo slug and
// dispatch label mapping are fixed at construction time.
package github

import "spindrift.dev/launcher/internal/forge"

// execClient is the gh-exec adapter.
type execClient struct {
	repo         string // owner/repo slug
	labels       forge.DispatchLabels
	branchPrefix string
}

// NewExecClient returns the gh-exec adapter for the given repo slug, backed
// by the gh CLI. It implements IssueTracker, CodeForge, and PRForge, so
// callers assign it to whichever seam(s) they need — the same concrete
// instance may be constructed twice (once per seam) or once and used for
// both. labels maps canonical DispatchState values to GitHub label names.
// branchPrefix is baked into AgentBranch's output.
func NewExecClient(repo string, labels forge.DispatchLabels, branchPrefix string) *execClient {
	return &execClient{repo: repo, labels: labels, branchPrefix: branchPrefix}
}

// AgentBranch returns branchPrefix + num.
func (e *execClient) AgentBranch(num string) string {
	return e.branchPrefix + num
}
