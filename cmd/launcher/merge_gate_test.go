package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

func baseConfig() config {
	return config{
		inProgressLabel:   "agent-in-progress",
		failedLabel:       "agent-failed",
		completeLabel:     "agent-complete",
		mergePollInterval: 0,   // no sleep in tests
		mergePollTimeout:  100, // large enough for multi-poll tests
		mergeMode:         "immediate",
		codeForge:         "github",
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
			c.mergePollTimeout = tc.timeout
			fc := forge.NewFake()
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
			if len(tc.checkStates) > 0 {
				fc.SetCheckStates(testPR, tc.checkStates)
			}
			if len(tc.checkStateErrs) > 0 {
				fc.SetCheckStateErrors(testPR, tc.checkStateErrs)
			}

			green, genuineRed := gateToGreen(c, fc, "1", testPR)

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

// TestMergeImmediate verifies the rebase-retry and conflict-resolve behaviors
// that run inside applyMergeMode for the immediate merge mode.
func TestMergeImmediate(t *testing.T) {
	cases := []struct {
		name                      string
		maxRebaseAttempts         int
		mergeErr                  error
		mergeErrs                 []error
		rebaseErr                 error
		rebaseErrs                []error
		conflictResolveErr        error
		noConflictResolveFn       bool
		wantErr                   bool
		wantMerged                bool
		wantRebaseCalled          int
		wantConflictResolveCalled int
	}{
		{
			name:       "clean merge succeeds",
			wantErr:    false,
			wantMerged: true,
		},
		{
			name:       "non-conflict merge failure is returned",
			mergeErr:   errors.New("required review missing"),
			wantErr:    true,
			wantMerged: false,
		},
		{
			name:              "conflict → rebase → retry succeeds",
			maxRebaseAttempts: 3,
			mergeErrs:         []error{forge.ErrMergeConflict, nil},
			wantErr:           false,
			wantMerged:        true,
			wantRebaseCalled:  1,
		},
		{
			name:              "conflict → rebase fails → error returned",
			maxRebaseAttempts: 3,
			mergeErrs:         []error{forge.ErrMergeConflict},
			rebaseErr:         errors.New("rebase failed: conflict"),
			wantErr:           true,
			wantMerged:        false,
			wantRebaseCalled:  1,
		},
		{
			name:              "conflict exhausts maxRebaseAttempts → error returned",
			maxRebaseAttempts: 1,
			mergeErrs:         []error{forge.ErrMergeConflict, forge.ErrMergeConflict},
			wantErr:           true,
			wantMerged:        false,
			wantRebaseCalled:  1,
		},
		{
			name:                      "rebase conflict → conflict-resolve fn succeeds → merge succeeds",
			maxRebaseAttempts:         3,
			mergeErrs:                 []error{forge.ErrMergeConflict, nil},
			rebaseErr:                 forge.ErrMergeConflict,
			wantErr:                   false,
			wantMerged:                true,
			wantRebaseCalled:          1,
			wantConflictResolveCalled: 1,
		},
		{
			name:                      "rebase conflict → conflict-resolve fn fails → error returned",
			maxRebaseAttempts:         3,
			mergeErrs:                 []error{forge.ErrMergeConflict},
			rebaseErr:                 forge.ErrMergeConflict,
			conflictResolveErr:        errors.New("agent could not resolve conflict"),
			wantErr:                   true,
			wantMerged:                false,
			wantRebaseCalled:          1,
			wantConflictResolveCalled: 1,
		},
		{
			// After conflict-resolve succeeds, the forge's mergeability
			// snapshot is briefly stale and the next Merge still reports a
			// conflict. The loop must retry Merge directly instead of
			// invoking Rebase a second time (the box already rebased and
			// force-pushed).
			name:                      "conflict-resolve succeeds → stale conflict on retry does not re-rebase",
			maxRebaseAttempts:         3,
			mergeErrs:                 []error{forge.ErrMergeConflict, forge.ErrMergeConflict, nil},
			rebaseErr:                 forge.ErrMergeConflict,
			wantErr:                   false,
			wantMerged:                true,
			wantRebaseCalled:          1,
			wantConflictResolveCalled: 1,
		},
		{
			name:                      "rebase conflict → no conflict-resolve fn → error returned",
			maxRebaseAttempts:         3,
			mergeErrs:                 []error{forge.ErrMergeConflict},
			rebaseErr:                 forge.ErrMergeConflict,
			noConflictResolveFn:       true,
			wantErr:                   true,
			wantMerged:                false,
			wantRebaseCalled:          1,
			wantConflictResolveCalled: 0,
		},
		{
			// A transient push failure (forge outage, network fault) during
			// the force-push must not block the merge outright — it's
			// retried, and here the retry succeeds.
			name:              "conflict → rebase transient push failure → retry succeeds",
			maxRebaseAttempts: 3,
			mergeErrs:         []error{forge.ErrMergeConflict, nil},
			rebaseErrs:        []error{forge.ErrTransientPushFailure, nil},
			wantErr:           false,
			wantMerged:        true,
			wantRebaseCalled:  2,
		},
		{
			// The forge stays down: every retry hits the same transient
			// error. The retry must be bounded — not spin indefinitely —
			// and the eventual failure must still surface to the caller.
			name:              "conflict → rebase transient push failure persists → retries exhausted, error returned",
			maxRebaseAttempts: 2,
			mergeErrs:         []error{forge.ErrMergeConflict},
			rebaseErrs: []error{
				forge.ErrTransientPushFailure,
				forge.ErrTransientPushFailure,
				forge.ErrTransientPushFailure,
			},
			wantErr:          true,
			wantMerged:       false,
			wantRebaseCalled: 3,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			if tc.maxRebaseAttempts != 0 {
				c.maxRebaseAttempts = tc.maxRebaseAttempts
			}
			fc := forge.NewFake()
			if len(tc.mergeErrs) > 0 {
				fc.MergeErrs = tc.mergeErrs
			} else {
				fc.MergeErr = tc.mergeErr
			}
			if len(tc.rebaseErrs) > 0 {
				fc.RebaseErrs = tc.rebaseErrs
			} else {
				fc.RebaseErr = tc.rebaseErr
			}
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.completeLabel}})

			conflictResolveCalls := 0
			var conflictResolveFn func(string) error
			if !tc.noConflictResolveFn {
				conflictResolveFn = func(_ string) error {
					conflictResolveCalls++
					return tc.conflictResolveErr
				}
			}

			err := mergeImmediate(c, fc, "1", testPR, conflictResolveFn)

			if (err != nil) != tc.wantErr {
				t.Errorf("mergeImmediate err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantMerged && fc.Merged != testPR {
				t.Errorf("Merge not called; fc.Merged=%q", fc.Merged)
			}
			if !tc.wantMerged && fc.Merged != "" {
				t.Errorf("Merge should not have been called; fc.Merged=%q", fc.Merged)
			}
			if got := len(fc.RebasedURLs); got != tc.wantRebaseCalled {
				t.Errorf("Rebase called %d times, want %d", got, tc.wantRebaseCalled)
			}
			if conflictResolveCalls != tc.wantConflictResolveCalled {
				t.Errorf("conflictResolveFn called %d times, want %d", conflictResolveCalls, tc.wantConflictResolveCalled)
			}
		})
	}
}

