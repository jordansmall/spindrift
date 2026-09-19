// Package passmachine holds the orchestrator's pure "continue to another
// pass, or stop" decision logic (issue #2548), extracted from three switch
// statements in cmd/launcher/orchestrator/run.go. It takes no cfg, state or
// I/O, so every transition is table-testable. Decision.CapFired feeds a
// byte-for-byte pinned op stream tests assert on; Reason is not pinned (#2655).
package passmachine

import (
	"fmt"
	"strings"
)

// PassKind names which pass shape just finished, or which one runs next.
type PassKind int

const (
	// KindLegacy is the legacy single loop's pass kind (run.go's pre-#2037
	// run()): BLOCK-driven passes against one prompt file, no review pass.
	KindLegacy PassKind = iota
	// KindImplement is the review loop's first pass, against cfg.promptFile.
	KindImplement
	// KindFix is the review loop's post-review pass, seeded with the
	// reviewer's BLOCK findings.
	KindFix
	// KindLand is the review loop's terminal pass, reached after an APPROVE
	// or after a cap committed the run to landing. It still makes edits: its
	// prompt carries the reviewer's non-blocking findings.
	KindLand
	// KindReview is the review loop's review pass, against
	// cfg.reviewPromptFile. Its verdict, not the implement/fix/land pass's
	// log, drives state.LastVerdict.
	KindReview
	// KindDeltaReview is the run-once review pass (issue #3246) that re-checks
	// only the delta a settled terminal land pass introduced. Unlike
	// KindReview, neither BLOCK nor APPROVE schedules another pass, so it
	// never loops (see deltaReviewTransition).
	KindDeltaReview
)

// Role is the string form of a pass's role, as sent in the pass_start op's
// Role field.
type Role string

// KindLegacy never sets Role.
const (
	// RoleImplement is the implement pass's Role.
	RoleImplement Role = "implement"
	// RoleReview is the review pass's Role.
	RoleReview Role = "review"
	// RoleFix is the fix pass's Role.
	RoleFix Role = "fix"
	// RoleLand is the terminal land pass's Role.
	RoleLand Role = "land"
	// RoleDeltaReview is the delta-review pass's Role.
	RoleDeltaReview Role = "delta-review"
)

// String returns the pass_start op's Role field value for k, and "" for
// KindLegacy, which never sets Role at all.
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
	case KindDeltaReview:
		return string(RoleDeltaReview)
	default:
		return ""
	}
}

// ManifestKind returns the pass-manifest entry's Kind field value for k:
// "legacy" for KindLegacy, else String(). Separate from String() because the
// pass_start op's Role must stay blank for the legacy loop, while the
// manifest's Kind must always name which pass shape ran.
func (k PassKind) ManifestKind() string {
	if k == KindLegacy {
		return "legacy"
	}
	return k.String()
}

// Verdict is the reviewer verdict word scanned from a pass's own log, or
// the empty string when the pass never produced one.
type Verdict string

const (
	// VerdictNone means the pass's log never resolved into a verdict word.
	VerdictNone Verdict = ""
	// VerdictBlock is the reviewer's "keep going" verdict.
	VerdictBlock Verdict = "BLOCK"
	// VerdictApprove is the reviewer's "done" verdict.
	VerdictApprove Verdict = "APPROVE"
)

// StopReason names why Transition decided to stop the loop. StopNone is
// returned only alongside a Continue: true Decision. Decision.Cap reuses this
// same type to name which cap fired, since the cause is the same whether it
// stops the loop (legacy) or commits it to one terminal land pass (review).
type StopReason int

// CapReason is StopReason under a name that doesn't say "Stop", for
// Decision.Cap on a decision that is continuing. A plain alias, so every
// constant below works as either without conversion.
type CapReason = StopReason

