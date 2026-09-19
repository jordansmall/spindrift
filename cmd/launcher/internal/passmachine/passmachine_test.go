package passmachine

import (
	"reflect"
	"testing"
)

// transitionTestCases is TestTransition's table, covering every decision point
// the orchestrator's two loops make (issue #2548). Each case names the exact
// Reason and CapFired strings the source switches emit, because other tests
// pin that op stream byte for byte. TestTransitionNeverReturnsEmptyReason
// deliberately does not reuse this table; see its own comment for why.
var transitionTestCases = []struct {
	name string
	in   Input
	want Decision
}{
	{
		name: "legacy stops on outcome reached even with BLOCK verdict",
		in: Input{
			PassJustExecuted: KindLegacy,
			Verdict:          VerdictBlock,
			HasOutcome:       true,
		},
		want: Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached},
	},
	{
		name: "legacy stops on no verdict",
		in: Input{
			PassJustExecuted: KindLegacy,
			Verdict:          VerdictNone,
			HasOutcome:       false,
		},
		want: Decision{Continue: false, Reason: "no verdict", Stop: StopNoVerdict},
	},
	{
		name: "legacy stops on verdict not BLOCK",
		in: Input{
			PassJustExecuted: KindLegacy,
			Verdict:          VerdictApprove,
			HasOutcome:       false,
		},
		want: Decision{Continue: false, Reason: "verdict not BLOCK", Stop: StopVerdictNotBlock},
	},
	{
		name: "legacy stops on max slices reached",
		in: Input{
			PassJustExecuted: KindLegacy,
			Verdict:          VerdictBlock,
			HasOutcome:       false,
			Pass:             3,
			Caps:             Caps{MaxSlices: 3},
		},
		want: Decision{Continue: false, Reason: "max slices reached", Stop: StopMaxSlicesReached},
	},
	{
		name: "legacy stops on max review rounds reached",
		in: Input{
			PassJustExecuted: KindLegacy,
			Verdict:          VerdictBlock,
			HasOutcome:       false,
			ReviewRounds:     2,
			Caps:             Caps{MaxReviewRounds: 2},
		},
		want: Decision{Continue: false, Reason: "max review rounds reached", Stop: StopMaxReviewRoundsReached},
	},
	{
		name: "legacy continues on BLOCK with no cap fired, increments review rounds",
		in: Input{
			PassJustExecuted: KindLegacy,
			Verdict:          VerdictBlock,
			HasOutcome:       false,
		},
		want: Decision{Continue: true, Reason: "blocked, running another pass", NextPass: KindLegacy, IncrementReviewRounds: true},
	},
	{
		name: "legacy: hasOutcome wins over every later case (priority)",
		in: Input{
			PassJustExecuted: KindLegacy,
			Verdict:          VerdictNone,
			HasOutcome:       true,
			Pass:             5,
			ReviewRounds:     5,
			Caps:             Caps{MaxSlices: 3, MaxReviewRounds: 2},
		},
		want: Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached},
	},
	{
		name: "legacy: no-verdict wins over verdict-not-BLOCK and caps (priority)",
		in: Input{
			PassJustExecuted: KindLegacy,
			Verdict:          VerdictNone,
			HasOutcome:       false,
			Pass:             5,
			ReviewRounds:     5,
			Caps:             Caps{MaxSlices: 3, MaxReviewRounds: 2},
		},
		want: Decision{Continue: false, Reason: "no verdict", Stop: StopNoVerdict},
	},
	{
		name: "legacy: verdict-not-BLOCK wins over caps (priority)",
		in: Input{
			PassJustExecuted: KindLegacy,
			Verdict:          VerdictApprove,
			HasOutcome:       false,
			Pass:             5,
			ReviewRounds:     5,
			Caps:             Caps{MaxSlices: 3, MaxReviewRounds: 2},
		},
		want: Decision{Continue: false, Reason: "verdict not BLOCK", Stop: StopVerdictNotBlock},
	},
	{
		name: "legacy: max-slices wins over max-review-rounds (priority)",
		in: Input{
			PassJustExecuted: KindLegacy,
			Verdict:          VerdictBlock,
			HasOutcome:       false,
			Pass:             3,
			ReviewRounds:     2,
			Caps:             Caps{MaxSlices: 3, MaxReviewRounds: 2},
		},
		want: Decision{Continue: false, Reason: "max slices reached", Stop: StopMaxSlicesReached},
	},

	{
		name: "implement stops on outcome reached",
		in: Input{
			PassJustExecuted: KindImplement,
			HasOutcome:       true,
		},
		want: Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached},
	},
	{
		name: "fix stops on terminal land pass reached no outcome",
		in: Input{
			PassJustExecuted: KindFix,
			HasOutcome:       false,
			LandPhase:        LandPhaseTerminalCommitted,
		},
		want: Decision{Continue: false, Reason: "terminal land pass reached no outcome", Stop: StopTerminalLandNoOutcome},
	},
	{
		name: "land stops on APPROVE with no terminal outcome",
		in: Input{
			PassJustExecuted: KindLand,
			HasOutcome:       false,
			LastVerdict:      VerdictApprove,
		},
		want: Decision{Continue: false, Reason: "land pass reached no terminal outcome after APPROVE", Stop: StopApproveNoOutcome},
	},
	{
		name: "implement continues to terminal land pass on max slices reached",
		in: Input{
			PassJustExecuted: KindImplement,
			HasOutcome:       false,
			Pass:             4,
			Caps:             Caps{MaxSlices: 4},
		},
		want: Decision{
			Continue:  true,
			Reason:    "max slices reached; running terminal land pass",
			NextPass:  KindLand,
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopMaxSlicesReached,
			CapFired:  "max slices reached",
		},
	},
	{
		name: "implement falls through to review when nothing matches",
		in: Input{
			PassJustExecuted: KindImplement,
			HasOutcome:       false,
		},
		want: Decision{Continue: true, Reason: "no cap fired, entering review pass", NextPass: KindReview},
	},
	{
		name: "fix falls through to review when LandPhase active and nothing else matches",
		in: Input{
			PassJustExecuted: KindFix,
			HasOutcome:       false,
			LandPhase:        LandPhaseActive,
		},
		want: Decision{Continue: true, Reason: "no cap fired, entering review pass", NextPass: KindReview},
	},
	{
		name: "implement/fix/land: hasOutcome wins over every later case (priority)",
		in: Input{
			PassJustExecuted: KindLand,
			HasOutcome:       true,
			LandPhase:        LandPhaseTerminalCommitted,
			LastVerdict:      VerdictApprove,
			Pass:             10,
			Caps:             Caps{MaxSlices: 3},
		},
		want: Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached},
	},
	{
		name: "implement/fix/land: LandPhaseTerminalCommitted wins over LastVerdict APPROVE and caps (priority)",
		in: Input{
			PassJustExecuted: KindFix,
			HasOutcome:       false,
			LandPhase:        LandPhaseTerminalCommitted,
			LastVerdict:      VerdictApprove,
			Pass:             10,
			Caps:             Caps{MaxSlices: 3},
		},
		want: Decision{Continue: false, Reason: "terminal land pass reached no outcome", Stop: StopTerminalLandNoOutcome},
	},
	{
		name: "implement/fix/land: LastVerdict APPROVE wins over maxSlices cap (priority)",
		in: Input{
			PassJustExecuted: KindLand,
			HasOutcome:       false,
			LandPhase:        LandPhaseActive,
			LastVerdict:      VerdictApprove,
			Pass:             10,
			Caps:             Caps{MaxSlices: 3},
		},
		want: Decision{Continue: false, Reason: "land pass reached no terminal outcome after APPROVE", Stop: StopApproveNoOutcome},
	},
	{
		name: "implement/fix/land: KindFix behaves identically to KindImplement",
		in: Input{
			PassJustExecuted: KindFix,
			HasOutcome:       false,
			Pass:             4,
			Caps:             Caps{MaxSlices: 4},
		},
		want: Decision{
			Continue:  true,
			Reason:    "max slices reached; running terminal land pass",
			NextPass:  KindLand,
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopMaxSlicesReached,
			CapFired:  "max slices reached",
		},
	},
	{
		name: "implement/fix/land: KindLand behaves identically to KindImplement",
		in: Input{
			PassJustExecuted: KindLand,
			HasOutcome:       false,
			Pass:             4,
			Caps:             Caps{MaxSlices: 4},
		},
		want: Decision{
			Continue:  true,
			Reason:    "max slices reached; running terminal land pass",
			NextPass:  KindLand,
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopMaxSlicesReached,
			CapFired:  "max slices reached",
		},
	},
	{
		name: "LandPhaseTerminalCommitted dispatches to terminalLandTransition on KindImplement regardless of LastVerdict/Caps",
		in: Input{
			PassJustExecuted: KindImplement,
			HasOutcome:       false,
			LandPhase:        LandPhaseTerminalCommitted,
			LastVerdict:      VerdictApprove,
			Pass:             1,
			Caps:             Caps{MaxSlices: 100, MaxReviewRounds: 100},
		},
		want: Decision{Continue: false, Reason: "terminal land pass reached no outcome", Stop: StopTerminalLandNoOutcome},
	},
	{
		name: "LandPhaseTerminalCommitted dispatches to terminalLandTransition on KindFix with HasOutcome true regardless of PassKind label",
		in: Input{
			PassJustExecuted: KindFix,
			HasOutcome:       true,
			LandPhase:        LandPhaseTerminalCommitted,
		},
		want: Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached},
	},
	{
		name: "LandPhaseTerminalCommitted dispatches to terminalLandTransition on KindLand regardless of Caps",
		in: Input{
			PassJustExecuted: KindLand,
			HasOutcome:       false,
			LandPhase:        LandPhaseTerminalCommitted,
			Caps:             Caps{MaxSlices: 1, MaxReviewRounds: 1},
			Pass:             1,
		},
		want: Decision{Continue: false, Reason: "terminal land pass reached no outcome", Stop: StopTerminalLandNoOutcome},
	},

	{
		name: "review: no verdict sets LandPhase and always continues (never stops)",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictNone,
		},
		want: Decision{
			Continue:  true,
			Reason:    "no verdict; running terminal land pass",
			NextPass:  KindLand,
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopNoVerdict,
			CapFired:  "no verdict",
		},
	},
	{
		name: "review: max slices reached sets LandPhase and increments review rounds since verdict is BLOCK",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictBlock,
			Pass:             5,
			Caps:             Caps{MaxSlices: 5},
		},
		want: Decision{
			Continue:              true,
			Reason:                "max slices reached; running terminal land pass",
			NextPass:              KindLand,
			LandPhase:             LandPhaseTerminalCommitted,
			Cap:                   StopMaxSlicesReached,
			CapFired:              "max slices reached",
			IncrementReviewRounds: true,
		},
	},
	{
		name: "review: max slices reached on APPROVE sets LandPhase but does not increment review rounds",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictApprove,
			Pass:             5,
			Caps:             Caps{MaxSlices: 5},
		},
		want: Decision{
			Continue:  true,
			Reason:    "max slices reached; running terminal land pass",
			NextPass:  KindLand,
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopMaxSlicesReached,
			CapFired:  "max slices reached",
		},
	},
	{
		name: "review: max review rounds reached on BLOCK sets LandPhase and increments review rounds",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictBlock,
			ReviewRounds:     2,
			Caps:             Caps{MaxReviewRounds: 2},
		},
		want: Decision{
			Continue:              true,
			Reason:                "max review rounds reached; running terminal land pass",
			NextPass:              KindLand,
			LandPhase:             LandPhaseTerminalCommitted,
			Cap:                   StopMaxReviewRoundsReached,
			CapFired:              "max review rounds reached",
			IncrementReviewRounds: true,
		},
	},
	{
		name: "review: APPROVE with no cap routes straight to land, no LandPhase commit, no increment",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictApprove,
		},
		want: Decision{Continue: true, Reason: "approved, running the land pass", NextPass: KindLand},
	},
	{
		name: "review: BLOCK with no cap falls through to fix but still increments review rounds",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictBlock,
		},
		want: Decision{Continue: true, Reason: "blocked, running another fix pass", NextPass: KindFix, IncrementReviewRounds: true},
	},
	{
		name: "review: BLOCK with no cap but LandPhase already TerminalCommitted from before still routes to land, with a Reason that reflects it",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictBlock,
			LandPhase:        LandPhaseTerminalCommitted,
		},
		want: Decision{
			Continue:              true,
			Reason:                "blocked, but the run is already committed to the terminal land pass; running it anyway",
			NextPass:              KindLand,
			IncrementReviewRounds: true,
		},
	},
	{
		name: "review: LandPhase already TerminalCommitted from before routes next pass to land even on plain APPROVE",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictApprove,
			LandPhase:        LandPhaseTerminalCommitted,
		},
		want: Decision{Continue: true, Reason: "approved, running the land pass", NextPass: KindLand},
	},
	{
		name: "review: no-verdict wins over max slices and max review rounds (priority)",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictNone,
			Pass:             5,
			ReviewRounds:     5,
			Caps:             Caps{MaxSlices: 5, MaxReviewRounds: 2},
		},
		want: Decision{
			Continue:  true,
			Reason:    "no verdict; running terminal land pass",
			NextPass:  KindLand,
			LandPhase: LandPhaseTerminalCommitted,
			Cap:       StopNoVerdict,
			CapFired:  "no verdict",
		},
	},
	{
		name: "review: max slices wins over max review rounds (priority)",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictBlock,
			Pass:             5,
			ReviewRounds:     5,
			Caps:             Caps{MaxSlices: 5, MaxReviewRounds: 2},
		},
		want: Decision{
			Continue:              true,
			Reason:                "max slices reached; running terminal land pass",
			NextPass:              KindLand,
			LandPhase:             LandPhaseTerminalCommitted,
			Cap:                   StopMaxSlicesReached,
			CapFired:              "max slices reached",
			IncrementReviewRounds: true,
		},
	},
	{
		name: "review: max review rounds only fires on BLOCK, not APPROVE",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictApprove,
			ReviewRounds:     5,
			Caps:             Caps{MaxReviewRounds: 2},
		},
		want: Decision{Continue: true, Reason: "approved, running the land pass", NextPass: KindLand},
	},
	{
		name: "review: budget exceeded on tokens sets LandPhase and increments review rounds",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictBlock,
			CumulativeTokens: 100,
			Caps:             Caps{MaxBudgetTokens: 100},
		},
		want: Decision{
			Continue:              true,
			Reason:                "budget exceeded; running terminal land pass",
			NextPass:              KindLand,
			LandPhase:             LandPhaseTerminalCommitted,
			Cap:                   StopBudgetExceeded,
			CapFired:              "budget exceeded (100 tokens >= cap 100)",
			IncrementReviewRounds: true,
		},
	},
	{
		name: "review: budget exceeded on USD alone sets LandPhase and increments review rounds",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictBlock,
			CumulativeUSD:    5,
			Caps:             Caps{MaxBudgetUSD: 5},
		},
		want: Decision{
			Continue:              true,
			Reason:                "budget exceeded; running terminal land pass",
			NextPass:              KindLand,
			LandPhase:             LandPhaseTerminalCommitted,
			Cap:                   StopBudgetExceeded,
			CapFired:              "budget exceeded ($5.0000 >= cap $5.0000)",
			IncrementReviewRounds: true,
		},
	},
	{
		name: "review: budget exceeded on both tokens and USD reports both dimensions in CapFired",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictBlock,
			CumulativeTokens: 100,
			CumulativeUSD:    5,
			Caps:             Caps{MaxBudgetTokens: 100, MaxBudgetUSD: 5},
		},
		want: Decision{
			Continue:              true,
			Reason:                "budget exceeded; running terminal land pass",
			NextPass:              KindLand,
			LandPhase:             LandPhaseTerminalCommitted,
			Cap:                   StopBudgetExceeded,
			CapFired:              "budget exceeded (100 tokens >= cap 100; $5.0000 >= cap $5.0000)",
			IncrementReviewRounds: true,
		},
	},
	{
		name: "review: unset budget caps never fire even with high cumulative usage, falls through to fix",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictBlock,
			CumulativeTokens: 1_000_000,
			CumulativeUSD:    1000,
			Caps:             Caps{MaxBudgetTokens: 0, MaxBudgetUSD: 0},
		},
		want: Decision{Continue: true, Reason: "blocked, running another fix pass", NextPass: KindFix, IncrementReviewRounds: true},
	},
	{
		name: "review: budget cap does not fire on APPROVE",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictApprove,
			CumulativeTokens: 100,
			Caps:             Caps{MaxBudgetTokens: 100},
		},
		want: Decision{Continue: true, Reason: "approved, running the land pass", NextPass: KindLand},
	},
	{
		name: "review: max review rounds wins over budget cap (priority)",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictBlock,
			ReviewRounds:     2,
			CumulativeTokens: 100,
			Caps:             Caps{MaxReviewRounds: 2, MaxBudgetTokens: 100},
		},
		want: Decision{
			Continue:              true,
			Reason:                "max review rounds reached; running terminal land pass",
			NextPass:              KindLand,
			LandPhase:             LandPhaseTerminalCommitted,
			Cap:                   StopMaxReviewRoundsReached,
			CapFired:              "max review rounds reached",
			IncrementReviewRounds: true,
		},
	},
	{
		name: "review: max slices wins over budget cap (priority)",
		in: Input{
			PassJustExecuted: KindReview,
			Verdict:          VerdictBlock,
			Pass:             5,
			CumulativeTokens: 100,
			Caps:             Caps{MaxSlices: 5, MaxBudgetTokens: 100},
		},
		want: Decision{
			Continue:              true,
			Reason:                "max slices reached; running terminal land pass",
			NextPass:              KindLand,
			LandPhase:             LandPhaseTerminalCommitted,
			Cap:                   StopMaxSlicesReached,
			CapFired:              "max slices reached",
			IncrementReviewRounds: true,
		},
	},

	// These cases cover the delta-review pass (issue #3246).
	{
		name: "delta review: BLOCK stops outright, no fix lap",
		in: Input{
			PassJustExecuted: KindDeltaReview,
			Verdict:          VerdictBlock,
		},
		want: Decision{
			Continue: false,
			Reason:   "delta review blocked; the run stops with no further fix lap",
			Stop:     StopDeltaReviewBlocked,
		},
	},
	{
		name: "delta review: APPROVE stops, run settles as usual",
		in: Input{
			PassJustExecuted: KindDeltaReview,
			Verdict:          VerdictApprove,
		},
		want: Decision{
			Continue: false,
			Reason:   "delta review approved; the run settles as usual",
			Stop:     StopDeltaReviewApproved,
		},
	},
	{
		name: "delta review: no verdict fails open, does not block the landing",
		in: Input{
			PassJustExecuted: KindDeltaReview,
			Verdict:          VerdictNone,
		},
		want: Decision{
			Continue: false,
			Reason:   "delta review produced no verdict; the run settles as usual rather than blocking a landing the review pass already approved",
			Stop:     StopDeltaReviewNoVerdict,
		},
	},
}