// TestApplyMergeMode_Immediate verifies that immediate mode calls fc.Merge.
func TestApplyMergeMode_Immediate(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "immediate"
	c.maxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.completeLabel}})

	err := applyMergeMode(c, fc, "1", testPR, nil)
	if err != nil {
		t.Errorf("applyMergeMode immediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("immediate mode must call Merge; fc.Merged=%q", fc.Merged)
	}
}

// TestApplyMergeMode_Manual verifies that manual mode does not call fc.Merge.
func TestApplyMergeMode_Manual(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "manual"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.completeLabel}})

	err := applyMergeMode(c, fc, "1", testPR, nil)
	if err != nil {
		t.Errorf("applyMergeMode manual: unexpected error: %v", err)
	}
	if fc.Merged != "" {
		t.Errorf("manual mode must not call Merge; fc.Merged=%q", fc.Merged)
	}
}

// TestApplyMergeMode_Auto_EnqueuesAutoMerge verifies that auto mode calls
// EnqueueAutoMerge and does not call fc.Merge.
func TestApplyMergeMode_Auto_EnqueuesAutoMerge(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "auto"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.completeLabel}})

	err := applyMergeMode(c, fc, "1", testPR, nil)
	if err != nil {
		t.Errorf("applyMergeMode auto: unexpected error: %v", err)
	}
	if fc.Merged != "" {
		t.Errorf("auto mode must not call Merge; fc.Merged=%q", fc.Merged)
	}
	if len(fc.EnqueueAutoMergeCalls) != 1 || fc.EnqueueAutoMergeCalls[0] != testPR {
		t.Errorf("auto mode must call EnqueueAutoMerge(%q); calls=%v", testPR, fc.EnqueueAutoMergeCalls)
	}
}

