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
		name           string
		timeout        int
		checkStates    []forge.RollupState
		mergeErr       error
		wantMerged     bool
		wantGenuineRed bool
		wantSwapAdd    string // expected label added in last SwapLabel call; "" = no swap
	}{
		{
			name:           "SUCCESS on first poll merges and completes",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StateSuccess},
			wantMerged:     true,
			wantGenuineRed: false,
			wantSwapAdd:    "agent-complete",
		},
		{
			name:           "PENDING then SUCCESS merges after one wait iteration",
			timeout:        100,
			checkStates:    []forge.RollupState{forge.StatePending, forge.StateSuccess},
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
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.mergePollTimeout = tc.timeout

			fc := forge.NewFake()
			fc.MergeErr = tc.mergeErr
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
			if len(tc.checkStates) > 0 {
				fc.SetCheckStates(testPR, tc.checkStates)
			}

			got, genuineRed := mergeWhenGreen(c, fc, "1", testPR)

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