func TestTransition(t *testing.T) {
	for _, tt := range transitionTestCases {
		t.Run(tt.name, func(t *testing.T) {
			got := Transition(tt.in)
			if got != tt.want {
				t.Errorf("Transition(%+v) =\n  %+v\nwant\n  %+v", tt.in, got, tt.want)
			}
		})
	}
}

// TestTransitionNeverReturnsEmptyReason guards issue #2655 acceptance criterion
// 3: a decision op must never reach stdout with an empty reason. It sweeps
// Input's own field values rather than replaying transitionTestCases, because
// a replay can only fail when TestTransition already fails; the sweep also
// catches a new fallthrough case that nobody has added a table entry for yet.
func TestTransitionNeverReturnsEmptyReason(t *testing.T) {
	passKinds := []PassKind{KindLegacy, KindImplement, KindFix, KindLand, KindReview, KindDeltaReview}
	verdicts := []Verdict{VerdictNone, VerdictBlock, VerdictApprove}
	hasOutcomes := []bool{true, false}
	landPhases := []LandPhase{LandPhaseActive, LandPhaseTerminalCommitted}
	lastVerdicts := []Verdict{VerdictNone, VerdictBlock, VerdictApprove}

	// Each scenario isolates one cap dimension. reviewTransition checks its caps
	// in a fixed priority order, so leaving MaxSlices enabled everywhere would
	// mask MaxReviewRounds and the budget caps from ever firing.
	capsScenarios := []Caps{
		{},
		{MaxSlices: 1},
		{MaxReviewRounds: 1},
		{MaxBudgetTokens: 100, MaxBudgetUSD: 1.0},
		{MaxSlices: 1, MaxReviewRounds: 1, MaxBudgetTokens: 100, MaxBudgetUSD: 1.0},
	}

	// The 1/1/100/1.0 values match the cap values in capsScenarios, so the three
	// levels sit below, at, and above whichever caps a scenario enables.
	budgetLevels := []struct {
		pass, reviewRounds int
		tokens             int
		usd                float64
	}{
		{pass: 0, reviewRounds: 0, tokens: 0, usd: 0},
		{pass: 1, reviewRounds: 1, tokens: 100, usd: 1.0},
		{pass: 2, reviewRounds: 2, tokens: 200, usd: 2.0},
	}

	count := 0
	for _, pk := range passKinds {
		for _, v := range verdicts {
			for _, ho := range hasOutcomes {
				for _, lp := range landPhases {
					for _, lv := range lastVerdicts {
						for _, caps := range capsScenarios {
							for _, bl := range budgetLevels {
								in := Input{
									PassJustExecuted: pk,
									Verdict:          v,
									HasOutcome:       ho,
									Pass:             bl.pass,
									ReviewRounds:     bl.reviewRounds,
									Caps:             caps,
									LandPhase:        lp,
									LastVerdict:      lv,
									CumulativeTokens: bl.tokens,
									CumulativeUSD:    bl.usd,
								}
								got := Transition(in)
								count++
								if got.Reason == "" {
									t.Errorf("Transition(%+v) returned empty Reason (Decision=%+v)", in, got)
								}
							}
						}
					}
				}
			}
		}
	}
	if count == 0 {
		t.Fatal("sweep exercised zero combinations; this test would pass vacuously")
	}
}