// TestApplyMergeMode_Auto_EnqueueFailureFallsBack verifies that when
// EnqueueAutoMerge fails, applyMergeMode returns nil (no agent-failed) and
// posts a warning comment to the issue.
func TestApplyMergeMode_Auto_EnqueueFailureFallsBack(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "auto"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.completeLabel}})
	fc.EnqueueAutoMergeErr = fmt.Errorf("gh pr merge --auto: permission denied")

	err := applyMergeMode(c, fc, "1", testPR, nil)
	if err != nil {
		t.Errorf("auto mode enqueue failure must not propagate error; got: %v", err)
	}
	if fc.Merged != "" {
		t.Errorf("auto mode must not call Merge; fc.Merged=%q", fc.Merged)
	}
	if len(fc.EnqueueAutoMergeCalls) == 0 {
		t.Error("EnqueueAutoMerge must have been called")
	}
	if len(fc.CommentCalls) == 0 {
		t.Error("a warning comment must be posted when auto-merge enqueue fails")
	}
}

// TestSelfHeal_MergeFailureAfterGreenKeepsComplete verifies that a merge
// failure after CI reaches green leaves the issue at agent-complete (not
// agent-failed) and returns (ok=true, merged=false).
func TestSelfHeal_MergeFailureAfterGreenKeepsComplete(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "immediate"
	c.maxRebaseAttempts = 0
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	// CI is green but merge fails.
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErr = errors.New("required review missing")

	ok, merged := selfHeal(c, fc, dispatch.NewFake(), "1", testPR)
	if !ok {
		t.Error("selfHeal must return ok=true when CI reached green (even if merge fails)")
	}
	if merged {
		t.Error("selfHeal must return merged=false when merge fails")
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, c.completeLabel) {
		t.Errorf("issue must carry %q after green+merge-failure; labels=%v", c.completeLabel, iss.Labels)
	}
	if containsLabel(iss.Labels, c.failedLabel) {
		t.Errorf("issue must NOT carry %q after merge failure on green PR; labels=%v", c.failedLabel, iss.Labels)
	}
}

// TestSelfHeal_MergeGuardHit_DowngradesToManual verifies that a PR touching a
// guarded path is never merged — regardless of MERGE_MODE — and instead posts
// a comment naming the matched path(s) and the knob, leaving the issue at
// agent-complete exactly like a manual-mode green PR.
func TestSelfHeal_MergeGuardHit_DowngradesToManual(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "immediate"
	c.mergeGuardPaths = ".github/**,**/CLAUDE.md"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{"src/main.go", ".github/workflows/ci.yml"})

	ok, merged := selfHeal(c, fc, dispatch.NewFake(), "1", testPR)
	if !ok {
		t.Error("selfHeal must return ok=true — CI reached green")
	}
	if merged {
		t.Error("selfHeal must return merged=false when the merge guard hits")
	}
	if fc.Merged != "" {
		t.Errorf("merge guard must prevent Merge from being called; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, c.completeLabel) {
		t.Errorf("issue must carry %q after a guard-downgraded green PR; labels=%v", c.completeLabel, iss.Labels)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one guard comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, ".github/workflows/ci.yml") {
		t.Errorf("comment must name the matched path; body=%q", body)
	}
	if !strings.Contains(body, "MERGE_GUARD_PATHS") {
		t.Errorf("comment must name the knob that triggered it; body=%q", body)
	}
}

