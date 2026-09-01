// Package passmachine holds the orchestrator's pure "continue to another
// pass, or stop" decision logic for its three decision points: the legacy
// single loop, the review loop's implement/fix/land pass, and the review
// pass itself.
//
// Decision.CapFired feeds a pinned op stream tests assert on, so its text
// is not free to change. The package is deliberately I/O-free -- Input in,
// Decision out, no cfg/state/stdout -- so every transition is table-testable
// without executing a Driver.
package passmachine

import (
	"fmt"
	"strings"
)

// PassKind names which of the orchestrator's five distinct pass shapes just
// finished executing (the input to a Transition call), or which one should
// run next (part of a Transition call's own Decision).
type PassKind int

const (
	// KindLegacy alternates BLOCK-driven passes against a single prompt
	// file, with no separate review pass.
	KindLegacy PassKind = iota
	// KindImplement is the review loop's first pass, against cfg.promptFile.
	KindImplement
	// KindFix is the post-review pass, seeded with the reviewer's BLOCK
	// findings.
	KindFix
	// KindLand is the terminal pass, reached after an APPROVE or once a cap
	// committed the run to landing. It can still edit -- it is seeded with
	// non-blocking findings and told to fix cheap ones inline -- but the
	// role names what makes it terminal, not those incidental edits.
	KindLand
	// KindReview is the review pass, whose verdict (not the implement/fix/
	// land pass's log) drives state.LastVerdict.
	KindReview
)

// Role is the string form of a pass's role, as sent in the pass_start op's
// Role field. Kept a plain string so this package need not import
// driver/claude's transcript.go.
type Role string

// KindLegacy never sets Role.
const (
	RoleImplement Role = "implement"
	RoleReview    Role = "review"
	RoleFix       Role = "fix"
	RoleLand      Role = "land"
)

// String returns k's pass_start Role value, or "" for KindLegacy, which has
// no role concept.
func (k PassKind) String() string {
	switch k {
	case KindImplement:
		return string(RoleImplement)
	case KindFix:
		return string(RoleFix)
	case KindLand:
		return string(RoleLand)
	case KindReview:
		return string(RoleReview)
	default:
		return ""
	}
}

// Verdict is the reviewer verdict word scanned from a pass's own log, or
// the empty string when the pass never produced one.
type Verdict string

const (
	// VerdictNone means the log never resolved into a verdict word.
	VerdictNone Verdict = ""
	// VerdictBlock is the reviewer's "keep going" verdict.
	VerdictBlock Verdict = "BLOCK"
	// VerdictApprove is the reviewer's "done" verdict.
	VerdictApprove Verdict = "APPROVE"
)

// StopReason names why Transition stopped the loop; StopNone accompanies
// every Continue: true Decision. The same type doubles as Decision.Cap's
// which-cap-fired name: the underlying cause is identical whether it stops
// the loop outright (legacy) or commits it to one terminal land pass
// (review loop).
type StopReason int

// CapReason is StopReason under a name that doesn't say "Stop", for
// Decision.Cap, which can be non-StopNone on a Continue: true Decision. An
// alias, not a distinct type, so the constants serve both without
// conversion.
type CapReason = StopReason

const (
	// StopNone means the loop is not stopping this pass -- Decision.Continue
	// is true.
	StopNone StopReason = iota
	// StopOutcomeReached fires when the pass that just ran reached its own
	// terminal SPINDRIFT_OUTCOME line.
	StopOutcomeReached
	// StopNoVerdict fires when a pass's log never scanned out a verdict word.
	StopNoVerdict
	// StopVerdictNotBlock fires on the legacy loop when the verdict was a
	// non-empty, non-BLOCK word.
	StopVerdictNotBlock
	// StopMaxSlicesReached fires when a positive cfg.maxSlices is reached --
	// a hard stop on the legacy loop, but on the review loop's two decision
	// points it instead commits the run to one terminal land pass.
	StopMaxSlicesReached
	// StopMaxReviewRoundsReached fires when a positive cfg.maxReviewRounds
	// is reached, with the same legacy-stops/review-lands split.
	StopMaxReviewRoundsReached
	// StopTerminalLandNoOutcome fires when the committed terminal land pass
	// itself produced no outcome -- the bound capping the terminal-land
	// mechanism at exactly one extra pass.
	StopTerminalLandNoOutcome
	// StopApproveNoOutcome fires when the pass following an APPROVE produced
	// no outcome -- the bound on the land-after-APPROVE mechanism.
	StopApproveNoOutcome
	// StopBudgetExceeded fires when a positive Caps.MaxBudgetTokens or
	// Caps.MaxBudgetUSD is reached, on a BLOCK verdict only -- same gating
	// as StopMaxReviewRoundsReached, since both cap a further review round,
	// which only BLOCK triggers. Append new reasons here rather than
	// inserting above, to keep existing ordinals stable.
	StopBudgetExceeded
)

