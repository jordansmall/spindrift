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
		MergePollInterval: 1,   // The Clock below never sleeps for real, so this costs nothing.
		MergePollTimeout:  100, // Large enough for multi-poll tests.
		MergeMode:         "immediate",
		// settle.New() only falls back to dispatch.RealClock() when Clock.Sleep
		// is nil, so this non-sleeping clock keeps every baseConfig() caller off
		// real time. Tests that assert sleep durations overwrite it.
		Clock: dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) {}},
	}
}

const testPR = "https://github.com/owner/repo/pull/42"

// boolPtr lets a table case distinguish "assert false" from "don't care" in an
// optional *bool field.
func boolPtr(b bool) *bool { return &b }

// testDispatchLabels mirrors lib/env-schema.nix and is pinned against the agent
// workflows by nix/checks/dispatch-labels.nix (issue #460).
var testDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// TestGateToGreen pins that gateToGreen never swaps a label itself. selfHeal
// owns agent-complete and swaps it only once the landing path settles (issue
// #757).
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
			// register, so the gate confirms with a second poll.
			name:        "SUCCESS then FAILURE in confirmation poll is genuine red",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StateFailure},
			want:        gateRedRetry,
		},
		{
			// A PENDING confirmation means another check registered but has not
			// settled, so the gate keeps waiting instead of calling it green.
			name:        "SUCCESS then PENDING in confirmation poll defers completion",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			want:        gateGreen,
		},
		{
			// An API error must not be silently dropped as StateNone.
			name:               "CheckState API error on first poll is non-retriable",
			timeout:            100,
			checkStateErrs:     []error{errors.New("gh api graphql: 403 Forbidden")},
			checkStates:        []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			want:               gateTerminal,
			wantReasonContains: "ci-check-error:",
		},
		{
			name:               "CheckState API error on confirmation poll is non-retriable",
			timeout:            100,
			checkStateErrs:     []error{nil, errors.New("gh api graphql: 403 Forbidden")},
			checkStates:        []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			want:               gateTerminal,
			wantReasonContains: "ci-check-error:",
		},
		{
			// issue #1652: an unchanged head SHA can carry a terminal SUCCESS
			// inherited from an earlier run. requireRegistration must not trust
			// it until a non-terminal state proves this run's checks are alive.
			name:                "requireRegistration waits out a stale SUCCESS until a fresh registration appears",
			timeout:             100,
			requireRegistration: true,
			checkStates:         []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			want:                gateGreen,
			wantSawNonTerminal:  boolPtr(true),
		},
		{
			// issue #2475: a PR that settled to SUCCESS long ago never polls
			// non-terminal again, so after registrationWindowPolls intervals of
			// nothing but SUCCESS the gate accepts it. timeout: 3 is the
			// boundary case, where the deadline equals the unclamped window
			// (registrationWindowPolls 3 * interval 1) rather than exceeding it.
			name:                "requireRegistration accepts a settled SUCCESS once the registration window elapses",
			timeout:             3,
			requireRegistration: true,
			checkStates:         []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			want:                gateGreen,
			wantSawNonTerminal:  boolPtr(false),
			wantWindowElapsed:   boolPtr(true),
		},
		{
			// The window is a bounded poll count, not a side effect of a tight
			// deadline, so a large MergePollTimeout still resolves to green.
			name:                "requireRegistration accepts a settled SUCCESS well before a large deadline elapses",
			timeout:             100,
			requireRegistration: true,
			checkStates:         []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			want:                gateGreen,
			wantSawNonTerminal:  boolPtr(false),
			wantWindowElapsed:   boolPtr(true),
		},
		{
			// issue #2475 follow-up: when the deadline (2s) is smaller than the
			// unclamped window (3*1), the window never elapses first and an
			// already-green adopted PR hangs into gateTerminal. gateToGreen
			// clamps the window to the deadline so the SUCCESS is still taken.
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
			// issue #2475: timeout: 1 clamps the window to one interval, so this
			// pins only that registrationWindowPolls is not 0 and the guard is
			// not bypassed outright; either would wrongly return gateGreen.
			// issue #2476: only the window-elapsed fallback cleared registered
			// here, never a real poll, so the reason must name the guard.
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
			// issue #2476 boundary: a real poll returns PENDING here, so the
			// guard cleared on genuine evidence rather than the window-elapsed
			// fallback. The later timeout must carry the generic ci-timeout
			// reason, not the registration-guard one.
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
			s := newTestSettle(c, fc, fc)

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