// TestSelfHeal_MergeGuardHit_AutoMode verifies the guard fires under
// MERGE_MODE=auto too — the acceptance criterion covers both immediate and
// auto, since the guard downgrades regardless of mode.
func TestSelfHeal_MergeGuardHit_AutoMode(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "auto"
	c.mergeGuardPaths = ".github/**"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{".github/workflows/ci.yml"})

	ok, merged := selfHeal(c, fc, dispatch.NewFake(), "1", testPR)
	if !ok || merged {
		t.Errorf("selfHeal(ok=%v, merged=%v), want (true, false) for a guard-hit auto-mode PR", ok, merged)
	}
	if len(fc.EnqueueAutoMergeCalls) != 0 {
		t.Errorf("guard hit must prevent EnqueueAutoMerge; calls=%v", fc.EnqueueAutoMergeCalls)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one guard comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
}

// TestSelfHeal_MergeGuardMiss_MergesNormally verifies that a green PR
// touching no guarded path proceeds exactly as it would with no guard set.
func TestSelfHeal_MergeGuardMiss_MergesNormally(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "immediate"
	c.mergeGuardPaths = ".github/**,**/CLAUDE.md"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{"src/main.go"})

	ok, merged := selfHeal(c, fc, dispatch.NewFake(), "1", testPR)
	if !ok || !merged {
		t.Errorf("selfHeal(ok=%v, merged=%v), want (true, true) for a non-guarded green PR", ok, merged)
	}
	if fc.Merged != testPR {
		t.Errorf("expected Merge to be called; fc.Merged=%q", fc.Merged)
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("no guard comment expected on a miss; got %+v", fc.CommentCalls)
	}
}

// TestSelfHeal_MergeGuardCheckError_FailsSafe verifies that when the changed-
// file list cannot be read at all, selfHeal fails safe: no merge, a
// precautionary comment, and the issue stays at agent-complete (not
// agent-failed) rather than silently falling through to MERGE_MODE.
func TestSelfHeal_MergeGuardCheckError_FailsSafe(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "immediate"
	c.mergeGuardPaths = ".github/**"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.PRFilesErr = errors.New("gh api pulls files: 403 Forbidden")

	ok, merged := selfHeal(c, fc, dispatch.NewFake(), "1", testPR)
	if !ok {
		t.Error("selfHeal must return ok=true — CI reached green")
	}
	if merged {
		t.Error("selfHeal must return merged=false when the guard check errors")
	}
	if fc.Merged != "" {
		t.Errorf("a guard-check error must prevent Merge from being called; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, c.completeLabel) {
		t.Errorf("issue must carry %q after a guard-check error on a green PR; labels=%v", c.completeLabel, iss.Labels)
	}
	if containsLabel(iss.Labels, c.failedLabel) {
		t.Errorf("issue must NOT carry %q after a guard-check error; labels=%v", c.failedLabel, iss.Labels)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one precautionary comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
}

// TestAdoptAndGate_ImmediateMergeFailureStaysComplete verifies that adoptAndGate
// in immediate mode does not demote the issue to agent-failed when the merge
// itself fails after CI goes green (spec: merge-blocked stays at agent-complete).
func TestAdoptAndGate_ImmediateMergeFailureStaysComplete(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "immediate"
	c.maxRebaseAttempts = 0
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErr = errors.New("required review missing")

	adoptAndGate(c, fc, dispatch.NewFake(), issue{number: "1"}, testPR)

	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, c.completeLabel) {
		t.Errorf("issue must carry %q after green+merge-failure; labels=%v", c.completeLabel, iss.Labels)
	}
	if containsLabel(iss.Labels, c.failedLabel) {
		t.Errorf("issue must NOT carry %q after merge failure on green PR; labels=%v", c.failedLabel, iss.Labels)
	}
}

// TestAutoMergePreflight verifies that checkAutoMergePreflight aborts dispatch
// when MERGE_MODE=auto and the repo disallows auto-merge, and is a no-op for
// other modes.
func TestAutoMergePreflight(t *testing.T) {
	cases := []struct {
		name             string
		mergeMode        string
		codeForge        string
		autoMergeAllowed bool
		autoMergeErr     error
		wantErr          bool
		wantErrContains  string
	}{
		{
			name:             "auto mode and repo allows auto-merge — ok",
			mergeMode:        "auto",
			autoMergeAllowed: true,
			wantErr:          false,
		},
		{
			name:             "auto mode and repo disallows auto-merge — abort",
			mergeMode:        "auto",
			autoMergeAllowed: false,
			wantErr:          true,
			wantErrContains:  "auto-merge",
		},
		{
			name:            "auto mode and CanAutoMerge API error — abort",
			mergeMode:       "auto",
			autoMergeErr:    fmt.Errorf("gh api graphql: 403 Forbidden"),
			wantErr:         true,
			wantErrContains: "403",
		},
		{
			name:             "immediate mode — no preflight check",
			mergeMode:        "immediate",
			autoMergeAllowed: false, // would fail if checked
			wantErr:          false,
		},
		{
			name:             "manual mode — no preflight check",
			mergeMode:        "manual",
			autoMergeAllowed: false,
			wantErr:          false,
		},
		{
			name:            "auto mode with CODE_FORGE=git — abort before any CanAutoMerge call",
			mergeMode:       "auto",
			codeForge:       "git",
			wantErr:         true,
			wantErrContains: "CODE_FORGE=github",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.mergeMode = tc.mergeMode
			if tc.codeForge != "" {
				c.codeForge = tc.codeForge
			}
			fc := forge.NewFake()
			fc.AutoMergeAllowed = tc.autoMergeAllowed
			fc.AutoMergeErr = tc.autoMergeErr

			err := checkAutoMergePreflight(c, fc)

			if (err != nil) != tc.wantErr {
				t.Errorf("checkAutoMergePreflight err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErrContains != "" && err != nil && !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrContains)
			}
		})
	}
}

// TestAdoptAndGate_ManualModeStaysComplete verifies that adoptAndGate in manual
// (and auto) mode leaves the issue at agent-complete and never swaps it to
// agent-failed after CI reaches green without a merge.
func TestAdoptAndGate_ManualModeStaysComplete(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			c := baseConfig()
			c.mergeMode = mode
			fc := forge.NewFake()
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
			fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

			adoptAndGate(c, fc, dispatch.NewFake(), issue{number: "1"}, testPR)

			iss, _ := fc.Issue("1")
			if !containsLabel(iss.Labels, c.completeLabel) {
				t.Errorf("mode=%s: issue must carry %q after green; labels=%v", mode, c.completeLabel, iss.Labels)
			}
			if containsLabel(iss.Labels, c.failedLabel) {
				t.Errorf("mode=%s: issue must NOT carry %q after green in non-immediate mode; labels=%v", mode, c.failedLabel, iss.Labels)
			}
		})
	}
}

