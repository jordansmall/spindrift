package forge

import "fmt"

// DepSource records whether a Dependency was resolved from the tracker's
// native dependency-relationship API or parsed from issue/file body text.
type DepSource int

const (
	// DepSourceUnknown is the zero value: no source was recorded for this
	// ref (e.g. a sources map lookup miss). Keeping it — rather than
	// DepSourceNative — as the zero value means a missing entry renders
	// "unknown" instead of silently misreporting "native".
	DepSourceUnknown DepSource = iota
	// DepSourceNative means the ref came from a native relationship (GitHub
	// issue-dependencies API, Jira "is blocked by" issue links).
	DepSourceNative
	// DepSourceBody means the ref was parsed from body text (inline
	// "blocked by #N" / "depends on #N", or a "## Blocked by" section).
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

// Dependency is a single resolved blocker reference: the blocking issue's
// canonical ID and the source DepsOf resolved it from.
type Dependency struct {
	ID     string
	Source DepSource
}

// Ref formats a blocker ID with its source annotation for operator-facing
// diagnostics, e.g. "#42 (native)" — the single renderer shared by the
// preview, blocked-skip, and blocked-claim marker call sites so the format
// exists exactly once.
func Ref(id string, source DepSource) string {
	return fmt.Sprintf("#%s (%s)", id, source)
}

// WithSource tags a batch of same-sourced IDs, the shape every DepsOf
// implementation resolves in one shot (a native list, or ParseBlockerRefs'
// output).
func WithSource(ids []string, source DepSource) []Dependency {
	deps := make([]Dependency, len(ids))
	for i, id := range ids {
		deps[i] = Dependency{ID: id, Source: source}
	}
	return deps
}

// IssueTracker is the seam through which the launcher reads issues and
// transitions their dispatch state. Implementations map DispatchState to
// their native mechanism (GitHub labels, Jira workflow statuses, local
// file frontmatter).
type IssueTracker interface {
	// ListIssues returns open issues in the given dispatch state, in canonical
	// order (GitHub: ascending issue number).
	ListIssues(state DispatchState) ([]Issue, error)
	// ListOpenIssues returns every open issue, in canonical order (GitHub:
	// ascending issue number), regardless of dispatch state — including
	// issues the operator has not yet triaged onto the dispatch lifecycle.
	// Unlike ListIssues, which filters to a single dispatch state's label,
	// this is the full backlog the Console browses.
	ListOpenIssues() ([]Issue, error)
	// Issue returns full details (body, labels, state) for the given number.
	Issue(num string) (Issue, error)
	// TransitionState moves issue num from state from to state to. It adds
	// the label for to and removes the label for from, matching the
	// SwapLabel(add, remove) contract with typed state identifiers.
	TransitionState(num string, from, to DispatchState) error
	// CompleteVerdict moves issue num from InProgress to its verdict-specific
	// terminal label — the research dispatch kind's Complete transition
	// (ADR 0022), which carries data plain TransitionState(num, InProgress,
	// Complete) cannot express: which of the three verdicts a human should
	// act on. Work-kind dispatches never call this; work's Complete carries
	// no verdict.
	CompleteVerdict(num string, verdict Verdict) error
	// DepsOf returns the canonical dependencies for the given issue, each
	// tagged with the source it was resolved from. Implementations prefer
	// the tracker's native dependency relationships (e.g. GitHub's
	// issue-dependencies API, Jira's "is blocked by" issue links) and fall
	// back to body-text parsing (GitHub body "depends on #N" / "## Blocked
	// by" section) only when native lookup yields no relationships or is
	// unavailable. Native wins when non-empty — body text is never merged
	// with a non-empty native result.
	DepsOf(num string) ([]Dependency, error)
	// TouchesOf returns the declared touch-set for the given issue — the
	// path globs an issue names as the files/areas its work will touch,
	// used by the wave engine's overlap gate. All adapters currently share
	// the body-grammar default (a "## Touches" section, ParseTouchPaths);
	// adapters remain free to go native later, mirroring DepsOf's
	// native-preferred-over-body pattern. An issue with no such section
	// returns nil, nil.
	TouchesOf(num string) ([]string, error)
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

// LandingRecorder is the optional IssueTracker surface for adapters that can
// persist where a Dispatch's work landed (ADR 0029). Only the local adapter
// implements it — github/jira issues close through the forge's own
// mechanisms and have no such ref to persist. Callers discover it with a
// type assertion — `lr, ok := it.(LandingRecorder)` — the same
// optional-interface pattern PRForge uses.
type LandingRecorder interface {
	// RecordLanding persists landing (a PR URL or push-only branch ref) as
	// issue num's immutable landing reference. Only the ref is stored; no
	// merge-state is cached — a later reconcile re-checks the forge live.
	RecordLanding(num, landing string) error
}

// IssueCloser is the optional IssueTracker surface for adapters with a
// native open/closed axis reconcile can flip (ADR 0029). Only the local
// adapter implements it — a github/jira issue closes through the forge's own
// merged-PR auto-close, with no separate axis for reconcile to drive.
// Callers discover it with a type assertion — `ic, ok := it.(IssueCloser)` —
// the same optional-interface pattern PRForge and LandingRecorder use.
type IssueCloser interface {
	// CloseIssue marks issue num closed (the local closed: axis, ADR 0029).
	// Reconcile is its sole caller.
	CloseIssue(num string) error
}

// AbandonedFlagger is the optional IssueTracker surface for adapters with a
// native abandoned axis reconcile can flip (ADR 0029). Only the local adapter
// implements it — a github/jira PR closed without merging needs no further
// local tracking. Callers discover it with a type assertion — `af, ok :=
// it.(AbandonedFlagger)` — the same optional-interface pattern IssueCloser
// and LandingRecorder use.
type AbandonedFlagger interface {
	// FlagAbandoned marks issue num abandoned (the local abandoned: axis,
	// ADR 0029) — set when the issue's landing PR was closed without
	// merging. Reconcile is its sole caller.
	FlagAbandoned(num string) error
}

// SeamLister is the optional IssueTracker surface for adapters that group
// issues under a parent/broad-ticket field (ADR 0033). Only the local
// adapter implements it — github/jira issues have no such grouping for the
// launcher to query. Callers discover it with a type assertion — `sl, ok :=
// it.(SeamLister)` — the same optional-interface pattern IssueCloser and
// LandingRecorder use.
type SeamLister interface {
	// AllIssues returns every issue (open or closed) the tracker holds, in
	// canonical order, regardless of parent, state, or dispatch marker —
	// the auto-surface sweep's basis for discovering every distinct
	// resolved parent across a mixed batch (ADR 0033, issue #1734).
	AllIssues() ([]Issue, error)
}

// LabeledTracker is the optional IssueTracker surface for adapters whose
// entire DispatchState space reduces to one DispatchLabels value (github,
// local, and the Fake test double). PickIssue's double-box guard (#1742)
// uses it to recognize a state the tracker's label family leaves unmapped
// (e.g. research's Complete, which reaches its terminal state through
// verdict labels instead, ADR 0022) and treat it as "never present"
// without paying a ListIssues round-trip — one that would otherwise
// false-match every open issue (GitHub ignores an empty --label filter;
// Local's frontmatter.State == "" matches every untriaged issue). Jira
// blends a per-state StatusMapping with Labels, which doesn't reduce to a
// single DispatchLabels value, so it doesn't implement this and keeps
// paying the round-trip. Callers discover it with a type assertion —
// `lt, ok := tracker.(LabeledTracker)` — the same optional-interface
// pattern IssueCloser and LandingRecorder use.
type LabeledTracker interface {
	// StateLabels returns the DispatchLabels family this tracker resolves
	// DispatchState values through.
	StateLabels() DispatchLabels
}
