package settle

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func baseConfig() Config {
	return Config{
		CompleteLabel:     "agent-complete",
		MergePollInterval: 0,   // no sleep in tests
		MergePollTimeout:  100, // large enough for multi-poll tests
		MergeMode:         "immediate",
	}
}

const testPR = "https://github.com/owner/repo/pull/42"

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
		name           string
		timeout        int
		checkStates    []forge.RollupState
		checkStateErrs []error
		want           gateResult
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
			name:        "NONE times out — non-genuine failure without swap",
			timeout:     0,
			checkStates: nil,
			want:        gateTerminal,
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
			name:           "CheckState API error on first poll is non-retriable",
			timeout:        100,
			checkStateErrs: []error{errors.New("gh api graphql: 403 Forbidden")},
			checkStates:    []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			want:           gateTerminal,
		},
		{
			// A 403 on the confirmation poll must surface as non-retriable.
			name:           "CheckState API error on confirmation poll is non-retriable",
			timeout:        100,
			checkStateErrs: []error{nil, errors.New("gh api graphql: 403 Forbidden")},
			checkStates:    []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			want:           gateTerminal,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.MergePollTimeout = tc.timeout
			fc := forge.NewFake()
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
			if len(tc.checkStates) > 0 {
				fc.SetCheckStates(testPR, tc.checkStates)
			}
			if len(tc.checkStateErrs) > 0 {
				fc.SetCheckStateErrors(testPR, tc.checkStateErrs)
			}
			s := New(c, fc, fc)

			got := s.gateToGreen("1", testPR)

			if got != tc.want {
				t.Errorf("gateToGreen = %v, want %v", got, tc.want)
			}
			if len(fc.TransitionStateCalls) > 0 {
				t.Errorf("gateToGreen must never swap state itself; got %d calls: %+v", len(fc.TransitionStateCalls), fc.TransitionStateCalls)
			}
		})
	}
}
