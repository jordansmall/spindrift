package passmachine

import "testing"

// TestTransition exercises Transition across every decision point the
// orchestrator's two loops make today (issue #2548): the legacy loop's
// single switch (run.go:244-259), the review loop's implement/fix/land
// switch (run.go:355-401, now split into implementFixTransition and
// terminalLandTransition per AC2), and the review loop's own review-pass
// switch (run.go:461-487). Each case names the exact decision-op Reason
// string and (where applicable) state.CapFired text the source switches
// emit, since both are part of a byte-for-byte-pinned op stream existing
// tests assert on. Cases that set CapFired also assert the typed Cap field
// alongside it.
func TestTransition(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want Decision
	}{
		// ---- legacy loop (run.go:244-259) ----
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
			want: Decision{Continue: true, Reason: "", NextPass: KindLegacy, IncrementReviewRounds: true},
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

		// ---- review loop implement/fix/land (run.go:355-401) ----
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
			name: "implement continues to fix on manifest dispatched",
			in: Input{
				PassJustExecuted:   KindImplement,
				HasOutcome:         false,
				ManifestDispatched: true,
			},
			want: Decision{Continue: true, Reason: "slice manifest dispatched", NextPass: KindFix},
		},
		{
			name: "implement falls through to review when nothing matches",
			in: Input{
				PassJustExecuted: KindImplement,
				HasOutcome:       false,
			},
			want: Decision{Continue: true, Reason: "", NextPass: KindReview},
		},
		{
			name: "fix falls through to review when LandPhase active and nothing else matches",
			in: Input{
				PassJustExecuted: KindFix,
				HasOutcome:       false,
				LandPhase:        LandPhaseActive,
			},
			want: Decision{Continue: true, Reason: "", NextPass: KindReview},
		},
		{
			name: "manifestDispatched overrides LandPhase's next-pass kind even when maxSlices ALSO fires this pass",
			in: Input{
				PassJustExecuted:   KindImplement,
				HasOutcome:         false,
				Pass:               4,
				Caps:               Caps{MaxSlices: 4},
				ManifestDispatched: true,
			},
			want: Decision{
				Continue:  true,
				Reason:    "max slices reached; running terminal land pass",
				NextPass:  KindFix,
				LandPhase: LandPhaseTerminalCommitted,
				Cap:       StopMaxSlicesReached,
				CapFired:  "max slices reached",
			},
		},
		{
			name: "implement/fix/land: hasOutcome wins over every later case (priority)",
			in: Input{
				PassJustExecuted:   KindLand,
				HasOutcome:         true,
				LandPhase:          LandPhaseTerminalCommitted,
				LastVerdict:        VerdictApprove,
				Pass:               10,
				Caps:               Caps{MaxSlices: 3},
				ManifestDispatched: true,
			},
			want: Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached},
		},
		{
			name: "implement/fix/land: LandPhaseTerminalCommitted wins over LastVerdict APPROVE and caps (priority)",
			in: Input{
				PassJustExecuted:   KindFix,
				HasOutcome:         false,
				LandPhase:          LandPhaseTerminalCommitted,
				LastVerdict:        VerdictApprove,
				Pass:               10,
				Caps:               Caps{MaxSlices: 3},
				ManifestDispatched: true,
			},
			want: Decision{Continue: false, Reason: "terminal land pass reached no outcome", Stop: StopTerminalLandNoOutcome},
		},
		{
			name: "implement/fix/land: LastVerdict APPROVE wins over maxSlices cap (priority)",
			in: Input{
				PassJustExecuted:   KindLand,
				HasOutcome:         false,
				LandPhase:          LandPhaseActive,
				LastVerdict:        VerdictApprove,
				Pass:               10,
				Caps:               Caps{MaxSlices: 3},
				ManifestDispatched: true,
			},
			want: Decision{Continue: false, Reason: "land pass reached no terminal outcome after APPROVE", Stop: StopApproveNoOutcome},
		},
		{
			name: "implement/fix/land: maxSlices cap wins over manifestDispatched case selection (though next pass kind still honors manifestDispatched)",
			in: Input{
				PassJustExecuted:   KindImplement,
				HasOutcome:         false,
				Pass:               3,
				Caps:               Caps{MaxSlices: 3},
				ManifestDispatched: true,
			},
			want: Decision{
				Continue:  true,
				Reason:    "max slices reached; running terminal land pass",
				NextPass:  KindFix,
				LandPhase: LandPhaseTerminalCommitted,
				Cap:       StopMaxSlicesReached,
				CapFired:  "max slices reached",
			},
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
			name: "LandPhaseTerminalCommitted dispatches to terminalLandTransition on KindImplement regardless of LastVerdict/Caps/ManifestDispatched",
			in: Input{
				PassJustExecuted:   KindImplement,
				HasOutcome:         false,
				LandPhase:          LandPhaseTerminalCommitted,
				LastVerdict:        VerdictApprove,
				Pass:               1,
				Caps:               Caps{MaxSlices: 100, MaxReviewRounds: 100},
				ManifestDispatched: true,
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

		// ---- review loop's own review-pass switch (run.go:461-487) ----
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
			name: "review: APPROVE with no cap falls through to fix, no LandPhase commit, no increment",
			in: Input{
				PassJustExecuted: KindReview,
				Verdict:          VerdictApprove,
			},
			want: Decision{Continue: true, Reason: "", NextPass: KindFix},
		},
		{
			name: "review: BLOCK with no cap falls through to fix but still increments review rounds",
			in: Input{
				PassJustExecuted: KindReview,
				Verdict:          VerdictBlock,
			},
			want: Decision{Continue: true, Reason: "", NextPass: KindFix, IncrementReviewRounds: true},
		},
		{
			name: "review: LandPhase already TerminalCommitted from before routes next pass to land even on plain APPROVE",
			in: Input{
				PassJustExecuted: KindReview,
				Verdict:          VerdictApprove,
				LandPhase:        LandPhaseTerminalCommitted,
			},
			want: Decision{Continue: true, Reason: "", NextPass: KindLand},
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
			want: Decision{Continue: true, Reason: "", NextPass: KindFix},
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
				CapFired:              "budget exceeded: 100 tokens >= cap 100",
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
				CapFired:              "budget exceeded: $5.0000 >= cap $5.0000",
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
				CapFired:              "budget exceeded: 100 tokens >= cap 100; $5.0000 >= cap $5.0000",
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
			want: Decision{Continue: true, Reason: "", NextPass: KindFix, IncrementReviewRounds: true},
		},
		{
			name: "review: budget cap does not fire on APPROVE",
			in: Input{
				PassJustExecuted: KindReview,
				Verdict:          VerdictApprove,
				CumulativeTokens: 100,
				Caps:             Caps{MaxBudgetTokens: 100},
			},
			want: Decision{Continue: true, Reason: "", NextPass: KindFix},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Transition(tt.in)
			if got != tt.want {
				t.Errorf("Transition(%+v) =\n  %+v\nwant\n  %+v", tt.in, got, tt.want)
			}
		})
	}
}

// TestBudgetExceeded exercises the budgetExceeded helper directly (issue
// #2694): each of the two cap dimensions can trip it independently, a zero
// cap on either dimension never fires regardless of usage, and the
// comparison is >= (at-cap fires), not > (strictly over).
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

// TestPassKindString pins the pass_start op's own Role field value for
// every PassKind (issue #2548 review) -- exercised only transitively by
// TestTransition's op-stream assertions until now.
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
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.kind.String(); got != tt.want {
				t.Errorf("PassKind(%d).String() = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}