// Caps carries the orchestrator's per-run budget caps. Zero disables a cap,
// each dimension independently. Static config only: the usage-so-far values
// these are compared against live on Input, mirroring the Pass/ReviewRounds
// split against MaxSlices/MaxReviewRounds.
type Caps struct {
	// MaxSlices is the coarse backstop on total pass count.
	MaxSlices int
	// MaxReviewRounds caps review rounds elapsed.
	MaxReviewRounds int
	// MaxBudgetTokens caps cumulative tokens, pre-summed across usage
	// categories by the caller rather than this package.
	MaxBudgetTokens int
	// MaxBudgetUSD caps cumulative USD cost.
	MaxBudgetUSD float64
}

// LandPhase names whether a prior decision has already committed this run to
// a terminal land pass. Transition dispatches the implement/fix/land point
// on this field alone, into two physically disjoint functions, so a future
// case added to either switch can never reorder the two rule sets against
// each other.
type LandPhase int

const (
	// LandPhaseActive is the ordinary state: not yet committed to landing.
	LandPhaseActive LandPhase = iota
	// LandPhaseTerminalCommitted means a cap already committed this run to
	// landing, regardless of the PassKind the pass that just ran carries.
	// The caller threads this field, not PassKind, into the next call's
	// Input.LandPhase.
	LandPhaseTerminalCommitted
)

// Input is everything one Transition call needs -- no cfg, state, or I/O,
// so every case is exercisable from a table test alone.
type Input struct {
	// PassJustExecuted selects the decision point. KindImplement, KindFix
	// and KindLand share one and are treated identically.
	PassJustExecuted PassKind
	// Verdict is the verdict word scanned from the pass that just ran.
	// Meaningful for KindLegacy and KindReview only.
	Verdict Verdict
	// HasOutcome reports whether the pass reached its terminal
	// SPINDRIFT_OUTCOME line. The review pass's decision point ignores it.
	HasOutcome bool
	// Pass is the 1-indexed count of passes run so far, including the one
	// that just finished.
	Pass int
	// ReviewRounds is the rounds elapsed strictly before this decision.
	ReviewRounds int
	Caps         Caps
	// LandPhase is state.TerminalLand going in, before this call may itself
	// commit it.
	LandPhase LandPhase
	// LastVerdict is state.LastVerdict going in, consulted only by the
	// implement/fix/land point's post-APPROVE check.
	LastVerdict Verdict
	// CumulativeTokens is the caller's sum across all usage categories;
	// this package does no summing. Consulted only by KindReview.
	CumulativeTokens int
	// CumulativeUSD is the cumulative cost, likewise KindReview-only.
	CumulativeUSD float64
}

// Decision is Transition's result: whether to continue into another pass
// (and if so, which kind, and what state mutations that continuation
// implies) or stop the loop outright (and why).
type Decision struct {
	// Continue is false when the loop should stop after the pass that just
	// ran; true when it should run another pass.
	Continue bool
	// Reason is the decision-op Reason text, or empty for a fallthrough
	// continue that matched no case.
	Reason string
	// Stop names why the loop is stopping -- StopNone whenever Continue.
	Stop StopReason
	// NextPass is meaningful only when Continue.
	NextPass PassKind
	// LandPhase is LandPhaseTerminalCommitted when this decision commits the
	// run to a terminal land pass. The caller persists it so a later call's
	// Input.LandPhase reflects it.
	LandPhase LandPhase
	// CapFired is the state.CapFired prose, set only alongside
	// LandPhaseTerminalCommitted. It doubles as operator-facing prompt text
	// and may be reworded; compare Cap instead when detecting a specific cap
	// programmatically.
	CapFired string
	// Cap is CapFired's typed counterpart, StopNone while LandPhaseActive.
	Cap CapReason
	// IncrementReviewRounds implies reviewRounds++ -- unconditional on
	// KindLegacy's continue path, gated on a BLOCK verdict for KindReview.
	IncrementReviewRounds bool
}

// Transition picks a decision point from in.PassJustExecuted. The shared
// implement/fix/land point dispatches further on in.LandPhase.
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

// legacyTransition is the legacy single loop's decision after each pass.
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
	// Only reachable on BLOCK with no outcome and no cap fired.
	return Decision{Continue: true, Reason: "blocked, running another pass", NextPass: KindLegacy, IncrementReviewRounds: true}
}

