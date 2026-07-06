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
	}
}

const testPR = "https://github.com/owner/repo/pull/42"

func TestMergeWhenGreen(t *testing.T) {
	cases := []struct {
		name                      string
		timeout                   int
		maxRebaseAttempts         int // 0 → use baseConfig default (3)
		checkStates               []forge.RollupState
		mergeErr                  error
		mergeErrs                 []error // per-call queue; overrides mergeErr when non-nil
		rebaseErr                 error
		conflictResolveErr        error // nil → resolve succeeds; non-nil → resolve fails
		noConflictResolveFn       bool  // when true pass nil conflictResolveFn
		wantMerged                bool
		wantGenuineRed            bool
		wantSwapAdd               string // expected label added in last SwapLabel call; "" = no swap
		wantRebaseCalled          int    // expected len(fc.RebasedURLs)
		wantConflictResolveCalled int
	}{
		{
			name:           "SUCCESS on first poll merges and completes",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			wantMerged:     true,
			wantGenuineRed: false,
			wantSwapAdd:    "agent-complete",
		},
		{
			name:           "PENDING then SUCCESS merges after one wait iteration",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			wantMerged:     true,
			wantGenuineRed: false,
			wantSwapAdd:    "agent-complete",
		},
		{
			name:           "FAILURE returns genuine-red signal without label swap",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateFailure},
			wantMerged:     false,
			wantGenuineRed: true,
			wantSwapAdd:    "", // selfHeal owns the label swap
		},
		{
			name:           "ERROR returns genuine-red signal without label swap",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateError},
			wantMerged:     false,
			wantGenuineRed: true,
			wantSwapAdd:    "", // selfHeal owns the label swap
		},
		{
			name:           "NONE (no checks registered) times out — non-genuine failure",
			timeout:        0, // expires on first iteration
			checkStates:    nil,
			wantMerged:     false,
			wantGenuineRed: false,
			wantSwapAdd:    "", // selfHeal owns the label swap
		},
		{
			name:           "merge command failure is non-genuine (not a CI red)",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateSuccess},
			mergeErr:       errors.New("merge failed"),
			wantMerged:     false,
			wantGenuineRed: false,
			wantSwapAdd:    "", // selfHeal owns the label swap
		},
		{
			name:              "conflict → rebase → re-poll → merge succeeds",
			timeout:           100,
			maxRebaseAttempts: 3,
			// Confirmation poll + merge poll each need their own SUCCESS.
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			// First Merge call returns ErrMergeConflict; second succeeds.
			mergeErrs:        []error{forge.ErrMergeConflict, nil},
			wantMerged:       true,
			wantGenuineRed:   false,
			wantSwapAdd:      "agent-complete",
			wantRebaseCalled: 1,
		},
		{
			name:              "conflict → rebase fails → non-retriable",
			timeout:           100,
			maxRebaseAttempts: 3,
			// Confirmation poll consumes a second SUCCESS before merge is attempted.
			checkStates:      []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			mergeErrs:        []error{forge.ErrMergeConflict},
			rebaseErr:        errors.New("rebase failed: conflict"),
			wantMerged:       false,
			wantGenuineRed:   false,
			wantSwapAdd:      "",
			wantRebaseCalled: 1,
		},
		{
			name:              "conflict exhausts maxRebaseAttempts → non-retriable",
			timeout:           100,
			maxRebaseAttempts: 1,
			// CI stays SUCCESS; merge keeps returning conflict; rebase keeps succeeding.
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			mergeErrs:   []error{forge.ErrMergeConflict, forge.ErrMergeConflict},
			wantMerged:  false,
			// Exactly 1 rebase attempt allowed; second conflict hits the cap.
			wantGenuineRed:   false,
			wantSwapAdd:      "",
			wantRebaseCalled: 1,
		},
		{
			name:              "rebase conflict → conflict-resolve fn called → merge succeeds",
			timeout:           100,
			maxRebaseAttempts: 3,
			// Confirmation poll needed before each Merge attempt.
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			// Merge conflicts; rebase itself also conflicts (fn resolves it); then merge succeeds.
			mergeErrs:                 []error{forge.ErrMergeConflict, nil},
			rebaseErr:                 forge.ErrMergeConflict,
			wantMerged:                true,
			wantGenuineRed:            false,
			wantSwapAdd:               "agent-complete",
			wantRebaseCalled:          1,
			wantConflictResolveCalled: 1,
		},
		{
			name:              "rebase conflict → conflict-resolve fn fails → non-retriable",
			timeout:           100,
			maxRebaseAttempts: 3,
			// Confirmation poll consumes a second SUCCESS before merge is attempted.
			checkStates:               []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			mergeErrs:                 []error{forge.ErrMergeConflict},
			rebaseErr:                 forge.ErrMergeConflict,
			conflictResolveErr:        errors.New("agent could not resolve conflict"),
			wantMerged:                false,
			wantGenuineRed:            false,
			wantSwapAdd:               "",
			wantRebaseCalled:          1,
			wantConflictResolveCalled: 1,
		},
		{
			name:              "rebase conflict → no conflict-resolve fn → non-retriable",
			timeout:           100,
			maxRebaseAttempts: 3,
			// Confirmation poll consumes a second SUCCESS before merge is attempted.
			checkStates:               []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			mergeErrs:                 []error{forge.ErrMergeConflict},
			rebaseErr:                 forge.ErrMergeConflict,
			noConflictResolveFn:       true,
			wantMerged:                false,
			wantGenuineRed:            false,
			wantSwapAdd:               "",
			wantRebaseCalled:          1,
			wantConflictResolveCalled: 0,
		},
		{
			// A partial check snapshot can briefly show SUCCESS before all jobs
			// are registered. A second poll that returns FAILURE is genuine red.
			name:           "SUCCESS then FAILURE in confirmation poll is genuine red",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateSuccess, forge.StateFailure},
			wantMerged:     false,
			wantGenuineRed: true,
			wantSwapAdd:    "",
		},
		{
			// Confirmation returns PENDING — another check registered but not
			// yet settled. Gate keeps waiting; eventually stabilises to SUCCESS.
			name:        "SUCCESS then PENDING in confirmation poll defers merge",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			wantMerged:  true,
			wantSwapAdd: "agent-complete",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.mergePollTimeout = tc.timeout
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
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
			if len(tc.checkStates) > 0 {
				fc.SetCheckStates(testPR, tc.checkStates)
			}

			conflictResolveCalls := 0
			var conflictResolveFn func(string) error
			if !tc.noConflictResolveFn {
				conflictResolveFn = func(_ string) error {
					conflictResolveCalls++
					return tc.conflictResolveErr
				}
			}

			got, genuineRed := mergeWhenGreen(c, fc, "1", testPR, conflictResolveFn)

			if got != tc.wantMerged {
				t.Errorf("mergeWhenGreen merged=%v, want %v", got, tc.wantMerged)
			}
			if genuineRed != tc.wantGenuineRed {
				t.Errorf("mergeWhenGreen genuineRed=%v, want %v", genuineRed, tc.wantGenuineRed)
			}
			if tc.wantMerged && fc.Merged != testPR {
				t.Errorf("Merge not called with PR URL; fc.Merged=%q", fc.Merged)
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

			if tc.wantSwapAdd != "" {
				if len(fc.SwapCalls) == 0 {
					t.Fatalf("expected SwapLabel call but got none")
				}
				last := fc.SwapCalls[len(fc.SwapCalls)-1]
				if last.Add != tc.wantSwapAdd {
					t.Errorf("last swap add=%q, want %q", last.Add, tc.wantSwapAdd)
				}
			} else {
				if len(fc.SwapCalls) > 0 {
					t.Errorf("expected no SwapLabel calls, got %d: %+v", len(fc.SwapCalls), fc.SwapCalls)
				}
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
