package forge

import "fmt"

// DepSource records whether a Dependency was resolved from the tracker's
// native dependency-relationship API or parsed from issue/file body text.
type DepSource int

const (
	// DepSourceUnknown is the zero value, deliberately not DepSourceNative:
	// a sources map lookup miss renders "unknown" instead of silently
	// misreporting "native".
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
// preview, blocked-skip, and blocked-claim marker call sites.
func Ref(id string, source DepSource) string {
	return fmt.Sprintf("#%s (%s)", id, source)
}

// WithSource tags a batch of same-sourced IDs, the shape every DepsOf
// implementation resolves in one shot.
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
	// ListOpenIssues returns every open issue, in canonical order, regardless
	// of dispatch state — including issues the operator has not yet triaged
	// onto the dispatch lifecycle. This is the full backlog the Console
	// browses.
	ListOpenIssues() ([]Issue, error)
	// Issue returns full details (body, labels, state) for the given number.
	Issue(num string) (Issue, error)
	// TransitionState moves issue num from state from to state to. It adds
	// the label for to and removes the label for from, matching the
	// SwapLabel(add, remove) contract with typed state identifiers.
	TransitionState(num string, from, to DispatchState) error
	// CompleteVerdict moves issue num from InProgress to its verdict-specific
	// terminal label — the research dispatch kind's Complete transition
	// (ADR 0022), which carries what plain TransitionState cannot: which of
	// the three verdicts a human should act on. Work-kind dispatches never
	// call this; work's Complete carries no verdict.
	CompleteVerdict(num string, verdict Verdict) error
	// DepsOf returns the canonical dependencies for the given issue, each
	// tagged with the source it was resolved from. Implementations prefer the
	// tracker's native dependency relationships and fall back to body-text
	// parsing only when native lookup yields nothing or is unavailable.
	// Native wins when non-empty — body text is never merged into a non-empty
	// native result.
	DepsOf(num string) ([]Dependency, error)
	// TouchesOf returns the declared touch-set for the given issue — the
	// path globs it names as the files its work will touch, used by the wave
	// engine's overlap gate. All adapters currently share the body-grammar
	// default (a "## Touches" section, ParseTouchPaths). An issue with no
	// such section returns nil, nil.
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

// The interfaces below are optional IssueTracker capabilities: a caller
// discovers each with a type assertion -- `x, ok := it.(Capability)` -- and
// falls back when an adapter does not implement it.

// BlockersLister is the optional surface for adapters with a genuine native
// reverse-dependency concept -- the issues a given issue blocks, as opposed to
// DepsOf's forward "blocked by" direction. Only github and jira implement it:
// both track blocked/blocking as a true bidirectional native relationship, so
// the reverse direction costs one more native call, not a whole-backlog scan
// (#1744). The local adapter's only blocker concept is one-directional
// body-text parsing, with no way to discover which other issues declare this
// one as a blocker short of scanning every issue file.
type BlockersLister interface {
	// BlocksOf returns the canonical issues that num blocks — DepsOf's
	// reverse direction — each tagged with the source it was resolved
	// from. Always DepSourceNative: there is no body-text grammar for
	// declaring a forward "blocks" relationship, so a body-sourced
	// blocked-by edge has no reverse this method can ever surface.
	BlocksOf(num string) ([]Dependency, error)
}

// HostPostedCommenter is the optional surface for adapters whose Comment call
// is safe to invoke host-side, from the Launcher's own credential (#1914):
// under BOX_FORGE_AND_ISSUE_ACCESS=read-only the Box holds no write token, so
// its blocked/verdict comment travels as a SPINDRIFT_COMMENT stdout block for
// the Launcher to post. Every current adapter satisfies it trivially; it
// exists so the read-only capability gate (#1916) has a named seam to assert
// against, in case a future adapter's Comment needs the Box's own credential.
type HostPostedCommenter interface {
	// Comment posts a comment on the issue -- identical to the base
	// IssueTracker.Comment, restated as a discoverable capability.
	Comment(num, body string) error
}

// HostPostedIssueFiler is the optional surface for host-side issue filing
// (#2018): under BOX_FORGE_AND_ISSUE_ACCESS=read-only the Box holds no write
// token, so the Launcher files the issue instead, from a title/body/labels the
// Box hands it as a SPINDRIFT_ISSUE_INTENT stdout signal -- the fourth
// host-mediated write channel alongside branch→bundle, PR→intent line, and
// comment→comment line (ADR 0034). The destination repo is implicit in which
// IssueTracker instance the Launcher holds, and labels are always supplied by
// the caller, never read back out of the Box's own payload (#1949's
// do-not-trust-the-agent-target invariant).
type HostPostedIssueFiler interface {
	// PostIssue files a new issue with the given title, body, and labels,
	// and returns its URL.
	PostIssue(title, body string, labels []string) (url string, err error)
}

// LandingRecorder is the optional surface for adapters that can persist where
// a Dispatch's work landed (ADR 0029). Only the local adapter implements it --
// github/jira issues close through the forge's own mechanisms and have no such
// ref to persist.
type LandingRecorder interface {
	// RecordLanding persists landing (a PR URL or push-only branch ref) as
	// issue num's immutable landing reference. Only the ref is stored; no
	// merge-state is cached — a later reconcile re-checks the forge live.
	RecordLanding(num, landing string) error
}

// GithubTracker is the optional capability marking the github adapter
// specifically (#2341) -- narrower than "not LandingRecorder," which local
// excludes but forgejo would still pass, even though forgejo issue numbers are
// a foreign namespace from GitHub's: injecting a GitHub `Closes #N` keyword
// against a forgejo-tracked issue would falsely reference (and could
// auto-close) an unrelated real GitHub issue #N. IsGithubTracker is exported
// rather than a sealed unexported marker because the implementer lives in
// forge/github, and an unexported method can only be satisfied from the
// interface's own package.
type GithubTracker interface {
	// IsGithubTracker is a no-op marker, present only so a type assertion
	// against GithubTracker succeeds. It always returns true.
	IsGithubTracker() bool
}

// IssueCloser is the optional surface for adapters with a native open/closed
// axis reconcile can flip (ADR 0029). Only the local adapter implements it --
// a github/jira issue closes through the forge's own merged-PR auto-close,
// with no separate axis for reconcile to drive.
type IssueCloser interface {
	// CloseIssue marks issue num closed (the local closed: axis, ADR 0029).
	// Reconcile is its sole caller.
	CloseIssue(num string) error
}

// MergeCloser is the optional surface for an adapter that can close an issue
// directly as settle's deterministic backstop (#1892) for a Code Forge's own
// merge-driven auto-close -- used only by settle's post-merge verification,
// never by reconcile. Deliberately named apart from IssueCloser: ISSUE_TRACKER
// and CODE_FORGE are selected independently, so ISSUE_TRACKER=local with
// CODE_FORGE=github is valid, and a CloseIssue-named surface would be
// satisfied by the local adapter too -- letting settle drive the local closed:
// axis, a write only reconcile's sweep may make.
type MergeCloser interface {
	// CloseMergedIssue closes issue num once settle has independently
	// confirmed a genuine merge. Idempotent: closing an already-closed issue
	// is a successful no-op.
	CloseMergedIssue(num string) error
}

// AbandonedFlagger is the optional surface for adapters with a native
// abandoned axis reconcile can flip (ADR 0029). Only the local adapter
// implements it -- a github/jira PR closed without merging needs no further
// local tracking.
type AbandonedFlagger interface {
	// FlagAbandoned marks issue num abandoned (the local abandoned: axis,
	// ADR 0029) — set when the issue's landing PR was closed without
	// merging. Reconcile is its sole caller.
	FlagAbandoned(num string) error
}

// SeamLister is the optional surface for adapters that group issues under a
// parent/broad-ticket field (ADR 0033). Only the local adapter implements it
// -- github/jira issues have no such grouping for the launcher to query.
type SeamLister interface {
	// AllIssues returns every issue (open or closed) the tracker holds, in
	// canonical order, regardless of parent, state, or dispatch marker —
	// the auto-surface sweep's basis for discovering every distinct resolved
	// parent across a mixed batch (ADR 0033).
	AllIssues() ([]Issue, error)
}

// PriorClaimStateReader is the optional surface for adapters that can look up
// the terminal dispatch state (Complete or Failed) an issue carried
// immediately before its most recent claim onto InProgress -- state the
// claim's own ClaimRemoveLabels strip destroys from the current label set.
// agent-recover.yml claims host-side, ahead of the launcher, so by the time
// recoverByNumber sees the issue its labels already read agent-in-progress;
// this is the only route back to what came before, letting a terminal recover
// failure restore a prior agent-complete rather than downgrade it (#2477).
type PriorClaimStateReader interface {
	// PriorClaimState returns the terminal DispatchState (Complete or
	// Failed) the issue carried immediately before its most recent claim
	// onto InProgress, and whether one was found at all — false when the
	// issue's history carries no terminal-label removal (e.g. a fresh
	// dispatch that was never previously terminal).
	PriorClaimState(num string) (DispatchState, bool, error)
}

// LabeledTracker is the optional surface for adapters whose entire
// DispatchState space reduces to one DispatchLabels value (github, local, and
// the Fake test double). PickIssue's double-box guard (#1742) uses it to
// recognize a state the tracker's label family leaves unmapped -- e.g.
// research's Complete, which reaches its terminal state through verdict labels
// instead (ADR 0022) -- without paying a ListIssues round-trip that would
// false-match every open issue (GitHub ignores an empty --label filter;
// Local's frontmatter.State == "" matches every untriaged issue). Jira blends
// a per-state StatusMapping with Labels, so it keeps paying the round-trip.
type LabeledTracker interface {
	// StateLabels returns the DispatchLabels family this tracker resolves
	// DispatchState values through.
	StateLabels() DispatchLabels
}
