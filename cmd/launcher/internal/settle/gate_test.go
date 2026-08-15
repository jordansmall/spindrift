package settle

import (
	"errors"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

func baseConfig() Config {
	return Config{
		CompleteLabel:     "agent-complete",
		MergePollInterval: 1,   // real, safe interval — baseConfig's Clock below never sleeps for real, so this doesn't slow tests
		MergePollTimeout:  100, // large enough for multi-poll tests
		MergeMode:         "immediate",
		// Non-sleeping by default so every caller of baseConfig() is safe
		// without having to inject its own recordingClock: settle.New()
		// only falls back to dispatch.RealClock() when Clock.Sleep is nil.
		// Callers that need to observe/assert sleep durations still inject
		// their own recordingClock() and overwrite this field per-test.
		Clock: dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) {}},
	}
}

const testPR = "https://github.com/owner/repo/pull/42"

// boolPtr returns a pointer to b, for table cases that need to distinguish
// "assert false" from "don't care" in an optional *bool field.
func boolPtr(b bool) *bool { return &b }

// testDispatchLabels is the conventional lifecycle-label set, mirrored from
// lib/env-schema.nix and pinned against the agent workflows by
// nix/checks/dispatch-labels.nix (issue #460). forge.NewFake takes labels as
// an explicit constructor argument rather than baking in a copy, so settle's
// tests share this one value instead of each restating the four label
// strings.
var testDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// TestGateToGreen verifies that gateToGreen itself performs no label swap —
// selfHeal owns agent-complete, swapping it only once the landing path
// settles (issue #757) — and returns gateRedRetry on a genuine CI failure.
func TestGateToGreen(t *testing.T) {
	cases := []struct {
		name                string
		timeout             int
		pollInterval        int // 0 means "use baseConfig's default (1)"
		checkStates         []forge.RollupState
		checkStateErrs      []error
		requireRegistration bool
		want                gateResult
		wantReasonContains  string
		wantSawNonTerminal  *bool
		wantWindowElapsed   *bool
	}{
		{
			name:        "SUCCESS on first poll reaches green without a swap",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			want:        gateGreen,
		},
		{
			name:        "PENDING then SUCCESS reaches green after one wait iteration",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			want:        gateGreen,
		},
		{
			name:        "FAILURE signals genuine-red without swap",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateFailure},
			want:        gateRedRetry,
		},
		{
			name:        "ERROR signals genuine-red without swap",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateError},
			want:        gateRedRetry,
		},
		{
			name:               "NONE times out — non-genuine failure without swap",
			timeout:            0,
			checkStates:        nil,
			want:               gateTerminal,
			wantReasonContains: "ci-timeout:",
		},
		{
			// A partial check snapshot can briefly show SUCCESS before all jobs
			// are registered. A second poll that returns FAILURE is genuine red.
			name:        "SUCCESS then FAILURE in confirmation poll is genuine red",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StateFailure},
			want:        gateRedRetry,
		},
		{
			// Confirmation returns PENDING — another check registered but not
			// yet settled. Gate keeps waiting; eventually stabilises to SUCCESS.
			name:        "SUCCESS then PENDING in confirmation poll defers completion",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			want:        gateGreen,
		},
		{
			// A 403 or other API error on the first poll must not be silently
			// dropped as StateNone.
			name:               "CheckState API error on first poll is non-retriable",
			timeout:            100,
			checkStateErrs:     []error{errors.New("gh api graphql: 403 Forbidden")},
			checkStates:        []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			want:               gateTerminal,
			wantReasonContains: "ci-check-error:",
		},
		{
			// A 403 on the confirmation poll must surface as non-retriable.
			name:               "CheckState API error on confirmation poll is non-retriable",
			timeout:            100,
			checkStateErrs:     []error{nil, errors.New("gh api graphql: 403 Forbidden")},
			checkStates:        []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			want:               gateTerminal,
			wantReasonContains: "ci-check-error:",
		},
		{
			// issue #1652: an unchanged head SHA can carry a terminal SUCCESS
			// rollup inherited from an earlier run that this process never
			// watched register. requireRegistration must not trust that
			// inherited SUCCESS until it has observed a non-terminal state
			// (here, PENDING) proving this run's own checks are alive.
			name:                "requireRegistration waits out a stale SUCCESS until a fresh registration appears",
			timeout:             100,
			requireRegistration: true,
			checkStates:         []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			want:                gateGreen,
			wantSawNonTerminal:  boolPtr(true),
		},
		{
			// issue #2475: a PR whose checks settled to SUCCESS long ago never
			// produces a non-terminal poll again, so the registration guard
			// must not withhold trust forever — after registrationWindowPolls
			// intervals of nothing but SUCCESS, gateToGreen treats that as
			// proof CI already finished and accepts it.
			// timeout: 3 is the boundary case — deadline == the unclamped
			// window here (registrationWindowPolls(3) * actualIv(1), where
			// actualIv is baseConfig's MergePollInterval:1 directly — no
			// flooring needed since it's already nonzero), so the window
			// elapses right as the deadline is hit rather than well before
			// it.
			name:                "requireRegistration accepts a settled SUCCESS once the registration window elapses",
			timeout:             3,
			requireRegistration: true,
			checkStates:         []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			want:                gateGreen,
			wantSawNonTerminal:  boolPtr(false),
			wantWindowElapsed:   boolPtr(true),
		},
		{
			// The registration window is a small, bounded number of polls, not
			// a side effect of a tight deadline: a settled SUCCESS resolves to
			// green long before a large MergePollTimeout would ever be hit.
			name:                "requireRegistration accepts a settled SUCCESS well before a large deadline elapses",
			timeout:             100,
			requireRegistration: true,
			checkStates:         []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			want:                gateGreen,
			wantSawNonTerminal:  boolPtr(false),
			wantWindowElapsed:   boolPtr(true),
		},
		{
			// issue #2475 follow-up: when MergePollTimeout is smaller than
			// registrationWindowPolls*MergePollInterval (here 3*1=3 unclamped,
			// vs a 2s deadline), the unclamped window never elapses before the
			// ci-timeout deadline hits — a legitimately-already-green adopted
			// PR would livelock into gateTerminal instead of being accepted.
			// gateToGreen must clamp the window to the deadline so a settled
			// SUCCESS is still accepted once the (now-clamped) window elapses,
			// same as the large-deadline case above.
			name:                "requireRegistration clamps the window to a deadline smaller than the unclamped window",
			timeout:             2,
			pollInterval:        1,
			requireRegistration: true,
			checkStates:         []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			want:                gateGreen,
			wantSawNonTerminal:  boolPtr(false),
			wantWindowElapsed:   boolPtr(true),
		},
		{
			// issue #2475: does not pin registrationWindowPolls' unclamped
			// value — with timeout: 1, the registration window itself gets
			// clamped down to 1 poll-interval too (see the clamp above), so
			// this only proves two coarser things: (a) registrationWindowPolls
			// isn't weakened all the way to 0 (which would make the window
			// elapse on the very first poll and wrongly return gateGreen
			// instead of gateTerminal), and (b) the guard isn't bypassed
			// entirely via `registered := true` at the top of gateToGreen
			// (same wrong gateGreen outcome).
			//
			// issue #2476: SUCCESS is the only state ever observed on a real
			// poll here — the only thing that ever "cleared" registered was
			// the window-elapsed synthetic fallback, never genuine evidence
			// (a real PENDING/EXPECTED/NONE poll). That is exactly the
			// registration-guard flavour of timeout, distinct from an
			// ordinary ran-out-the-clock one, so the reason must name the
			// guard explicitly.
			name:                "requireRegistration does not disable the window or bypass the guard entirely",
			timeout:             1,
			requireRegistration: true,
			checkStates:         []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			want:                gateTerminal,
			wantReasonContains:  "registration guard never cleared",
			wantSawNonTerminal:  boolPtr(false),
			wantWindowElapsed:   boolPtr(true),
		},
		{
			// issue #2476 boundary: unlike the case above, a genuine
			// PENDING is actually observed on a real poll here — real
			// evidence the guard cleared for a legitimate reason, not just
			// the window-elapsed fallback. A timeout that follows must
			// still get the generic ci-timeout reason, not the
			// registration-guard one, even though requireRegistration was
			// set.
			name:                "requireRegistration timeout after genuine pending evidence gets the generic reason",
			timeout:             0,
			requireRegistration: true,
			checkStates:         []forge.RollupState{forge.StatePending},
			want:                gateTerminal,
			wantReasonContains:  "ci-timeout: CI-watch deadline reached",
			wantSawNonTerminal:  boolPtr(true),
			wantWindowElapsed:   boolPtr(false),
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.MergePollTimeout = tc.timeout
			if tc.pollInterval != 0 {
				c.MergePollInterval = tc.pollInterval
			}
			fc := forge.NewFake()
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
			if len(tc.checkStates) > 0 {
				fc.SetCheckStates(testPR, tc.checkStates)
			}
			if len(tc.checkStateErrs) > 0 {
				fc.SetCheckStateErrors(testPR, tc.checkStateErrs)
			}
			s := New(c, fc, fc)

			obs, reason := s.gateToGreen("1", 0, testPR, tc.requireRegistration)

			if obs.outcome != tc.want {
				t.Errorf("gateToGreen = %v, want %v", obs.outcome, tc.want)
			}
			if tc.wantReasonContains != "" {
				if !strings.Contains(reason, tc.wantReasonContains) {
					t.Errorf("gateToGreen reason = %q, want a substring containing %q", reason, tc.wantReasonContains)
				}
			} else if reason != "" {
				t.Errorf("gateToGreen reason = %q, want empty for a non-terminal outcome", reason)
			}
			if tc.wantSawNonTerminal != nil && obs.sawNonTerminal != *tc.wantSawNonTerminal {
				t.Errorf("obs.sawNonTerminal = %v, want %v", obs.sawNonTerminal, *tc.wantSawNonTerminal)
			}
			if tc.wantWindowElapsed != nil && obs.windowElapsed != *tc.wantWindowElapsed {
				t.Errorf("obs.windowElapsed = %v, want %v", obs.windowElapsed, *tc.wantWindowElapsed)
			}
			if len(fc.TransitionStateCalls) > 0 {
				t.Errorf("gateToGreen must never swap state itself; got %d calls: %+v", len(fc.TransitionStateCalls), fc.TransitionStateCalls)
			}
		})
	}
}