// TestSelfHeal_GitForge_PushOnlyLanding verifies that for CODE_FORGE=git,
// selfHeal skips the CI-wait/merge-gate entirely (there is no CI or PR to
// watch — the Box already pushed the branch) and instead marks the issue
// Complete immediately, then applies MERGE_MODE against the git Code Forge's
// push-only Merge.
func TestSelfHeal_GitForge_PushOnlyLanding(t *testing.T) {
	cases := []struct {
		name       string
		mergeMode  string
		wantMerged bool
	}{
		{name: "manual leaves the branch as pushed", mergeMode: "manual", wantMerged: false},
		{name: "immediate pushes to the target branch", mergeMode: "immediate", wantMerged: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.codeForge = "git"
			c.mergeMode = tc.mergeMode
			fc := forge.NewFake()
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
			branch := "agent/issue-1"

			ok, merged := selfHeal(c, fc, dispatch.NewFake(), "1", branch)

			if !ok {
				t.Fatal("selfHeal must return ok=true for CODE_FORGE=git — there is no CI to fail")
			}
			if merged != tc.wantMerged {
				t.Errorf("selfHeal merged=%v, want %v", merged, tc.wantMerged)
			}
			if tc.wantMerged && fc.Merged != branch {
				t.Errorf("expected Merge(%q); fc.Merged=%q", branch, fc.Merged)
			}
			if !tc.wantMerged && fc.Merged != "" {
				t.Errorf("Merge must not be called; fc.Merged=%q", fc.Merged)
			}
			iss, _ := fc.Issue("1")
			if !containsLabel(iss.Labels, c.completeLabel) {
				t.Errorf("issue must carry %q; labels=%v", c.completeLabel, iss.Labels)
			}
		})
	}
}

