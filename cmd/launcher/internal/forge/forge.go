// Package forge is the interface through which the Harness speaks to the
// Target repo's host. GitHub is today's only adapter.
package forge

import (
	"errors"
	"strings"
)

// ErrMergeConflict is returned by Merge when the PR branch conflicts with the
// base branch. Callers may rebase the head branch and retry.
var ErrMergeConflict = errors.New("merge conflict")

// ErrMergeBlockedByChecks is returned by Merge when the PR has no content
// conflict but required checks are pending or failing. The gh CLI reports this
// with the same "not mergeable" wording as a genuine conflict, so callers must
// tell the two apart by the PR's mergeable state (see PRForge.Mergeable). A
// rebase does not help here; the only next step is to retry once checks settle.
var ErrMergeBlockedByChecks = errors.New("merge blocked by checks")

// ErrTransientPushFailure is returned by Rebase when its force-push fails for a
// reason unrelated to branch state, such as a forge outage or a locked ref, and
// not a stale-lease or non-fast-forward rejection. Callers may retry a bounded
// number of times.
var ErrTransientPushFailure = errors.New("transient push failure")

// ErrMergeTransient is returned by Merge when the merge call fails for a
// transient transport or server reason rather than a genuine rejection
// (conflict, blocked by checks, branch protection). Callers may retry with
// backoff.
var ErrMergeTransient = errors.New("transient merge failure")

// ErrAuthFailure is returned by Probe when the forge credentials are missing or
// invalid. Callers should advise the user to check GH_TOKEN.
var ErrAuthFailure = errors.New("forge auth failure")

// ErrRepoNotFound is returned by Probe when the configured repository cannot be
// reached or does not exist under the authenticated account.
var ErrRepoNotFound = errors.New("forge repo not found")

// ErrBundleNotFound is returned by RelayBundle when the outbox bundle is absent
// because the Box's branch range was empty (issue #2096), a benign "nothing to
// relay" outcome. A present but corrupt bundle is a genuine error, not this.
var ErrBundleNotFound = errors.New("forge bundle not found")

// ErrNotFound is the generic per-resource sentinel for any REST lookup, unlike
// ErrRepoNotFound, which means the configured repository itself is unreachable.
var ErrNotFound = errors.New("forge: not found")

// ErrRateLimit is returned when GitHub rate-limits the caller, on either the
// primary hourly quota or the secondary abuse-detection limit.
var ErrRateLimit = errors.New("forge rate limited")

// Issue is a GitHub issue as seen by the launcher.
type Issue struct {
	Number string // launcher keeps issue numbers as strings
	Title  string
	Body   string
	State  IssueState
	Labels []string
	// Landing is the local adapter's immutable landing ref (ADR 0029, a PR URL
	// or push-only branch ref). Empty for github/jira, which have no such field.
	Landing string
	// Abandoned reports the local adapter's abandoned: axis (ADR 0029), set by
	// reconcile when the issue's landing PR closed without merging. Always false
	// for github/jira.
	Abandoned bool
	// Parent is the local adapter's parent: frontmatter field (ADR 0033), the
	// broad-ticket key CODE_FORGE=local resolves the Integration branch from.
	// Empty for github/jira, and for a parentless local seam, which is its own
	// broad ticket keyed on its own slug (see local.ResolveParent).
	Parent string
	// Priority is the canonical dispatch priority (ADR 0040), resolved by each
	// IssueTracker adapter from its own agent-priority-* labels. The sort that
	// consumes it is not part of this type.
	Priority Priority
}

// Priority is the canonical dispatch priority (ADR 0040): Critical > High >
// Normal > Low. Each IssueTracker adapter resolves it from its own
// agent-priority-* labels at its own edge. The zero value is PriorityNormal,
// the tier an unlabeled issue occupies, and the constants are ordered so that
// < and > already express the ADR's total order.
type Priority int

const (
	// PriorityLow runs only when the pool would otherwise idle.
	PriorityLow Priority = iota - 1
	PriorityNormal
	PriorityHigh
	// PriorityCritical wins if an issue somehow carries more than one priority
	// label.
	PriorityCritical
)

// String renders the priority as its lowercase tier name.
func (p Priority) String() string {
	switch p {
	case PriorityCritical:
		return "critical"
	case PriorityHigh:
		return "high"
	case PriorityNormal:
		return "normal"
	case PriorityLow:
		return "low"
	default:
		return "normal"
	}
}

// IssueState is the canonical open/closed state of an issue. Each IssueTracker
// adapter translates its own native representation at its own edge; no
// adapter's native literal leaks past that boundary.
type IssueState string

const (
	IssueOpen   IssueState = "OPEN"
	IssueClosed IssueState = "CLOSED"
	// IssueMerged is what gh issue view reports when a blocker ref resolves to a
	// merged PR rather than an agent-worked issue.
	IssueMerged IssueState = "MERGED"
)

// PR is a GitHub pull request as seen by the launcher.
type PR struct {
	URL string
}

// PRState is the canonical state of a pull request. The push-only git adapter
// has no PR concept and never returns one.
type PRState string

const (
	PROpen   PRState = "OPEN"
	PRMerged PRState = "MERGED"
	PRClosed PRState = "CLOSED"
)

// MergeableState is GitHub's classification of whether a PR's changes conflict
// with its base branch. It is distinct from RollupState (CI results) and from
// required-review or branch-protection gating: Merge can still refuse a
// MergeableMergeable PR whose checks have not passed.
type MergeableState string

// Known MergeableState values returned by the GitHub API or by the fake.
const (
	MergeableUnknown     MergeableState = "UNKNOWN"
	MergeableMergeable   MergeableState = "MERGEABLE"
	MergeableConflicting MergeableState = "CONFLICTING"
)

// ClassifyMergeFailure maps a PR's MergeableState to the sentinel a failed
// Merge should return, so every adapter makes the conflict-vs-checks
// distinction in one place. ok is false for a state the caller must not mask
// behind a sentinel (MergeableUnknown, or an unrecognized adapter-native
// value), telling it to build its own raw error instead.
func ClassifyMergeFailure(state MergeableState) (err error, ok bool) {
	switch state {
	case MergeableConflicting:
		return ErrMergeConflict, true
	case MergeableMergeable:
		return ErrMergeBlockedByChecks, true
	default:
		return nil, false
	}
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

// MaxFailureDetailBytes bounds the string RenderFailureDetail returns, so a
// large CI log excerpt cannot blow the fix Box's env/prompt budget.
const MaxFailureDetailBytes = 4000

// FailureDetailEntry is one failing check or status, normalized from an
// adapter's native shape into the fields RenderFailureDetail formats.
type FailureDetailEntry struct {
	Name    string
	State   string
	Summary string
}

// RenderFailureDetail formats failing entries into a bounded excerpt, truncated
// to MaxFailureDetailBytes. Callers must filter entries down to failing ones
// first.
func RenderFailureDetail(entries []FailureDetailEntry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Name)
		b.WriteString(": ")
		b.WriteString(e.State)
		b.WriteString("\n")
		if e.Summary != "" {
			b.WriteString(e.Summary)
			b.WriteString("\n")
		}
		b.WriteString("---\n")
	}
	s := strings.TrimSpace(b.String())
	if len(s) > MaxFailureDetailBytes {
		s = s[:MaxFailureDetailBytes]
	}
	return s
}
