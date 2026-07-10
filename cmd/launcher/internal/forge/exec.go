package forge

// execClient is the gh-exec adapter. It satisfies Client using the gh CLI.
// GH_TOKEN is read from the ambient environment; the repo slug and dispatch
// label mapping are fixed at construction time.
type execClient struct {
	repo         string // owner/repo slug
	labels       DispatchLabels
	branchPrefix string
}

// NewExecClient returns a Client backed by the gh CLI for the given repo slug.
// labels maps canonical DispatchState values to GitHub label names.
// branchPrefix is baked into AgentBranch's output.
func NewExecClient(repo string, labels DispatchLabels, branchPrefix string) Client {
	return &execClient{repo: repo, labels: labels, branchPrefix: branchPrefix}
}

// AgentBranch returns branchPrefix + num.
func (e *execClient) AgentBranch(num string) string {
	return e.branchPrefix + num
}
