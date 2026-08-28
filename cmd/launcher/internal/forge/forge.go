// Package forge is the seam through which the Harness speaks to the Target
// repo's host. GitHub is today's only adapter; the name goes into the glossary.
package forge

import (
	"errors"
	"strings"
)

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

// ErrMergeTransient is returned by Merge when the merge call itself fails
// for a transient transport/server reason — an HTTP 5xx from the forge, a
// network timeout, or similar — as opposed to a genuine merge rejection
// (conflict, blocked by checks, branch protection). Callers may retry a
// bounded number of times with backoff.
var ErrMergeTransient = errors.New("transient merge failure")

// ErrAuthFailure is returned by Probe when the forge credentials are missing
// or invalid. Callers should advise the user to check GH_TOKEN.
var ErrAuthFailure = errors.New("forge auth failure")

// ErrRepoNotFound is returned by Probe when the configured repository cannot
// be reached or does not exist under the authenticated account.
var ErrRepoNotFound = errors.New("forge repo not found")

// ErrBundleNotFound is returned by a BundleRelay's RelayBundle when the
// outbox seam bundle is simply absent — nothing was written because the
// Box's branch range was empty (issue #2096), a benign "nothing to relay"
// outcome the blocked-hand-off call site logs informationally rather than
// as a relay failure. A bundle that is present but unreadable or corrupt
// is a genuine error and is NOT this sentinel.
var ErrBundleNotFound = errors.New("forge bundle not found")

// ErrNotFound is returned when a requested resource (an issue, a pull
// request, a label, etc.) does not exist on the forge. Unlike
// ErrRepoNotFound, which is Probe-specific (the configured repository
// itself is unreachable), ErrNotFound is the generic per-resource sentinel
// callers can errors.Is check for any REST lookup.
var ErrNotFound = errors.New("forge: not found")

// ErrRateLimit is returned when a forge operation fails because GitHub is
// rate-limiting the caller — either the primary hourly API quota or the
// secondary/abuse-detection limit. Callers may retry after backing off.
var ErrRateLimit = errors.New("forge rate limited")

// Issue is a GitHub issue as seen by the launcher.
type Issue struct {
	Number string // launcher keeps issue numbers as strings
	Title  string
	Body   string
	State  IssueState
	Labels []string
	// Landing is the local adapter's immutable landing ref (ADR 0029, a PR
	// URL or push-only branch ref) — empty for github/jira, which have no
	// such field to report.
	Landing string
	// Abandoned reports the local adapter's abandoned: axis (ADR 0029) — set
	// by reconcile when the issue's landing PR closed without merging.
	// Always false for github/jira, which have no such field to report.
	Abandoned bool
	// Parent is the local adapter's opaque, operator-authored parent:
	// frontmatter field (ADR 0033) — the broad-ticket key CODE_FORGE=local
	// resolves this seam's Integration branch from. Empty for github/jira,
	// which have no such field, and for a local issue with no parent: set
	// (a parentless seam is its own broad ticket, keyed on its own slug
	// instead — see local.ResolveParent).
	Parent string
	// Priority is the canonical dispatch priority (ADR 0040), resolved by
	// each IssueTracker adapter from its own agent-priority-* labels (or
	// left at the zero value PriorityNormal for adapters that don't map
	// priority labels yet). The launcher sorts the dispatchable pool by
	// this value, highest tier first, with oldest-first as the
	// within-tier tiebreaker — that sort is not part of this type.
	Priority Priority
}

// Priority is the canonical dispatch priority a launcher sorts the
// dispatchable pool by (ADR 0040): Critical > High > Normal > Low. Each
// IssueTracker adapter (github, jira, local, and the fake) translates its
// own agent-priority-* labels to these values at its own edge — priority is
// a canonical launcher concept resolved from labels, never a native
// per-tracker field or a per-adapter sort.
//
// The zero value is PriorityNormal, so an issue built without setting
// Priority (e.g. an adapter that hasn't wired label resolution yet) safely
// defaults to the tier an unlabeled issue occupies. The four constants are
// ordered so that plain Go comparison operators (<, >) already express the
// ADR's total order — a future central sort can compare two Priority values
// directly with no separate ranking method.
type Priority int

const (
	// PriorityLow is the "run only when the pool would otherwise idle"
	// tier — the ADR's bury tier, below the Normal default.
	PriorityLow Priority = iota - 1
	// PriorityNormal is the zero value and the tier an unlabeled issue
	// occupies.
	PriorityNormal
	// PriorityHigh boosts an issue above the Normal default.
	PriorityHigh
	// PriorityCritical is the top tier; highest label wins if an issue
	// somehow carries more than one priority label.
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
	URL string
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

// ClassifyMergeFailure maps a PR's MergeableState to the sentinel error a
// failed Merge should return, shared by every CodeForge adapter so the
// conflict-vs-checks distinction (see ErrMergeConflict, ErrMergeBlockedByChecks)
// is made in one place. ok is false for any state the caller must not mask
// behind a sentinel — MergeableUnknown, or any adapter-native value this
// package doesn't recognize — telling the caller to build its own
// adapter-specific raw error instead.
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
// adapter's native shape (github: CheckRun name+conclusion+summary or
// StatusContext context+state+description, unioned; forgejo: context+state+
// description) into the common fields RenderFailureDetail formats.
type FailureDetailEntry struct {
	Name    string
	State   string
	Summary string
}

// RenderFailureDetail formats already-filtered failing entries into a
// bounded, human-readable excerpt: one "Name: State" header per entry plus
// its summary, truncated to MaxFailureDetailBytes. Callers are responsible
// for filtering entries down to failing ones before calling this function.
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