const (
	// StopNone means the loop is not stopping this pass.
	StopNone StopReason = iota
	// StopOutcomeReached fires when the pass that just ran reached its
	// terminal SPINDRIFT_OUTCOME line.
	StopOutcomeReached
	// StopNoVerdict fires on the legacy decision when the pass's log scanned
	// out no verdict word, and as Decision.Cap on the review-pass decision
	// for the same case.
	StopNoVerdict
	// StopVerdictNotBlock fires on the legacy decision when the verdict was
	// non-empty and not BLOCK.
	StopVerdictNotBlock
	// StopMaxSlicesReached fires when cfg.maxSlices is positive and the pass
	// count has reached it. A hard stop on the legacy loop; on the review
	// loop it commits the run to one terminal land pass (see LandPhase).
	StopMaxSlicesReached
	// StopMaxReviewRoundsReached fires when cfg.maxReviewRounds is positive
	// and reviewRounds has reached it. A hard stop on the legacy loop; on the
	// review-pass decision it commits the run to one terminal land pass.
	StopMaxReviewRoundsReached
	// StopTerminalLandNoOutcome fires when the committed terminal land pass
	// itself produced no outcome. It bounds the terminal-land mechanism at
	// exactly one extra pass.
	StopTerminalLandNoOutcome
	// StopApproveNoOutcome fires when the pass following an APPROVE verdict
	// produced no outcome. It bounds the land-after-APPROVE mechanism.
	StopApproveNoOutcome
	// StopBudgetExceeded fires when Caps.MaxBudgetTokens or Caps.MaxBudgetUSD
	// is positive and cumulative usage has reached it, on a BLOCK verdict
	// only, because it caps a further review round. On the review-pass
	// decision it commits the run to a terminal land pass (issue #2694).
	StopBudgetExceeded
	// StopDeltaReviewBlocked fires on a BLOCK delta-review verdict (issue
	// #3246): the run stops outright with no further fix lap, unlike a
	// KindReview BLOCK, which schedules KindFix.
	StopDeltaReviewBlocked
	// StopDeltaReviewApproved fires on an APPROVE delta-review verdict: the
	// run settles as usual.
	StopDeltaReviewApproved
	// StopDeltaReviewNoVerdict fires when the delta-review pass produced no
	// verdict. It fails open to the same settle-as-usual outcome as
	// StopDeltaReviewApproved, because a malfunctioning extra gate must not
	// strand a run the review pass already approved.
	StopDeltaReviewNoVerdict
)

// Caps carries the orchestrator-configured caps a Transition decision may
// consult. A zero value disables that cap, matching cfg.maxSlices's "0 means
// unlimited" convention. Static per-run config only: the usage-so-far values
// these caps are compared against live on Input.
type Caps struct {
	// MaxSlices is the coarse backstop on total pass count (cfg.maxSlices).
	MaxSlices int
	// MaxReviewRounds is the cap on review rounds elapsed (cfg.maxReviewRounds).
	MaxReviewRounds int
	// MaxBudgetTokens is the cap on cumulative token usage, which the caller
	// sums across usage categories, not this package. 0 disables this
	// dimension independently of MaxBudgetUSD (issue #2694).
	MaxBudgetTokens int
	// MaxBudgetUSD is the cap on cumulative USD cost. 0 disables this
	// dimension independently of MaxBudgetTokens (issue #2694).
	MaxBudgetUSD float64
}

// LandPhase names whether a prior decision has already committed this run to
// a terminal land pass (issue #2548 AC2). Transition dispatches the
// implement/fix/land decision on this field alone, so terminalLandTransition
// and implementFixTransition can never be reordered against each other.
type LandPhase int

const (
	// LandPhaseActive is the ordinary state: nothing has committed this run
	// to a terminal land pass yet.
	LandPhaseActive LandPhase = iota
	// LandPhaseTerminalCommitted means a cap firing on an earlier decision
	// already committed this run to landing, whatever PassKind label the
	// pass that just ran carries. The caller threads this field, not
	// PassKind, back into the next call's Input.LandPhase.
	LandPhaseTerminalCommitted
)

