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

// TestMergeWhenGreen_SuccessOnFirstPoll: SUCCESS on the first poll → merge and
// swap to completeLabel.
func TestMergeWhenGreen_SuccessOnFirstPoll(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess})

	if !mergeWhenGreen(c, fc, "1", testPR) {
		t.Fatal("want true (merged), got false")
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called with PR URL; fc.Merged=%q", fc.Merged)
	}
	assertLastSwap(t, fc, "1", c.completeLabel, c.inProgressLabel)
}

// TestMergeWhenGreen_PendingThenSuccess: PENDING then SUCCESS → merge after
// one wait iteration, no real sleep required.
func TestMergeWhenGreen_PendingThenSuccess(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess})

	if !mergeWhenGreen(c, fc, "2", testPR) {
		t.Fatal("want true (merged), got false")
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called; fc.Merged=%q", fc.Merged)
	}
	assertLastSwap(t, fc, "2", c.completeLabel, c.inProgressLabel)
}

// TestMergeWhenGreen_FailureRefuses: FAILURE → refuse immediately and swap to
// failedLabel.
func TestMergeWhenGreen_FailureRefuses(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})

	if mergeWhenGreen(c, fc, "3", testPR) {
		t.Fatal("want false (refused), got true")
	}
	if fc.Merged != "" {
		t.Errorf("Merge should not have been called; fc.Merged=%q", fc.Merged)
	}
	assertLastSwap(t, fc, "3", c.failedLabel, c.inProgressLabel)
}

// TestMergeWhenGreen_ErrorRefuses: ERROR → refuse immediately.
func TestMergeWhenGreen_ErrorRefuses(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "4", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateError})

	if mergeWhenGreen(c, fc, "4", testPR) {
		t.Fatal("want false (refused), got true")
	}
	assertLastSwap(t, fc, "4", c.failedLabel, c.inProgressLabel)
}

// TestMergeWhenGreen_NoneTimesOut: NONE (no checks registered) keeps waiting
// and refuses once the timeout is reached.
func TestMergeWhenGreen_NoneTimesOut(t *testing.T) {
	c := baseConfig()
	c.mergePollTimeout = 0 // expire immediately
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "5", Labels: []string{c.inProgressLabel}})
	// No scripted states → CheckState returns StateNone each call.

	if mergeWhenGreen(c, fc, "5", testPR) {
		t.Fatal("want false (timeout), got true")
	}
	if fc.Merged != "" {
		t.Errorf("Merge should not have been called; fc.Merged=%q", fc.Merged)
	}
	assertLastSwap(t, fc, "5", c.failedLabel, c.inProgressLabel)
}

// TestMergeWhenGreen_MergeFailureRefuses: SUCCESS state but Merge returns an
// error → treated as a failure, not a success.
func TestMergeWhenGreen_MergeFailureRefuses(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.MergeErr = errors.New("merge failed")
	fc.SetIssue(forge.Issue{Number: "6", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess})

	if mergeWhenGreen(c, fc, "6", testPR) {
		t.Fatal("want false (merge error), got true")
	}
	if fc.Merged != "" {
		t.Errorf("Merged should be empty after failed merge; fc.Merged=%q", fc.Merged)
	}
	assertLastSwap(t, fc, "6", c.failedLabel, c.inProgressLabel)
}

// assertLastSwap verifies the most recent SwapLabel call on the fake.
func assertLastSwap(t *testing.T, fc *forge.Fake, num, add, remove string) {
	t.Helper()
	if len(fc.SwapCalls) == 0 {
		t.Fatalf("no SwapLabel calls recorded")
	}
	last := fc.SwapCalls[len(fc.SwapCalls)-1]
	if last.Num != num || last.Add != add || last.Remove != remove {
		t.Errorf("last swap: got (%q add=%q remove=%q), want (%q add=%q remove=%q)",
			last.Num, last.Add, last.Remove, num, add, remove)
	}
}

// TestNoGhExecOutsideForge ensures that no Go source file in cmd/launcher
// (outside internal/forge) calls exec.Command("gh" directly, keeping all gh
// logic behind the forge seam.
func TestNoGhExecOutsideForge(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(data), `exec.Command("gh"`) {
			t.Errorf("%s: contains exec.Command(\"gh\") — all gh calls must go through forge.Client", f)
		}
	}
}
