// Package passmachine holds the orchestrator's pure "continue to another
// pass, or stop" decision logic (issue #2548), extracted verbatim from the
// three switch statements in cmd/launcher/orchestrator/run.go: the legacy
// single loop's own decision (run.go:244-259), the review loop's
// implement/fix/land decision (run.go:355-401), and the review loop's own
// review-pass decision (run.go:461-487). Transition reproduces every case,
// string, and priority order of those three switches exactly -- zero
// behavior change -- because the decision op's Reason field and
// state.CapFired are part of a byte-for-byte-pinned op stream existing
// tests assert on. This package is deliberately I/O-free: it takes an
// Input struct and returns a Decision, with no access to cfg, state, or
// stdout, so its every transition can be table-tested without executing a
// Driver.
package passmachine

// PassKind names which of the orchestrator's five distinct pass shapes just
// finished executing (the input to a Transition call), or which one should
// run next (part of a Transition call's own Decision).
type PassKind int

const (
	// KindLegacy is the legacy single loop's own pass kind (run.go's
	// pre-#2037 run()) -- the loop that alternates BLOCK-driven passes
	// against a single prompt file, with no separate review pass.
	KindLegacy PassKind = iota
	// KindImplement is the review loop's first pass: a fresh implement
	// session against cfg.promptFile.
	KindImplement
	// KindFix is the review loop's post-review pass: another lap through
	// the same implement/fix code path, seeded with the reviewer's BLOCK
	// findings.
	KindFix
	// KindLand is the review loop's terminal pass: another lap through the
	// same implement/fix code path that finds nothing left to fix, either
	// because the prior review APPROVEd or because a cap committed the run
	// to a terminal land pass.
	KindLand
	// KindReview is the review loop's review pass: a fresh session against
	// cfg.reviewPromptFile, whose own verdict (not the implement/fix/land
	// pass's log) is what drives state.LastVerdict.
	KindReview
)

// String returns the pass_start op's own Role field value for k --
// "implement"/"fix"/"land"/"review" for the review loop's four pass kinds,
// matching run.go's own pre-#2548 implRole string literals and its review
// pass's literal "review" Role value, and "" for KindLegacy, which never
// sets Role at all (the legacy single loop has no role concept).
func (k PassKind) String() string {
	switch k {
	case KindImplement:
		return "implement"
	case KindFix:
		return "fix"
	case KindLand:
		return "land"
	case KindReview:
		return "review"
	default:
		return ""
	}
}

// Verdict is the reviewer verdict word scanned from a pass's own log, or
// the empty string when the pass never produced one.
type Verdict string

const (
	// VerdictNone means the pass's log never resolved into a verdict word
	// at all.
	VerdictNone Verdict = ""
	// VerdictBlock is the reviewer's "keep going" verdict.
	VerdictBlock Verdict = "BLOCK"
	// VerdictApprove is the reviewer's "done" verdict.
	VerdictApprove Verdict = "APPROVE"
)

// StopReason names why Transition decided to stop the loop -- the zero
// value, StopNone, is only ever returned alongside a Continue: true
// Decision (a review-pass decision, per run.go:461-487, never stops at
// all, so every KindReview Decision carries StopNone). This same type also
// names which cap fired via Decision.Cap (below) -- reusing StopReason's
// existing StopMaxSlicesReached/StopMaxReviewRoundsReached/StopNoVerdict
// constants for both purposes, since the underlying cause is the same
// whether it winds up stopping the loop outright (legacy) or committing it
// to one terminal land pass (review loop).
type StopReason int

// CapReason is StopReason under a name that doesn't say "Stop" for a
// decision that isn't stopping (Decision.Cap, below, can be non-StopNone on
// a Continue: true Decision) -- a plain alias, not a distinct type, so
// every StopMaxSlicesReached/StopMaxReviewRoundsReached/StopNoVerdict
// constant is usable as either without conversion.
type CapReason = StopReason