// Input is everything a single Transition call needs, with no cfg, state or
// I/O, so every case is exercisable from a table test alone.
type Input struct {
	// PassJustExecuted names which decision point this call is evaluating.
	// KindImplement, KindFix and KindLand share one, treated identically.
	PassJustExecuted PassKind
	// Verdict is the verdict word scanned from the pass that just ran.
	// Meaningful for KindLegacy and KindReview only; an implement, fix or
	// land pass log is scanned only for HasOutcome.
	Verdict Verdict
	// HasOutcome is whether the pass that just ran reached its terminal
	// SPINDRIFT_OUTCOME line. A review pass's decision never consults it.
	HasOutcome bool
	// Pass is the 1-indexed count of passes run so far, including the one
	// that just finished, compared against Caps.MaxSlices.
	Pass int
	// ReviewRounds is the number of review rounds elapsed strictly before
	// this decision, compared against Caps.MaxReviewRounds.
	ReviewRounds int
	// Caps are the static per-run caps this decision checks against.
	Caps Caps
	// LandPhase is state.TerminalLand's value going into this decision,
	// before this call may commit it to LandPhaseTerminalCommitted.
	LandPhase LandPhase
	// LastVerdict is state.LastVerdict going into this decision. Meaningful
	// for KindImplement/KindFix/KindLand only, for the "land pass reached no
	// terminal outcome after APPROVE" check.
	LastVerdict Verdict
	// CumulativeTokens is the caller's sum of token usage so far across the
	// four usage.Usage categories; this package does no summing. Meaningful
	// only for KindReview's decision point (issue #2694).
	CumulativeTokens int
	// CumulativeUSD is the cumulative USD cost so far. Meaningful only for
	// KindReview's decision point (issue #2694).
	CumulativeUSD float64
}

// Decision is Transition's result: whether to continue into another pass, and
// if so which kind and what state mutations that implies, or stop the loop.
type Decision struct {
	// Continue is false when the loop should stop after this pass.
	Continue bool
	// Reason is the decision-op Reason text for whichever case matched.
	Reason string
	// Stop names why the loop is stopping, StopNone when Continue is true.
	Stop StopReason
	// NextPass names which pass kind runs next, only when Continue is true.
	NextPass PassKind
	// LandPhase is LandPhaseTerminalCommitted when this decision commits the
	// run to a terminal land pass. The caller persists it so a later call's
	// Input.LandPhase reflects it.
	LandPhase LandPhase
	// CapFired is the state.CapFired text, set only when LandPhase is
	// LandPhaseTerminalCommitted.
	CapFired string
	// Cap is the typed counterpart to CapFired, StopNone whenever LandPhase
	// is LandPhaseActive. Detect a specific cap against this, not against
	// CapFired: that prose doubles as operator-facing prompt text
	// (run.go's seedPromptFromState) and can be reworded independently.
	Cap CapReason
	// IncrementReviewRounds is true when this decision implies
	// reviewRounds++: unconditional on KindLegacy's continue path, gated on
	// a BLOCK verdict on KindReview's decision point.
	IncrementReviewRounds bool
}

// Transition runs one of the orchestrator decision switches, chosen by
// in.PassJustExecuted. KindImplement, KindFix and KindLand share the
// implement/fix/land decision point, which dispatches again on in.LandPhase
// (issue #2548 AC2).
func Transition(in Input) Decision {
	switch in.PassJustExecuted {
	case KindLegacy:
		return legacyTransition(in)
	case KindReview:
		return reviewTransition(in)
	case KindDeltaReview:
		return deltaReviewTransition(in)
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
	return Decision{Continue: true, Reason: "blocked, running another pass", NextPass: KindLegacy, IncrementReviewRounds: true}
}

// terminalLandTransition is the in.LandPhase == LandPhaseTerminalCommitted
// half of the implement/fix/land decision (issue #2548 AC2): once the run is
// committed to landing, only whether the pass reached its outcome matters.
// Kept disjoint from implementFixTransition so the two rule sets cannot be
// reordered against each other.
func terminalLandTransition(in Input) Decision {
	if in.HasOutcome {
		return Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached}
	}
	return Decision{Continue: false, Reason: "terminal land pass reached no outcome", Stop: StopTerminalLandNoOutcome}
}

// implementFixTransition is the in.LandPhase == LandPhaseActive half of the
// implement/fix/land decision (issue #2548 AC2). It carries no terminal-land
// case on purpose: Transition dispatches that to terminalLandTransition, so
// this switch's case order can never reprioritize against it.
func implementFixTransition(in Input) Decision {
	switch {
	case in.HasOutcome:
		return Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached}
	// After an APPROVE the land pass runs exactly once. The
	// required_marker_gate session-resume nudge recovers a land pass cut off
	// before its outcome within that same pass (issue #2044), not here: a
	// fresh land pass re-invokes the Filer on every extra lap (issue #2069).
	case in.LastVerdict == VerdictApprove:
		return Decision{Continue: false, Reason: "land pass reached no terminal outcome after APPROVE", Stop: StopApproveNoOutcome}
	// maxSlices is a hard ceiling on total driver-exec invocations (#2457).
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
	return Decision{Continue: true, Reason: "no cap fired, entering review pass", NextPass: KindReview}
}

