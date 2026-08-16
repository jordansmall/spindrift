package passmachine

import "testing"

// TestTransition exercises Transition across every decision point the
// orchestrator's two loops make today (issue #2548): the legacy loop's
// single switch (run.go:244-259), the review loop's implement/fix/land
// switch (run.go:355-401), and the review loop's own review-pass switch
// (run.go:461-487). Each case names the exact decision-op Reason string and
// (where applicable) state.CapFired text the source switches emit, since
// both are part of a byte-for-byte-pinned op stream existing tests assert
// on.
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
				TerminalLand:     true,
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
				Continue:        true,
				Reason:          "max slices reached; running terminal land pass",
				NextPass:        KindLand,
				SetTerminalLand: true,
				CapFired:        "max slices reached",
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
			name: "fix falls through to review when TerminalLand false and nothing else matches",
			in: Input{
				PassJustExecuted: KindFix,
				HasOutcome:       false,
				TerminalLand:     false,
			},
			want: Decision{Continue: true, Reason: "", NextPass: KindReview},
		},
		{
			name: "manifestDispatched overrides TerminalLand's next-pass kind even when maxSlices ALSO fires this pass",
			in: Input{
				PassJustExecuted:   KindImplement,
				HasOutcome:         false,
				Pass:               4,
				Caps:               Caps{MaxSlices: 4},
				ManifestDispatched: true,
			},
			want: Decision{
				Continue:        true,
				Reason:          "max slices reached; running terminal land pass",
				NextPass:        KindFix,
				SetTerminalLand: true,
				CapFired:        "max slices reached",
			},
		},
		{
			name: "implement/fix/land: hasOutcome wins over every later case (priority)",
			in: Input{
				PassJustExecuted:   KindLand,
				HasOutcome:         true,
				TerminalLand:       true,
				LastVerdict:        VerdictApprove,
				Pass:               10,
				Caps:               Caps{MaxSlices: 3},
				ManifestDispatched: true,
			},
			want: Decision{Continue: false, Reason: "outcome reached", Stop: StopOutcomeReached},
		},
		{
			name: "implement/fix/land: TerminalLand wins over LastVerdict APPROVE and caps (priority)",
			in: Input{
				PassJustExecuted:   KindFix,
				HasOutcome:         false,
				TerminalLand:       true,
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
				TerminalLand:       false,
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
				Continue:        true,
				Reason:          "max slices reached; running terminal land pass",
				NextPass:        KindFix,
				SetTerminalLand: true,
				CapFired:        "max slices reached",
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
				Continue:        true,
				Reason:          "max slices reached; running terminal land pass",
				NextPass:        KindLand,
				SetTerminalLand: true,
				CapFired:        "max slices reached",
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
				Continue:        true,
				Reason:          "max slices reached; running terminal land pass",
				NextPass:        KindLand,
				SetTerminalLand: true,
				CapFired:        "max slices reached",
			},
		},

		// ---- review loop's own review-pass switch (run.go:461-487) ----
		{
			name: "review: no verdict sets TerminalLand and always continues (never stops)",
			in: Input{
				PassJustExecuted: KindReview,
				Verdict:          VerdictNone,
			},
			want: Decision{
				Continue:        true,
				Reason:          "no verdict; running terminal land pass",
				NextPass:        KindLand,
				SetTerminalLand: true,
				CapFired:        "no verdict",
			},
		},
		{
			name: "review: max slices reached sets TerminalLand and increments review rounds since verdict is BLOCK",
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
				SetTerminalLand:       true,
				CapFired:              "max slices reached",
				IncrementReviewRounds: true,
			},
		},
		{
			name: "review: max slices reached on APPROVE sets TerminalLand but does not increment review rounds",
			in: Input{
				PassJustExecuted: KindReview,
				Verdict:          VerdictApprove,
				Pass:             5,
				Caps:             Caps{MaxSlices: 5},
			},
			want: Decision{
				Continue:        true,
				Reason:          "max slices reached; running terminal land pass",
				NextPass:        KindLand,
				SetTerminalLand: true,
				CapFired:        "max slices reached",
			},
		},
		{
			name: "review: max review rounds reached on BLOCK sets TerminalLand and increments review rounds",
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
				SetTerminalLand:       true,
				CapFired:              "max review rounds reached",
				IncrementReviewRounds: true,
			},
		},
		{
			name: "review: APPROVE with no cap falls through to fix, no TerminalLand, no increment",
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
			name: "review: TerminalLand already true from before routes next pass to land even on plain APPROVE",
			in: Input{
				PassJustExecuted: KindReview,
				Verdict:          VerdictApprove,
				TerminalLand:     true,
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
				Continue:        true,
				Reason:          "no verdict; running terminal land pass",
				NextPass:        KindLand,
				SetTerminalLand: true,
				CapFired:        "no verdict",
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
				SetTerminalLand:       true,
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
