package forge

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"
)

// DepSource records whether a Dependency came from the tracker's native
// dependency API or was parsed from issue body text.
type DepSource int

const (
	// DepSourceUnknown is the zero value, so a sources map miss renders
	// "unknown" rather than silently misreporting "native".
	DepSourceUnknown DepSource = iota
	// DepSourceNative means a native relationship (GitHub's
	// issue-dependencies API, Jira "is blocked by" links).
	DepSourceNative
	// DepSourceBody means the ref came from body text: inline "blocked by
	// #N" / "depends on #N", or a "## Blocked by" section.
	DepSourceBody
)

// String renders the source for operator-facing diagnostics.
func (s DepSource) String() string {
	switch s {
	case DepSourceNative:
		return "native"
	case DepSourceBody:
		return "body"
	default:
		return "unknown"
	}
}

// Dependency is a single resolved blocker reference.
type Dependency struct {
	ID     string
	Source DepSource
}

// Ref formats a blocker ID with its source annotation, e.g. "#42 (native)".
// The preview, blocked-skip, and blocked-claim markers all call it so the
// format exists once.
func Ref(id string, source DepSource) string {
	return fmt.Sprintf("#%s (%s)", id, source)
}

// WithSource tags a batch of same-sourced IDs.
func WithSource(ids []string, source DepSource) []Dependency {
	deps := make([]Dependency, len(ids))
	for i, id := range ids {
		deps[i] = Dependency{ID: id, Source: source}
	}
	return deps
}

