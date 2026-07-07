package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		wantSwapAdd    string
	}{
		{
			name:           "SUCCESS on first poll swaps agent-complete",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			wantGreen:      true,
			wantGenuineRed: false,
			wantSwapAdd:    "agent-complete",
		},
		{
			name:           "PENDING then SUCCESS swaps after one wait iteration",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			wantGreen:      true,
			wantGenuineRed: false,
			wantSwapAdd:    "agent-complete",
		},
		{
			name:           "FAILURE signals genuine-red without swap",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateFailure},
			wantGreen:      false,
			wantGenuineRed: true,
			wantSwapAdd:    "",
		},
		{
			name:           "ERROR signals genuine-red without swap",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateError},
			wantGreen:      false,
			wantGenuineRed: true,
			wantSwapAdd:    "",
		},
		{
			name:           "NONE times out — non-genuine failure without swap",
			timeout:        0,
			checkStates:    nil,
			wantGreen:      false,
			wantGenuineRed: false,
			wantSwapAdd:    "",
		},
		{
			// A partial check snapshot can briefly show SUCCESS before all jobs
			// are registered. A second poll that returns FAILURE is genuine red.
			name:           "SUCCESS then FAILURE in confirmation poll is genuine red",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateSuccess, forge.StateFailure},
			wantGreen:      false,
			wantGenuineRed: true,
			wantSwapAdd:    "",
		},
		{
			// Confirmation returns PENDING — another check registered but not
			// yet settled. Gate keeps waiting; eventually stabilises to SUCCESS.
			name:        "SUCCESS then PENDING in confirmation poll defers completion",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			wantGreen:   true,
			wantSwapAdd: "agent-complete",
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
			if tc.wantSwapAdd != "" {
				if len(fc.SwapCalls) == 0 {
					t.Fatalf("expected SwapLabel call, got none")
				}
				last := fc.SwapCalls[len(fc.SwapCalls)-1]
				if last.Add != tc.wantSwapAdd {
					t.Errorf("swap add=%q, want %q", last.Add, tc.wantSwapAdd)
				}
			} else {
				if len(fc.SwapCalls) > 0 {
					t.Errorf("expected no SwapLabel calls, got %d: %+v", len(fc.SwapCalls), fc.SwapCalls)
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
			fc.RebaseErr = tc.rebaseErr
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

// TestApplyMergeMode_Auto verifies that auto mode is accepted and does not
// call fc.Merge (routed through manual path pending native auto-merge).
func TestApplyMergeMode_Auto(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "auto"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.completeLabel}})

	err := applyMergeMode(c, fc, "1", testPR, nil)
	if err != nil {
		t.Errorf("applyMergeMode auto: unexpected error: %v", err)
	}
	if fc.Merged != "" {
		t.Errorf("auto mode must not call Merge (pending slice); fc.Merged=%q", fc.Merged)
	}
}

// TestSelfHeal_MergeFailureAfterGreenKeepsComplete verifies that a merge
// failure after CI reaches green leaves the issue at agent-complete (not
// agent-failed).
func TestSelfHeal_MergeFailureAfterGreenKeepsComplete(t *testing.T) {
	c := baseConfig()
	c.mergeMode = "immediate"
	c.maxRebaseAttempts = 0
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	// CI is green but merge fails.
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErr = errors.New("required review missing")

	ok := selfHeal(c, fc, func(int) error { return nil }, nil, "1", testPR)
	if !ok {
		t.Error("selfHeal must return true when CI reached green (even if merge fails)")
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, c.completeLabel) {
		t.Errorf("issue must carry %q after green+merge-failure; labels=%v", c.completeLabel, iss.Labels)
	}
	if containsLabel(iss.Labels, c.failedLabel) {
		t.Errorf("issue must NOT carry %q after merge failure on green PR; labels=%v", c.failedLabel, iss.Labels)
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

			adoptAndGate(c, fc, issue{number: "1"}, testPR, func(int) error { return nil }, nil)

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
