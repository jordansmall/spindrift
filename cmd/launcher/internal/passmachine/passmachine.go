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
// all, so every KindReview Decision carries StopNone).
type StopReason int

const (
	// StopNone means the loop is not stopping this pass -- Decision.Continue
	// is true.
	StopNone StopReason = iota
	// StopOutcomeReached fires when the pass that just ran reached its own
	// terminal SPINDRIFT_OUTCOME line.
	StopOutcomeReached
	// StopNoVerdict fires on the legacy loop's own decision when the pass's
	// log never scanned out a verdict word.
	StopNoVerdict
	// StopVerdictNotBlock fires on the legacy loop's own decision when the
	// verdict was a non-empty, non-BLOCK word (i.e. APPROVE).
	StopVerdictNotBlock
	// StopMaxSlicesReached fires when cfg.maxSlices is a positive cap and
	// the pass count has reached or exceeded it -- on the legacy loop this
	// is a hard stop; on the review loop's two decision points it instead
	// commits the run to one terminal land pass (see SetTerminalLand).
	StopMaxSlicesReached
	// StopMaxReviewRoundsReached fires when cfg.maxReviewRounds is a
	// positive cap and reviewRounds has reached or exceeded it -- on the
	// legacy loop this is a hard stop; on the review loop's own review-pass
	// decision it instead commits the run to one terminal land pass.
	StopMaxReviewRoundsReached
	// StopTerminalLandNoOutcome fires on the review loop's implement/fix/
	// land decision when the pass that just ran was itself the committed
	// terminal land pass (state.TerminalLand was already true going in) and
	// still produced no outcome -- the bound that caps the terminal-land
	// mechanism at exactly one extra pass.
	StopTerminalLandNoOutcome
	// StopApproveNoOutcome fires on the review loop's implement/fix/land
	// decision when the pass that just ran followed an APPROVE verdict and
	// still produced no outcome -- the bound on the land-after-APPROVE
	// mechanism.
	StopApproveNoOutcome
)

// Caps carries the two orchestrator-configured budget caps a Transition
// decision may consult -- a zero value (0) for either field means that cap
// is disabled, mirroring cfg.maxSlices/cfg.maxReviewRounds's own "0 means
// unlimited" convention.
type Caps struct {
	// MaxSlices is the coarse backstop on total pass count (cfg.maxSlices).
	MaxSlices int
	// MaxReviewRounds is the cap on review rounds elapsed (cfg.maxReviewRounds).
	MaxReviewRounds int
}

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
	// TerminalLand is state.TerminalLand's value going into this decision
	// (before this call may set it).
	TerminalLand bool
	// LastVerdict is state.LastVerdict going into this decision. Meaningful
	// for KindImplement/KindFix/KindLand only (the "land pass reached no
	// terminal outcome after APPROVE" check).
	LastVerdict Verdict
	// ManifestDispatched is whether this pass dispatched a slice manifest
	// to parallel workers. Meaningful for KindImplement/KindFix/KindLand
	// only.
	ManifestDispatched bool
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
	// SetTerminalLand is true when this decision sets state.TerminalLand =
	// true (a cap committing the run to one terminal land pass).
	SetTerminalLand bool
	// CapFired is the exact state.CapFired text the source switch assigns
	// -- set only when SetTerminalLand is true.
	CapFired string
	// IncrementReviewRounds is true when this decision implies
	// reviewRounds++ (unconditional on KindLegacy's own continue path;
	// gated on reviewVerdict == BLOCK, regardless of which case matched,
	// on KindReview's own decision point).
	IncrementReviewRounds bool
}

// Transition reproduces exactly one of the three orchestrator decision
// switches, chosen by in.PassJustExecuted -- KindImplement, KindFix, and
// KindLand all share the same implement/fix/land decision point.
func Transition(in Input) Decision {
	switch in.PassJustExecuted {
	case KindLegacy:
		return legacyTransition(in)
	case KindReview:
		return reviewTransition(in)
	default:
		// KindImplement, KindFix, KindLand.
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

// implementFixTransition reproduces run.go:355-401, the review loop's
// decision after an implement, fix, or land pass -- all three share this
// exact code path in run.go today.
func implementFixTransition(in Input) Decision {
	switch {
	case in.HasOutcome:
		return Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached}
	case in.TerminalLand:
		return Decision{Continue: false, Reason: "terminal land pass reached no outcome", Stop: StopTerminalLandNoOutcome}
	case in.LastVerdict == VerdictApprove:
		return Decision{Continue: false, Reason: "land pass reached no terminal outcome after APPROVE", Stop: StopApproveNoOutcome}
	case in.Caps.MaxSlices > 0 && in.Pass >= in.Caps.MaxSlices:
		// manifestDispatched is checked regardless of which case fired the
		// continue -- so even when this maxSlices case is what set
		// TerminalLand, if ManifestDispatched is ALSO true this same pass,
		// the next pass is still Fix, not Land; TerminalLand stays true in
		// the returned Decision and is caught by the "TerminalLand: stop"
		// case the pass after next.
		next := KindLand
		if in.ManifestDispatched {
			next = KindFix
		}
		return Decision{
			Continue:        true,
			Reason:          "max slices reached; running terminal land pass",
			NextPass:        next,
			SetTerminalLand: true,
			CapFired:        "max slices reached",
		}
	case in.ManifestDispatched:
		return Decision{Continue: true, Reason: "slice manifest dispatched", NextPass: KindFix}
	}
	// No case matched: falls through to entering the review pass.
	return Decision{Continue: true, Reason: "", NextPass: KindReview}
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
			Continue:        true,
			Reason:          "no verdict; running terminal land pass",
			SetTerminalLand: true,
			CapFired:        "no verdict",
		}
	case in.Caps.MaxSlices > 0 && in.Pass >= in.Caps.MaxSlices:
		d = Decision{
			Continue:        true,
			Reason:          "max slices reached; running terminal land pass",
			SetTerminalLand: true,
			CapFired:        "max slices reached",
		}
	case in.Verdict == VerdictBlock && in.Caps.MaxReviewRounds > 0 && in.ReviewRounds >= in.Caps.MaxReviewRounds:
		d = Decision{
			Continue:        true,
			Reason:          "max review rounds reached; running terminal land pass",
			SetTerminalLand: true,
			CapFired:        "max review rounds reached",
		}
	default:
		d = Decision{Continue: true, Reason: ""}
	}

	// Unconditional, regardless of which case fired: a BLOCK verdict
	// increments reviewRounds, even for a capped BLOCK.
	if in.Verdict == VerdictBlock {
		d.IncrementReviewRounds = true
	}

	// TerminalLand true either from before, or just set above by this
	// decision: next pass kind is Land; else Fix.
	if in.TerminalLand || d.SetTerminalLand {
		d.NextPass = KindLand
	} else {
		d.NextPass = KindFix
	}

	return d
}