// IssueTracker reads issues and transitions their dispatch state.
// Implementations map DispatchState to their own mechanism: GitHub labels,
// Jira workflow statuses, local file frontmatter.
type IssueTracker interface {
	// ListIssues returns open issues in the given dispatch state, in
	// canonical order (GitHub: ascending issue number).
	ListIssues(state DispatchState) ([]Issue, error)
	// ListOpenIssues returns every open issue in canonical order whatever
	// its dispatch state, including issues the operator has not yet
	// triaged: the full backlog reconcile and the finding dedup fallback read.
	ListOpenIssues() ([]Issue, error)
	// Issue returns full details (body, labels, state) for the given number.
	Issue(num string) (Issue, error)
	// TransitionState moves issue num from state from to state to, adding
	// the label for to and removing the label for from.
	TransitionState(num string, from, to DispatchState) error
	// CompleteVerdict moves issue num from InProgress to its
	// verdict-specific terminal label, the research kind's Complete
	// transition (ADR 0022). Plain TransitionState cannot express which
	// verdict a human should act on. Work-kind dispatches never call this.
	CompleteVerdict(num string, verdict Verdict) error
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

// BlockersLister is the optional IssueTracker capability for adapters with a
// native reverse-dependency concept, the issues a given issue blocks. Only
// github, jira, and forgejo track blocked/blocking bidirectionally, so the
// reverse direction costs one more native call rather than a whole-backlog
// scan (issue #1744).
type BlockersLister interface {
	// BlocksOf returns the canonical issues that num blocks, DepsOf's
	// reverse direction. Always DepSourceNative: no body-text grammar
	// declares a forward "blocks" relationship, so a body-sourced
	// blocked-by edge has no reverse to surface.
	BlocksOf(num string) ([]Dependency, error)
}

// LabeledBacklogLister is the optional IssueTracker capability for the
// finding-backlog dedup scan (issue #3873), which reads closed findings too:
// a closed finding is a triage decision the host must not refile.
// ListOpenIssues won't do: on GitHub it omits body and truncates to the
// *oldest* ResultPageLimit issues. Its newest-first order means a truncated
// page drops the oldest findings, not the ones a later run is likeliest to
// re-file. An implementation may return the labels that succeeded with a nil
// error, failing only when every label failed.
type LabeledBacklogLister interface {
	// ListIssuesWithLabels returns every issue in state carrying at least
	// one of labels, Body populated, newest first. state must be IssueOpen
	// or IssueClosed. An empty labels returns no issues and a nil error.
	ListIssuesWithLabels(state IssueState, labels []string) ([]Issue, error)
}

// RouteMatcher is the optional IssueTracker capability that tells whether one
// issue belongs to the tracker's Issue route (ADR 0075). Issue reads stay
// unrouted, so a caller holding an issue number from outside the route's
// listings asks this before acting on it. An empty route matches every issue.
type RouteMatcher interface {
	// MatchesRoute reports whether issue number carries every route selector.
	// A missing issue is an error, as for Issue.
	MatchesRoute(number string) (bool, error)
}

// IssueSearcher is the optional IssueTracker capability for a free-text search
// of the backlog, so the in-Box Filer can dedup against it without a forge
// token (issue #6895). The query comes from an untrusted Box: an
// implementation must treat it as plain terms, never as tracker query syntax
// that could widen the search past this repo.
type IssueSearcher interface {
	// SearchIssues returns the issues in state whose title or body contains
	// every whitespace-separated term of text, in the tracker's own order, at
	// most limit.
	// state must be IssueOpen or IssueClosed. A non-empty label restricts the
	// result to issues carrying exactly that label. Results carry Number,
	// Title, State and Labels. An implementation MAY return an error for text it
	// cannot search verbatim: GitHub rejects a double quote, backslash or
	// control character; the other adapters pass the text through.
	SearchIssues(text string, state IssueState, label string, limit int) ([]Issue, error)
}

// MergeLabeledIssues calls fetch once per label, merges the results by
// Number, and sorts them newest first. A failed label (missing from the
// repo, or a transient rate limit) is logged and skipped so it cannot
// discard what the other labels found; only a failure on every label is
// returned, so a total outage still reaches the caller's fallback. source
// prefixes the warning and error text (e.g. "gh issue list").
func MergeLabeledIssues(source string, labels []string, fetch func(label string) ([]Issue, error)) ([]Issue, error) {
	seen := make(map[string]bool)
	var issues []Issue
	failures := 0
	var lastErr error
	for _, label := range labels {
		got, err := fetch(label)
		if err != nil {
			lastErr = err
			fmt.Fprintf(os.Stderr, "WARNING: %s: label %s failed: %v\n", source, label, err)
			failures++
			continue
		}
		for _, iss := range got {
			if seen[iss.Number] {
				continue
			}
			seen[iss.Number] = true
			issues = append(issues, iss)
		}
	}
	if len(labels) > 0 && failures == len(labels) {
		return nil, fmt.Errorf("%s: all %d label(s) failed: %w", source, len(labels), lastErr)
	}
	// LabeledBacklogLister promises newest-first.
	sort.Slice(issues, func(i, j int) bool {
		ni, _ := strconv.Atoi(issues[i].Number)
		nj, _ := strconv.Atoi(issues[j].Number)
		return ni > nj
	})
	return issues, nil
}

// HostPostedCommenter is the optional IssueTracker capability for adapters
// whose Comment the Launcher may call host-side from its own credential
// (issue #1914): a read-only Box holds no write token, so its comment travels
// as a SPINDRIFT_COMMENT stdout block. Every adapter satisfies it today; it
// exists so the read-only gate (issue #1916) has a name to assert against.
type HostPostedCommenter interface {
	// Comment posts a comment on the issue.
	Comment(num, body string) error
}

// HostPostedIssueFiler is the optional IssueTracker capability for host-side
// issue filing (issue #2018): a read-only Box cannot create an issue, so it
// hands the Launcher a SPINDRIFT_ISSUE_INTENT stdout signal (ADR 0034). Labels
// always come from the caller and the destination repo from the tracker
// instance, never from the Box's payload (issue #1949).
type HostPostedIssueFiler interface {
	// PostIssue files a new issue and returns its URL.
	PostIssue(title, body string, labels []string) (url string, err error)
}

// DemandCounter is the optional IssueTracker capability for adapters that
// can answer "how many issues are waiting?" for the daemon's Demand probe
// (ADR 0059). The count is advisory: it tallies labelled candidates, not
// issues whose blockers have cleared, so a caller treats a nonzero count as
// "worth starting a child", never as a promise of dispatchable work.
type DemandCounter interface {
	// CountReady returns the number of open issues in the Dispatchable
	// state -- the set ListIssues(Dispatchable) returns.
	//
	// fresh asks an adapter that caches change tokens (e.g. an ETag) to skip
	// its conditional request and re-read; an adapter without a cache
	// ignores it.
	CountReady(fresh bool) (int, error)
	// ProbeInterval is the adapter's default cadence for re-asking
	// CountReady; a cheap in-process scan answers faster than a
	// rate-limited API.
	ProbeInterval() time.Duration
}

// IssueLabeler is the optional IssueTracker capability for adding labels
// outside TransitionState's DispatchState mapping (issue #4074): the
// butler's patch rung promotes its finding issue to a work label after a
// failed patch push or PR create, and the butler label family has no Ready
// label to transition through.
type IssueLabeler interface {
	// AddLabels adds labels to issue num, leaving any labels already present
	// untouched.
	AddLabels(num string, labels []string) error
}

// IssueTypeChecker is the optional IssueTracker capability for adapters whose
// issue creation needs a configured issue type the project may not offer
// (Jira's JIRA_ISSUE_TYPE, issue #6352), so doctor can catch the mismatch
// before the first host-side filing fails.
type IssueTypeChecker interface {
	// CheckIssueType returns an error wrapping ErrUnknownIssueType when the
	// configured type is absent from the project, and otherwise the required
	// create-screen fields the host's own create request does not fill, each
	// named for an operator. Any other error means the check could not run.
	CheckIssueType() (unfilled []string, err error)
}

// StatusMappingChecker is the optional IssueTracker capability for adapters
// that map dispatch states to workflow statuses (Jira's JIRA_STATUS_MAPPING,
// issue #6353), so doctor can catch a mapped status the project's workflow
// lacks before a transition to it silently never happens.
type StatusMappingChecker interface {
	// CheckMappedStatuses returns the mapped status names absent from the
	// project's workflow, deduplicated and sorted; empty when none is missing
	// or nothing is mapped. An error means the check could not run.
	CheckMappedStatuses() (missing []string, err error)
}

// StatusCategoryDone is the category of a terminal workflow status (Jira's
// Done category) on a StatusCategoryReader's answer.
const StatusCategoryDone = "done"

// StatusCategoryReader is the optional IssueTracker capability for adapters
// that map dispatch states to workflow statuses and can read each status's
// category (issue #7337), so a routed daemon can warn when Complete maps to
// a terminal status: closing on a green PR releases cross-route dependents
// before that PR merges (ADR 0075).
type StatusCategoryReader interface {
	// MappedStatusCategory returns the category of the workflow status state
	// maps to: "" when state maps to none or the tracker reports no category
	// for it. An error means the read could not run.
	MappedStatusCategory(state DispatchState) (string, error)
}

// LandingRecorder is the optional IssueTracker capability for adapters that
// can persist where a Dispatch's work landed (ADR 0029). Only the local
// adapter implements it; github and jira issues close through the forge's own
// mechanisms and have no such ref to persist.
type LandingRecorder interface {
	// RecordLanding persists landing (a PR URL or push-only branch ref) as
	// issue num's immutable landing reference. Only the ref is stored, no
	// merge state, so a later reconcile re-checks the forge live.
	RecordLanding(num, landing string) error
}

// LandingPassRecorder is the optional IssueTracker capability for recording
// which pass produced a landing's outcome (ADR 0029, issue #2983). Advisory
// provenance only: no settle decision or Resolved tier selection reads it.
// Only the local adapter implements it.
//
// The pass is recorded at settle's landing write (issue #5272), so it need
// not describe the merged head: settle-dispatched CI fix passes on a read-only
// PR-shaped forge move the head without re-recording it; a push-only landing
// has no fix pass, so there it is the merged head. Reconcile's ref upgrades
// leave it unchanged; a landing reconcile discovers clears it.
type LandingPassRecorder interface {
	// RecordLandingPass persists the 1-indexed pass number and role
	// ("implement", "review", "fix", "land", ...) of the pass that produced
	// issue num's recorded landing. Best-effort and additive: it never
	// changes what RecordLanding does. Pass 0 with an empty kind clears it.
	RecordLandingPass(num string, pass int, kind string) error
}

// IssueCloser is the optional IssueTracker capability for adapters with a
// native open/closed axis reconcile can flip (ADR 0029). Only the local
// adapter implements it; a github or jira issue closes through the forge's
// own merged-PR auto-close.
type IssueCloser interface {
	// CloseIssue marks issue num closed (the local closed: axis, ADR 0029).
	// Reconcile is its usual caller; settle's already-resolved arm (issue
	// #4017) also calls it directly.
	CloseIssue(num string) error
}

// MergeCloser is the optional IssueTracker capability for settle's backstop
// close after a forge merge (issue #1892) and its close on
// status=already-resolved (issue #4015). It is not named CloseIssue because
// ISSUE_TRACKER=local with CODE_FORGE=github is valid, and a shared name
// would let the merge backstop close a local issue, whose closed: axis only
// reconcile writes for landed work (settle's already-resolved close uses
// IssueCloser, issue #4017).
type MergeCloser interface {
	// CloseMergedIssue closes issue num once settle has independently
	// confirmed either a genuine merge or that the work is already resolved
	// (status=already-resolved, issue #4015). Idempotent.
	CloseMergedIssue(num string) error
}

// AbandonedFlagger is the optional IssueTracker capability for adapters with a
// native abandoned axis reconcile can flip (ADR 0029). Only the local adapter
// implements it; a github or jira PR closed without merging needs no further
// local tracking.
type AbandonedFlagger interface {
	// FlagAbandoned marks issue num abandoned (the local abandoned: axis,
	// ADR 0029) when its landing PR was closed without merging. Reconcile
	// is its sole caller.
	FlagAbandoned(num string) error
}

// SeamLister is the optional IssueTracker capability for adapters that group
// issues under a parent/broad-ticket field (ADR 0033). Only the local adapter
// implements it; github and jira have no such grouping to query.
type SeamLister interface {
	// AllIssues returns every issue the tracker holds, open or closed, in
	// canonical order and whatever its parent, state, or dispatch marker.
	// The auto-surface sweep uses it to find every distinct resolved parent
	// across a mixed batch (ADR 0033, issue #1734).
	AllIssues() ([]Issue, error)
}

// PriorClaimStateReader is the optional IssueTracker capability for reading
// the terminal dispatch state an issue held just before its latest claim onto
// InProgress, which the claim's TransitionRemoveLabels strip destroys. Since
// agent-recover.yml claims ahead of the launcher, this is the recover path's
// one route back to agent-complete instead of a downgrade (issue #2477).
type PriorClaimStateReader interface {
	// PriorClaimState returns the terminal DispatchState (Complete or
	// Failed) the issue carried before its most recent claim onto
	// InProgress, and whether one was found. It reports false when the
	// issue's history holds no terminal-label removal, such as a fresh
	// dispatch that was never terminal.
	PriorClaimState(num string) (DispatchState, bool, error)
}

// LabeledTracker is the optional IssueTracker capability for adapters whose
// whole DispatchState space reduces to one DispatchLabels value (github,
// forgejo, local, the Fake; jira's StatusMapping blend does not). PickIssue's
// double-box guard (#1742) uses it to spot a state the labels leave unmapped,
// like research's Complete (ADR 0022), where a ListIssues filter would match
// every open issue.
type LabeledTracker interface {
	// StateLabels returns the DispatchLabels family this tracker resolves
	// DispatchState values through.
	StateLabels() DispatchLabels
}