// TestBudgetExceeded covers the budgetExceeded helper (issue #2694): either cap
// dimension can trip it alone, a zero cap never fires whatever the usage, and
// the comparison is >= (at-cap fires), not > (strictly over).
func TestBudgetExceeded(t *testing.T) {
	tests := []struct {
		name   string
		caps   Caps
		tokens int
		usd    float64
		want   bool
	}{
		{
			name:   "token cap at boundary fires",
			caps:   Caps{MaxBudgetTokens: 100},
			tokens: 100,
			want:   true,
		},
		{
			name:   "token cap under boundary does not fire",
			caps:   Caps{MaxBudgetTokens: 100},
			tokens: 99,
			want:   false,
		},
		{
			name: "USD cap at boundary fires",
			caps: Caps{MaxBudgetUSD: 5},
			usd:  5,
			want: true,
		},
		{
			name: "USD cap under boundary does not fire",
			caps: Caps{MaxBudgetUSD: 5},
			usd:  4.99,
			want: false,
		},
		{
			name:   "both caps unset never fires regardless of usage",
			caps:   Caps{},
			tokens: 1_000_000,
			usd:    1000,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := budgetExceeded(tt.caps, tt.tokens, tt.usd)
			if got != tt.want {
				t.Errorf("budgetExceeded(%+v, %d, %v) = %v, want %v", tt.caps, tt.tokens, tt.usd, got, tt.want)
			}
			if (reason != "") != tt.want {
				t.Errorf("budgetExceeded(%+v, %d, %v) reason = %q, want empty iff not exceeded (exceeded = %v)", tt.caps, tt.tokens, tt.usd, reason, tt.want)
			}
		})
	}
}

