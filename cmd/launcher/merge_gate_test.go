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
		name        string
		timeout     int
		checkStates []forge.RollupState
		mergeErr    error
		wantMerged  bool
		wantSwapAdd string // expected label added in last SwapLabel call
	}{
		{
			name:        "SUCCESS on first poll merges and completes",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateSuccess},
			wantMerged:  true,
			wantSwapAdd: "agent-complete",
		},
		{
			name:        "PENDING then SUCCESS merges after one wait iteration",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StatePending, forge.StateSuccess},
			wantMerged:  true,
			wantSwapAdd: "agent-complete",
		},
		{
			name:        "FAILURE refuses immediately and labels agent-failed",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateFailure},
			wantMerged:  false,
			wantSwapAdd: "agent-failed",
		},
		{
			name:        "ERROR refuses immediately and labels agent-failed",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateError},
			wantMerged:  false,
			wantSwapAdd: "agent-failed",
		},
		{
			name:        "NONE (no checks registered) times out and labels agent-failed",
			timeout:     0, // expires on first iteration
			checkStates: nil,
			wantMerged:  false,
			wantSwapAdd: "agent-failed",
		},
		{
			name:        "merge command failure surfaces as refusal not success",
			timeout:     100,
			checkStates: []forge.RollupState{forge.StateSuccess},
			mergeErr:    errors.New("merge failed"),
			wantMerged:  false,
			wantSwapAdd: "agent-failed",
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

			got := mergeWhenGreen(c, fc, "1", testPR)

			if got != tc.wantMerged {
				t.Errorf("mergeWhenGreen returned %v, want %v", got, tc.wantMerged)
			}
			if tc.wantMerged && fc.Merged != testPR {
				t.Errorf("Merge not called with PR URL; fc.Merged=%q", fc.Merged)
			}
			if !tc.wantMerged && fc.Merged != "" {
				t.Errorf("Merge should not have been called; fc.Merged=%q", fc.Merged)
			}

			if len(fc.SwapCalls) == 0 {
				t.Fatalf("no SwapLabel calls recorded")
			}
			last := fc.SwapCalls[len(fc.SwapCalls)-1]
			if last.Add != tc.wantSwapAdd {
				t.Errorf("last swap add=%q, want %q", last.Add, tc.wantSwapAdd)
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