// TestSelfHeal_GitForge_PushFailureStaysCompleteNotFailed verifies that for
// CODE_FORGE=git, a push failure under MERGE_MODE=immediate leaves the issue
// at agent-complete with a comment — never demoted to agent-failed — matching
// the github adapter's post-green merge-blocked contract (ADR 0012).
func TestSelfHeal_GitForge_PushFailureStaysCompleteNotFailed(t *testing.T) {
	c := baseConfig()
	c.codeForge = "git"
	c.mergeMode = "immediate"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.MergeErr = errors.New("remote rejected: non-fast-forward")
	branch := "agent/issue-1"

	ok, merged := selfHeal(c, fc, dispatch.NewFake(), "1", branch)

	if !ok {
		t.Error("selfHeal must return ok=true — there is no CI to fail for CODE_FORGE=git")
	}
	if merged {
		t.Error("selfHeal must return merged=false when the push fails")
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, c.completeLabel) {
		t.Errorf("issue must carry %q after a push failure; labels=%v", c.completeLabel, iss.Labels)
	}
	if containsLabel(iss.Labels, c.failedLabel) {
		t.Errorf("issue must NOT carry %q after a push failure; labels=%v", c.failedLabel, iss.Labels)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one merge-blocked comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
}

// TestNoGhExecOutsideForge walks all non-test Go source files in cmd/launcher,
// excluding internal/forge, and fails if any contain exec.Command("gh" —
// keeping all gh API calls behind the forge seam.
func TestNoGhExecOutsideForge(t *testing.T) {
	// Tests run with CWD = the package directory (cmd/launcher).
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip the forge package itself — that's where gh calls are allowed.
		if strings.HasPrefix(filepath.ToSlash(path), "internal/forge") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), `exec.Command("gh"`) {
			t.Errorf("%s: contains exec.Command(\"gh\") — all gh calls must go through forge.Client", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestNoRunnerExecOutsidePackage walks all non-test Go source files in
// cmd/launcher, excluding internal/runner, and fails if any contain an
// exec.Command literal for the container CLI or system tools — keeping all
// sandbox life-cycle calls behind the runner seam.
func TestNoRunnerExecOutsidePackage(t *testing.T) {
	forbidden := []string{
		`exec.Command("bwrap"`,
		`exec.Command("nix"`,
		`exec.Command("podman"`,
		`exec.Command("docker"`,
	}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(path), "internal/runner") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, needle := range forbidden {
			if strings.Contains(content, needle) {
				t.Errorf("%s: contains %q — all sandbox exec calls must go through runner.Runner", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestNoOutcomeParsingOutsidePackage walks all non-test Go source files in
// cmd/launcher, excluding internal/outcome, and fails if any contain the
// SPINDRIFT_OUTCOME prefix literal — keeping all outcome parsing behind the
// outcome seam.
func TestNoOutcomeParsingOutsidePackage(t *testing.T) {
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip the outcome package — that's where the parsing lives.
		if strings.HasPrefix(filepath.ToSlash(path), "internal/outcome") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Check both Go string-quoting styles; backtick literals bypass a
		// double-quote-only check.
		content := string(data)
		if strings.Contains(content, `"SPINDRIFT_OUTCOME "`) ||
			strings.Contains(content, "`SPINDRIFT_OUTCOME `") {
			t.Errorf("%s: contains SPINDRIFT_OUTCOME parsing — all outcome parsing must go through internal/outcome", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestNoBoxConstructionOutsideDispatchPackage walks all non-test Go source
// files in cmd/launcher, excluding internal/dispatch, and fails if any
// construct a runner.Box, open an issue log file for writing, or classify a
// Driver exit directly — the per-issue execution seam established by issue
// #441.
func TestNoBoxConstructionOutsideDispatchPackage(t *testing.T) {
	forbidden := []string{
		`runner.Box{`,
		`os.Create(`,
		`.ClassifyTransient(`,
	}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(path), "internal/dispatch") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, needle := range forbidden {
			if strings.Contains(content, needle) {
				t.Errorf("%s: contains %q — all Box construction, issue-log creation, and Driver classification must go through internal/dispatch", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