const (
	// StopNone means the loop is not stopping this pass -- Decision.Continue
	// is true.
	StopNone StopReason = iota
	// StopOutcomeReached fires when the pass that just ran reached its own
	// terminal SPINDRIFT_OUTCOME line.
	StopOutcomeReached
	// StopNoVerdict fires on the legacy loop's own decision when the pass's
	// log never scanned out a verdict word, and (as Decision.Cap) on the
	// review loop's own review-pass decision when a review pass never
	// resolved into a verdict word at all.
	StopNoVerdict
	// StopVerdictNotBlock fires on the legacy loop's own decision when the
	// verdict was a non-empty, non-BLOCK word (i.e. APPROVE).
	StopVerdictNotBlock
	// StopMaxSlicesReached fires when cfg.maxSlices is a positive cap and
	// the pass count has reached or exceeded it -- on the legacy loop this
	// is a hard stop; on the review loop's two decision points it instead
	// commits the run to one terminal land pass (see LandPhase).
	StopMaxSlicesReached
	// StopMaxReviewRoundsReached fires when cfg.maxReviewRounds is a
	// positive cap and reviewRounds has reached or exceeded it -- on the
	// legacy loop this is a hard stop; on the review loop's own review-pass
	// decision it instead commits the run to one terminal land pass.
	StopMaxReviewRoundsReached
	// StopBudgetExceeded fires when Caps.MaxBudgetTokens or Caps.MaxBudgetUSD
	// is a positive cap and the cumulative usage so far (Input.CumulativeTokens/
	// CumulativeUSD) has reached or exceeded it -- on the review loop's own
	// review-pass decision it, like StopMaxReviewRoundsReached, instead
	// commits the run to one terminal land pass rather than stopping outright
	// (issue #2694).
	StopBudgetExceeded
	// StopTerminalLandNoOutcome fires on the review loop's implement/fix/
	// land decision when the pass that just ran was itself the committed
	// terminal land pass (in.LandPhase was already LandPhaseTerminalCommitted
	// going in) and still produced no outcome -- the bound that caps the
	// terminal-land mechanism at exactly one extra pass.
	StopTerminalLandNoOutcome
	// StopApproveNoOutcome fires on the review loop's implement/fix/land
	// decision when the pass that just ran followed an APPROVE verdict and
	// still produced no outcome -- the bound on the land-after-APPROVE
	// mechanism.
	StopApproveNoOutcome
)

// Caps carries the orchestrator-configured budget caps a Transition decision
// may consult -- a zero value (0) for any field means that cap is disabled,
// mirroring cfg.maxSlices/cfg.maxReviewRounds's own "0 means unlimited"
// convention. Static, per-run config only -- the dynamic usage-so-far values
// these token/USD caps are compared against live on Input instead
// (Input.CumulativeTokens/CumulativeUSD), mirroring the existing
// Pass/ReviewRounds split against MaxSlices/MaxReviewRounds.
type Caps struct {
	// MaxSlices is the coarse backstop on total pass count (cfg.maxSlices).
	MaxSlices int
	// MaxReviewRounds is the cap on review rounds elapsed (cfg.maxReviewRounds).
	MaxReviewRounds int
	// MaxBudgetTokens is the cap on cumulative token usage (pre-summed across
	// input/output/cache-read/cache-creation categories by the caller, not
	// this package), compared against Input.CumulativeTokens. 0 disables this
	// dimension independently of MaxBudgetUSD (issue #2694).
	MaxBudgetTokens int
	// MaxBudgetUSD is the cap on cumulative USD cost, compared against
	// Input.CumulativeUSD. 0 disables this dimension independently of
	// MaxBudgetTokens (issue #2694).
	MaxBudgetUSD float64
}

// LandPhase names whether a prior decision has already committed this run
// to a terminal land pass (issue #2548 AC2). Transition dispatches the
// implement/fix/land decision point to one of two entirely separate
// functions based on this field alone -- terminalLandTransition once
// LandPhaseTerminalCommitted, implementFixTransition while still
// LandPhaseActive -- so the two rule sets live in physically disjoint
// functions and can never again be reordered against each other by a
// future case added to either switch.
type LandPhase int

const (
	// LandPhaseActive is the ordinary state: no prior decision has yet
	// committed this run to a terminal land pass.
	LandPhaseActive LandPhase = iota
	// LandPhaseTerminalCommitted means a prior decision -- a maxSlices cap
	// firing on the implement/fix/land decision point, or a no-verdict/
	// maxSlices/maxReviewRounds cap firing on the review-pass decision
	// point -- already committed this run to landing, regardless of the
	// PassKind label the pass that just ran happens to carry. In
	// particular, when a maxSlices cap and a manifest dispatch both fire on
	// the same pass, the NextPass label stays KindFix (for pass_start's own
	// Role display) even though LandPhase is already TerminalCommitted; the
	// caller threads this field, not PassKind, back into the next call's
	// Input.LandPhase.
	LandPhaseTerminalCommitted
)

