// Package forge is the seam through which the Harness speaks to the Target
// repo's host. GitHub is today's only adapter; the name goes into the glossary.
package forge

import "errors"

// ErrMergeConflict is returned by Merge when the PR branch cannot be
// auto-merged due to conflicts with the base branch. Callers may attempt to
// rebase the head branch and retry.
var ErrMergeConflict = errors.New("merge conflict")

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

// Client is the combined forge seam — IssueTracker and CodeForge in one.
// Use the narrower seam types where a function only needs one axis.
type Client interface {
	IssueTracker
	CodeForge
}

// combinedClient composes an independently-selected IssueTracker and
// CodeForge into a Client, so the two axes can vary independently (ADR
// 0013) without every call site threading both seams through by hand.
type combinedClient struct {
	IssueTracker
	CodeForge
}

// NewClient combines it and cf into a Client.
func NewClient(it IssueTracker, cf CodeForge) Client {
	return combinedClient{IssueTracker: it, CodeForge: cf}
}

// Probe disambiguates the Probe method both embedded seams declare,
// delegating to the CodeForge (matching Client.Probe's historical meaning of
// "is the repository reachable"). Callers that need each seam probed
// independently should call it.Probe() and cf.Probe() directly instead.
func (c combinedClient) Probe() (string, error) {
	return c.CodeForge.Probe()
}
