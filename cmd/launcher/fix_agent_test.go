package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

const testFixPR = "https://github.com/owner/repo/pull/99"

func fixConfig(maxFixAttempts int) config {
	c := baseConfig()
	c.maxFixAttempts = maxFixAttempts
	return c
}

// noFix is a run function that records calls but always succeeds.
func trackFix(calls *[]int) func(int) error {
	return func(pass int) error {
		*calls = append(*calls, pass)
		return nil
	}
}

func TestSelfHeal_SuccessFirstTry(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StateSuccess})

	var fixCalls []int
	merged := selfHeal(c, fc, trackFix(&fixCalls), nil, "1", testFixPR)

	if !merged {
		t.Error("expected merged=true on first-try SUCCESS")
	}
	if len(fixCalls) != 0 {
		t.Errorf("expected no fix calls, got %v", fixCalls)
	}
	if len(fc.SwapCalls) == 0 {
		t.Fatal("expected at least one SwapLabel call (complete label)")
	}
	if last := fc.SwapCalls[len(fc.SwapCalls)-1]; last.Add != c.completeLabel {
		t.Errorf("last swap add=%q, want %q", last.Add, c.completeLabel)
	}
}

func TestSelfHeal_GenuineRedMaxZero(t *testing.T) {
	c := fixConfig(0) // no fix passes allowed
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StateFailure})

	var fixCalls []int
	merged := selfHeal(c, fc, trackFix(&fixCalls), nil, "1", testFixPR)

	if merged {
		t.Error("expected merged=false (maxFixAttempts=0)")
	}
	if len(fixCalls) != 0 {
		t.Errorf("expected no fix calls (maxFixAttempts=0), got %v", fixCalls)
	}
	if len(fc.SwapCalls) == 0 {
		t.Fatal("expected SwapLabel call for failedLabel")
	}
	if last := fc.SwapCalls[len(fc.SwapCalls)-1]; last.Add != c.failedLabel {
		t.Errorf("last swap add=%q, want %q", last.Add, c.failedLabel)
	}
}

func TestSelfHeal_GenuineRedFixSucceeds(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	// First poll: FAILURE; after fix box: SUCCESS
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess})

	var fixCalls []int
	merged := selfHeal(c, fc, trackFix(&fixCalls), nil, "1", testFixPR)

	if !merged {
		t.Error("expected merged=true after one fix pass")
	}
	if len(fixCalls) != 1 || fixCalls[0] != 1 {
		t.Errorf("expected exactly fix-pass-1, got %v", fixCalls)
	}
	if len(fc.SwapCalls) == 0 {
		t.Fatal("expected SwapLabel call (complete label)")
	}
	if last := fc.SwapCalls[len(fc.SwapCalls)-1]; last.Add != c.completeLabel {
		t.Errorf("last swap add=%q, want %q", last.Add, c.completeLabel)
	}
}

func TestSelfHeal_ExhaustsAllPasses(t *testing.T) {
	c := fixConfig(2)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	// All polls return FAILURE — never fixed.
	fc.SetCheckStates(testFixPR, []forge.RollupState{
		forge.StateFailure,
		forge.StateFailure,
		forge.StateFailure,
	})

	var fixCalls []int
	merged := selfHeal(c, fc, trackFix(&fixCalls), nil, "1", testFixPR)

	if merged {
		t.Error("expected merged=false after exhausting all fix passes")
	}
	if len(fixCalls) != 2 {
		t.Errorf("expected %d fix calls (maxFixAttempts), got %d: %v", c.maxFixAttempts, len(fixCalls), fixCalls)
	}
	// Fix passes should be numbered 1, 2
	for i, p := range fixCalls {
		if p != i+1 {
			t.Errorf("fixCalls[%d]=%d, want %d", i, p, i+1)
		}
	}
	if len(fc.SwapCalls) == 0 {
		t.Fatal("expected SwapLabel call for failedLabel")
	}
	if last := fc.SwapCalls[len(fc.SwapCalls)-1]; last.Add != c.failedLabel {
		t.Errorf("last swap add=%q, want %q", last.Add, c.failedLabel)
	}
}

func TestSelfHeal_ErrorStateTriggersFixPass(t *testing.T) {
	c := fixConfig(1)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	// ERROR is genuine red just like FAILURE; fix pass should be triggered.
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StateError, forge.StateSuccess})

	var fixCalls []int
	merged := selfHeal(c, fc, trackFix(&fixCalls), nil, "1", testFixPR)

	if !merged {
		t.Error("expected merged=true after ERROR then SUCCESS with fix pass")
	}
	if len(fixCalls) != 1 {
		t.Errorf("expected 1 fix call, got %v", fixCalls)
	}
}

func TestSelfHeal_PendingTimeoutNoFix(t *testing.T) {
	c := fixConfig(3)
	c.mergePollTimeout = 0 // expire immediately
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StatePending})

	var fixCalls []int
	merged := selfHeal(c, fc, trackFix(&fixCalls), nil, "1", testFixPR)

	if merged {
		t.Error("expected merged=false on PENDING timeout")
	}
	if len(fixCalls) != 0 {
		t.Errorf("expected no fix calls on PENDING timeout, got %v", fixCalls)
	}
	if len(fc.SwapCalls) == 0 {
		t.Fatal("expected SwapLabel call for failedLabel")
	}
	if last := fc.SwapCalls[len(fc.SwapCalls)-1]; last.Add != c.failedLabel {
		t.Errorf("last swap add=%q, want %q", last.Add, c.failedLabel)
	}
}

func TestSelfHeal_DefaultMaxFixAttempts(t *testing.T) {
	// MAX_FIX_ATTEMPTS defaults to 3; zero is a valid override (disables retries).
	if got := atoiNonneg("3", 3); got != 3 {
		t.Errorf("default MAX_FIX_ATTEMPTS=3 parsed as %d", got)
	}
	if got := atoiNonneg("0", 3); got != 0 {
		t.Errorf("MAX_FIX_ATTEMPTS=0 should be valid (disable retries), got %d", got)
	}
}