// Input is everything a single Transition call needs to reproduce one of
// the three source switches' decisions -- no cfg, state, or I/O, so every
// case is exercisable from a table test alone.
type Input struct {
	// PassJustExecuted names which pass kind's decision point this call is
	// evaluating -- KindImplement, KindFix, and KindLand share one decision
	// point (run.go:355-401) and are treated identically.
	PassJustExecuted PassKind
	// Verdict is the verdict word scanned from the pass that just ran.
	// Meaningful for KindLegacy (the pass's own verdict) and KindReview
	// (the review pass's own reviewVerdict) only -- zero/irrelevant for
	// KindImplement/KindFix/KindLand, whose own pass log is scanned only
	// for HasOutcome.
	Verdict Verdict
	// HasOutcome is whether the pass that just ran reached its own
	// terminal SPINDRIFT_OUTCOME line. Meaningful for KindLegacy and
	// KindImplement/KindFix/KindLand only -- a review pass's own decision
	// point (run.go:461-487) never consults it.
	HasOutcome bool
	// Pass is the 1-indexed count of passes run so far, including the one
	// that just finished -- compared against Caps.MaxSlices.
	Pass int
	// ReviewRounds is the number of review rounds elapsed strictly before
	// this decision -- compared against Caps.MaxReviewRounds.
	ReviewRounds int
	// Caps carries the two orchestrator-configured budget caps.
	Caps Caps
	// LandPhase is state.TerminalLand's value going into this decision,
	// converted to the machine's own LandPhase type (before this call may
	// commit it to LandPhaseTerminalCommitted) -- see LandPhase's own doc
	// comment for how Transition dispatches on it at the implement/fix/land
	// decision point.
	LandPhase LandPhase
	// LastVerdict is state.LastVerdict going into this decision. Meaningful
	// for KindImplement/KindFix/KindLand only (the "land pass reached no
	// terminal outcome after APPROVE" check).
	LastVerdict Verdict
	// ManifestDispatched is whether this pass dispatched a slice manifest
	// to parallel workers. Meaningful for KindImplement/KindFix/KindLand
	// only.
	ManifestDispatched bool
	// CumulativeTokens is the caller's own sum of cumulative token usage so
	// far (across all four usage.Usage token categories -- this package does
	// no summing itself), compared against Caps.MaxBudgetTokens. Meaningful
	// only for KindReview's own decision point, mirroring how ReviewRounds is
	// compared against Caps.MaxReviewRounds there (issue #2694).
	CumulativeTokens int
	// CumulativeUSD is the cumulative USD cost so far, compared against
	// Caps.MaxBudgetUSD. Meaningful only for KindReview's own decision point,
	// mirroring CumulativeTokens (issue #2694).
	CumulativeUSD float64
}

// Decision is Transition's result: whether to continue into another pass
// (and if so, which kind, and what state mutations that continuation
// implies) or stop the loop outright (and why).
type Decision struct {
	// Continue is false when the loop should stop after the pass that just
	// ran; true when it should run another pass.
	Continue bool
	// Reason is the exact decision-op Reason text the source switch emits
	// for whichever case matched -- byte-identical to today's strings, or
	// the empty string for a fallthrough continue that matched no case.
	Reason string
	// Stop names why the loop is stopping -- StopNone whenever Continue is
	// true.
	Stop StopReason
	// NextPass names which pass kind runs next -- meaningful only when
	// Continue is true.
	NextPass PassKind
	// LandPhase is LandPhaseTerminalCommitted when this decision commits
	// the run to a terminal land pass (a cap firing) -- the caller persists
	// this onto its own state so a LATER call's Input.LandPhase reflects
	// it; LandPhaseActive (the zero value) otherwise.
	LandPhase LandPhase
	// CapFired is the exact state.CapFired text the source switch assigns
	// -- set only when LandPhase is LandPhaseTerminalCommitted.
	CapFired string
	// Cap is the typed counterpart to CapFired: StopNone (the zero value)
	// whenever LandPhase is LandPhaseActive, else the CapReason naming
	// which cap fired (StopMaxSlicesReached, StopMaxReviewRoundsReached, or
	// StopNoVerdict for the review pass's own "no verdict" case). Callers
	// that need to detect a specific cap programmatically (e.g. caps.go's
	// own simulateReviewRoundCapPass) compare against this instead of
	// CapFired's prose string, which doubles as operator-facing prompt text
	// (run.go's seedPromptFromState) and can be reworded independently.
	Cap CapReason
	// IncrementReviewRounds is true when this decision implies
	// reviewRounds++ (unconditional on KindLegacy's own continue path;
	// gated on reviewVerdict == BLOCK, regardless of which case matched,
	// on KindReview's own decision point).
	IncrementReviewRounds bool
}

