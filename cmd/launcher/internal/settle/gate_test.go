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

// TestGateToGreen verifies that gateToGreen swaps agent-complete on confirmed
// green (independent of any merge attempt) and signals genuineRed on failure.
func TestGateToGreen(t *testing.T) {
	cases := []struct {
		name           string
		timeout        int
		checkStates    []forge.RollupState
		checkStateErrs []error
		wantGreen      bool
		wantGenuineRed bool
		wantTransition bool
		wantTo         forge.DispatchState
	}{
		{
			name:           "SUCCESS on first poll swaps agent-complete",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			wantGreen:      true,
			wantGenuineRed: false,
			wantTransition: true,
			wantTo:         forge.Complete,
		},
		{
			name:           "PENDING then SUCCESS swaps after one wait iteration",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			wantGreen:      true,
			wantGenuineRed: false,
			wantTransition: true,
			wantTo:         forge.Complete,
		},
		{
			name:           "FAILURE signals genuine-red without swap",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateFailure},
			wantGreen:      false,
			wantGenuineRed: true,
		},
		{
			name:           "ERROR signals genuine-red without swap",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateError},
			wantGreen:      false,
			wantGenuineRed: true,
		},
		{
			name:           "NONE times out — non-genuine failure without swap",
			timeout:        0,
			checkStates:    nil,
			wantGreen:      false,
			wantGenuineRed: false,
		},
		{
			// A partial check snapshot can briefly show SUCCESS before all jobs
			// are registered. A second poll that returns FAILURE is genuine red.
			name:           "SUCCESS then FAILURE in confirmation poll is genuine red",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateSuccess, forge.StateFailure},
			wantGreen:      false,
			wantGenuineRed: true,
		},
		{
			// Confirmation returns PENDING — another check registered but not
			// yet settled. Gate keeps waiting; eventually stabilises to SUCCESS.
			name:           "SUCCESS then PENDING in confirmation poll defers completion",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateSuccess, forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			wantGreen:      true,
			wantTransition: true,
			wantTo:         forge.Complete,
		},
		{
			// A 403 or other API error on the first poll must not be silently
			// dropped as StateNone.
			name:           "CheckState API error on first poll is non-retriable",
			timeout:        100,
			checkStateErrs: []error{errors.New("gh api graphql: 403 Forbidden")},
			checkStates:    []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			wantGreen:      false,
			wantGenuineRed: false,
		},
		{
			// A 403 on the confirmation poll must surface as non-retriable.
			name:           "CheckState API error on confirmation poll is non-retriable",
			timeout:        100,
			checkStateErrs: []error{nil, errors.New("gh api graphql: 403 Forbidden")},
			checkStates:    []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			wantGreen:      false,
			wantGenuineRed: false,
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
			s := New(c, fc)

			green, genuineRed := s.gateToGreen("1", testPR)

			if green != tc.wantGreen {
				t.Errorf("gateToGreen green=%v, want %v", green, tc.wantGreen)
			}
			if genuineRed != tc.wantGenuineRed {
				t.Errorf("gateToGreen genuineRed=%v, want %v", genuineRed, tc.wantGenuineRed)
			}
			if tc.wantTransition {
				if len(fc.TransitionStateCalls) == 0 {
					t.Fatalf("expected TransitionState call, got none")
				}
				last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]
				if last.To != tc.wantTo {
					t.Errorf("TransitionState To=%v, want %v", last.To, tc.wantTo)
				}
			} else {
				if len(fc.TransitionStateCalls) > 0 {
					t.Errorf("expected no TransitionState calls, got %d: %+v", len(fc.TransitionStateCalls), fc.TransitionStateCalls)
				}
			}
		})
	}
}