// terminalLandTransition is the LandPhaseTerminalCommitted half of the
// implement/fix/land point: once committed to landing, nothing about the
// pass that just ran matters except whether it reached its outcome. Kept
// physically disjoint from implementFixTransition so a future case added to
// either can never reorder the two rule sets against each other.
func terminalLandTransition(in Input) Decision {
	if in.HasOutcome {
		return Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached}
	}
	return Decision{Continue: false, Reason: "terminal land pass reached no outcome", Stop: StopTerminalLandNoOutcome}
}

// implementFixTransition is the LandPhaseActive half: the ordinary APPROVE
// and maxSlices rules. It deliberately carries no terminal-land case, so
// this switch's ordering can never reprioritize against that commitment.
func implementFixTransition(in Input) Decision {
	switch {
	case in.HasOutcome:
		return Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached}
	// After an APPROVE the land pass runs exactly once. A land pass cut off
	// before its outcome is recovered by the within-pass marker-gate resume
	// nudge inside that same driver-exec, not by re-entering here: a fresh
	// land pass would re-invoke the Filer on every extra lap.
	case in.LastVerdict == VerdictApprove:
		return Decision{Continue: false, Reason: "land pass reached no terminal outcome after APPROVE", Stop: StopApproveNoOutcome}
	// maxSlices is a hard ceiling on total driver-exec invocations.
	case in.Caps.MaxSlices > 0 && in.Pass >= in.Caps.MaxSlices:
		return Decision{
			Continue:  true,
			Reason:    "max slices reached; running terminal land pass",
			NextPass:  KindLand,
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopMaxSlicesReached,
			CapFired:  "max slices reached",
		}
	}
	// No case matched: falls through to entering the review pass.
	return Decision{Continue: true, Reason: "no cap fired, entering review pass", NextPass: KindReview}
}

// budgetExceeded reports whether either budget dimension tripped, and a
// reason naming which. Deliberately duplicated from settle's identical
// helper rather than imported, to keep this package free of that dependency.
func budgetExceeded(caps Caps, tokens int, usd float64) (bool, string) {
	var reasons []string
	if caps.MaxBudgetTokens > 0 && tokens >= caps.MaxBudgetTokens {
		reasons = append(reasons, fmt.Sprintf("%d tokens >= cap %d", tokens, caps.MaxBudgetTokens))
	}
	if caps.MaxBudgetUSD > 0 && usd >= caps.MaxBudgetUSD {
		reasons = append(reasons, fmt.Sprintf("$%.4f >= cap $%.4f", usd, caps.MaxBudgetUSD))
	}
	if len(reasons) == 0 {
		return false, ""
	}
	return true, strings.Join(reasons, "; ")
}

// reviewTransition is the decision after a review pass. Every branch
// continues: deliberately, a review pass alone never stops the run.
func reviewTransition(in Input) Decision {
	var d Decision
	alreadyCommitted := in.LandPhase == LandPhaseTerminalCommitted
	budgetHit, budgetReason := budgetExceeded(in.Caps, in.CumulativeTokens, in.CumulativeUSD)
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
	// Must come last among the caps: when a budget cap and an earlier cap
	// both fire on the same pass, the earlier one keeps reporting priority.
	case in.Verdict == VerdictBlock && budgetHit:
		d = Decision{
			Continue:  true,
			Reason:    "budget exceeded; running terminal land pass",
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopBudgetExceeded,
			CapFired:  "budget exceeded (" + budgetReason + ")",
		}
	case in.Verdict == VerdictApprove:
		// A plain APPROVE deliberately does not stop the run: the
		// implement/fix pass it followed was told to stop right after
		// COMMIT, so at approval the work is committed but not pushed, has
		// no PR, and produced no outcome -- one terminal pass lands it.
		d = Decision{Continue: true, Reason: "approved, running the land pass"}
	default:
		// A plain BLOCK with no cap hit normally needs another fix pass --
		// but when already committed on entry, the dispatch below routes to
		// KindLand, so the Reason must say so too.
		reason := "blocked, running another fix pass"
		if alreadyCommitted {
			reason = "blocked, but the run is already committed to the terminal land pass; running it anyway"
		}
		d = Decision{Continue: true, Reason: reason}
	}

	// A BLOCK increments reviewRounds regardless of which case fired, even
	// a capped BLOCK.
	if in.Verdict == VerdictBlock {
		d.IncrementReviewRounds = true
	}

	// Committed before, committed just now, or a plain APPROVE (always
	// "nothing left to fix, land it", even with no cap in play).
	if alreadyCommitted || d.LandPhase == LandPhaseTerminalCommitted || in.Verdict == VerdictApprove {
		d.NextPass = KindLand
	} else {
		d.NextPass = KindFix
	}

	return d
}