// Transition reproduces exactly one of the three orchestrator decision
// switches, chosen by in.PassJustExecuted -- KindImplement, KindFix, and
// KindLand all share the same implement/fix/land decision point, which
// itself dispatches on in.LandPhase (issue #2548 AC2) between
// implementFixTransition and terminalLandTransition.
func Transition(in Input) Decision {
	switch in.PassJustExecuted {
	case KindLegacy:
		return legacyTransition(in)
	case KindReview:
		return reviewTransition(in)
	default:
		// KindImplement, KindFix, KindLand.
		if in.LandPhase == LandPhaseTerminalCommitted {
			return terminalLandTransition(in)
		}
		return implementFixTransition(in)
	}
}

// legacyTransition reproduces run.go:244-259, the legacy single loop's own
// decision after each pass.
func legacyTransition(in Input) Decision {
	switch {
	case in.HasOutcome:
		return Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached}
	case in.Verdict == VerdictNone:
		return Decision{Continue: false, Reason: "no verdict", Stop: StopNoVerdict}
	case in.Verdict != VerdictBlock:
		return Decision{Continue: false, Reason: "verdict not BLOCK", Stop: StopVerdictNotBlock}
	case in.Caps.MaxSlices > 0 && in.Pass >= in.Caps.MaxSlices:
		return Decision{Continue: false, Reason: "max slices reached", Stop: StopMaxSlicesReached}
	case in.Caps.MaxReviewRounds > 0 && in.ReviewRounds >= in.Caps.MaxReviewRounds:
		return Decision{Continue: false, Reason: "max review rounds reached", Stop: StopMaxReviewRoundsReached}
	}
	// Only reachable when Verdict == BLOCK, HasOutcome is false, and
	// neither cap fired: reviewRounds++ unconditionally, loop continues
	// with the same single pass kind.
	return Decision{Continue: true, Reason: "", NextPass: KindLegacy, IncrementReviewRounds: true}
}

// terminalLandTransition reproduces the in.LandPhase ==
// LandPhaseTerminalCommitted half of what was previously a single switch at
// run.go:355-401 (issue #2548 AC2): once a prior decision has already
// committed this run to a terminal land pass, nothing else about the pass
// that just ran matters except whether it finally reached its own outcome.
// Kept as its own function, physically disjoint from implementFixTransition,
// so the two rule sets can never again be reordered against each other by a
// future case added to either one.
func terminalLandTransition(in Input) Decision {
	if in.HasOutcome {
		return Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached}
	}
	return Decision{Continue: false, Reason: "terminal land pass reached no outcome", Stop: StopTerminalLandNoOutcome}
}

// implementFixTransition reproduces the in.LandPhase == LandPhaseActive half
// of what was previously a single switch at run.go:355-401 (issue #2548
// AC2): the ordinary implement/fix/land decision rules (APPROVE, maxSlices,
// manifest dispatch). It deliberately carries NO terminal-land case -- once
// a prior decision commits this run to landing, Transition dispatches to
// terminalLandTransition instead, so that commitment's own rule lives
// somewhere this switch's case order can never reprioritize against it.
func implementFixTransition(in Input) Decision {
	switch {
	case in.HasOutcome:
		return Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached}
	// After an APPROVE verdict the land pass runs exactly once: a land pass
	// cut off before its own terminal SPINDRIFT_OUTCOME is recovered by the
	// within-pass required_marker_gate session-resume nudge (issue #2044,
	// agent/entrypoint.sh) inside that single land driver-exec, not by
	// re-entering this decision again -- a fresh land pass would re-invoke
	// the Filer / FILE ISSUES step on every extra lap, bounded only by the
	// coarse maxSlices cap below (issue #2069).
	case in.LastVerdict == VerdictApprove:
		return Decision{Continue: false, Reason: "land pass reached no terminal outcome after APPROVE", Stop: StopApproveNoOutcome}
	// This case must come before ManifestDispatched below: maxSlices is a
	// hard ceiling on total driver-exec invocations (issue #2457), and a
	// coordinator that re-emits a slice manifest every single pass must not
	// be able to keep matching ManifestDispatched first forever and defeat
	// that ceiling (issue #2058 review).
	case in.Caps.MaxSlices > 0 && in.Pass >= in.Caps.MaxSlices:
		// manifestDispatched is checked regardless of which case fired the
		// continue -- so even when this maxSlices case is what committed
		// LandPhase to LandPhaseTerminalCommitted, if ManifestDispatched is
		// ALSO true this same pass, the next pass is still Fix, not Land;
		// LandPhase stays TerminalCommitted in the returned Decision (the
		// caller persists it onto state) and is caught by
		// terminalLandTransition the pass after next.
		next := KindLand
		if in.ManifestDispatched {
			next = KindFix
		}
		return Decision{
			Continue:  true,
			Reason:    "max slices reached; running terminal land pass",
			NextPass:  next,
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopMaxSlicesReached,
			CapFired:  "max slices reached",
		}
	// A manifest dispatch keeps the loop going when neither case above
	// already fired this pass -- a pass that just dispatched workers isn't
	// done yet regardless of what verdict state happens to be sitting
	// around from a prior pass (issue #2059 AC1).
	case in.ManifestDispatched:
		return Decision{Continue: true, Reason: "slice manifest dispatched", NextPass: KindFix}
	}
	// No case matched: falls through to entering the review pass.
	return Decision{Continue: true, Reason: "", NextPass: KindReview}
}

