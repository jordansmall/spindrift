// Package forge is the seam through which the Harness speaks to the Target
// repo's host. GitHub is today's only adapter; the name goes into the glossary.
package forge

import "errors"

// ErrMergeConflict is returned by Merge when the PR branch cannot be
// auto-merged due to conflicts with the base branch. Callers may attempt to
// rebase the head branch and retry.
var ErrMergeConflict = errors.New("merge conflict")

// ErrMergeBlockedByChecks is returned by Merge when the PR itself has no
// content conflict (mergeable state MERGEABLE) but required status checks
// are still pending or failing, so the forge refuses the merge. GitHub's gh
// CLI reports this refusal with the same "not mergeable" wording as a
// genuine conflict; callers must tell the two apart by querying the PR's
// mergeable state (see PRForge.Mergeable) rather than the refusal text.
// Unlike ErrMergeConflict, this is not conflict-resolvable — retrying the
// merge once checks settle is the only valid next step.
var ErrMergeBlockedByChecks = errors.New("merge blocked by checks")

// ErrTransientPushFailure is returned by Rebase when its force-push fails
// for a reason unrelated to the branch state — a forge outage, network
// fault, or locked ref — as opposed to a genuine stale-lease or
// non-fast-forward rejection. Callers may retry a bounded number of times.
var ErrTransientPushFailure = errors.New("transient push failure")

// ErrAuthFailure is returned by Probe when the forge credentials are missing
// or invalid. Callers should advise the user to check GH_TOKEN.
var ErrAuthFailure = errors.New("forge auth failure")

// ErrRepoNotFound is returned by Probe when the configured repository cannot
// be reached or does not exist under the authenticated account.
var ErrRepoNotFound = errors.New("forge repo not found")

// Issue is a GitHub issue as seen by the launcher.
type Issue struct {
	Number string // launcher keeps issue numbers as strings
	Title  string
	Body   string
	State  IssueState
	Labels []string
}

// IssueState is the canonical open/closed state of an issue. Each
// IssueTracker adapter (github, jira, local, and the fake) translates its own
// native representation to these values at its own edge; no adapter's native
// literal should leak past that boundary.
type IssueState string

const (
	IssueOpen   IssueState = "OPEN"
	IssueClosed IssueState = "CLOSED"
	// IssueMerged is the state gh issue view reports when a blocker ref
	// resolves to a merged PR rather than an agent-worked issue.
	IssueMerged IssueState = "MERGED"
)

// PR is a GitHub pull request as seen by the launcher.
type PR struct {
	URL     string
	IsDraft bool
}

// PRState is the canonical state of a pull request. Each CodeForge adapter
// (github, the fake) translates its own native representation to these
// values at its own edge; the push-only git adapter has no PR concept and
// never returns one.
type PRState string

const (
	PROpen   PRState = "OPEN"
	PRMerged PRState = "MERGED"
	PRClosed PRState = "CLOSED"
)

// MergeableState is GitHub's PR-content mergeability classification —
// whether the PR's changes conflict with its base branch. It is distinct
// from RollupState (CI check results) and from required-review/branch-
// protection gating: a MergeableMergeable PR can still be refused by Merge
// if its checks haven't passed.
type MergeableState string

// Known MergeableState values returned by the GitHub API or by the fake.
const (
	MergeableUnknown     MergeableState = "UNKNOWN"
	MergeableMergeable   MergeableState = "MERGEABLE"
	MergeableConflicting MergeableState = "CONFLICTING"
)

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
