package main

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

const testFixPR = "https://github.com/owner/repo/pull/99"

func fixConfig(maxFixAttempts int) config {
	c := baseConfig()
	c.maxFixAttempts = maxFixAttempts
	return c
}

// fixPasses extracts the 1-based pass numbers recorded on a Fake Dispatcher.
func fixPasses(d *dispatch.Fake) []int {
	var passes []int
	for _, call := range d.FixCalls {
		passes = append(passes, call.Pass)
	}
	return passes
}

// TestSelfHeal_ForwardsFailureDetailToFix verifies that on genuine-red,
// selfHeal captures fc.FailureDetail(pr) and forwards it as the second
// argument to Fix — the fix box's CI_FAILURE_SUMMARY (issue #426).
func TestSelfHeal_ForwardsFailureDetailToFix(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess})
	fc.SetFailureDetail(testFixPR, "lint: FAILURE\n2 errors")

	d := dispatch.NewFake()
	_, merged := selfHeal(c, fc, d, "1", testFixPR)

	if !merged {
		t.Fatal("expected merged=true after one fix pass")
	}
	if len(d.FixCalls) != 1 || d.FixCalls[0].CIFailureSummary != "lint: FAILURE\n2 errors" {
		t.Errorf("want fix pass forwarded the scripted failure detail; got %+v", d.FixCalls)
	}
}

// TestSelfHeal_EmptyFailureDetailFallsBackWithNoError verifies that when
// FailureDetail returns an error (fetch failed) or "" (nothing scripted),
// selfHeal still dispatches the fix pass with an empty summary rather than
// failing the fix pass outright — the fetch is best-effort.
func TestSelfHeal_EmptyFailureDetailFallsBackWithNoError(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess})
	fc.FailureDetailErr = errors.New("gh api graphql: 403 Forbidden")

	d := dispatch.NewFake()
	_, merged := selfHeal(c, fc, d, "1", testFixPR)

	if !merged {
		t.Fatal("a FailureDetail fetch error must not block the fix pass")
	}
	if len(d.FixCalls) != 1 || d.FixCalls[0].CIFailureSummary != "" {
		t.Errorf("want empty summary on fetch error, got %+v", d.FixCalls)
	}
}

func TestSelfHeal_SuccessFirstTry(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	_, merged := selfHeal(c, fc, d, "1", testFixPR)

	if !merged {
		t.Error("expected merged=true on first-try SUCCESS")
	}
	if len(d.FixCalls) != 0 {
		t.Errorf("expected no fix calls, got %+v", d.FixCalls)
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("expected at least one TransitionState call (Complete)")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Complete {
		t.Errorf("last transition To=%v, want Complete", last.To)
	}
}

func TestSelfHeal_GenuineRedMaxZero(t *testing.T) {
	c := fixConfig(0) // no fix passes allowed
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StateFailure})

	d := dispatch.NewFake()
	_, merged := selfHeal(c, fc, d, "1", testFixPR)

	if merged {
		t.Error("expected merged=false (maxFixAttempts=0)")
	}
	if len(d.FixCalls) != 0 {
		t.Errorf("expected no fix calls (maxFixAttempts=0), got %+v", d.FixCalls)
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("expected TransitionState call for Failed")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Failed {
		t.Errorf("last transition To=%v, want Failed", last.To)
	}
}

func TestSelfHeal_GenuineRedFixSucceeds(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	// First poll: FAILURE; after fix box: SUCCESS (plus confirmation poll)
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	_, merged := selfHeal(c, fc, d, "1", testFixPR)

	if !merged {
		t.Error("expected merged=true after one fix pass")
	}
	if passes := fixPasses(d); len(passes) != 1 || passes[0] != 1 {
		t.Errorf("expected exactly fix-pass-1, got %v", passes)
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("expected TransitionState call (Complete)")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Complete {
		t.Errorf("last transition To=%v, want Complete", last.To)
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

	d := dispatch.NewFake()
	_, merged := selfHeal(c, fc, d, "1", testFixPR)

	if merged {
		t.Error("expected merged=false after exhausting all fix passes")
	}
	passes := fixPasses(d)
	if len(passes) != 2 {
		t.Errorf("expected %d fix calls (maxFixAttempts), got %d: %v", c.maxFixAttempts, len(passes), passes)
	}
	// Fix passes should be numbered 1, 2
	for i, p := range passes {
		if p != i+1 {
			t.Errorf("passes[%d]=%d, want %d", i, p, i+1)
		}
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("expected TransitionState call for Failed")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Failed {
		t.Errorf("last transition To=%v, want Failed", last.To)
	}
}

func TestSelfHeal_ErrorStateTriggersFixPass(t *testing.T) {
	c := fixConfig(1)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	// ERROR is genuine red just like FAILURE; fix pass should be triggered.
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StateError, forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	_, merged := selfHeal(c, fc, d, "1", testFixPR)

	if !merged {
		t.Error("expected merged=true after ERROR then SUCCESS with fix pass")
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("expected 1 fix call, got %+v", d.FixCalls)
	}
}

func TestSelfHeal_PendingTimeoutNoFix(t *testing.T) {
	c := fixConfig(3)
	c.mergePollTimeout = 0 // expire immediately
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testFixPR, []forge.RollupState{forge.StatePending})

	d := dispatch.NewFake()
	_, merged := selfHeal(c, fc, d, "1", testFixPR)

	if merged {
		t.Error("expected merged=false on PENDING timeout")
	}
	if len(d.FixCalls) != 0 {
		t.Errorf("expected no fix calls on PENDING timeout, got %+v", d.FixCalls)
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("expected TransitionState call for Failed")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Failed {
		t.Errorf("last transition To=%v, want Failed", last.To)
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