// budgetExceeded reports whether tokens or usd has reached or exceeded
// either of caps.MaxBudgetTokens/caps.MaxBudgetUSD -- deliberately duplicated
// from settle's own budgetExceeded (cmd/launcher/internal/settle/budget.go)
// rather than imported, to keep this package dependency-free of settle. Same
// "0 disables this dimension, independently of the other" convention as
// every other cap in this file: a zero cap never fires regardless of tokens
// or usd, and either dimension alone can trip it.
func budgetExceeded(caps Caps, tokens int, usd float64) bool {
	if caps.MaxBudgetTokens > 0 && tokens >= caps.MaxBudgetTokens {
		return true
	}
	if caps.MaxBudgetUSD > 0 && usd >= caps.MaxBudgetUSD {
		return true
	}
	return false
}

// reviewTransition reproduces run.go:461-487, the review loop's own
// decision after a review pass. Every branch of this decision continues --
// there is no case that stops the loop; that is existing, deliberate
// behavior (a review pass alone never stops the run).
func reviewTransition(in Input) Decision {
	var d Decision
	switch {
	case in.Verdict == VerdictNone:
		d = Decision{
			Continue:  true,
			Reason:    "no verdict; running terminal land pass",
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopNoVerdict,
			CapFired:  "no verdict",
		}
	case in.Caps.MaxSlices > 0 && in.Pass >= in.Caps.MaxSlices:
		d = Decision{
			Continue:  true,
			Reason:    "max slices reached; running terminal land pass",
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopMaxSlicesReached,
			CapFired:  "max slices reached",
		}
	case in.Verdict == VerdictBlock && in.Caps.MaxReviewRounds > 0 && in.ReviewRounds >= in.Caps.MaxReviewRounds:
		d = Decision{
			Continue:  true,
			Reason:    "max review rounds reached; running terminal land pass",
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopMaxReviewRoundsReached,
			CapFired:  "max review rounds reached",
		}
	// This case must come last among the caps above: when a budget cap and
	// an earlier cap (no-verdict, maxSlices, maxReviewRounds) both fire on
	// the same pass, the earlier cap's Reason/CapFired/Cap keep reporting
	// priority over this one -- the same ordering rule as the
	// ManifestDispatched case in implementFixTransition above.
	case in.Verdict == VerdictBlock && budgetExceeded(in.Caps, in.CumulativeTokens, in.CumulativeUSD):
		d = Decision{
			Continue:  true,
			Reason:    "budget exceeded; running terminal land pass",
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopBudgetExceeded,
			CapFired:  "budget exceeded",
		}
	default:
		d = Decision{Continue: true, Reason: ""}
	}

	// Unconditional, regardless of which case fired: a BLOCK verdict
	// increments reviewRounds, even for a capped BLOCK.
	if in.Verdict == VerdictBlock {
		d.IncrementReviewRounds = true
	}

	// LandPhase already TerminalCommitted from before, or just committed
	// above by this decision: next pass kind is Land; else Fix.
	if in.LandPhase == LandPhaseTerminalCommitted || d.LandPhase == LandPhaseTerminalCommitted {
		d.NextPass = KindLand
	} else {
		d.NextPass = KindFix
	}

	return d
}
