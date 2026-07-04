// Package forge is the seam through which the Harness speaks to the Target
// repo's host. GitHub is today's only adapter; the name goes into the glossary.
package forge

// Issue is a GitHub issue as seen by the launcher.
type Issue struct {
	Number string // launcher keeps issue numbers as strings
	Title  string
	Body   string
	State  string // OPEN | CLOSED
	Labels []string
}

// PR is a GitHub pull request as seen by the launcher.
type PR struct {
	URL     string
	IsDraft bool
}

// RollupState is the aggregate CI status of a PR's head commit.
type RollupState string

// Known RollupState values returned by the GitHub API or by the fake.
const (
	StateSuccess  RollupState = "SUCCESS"
	StatePending  RollupState = "PENDING"
	StateExpected RollupState = "EXPECTED"
	StateFailure  RollupState = "FAILURE"
	StateError    RollupState = "ERROR"
	StateNone     RollupState = "NONE" // no checks registered on this commit
)

// Client is the forge seam — all GitHub API calls go through here.
type Client interface {
	ListIssues(label string) ([]Issue, error)        // open issues, oldest-first
	Issue(num string) (Issue, error)                 // body + labels + state, one gh call
	SwapLabel(num, add, remove string) error
	OpenPRForBranch(branch string) (PR, bool, error) // false = no open PR
	PRState(url string) (string, error)              // MERGED check
	CheckState(url string) (RollupState, error)      // aggregate statusCheckRollup
	Merge(url string) error                          // rebase + delete branch
}