// TestPassKindString pins the pass_start op's Role field value for every
// PassKind (issue #2548 review).
func TestPassKindString(t *testing.T) {
	tests := []struct {
		kind PassKind
		want string
	}{
		{KindLegacy, ""},
		{KindImplement, "implement"},
		{KindFix, "fix"},
		{KindLand, "land"},
		{KindReview, "review"},
		{KindDeltaReview, "delta-review"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.kind.String(); got != tt.want {
				t.Errorf("PassKind(%d).String() = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

// TestPassKindManifestKind pins the pass-manifest entry's Kind field for every
// PassKind (issue #2983). KindLegacy is the one that differs: String() returns
// "", which is right for the pass_start Role field but wrong for a manifest
// field that must always name the pass shape.
func TestPassKindManifestKind(t *testing.T) {
	tests := []struct {
		kind PassKind
		want string
	}{
		{KindLegacy, "legacy"},
		{KindImplement, KindImplement.String()},
		{KindFix, KindFix.String()},
		{KindLand, KindLand.String()},
		{KindReview, KindReview.String()},
		{KindDeltaReview, KindDeltaReview.String()},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.kind.ManifestKind(); got != tt.want {
				t.Errorf("PassKind(%d).ManifestKind() = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

// TestRoleConstantsAreNamedType pins the Role constants to the named Role type
// (issue #2766). The field is any, not Role: a []Role literal would implicitly
// convert an untyped string constant, so the check would pass either way. An
// any-valued field keeps an untyped constant's default type (string) and a
// declared one's type (Role), so reflect.TypeOf tells the two apart.
func TestRoleConstantsAreNamedType(t *testing.T) {
	tests := []struct {
		name string
		role any
	}{
		{"RoleImplement", RoleImplement},
		{"RoleReview", RoleReview},
		{"RoleFix", RoleFix},
		{"RoleLand", RoleLand},
		{"RoleDeltaReview", RoleDeltaReview},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := reflect.TypeOf(tt.role), reflect.TypeOf(Role("")); got != want {
				t.Errorf("reflect.TypeOf(%s) = %v, want %v", tt.name, got, want)
			}
		})
	}
}

// TestExtraPassAllowed covers every ExtraPassAllowed branch (issue #3246): all
// three caps disabled, each cap at its threshold (it fires on >=, not >), each
// cap one below, and the slices-then-tokens-then-USD precedence when more than
// one cap would fire at once.
func TestExtraPassAllowed(t *testing.T) {
	tests := []struct {
		name             string
		caps             Caps
		pass             int
		cumulativeTokens int
		cumulativeUSD    float64
		wantOK           bool
		wantReason       CapReason
	}{
		{
			name:       "every cap disabled always allows the extra pass",
			caps:       Caps{},
			pass:       1_000_000,
			wantOK:     true,
			wantReason: StopNone,
		},
		{
			name:       "MaxSlices at threshold fires",
			caps:       Caps{MaxSlices: 3},
			pass:       3,
			wantOK:     false,
			wantReason: StopMaxSlicesReached,
		},
		{
			name:       "MaxSlices one below threshold does not fire",
			caps:       Caps{MaxSlices: 3},
			pass:       2,
			wantOK:     true,
			wantReason: StopNone,
		},
		{
			name:             "MaxBudgetTokens at threshold fires",
			caps:             Caps{MaxBudgetTokens: 100},
			cumulativeTokens: 100,
			wantOK:           false,
			wantReason:       StopBudgetExceeded,
		},
		{
			name:             "MaxBudgetTokens one below threshold does not fire",
			caps:             Caps{MaxBudgetTokens: 100},
			cumulativeTokens: 99,
			wantOK:           true,
			wantReason:       StopNone,
		},
		{
			name:          "MaxBudgetUSD at threshold fires",
			caps:          Caps{MaxBudgetUSD: 5},
			cumulativeUSD: 5,
			wantOK:        false,
			wantReason:    StopBudgetExceeded,
		},
		{
			name:          "MaxBudgetUSD one below threshold does not fire",
			caps:          Caps{MaxBudgetUSD: 5},
			cumulativeUSD: 4.99,
			wantOK:        true,
			wantReason:    StopNone,
		},
		{
			name:             "MaxSlices wins over MaxBudgetTokens and MaxBudgetUSD (precedence)",
			caps:             Caps{MaxSlices: 3, MaxBudgetTokens: 100, MaxBudgetUSD: 5},
			pass:             3,
			cumulativeTokens: 100,
			cumulativeUSD:    5,
			wantOK:           false,
			wantReason:       StopMaxSlicesReached,
		},
		{
			name:             "MaxBudgetTokens wins over MaxBudgetUSD when MaxSlices does not fire (precedence)",
			caps:             Caps{MaxSlices: 3, MaxBudgetTokens: 100, MaxBudgetUSD: 5},
			pass:             2,
			cumulativeTokens: 100,
			cumulativeUSD:    5,
			wantOK:           false,
			wantReason:       StopBudgetExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOK, gotReason := ExtraPassAllowed(tt.caps, tt.pass, tt.cumulativeTokens, tt.cumulativeUSD)
			if gotOK != tt.wantOK || gotReason != tt.wantReason {
				t.Errorf("ExtraPassAllowed(%+v, %d, %d, %v) = (%v, %v), want (%v, %v)",
					tt.caps, tt.pass, tt.cumulativeTokens, tt.cumulativeUSD, gotOK, gotReason, tt.wantOK, tt.wantReason)
			}
		})
	}
}
