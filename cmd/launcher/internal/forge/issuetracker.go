package forge

// IssueTracker is the seam through which the launcher reads issues and
// transitions their dispatch state. Implementations map DispatchState to
// their native mechanism (GitHub labels, Jira workflow statuses, local
// file frontmatter).
type IssueTracker interface {
	// ListIssues returns open issues in the given dispatch state, in canonical
	// order (GitHub: ascending issue number).
	ListIssues(state DispatchState) ([]Issue, error)
	// Issue returns full details (body, labels, state) for the given number.
	Issue(num string) (Issue, error)
	// TransitionState moves issue num from state from to state to. It adds
	// the label for to and removes the label for from, matching the
	// SwapLabel(add, remove) contract with typed state identifiers.
	TransitionState(num string, from, to DispatchState) error
	// DepsOf returns the canonical dependency IDs for the given issue.
	// Implementations parse the issue's native dependency format (e.g. GitHub
	// body "depends on #N" / "## Blocked by" section).
	DepsOf(num string) ([]string, error)
	// Comment posts a comment on the issue.
	Comment(num, body string) error
	// Probe checks issue tracker connectivity and returns the resolved slug.
	Probe() (string, error)
	// ListLabels returns the names of all labels defined in the repository.
	ListLabels() ([]string, error)
	// CreateLabel creates a label with the given name, description, and hex
	// color (without the leading #).
	CreateLabel(name, description, color string) error
}
