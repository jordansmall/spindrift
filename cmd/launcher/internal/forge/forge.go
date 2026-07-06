// Package forge is the seam through which the Harness speaks to the Target
// repo's host. GitHub is today's only adapter; the name goes into the glossary.
package forge

import "errors"

// ErrMergeConflict is returned by Merge when the PR branch cannot be
// auto-merged due to conflicts with the base branch. Callers may attempt to
// rebase the head branch and retry.
var ErrMergeConflict = errors.New("merge conflict")

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
	ListIssues(label string) ([]Issue, error) // open issues, oldest-first
	Issue(num string) (Issue, error)          // body + labels + state, one gh call
	SwapLabel(num, add, remove string) error
	Comment(num, body string) error
	OpenPRForBranch(branch string) (PR, bool, error) // false = no open PR
	PRState(url string) (string, error)              // MERGED check
	CheckState(url string) (RollupState, error)      // aggregate statusCheckRollup
	Merge(url string) error                          // rebase + delete branch
	Rebase(prURL string) error                       // checkout head, rebase onto base, force-push
}