// budgetExceeded reports whether tokens or usd reached either budget cap, and
// a reason naming which dimensions tripped. Duplicated from settle's own
// budgetExceeded (cmd/launcher/internal/settle/budget.go), same message
// format, rather than imported, to keep this package free of that dependency.
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

// reviewTransition is the review loop's decision after a review pass. Every
// branch continues by design: a review pass alone never stops the run.
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
	// This case must stay last among the caps: when a budget cap and an
	// earlier cap both fire on the same pass, the earlier cap's
	// Reason/CapFired/Cap keep reporting priority over this one.
	case in.Verdict == VerdictBlock && budgetHit:
		d = Decision{
			Continue:  true,
			Reason:    "budget exceeded; running terminal land pass",
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopBudgetExceeded,
			CapFired:  "budget exceeded (" + budgetReason + ")",
		}
	case in.Verdict == VerdictApprove:
		// A plain APPROVE does not stop the run: the implement/fix pass it
		// followed stopped right after COMMIT, so the work is committed but
		// unpushed, has no PR, and produced no outcome. One more terminal
		// pass lands it (issue #2069).
		d = Decision{Continue: true, Reason: "approved, running the land pass"}
	default:
		// A plain BLOCK with no cap hit normally needs another fix pass, but
		// if in.LandPhase was already TerminalCommitted on entry the
		// dispatch below still routes NextPass to KindLand, so the Reason
		// must say so too (issue #2782).
		reason := "blocked, running another fix pass"
		if alreadyCommitted {
			reason = "blocked, but the run is already committed to the terminal land pass; running it anyway"
		}
		d = Decision{Continue: true, Reason: reason}
	}

	// A BLOCK verdict increments reviewRounds whichever case fired above,
	// even for a capped BLOCK.
	if in.Verdict == VerdictBlock {
		d.IncrementReviewRounds = true
	}

	// A plain APPROVE means "nothing left to fix, land it" even with no cap
	// in play, so it routes to KindLand like a committed LandPhase does.
	if alreadyCommitted || d.LandPhase == LandPhaseTerminalCommitted || in.Verdict == VerdictApprove {
		d.NextPass = KindLand
	} else {
		d.NextPass = KindFix
	}

	return d
}

// deltaReviewTransition is KindDeltaReview's decision (issue #3246). Every
// branch returns Continue: false: the delta-review pass runs at most once,
// gated by ExtraPassAllowed, so there is nothing left to schedule.
func deltaReviewTransition(in Input) Decision {
	switch in.Verdict {
	case VerdictBlock:
		return Decision{
			Continue: false,
			Reason:   "delta review blocked; the run stops with no further fix lap",
			Stop:     StopDeltaReviewBlocked,
		}
	case VerdictApprove:
		return Decision{
			Continue: false,
			Reason:   "delta review approved; the run settles as usual",
			Stop:     StopDeltaReviewApproved,
		}
	default:
		// VerdictNone fails open to the same settle-as-usual outcome as
		// VerdictApprove; see StopDeltaReviewNoVerdict for why.
		return Decision{
			Continue: false,
			Reason:   "delta review produced no verdict; the run settles as usual rather than blocking a landing the review pass already approved",
			Stop:     StopDeltaReviewNoVerdict,
		}
	}
}

// ExtraPassAllowed reports whether the orchestrator may still spend the one
// extra delta-review pass issue #3246 allows once a terminal land pass has
// settled. It skips MaxReviewRounds, since that pass is not a round of the
// implement/fix/review loop. pass counts the settled land pass too, and the
// reused StopReasons let a caller match a cap here against Transition's.
func ExtraPassAllowed(caps Caps, pass int, cumulativeTokens int, cumulativeUSD float64) (bool, CapReason) {
	switch {
	case caps.MaxSlices > 0 && pass >= caps.MaxSlices:
		return false, StopMaxSlicesReached
	case caps.MaxBudgetTokens > 0 && cumulativeTokens >= caps.MaxBudgetTokens:
		return false, StopBudgetExceeded
	case caps.MaxBudgetUSD > 0 && cumulativeUSD >= caps.MaxBudgetUSD:
		return false, StopBudgetExceeded
	}
	return true, StopNone
}
