package settle

import (
	"errors"
	"strings"
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
		name                string
		timeout             int
		checkStates         []forge.RollupState
		checkStateErrs      []error
		requireRegistration bool
		want                gateResult
		wantReasonContains  string
		wantCheckStateCalls int // 0 means "don't check"
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
			// A no-guard implementation (registered always true) would also
			// reach gateGreen here, but after consuming only 2 of the 5
			// scripted states (trusting the first SUCCESS, confirming with
			// the second). The correctly-guarded path defers on both leading
			// SUCCESSes, registers on the PENDING, then trusts+confirms on
			// the trailing SUCCESS pair — consuming all 5. Pinning the call
			// count is what actually distinguishes the two implementations;
			// the final verdict alone does not.
			wantCheckStateCalls: 5,
		},
		{
			// issue #2475: a PR whose checks settled to SUCCESS long ago never
			// produces a non-terminal poll again, so the registration guard
			// must not withhold trust forever — after registrationWindowPolls
			// intervals of nothing but SUCCESS, gateToGreen treats that as
			// proof CI already finished and accepts it.
			name:                "requireRegistration accepts a settled SUCCESS once the registration window elapses",
			timeout:             3,
			requireRegistration: true,
			checkStates:         []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			want:                gateGreen,
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
		},
		{
			// issue #2475: pins registrationWindowPolls' lower bound. With
			// baseConfig's MergePollInterval:0 (actualIv floors to 1) and
			// registrationWindowPolls==3, the registration window is 3
			// poll-intervals wide, but MergePollTimeout(deadline) here is
			// only 1 — the deadline is hit while the rollup has read nothing
			// but SUCCESS and the window has NOT yet elapsed, so the guard
			// must still be withholding trust and the loop must time out
			// rather than accept. This catches two specific regressions: (a)
			// registrationWindowPolls being weakened toward 0 (which would
			// make the window elapse on the very first poll and wrongly
			// return gateGreen instead of gateTerminal), and (b) the guard
			// being bypassed entirely via `registered := true` at the top of
			// gateToGreen (same wrong gateGreen outcome).
			name:                "requireRegistration still withholds trust before the window elapses",
			timeout:             1,
			requireRegistration: true,
			checkStates:         []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			want:                gateTerminal,
			wantReasonContains:  "ci-timeout:",
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

			got, reason := s.gateToGreen("1", 0, testPR, tc.requireRegistration)

			if got != tc.want {
				t.Errorf("gateToGreen = %v, want %v", got, tc.want)
			}
			if tc.wantCheckStateCalls != 0 && fc.CheckStateCalls != tc.wantCheckStateCalls {
				t.Errorf("CheckStateCalls = %d, want %d", fc.CheckStateCalls, tc.wantCheckStateCalls)
			}
			if tc.wantReasonContains != "" {
				if !strings.Contains(reason, tc.wantReasonContains) {
					t.Errorf("gateToGreen reason = %q, want a substring containing %q", reason, tc.wantReasonContains)
				}
			} else if reason != "" {
				t.Errorf("gateToGreen reason = %q, want empty for a non-terminal outcome", reason)
			}
			if len(fc.TransitionStateCalls) > 0 {
				t.Errorf("gateToGreen must never swap state itself; got %d calls: %+v", len(fc.TransitionStateCalls), fc.TransitionStateCalls)
			}
		})
	}
}
