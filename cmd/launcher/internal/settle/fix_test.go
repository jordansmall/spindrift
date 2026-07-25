package settle

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

func fixConfig(maxFixAttempts int) Config {
	c := baseConfig()
	c.MaxFixAttempts = maxFixAttempts
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
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess})
	fc.SetFailureDetail(testPR, "lint: FAILURE\n2 errors")
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingMerged {
		t.Fatalf("selfHeal = %v, want landingMerged after one fix pass", landing)
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
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess})
	fc.FailureDetailErr = errors.New("gh api graphql: 403 Forbidden")
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingMerged {
		t.Fatalf("selfHeal = %v; a FailureDetail fetch error must not block the fix pass", landing)
	}
	if len(d.FixCalls) != 1 || d.FixCalls[0].CIFailureSummary != "" {
		t.Errorf("want empty summary on fetch error, got %+v", d.FixCalls)
	}
}

func TestSelfHeal_SuccessFirstTry(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged on first-try SUCCESS", landing)
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
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed (maxFixAttempts=0)", landing)
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
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	// First poll: FAILURE; after fix box: SUCCESS (plus confirmation poll)
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged after one fix pass", landing)
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
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	// All polls return FAILURE — never fixed.
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateFailure,
		forge.StateFailure,
		forge.StateFailure,
	})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed after exhausting all fix passes", landing)
	}
	passes := fixPasses(d)
	if len(passes) != 2 {
		t.Errorf("expected %d fix calls (maxFixAttempts), got %d: %v", c.MaxFixAttempts, len(passes), passes)
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

// TestSelfHeal_FixFailureStopsImmediately verifies that when d.Fix reports
// !Success, selfHeal lands failed right away instead of re-polling the same
// (unchanged) head and burning the rest of the fix-pass budget against the
// identical cached rollup (issue #1980). The rollup is scripted FAILURE on
// every poll — standing in for a head that never advances because the fix
// box never pushed — so a buggy loop that keeps retrying would burn all 3
// passes instead of stopping after the first.
func TestSelfHeal_FixFailureStopsImmediately(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateFailure, forge.StateFailure, forge.StateFailure, forge.StateFailure,
	})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	d.FixResult = dispatch.Result{Success: false}
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed after a failed fix pass", landing)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("expected exactly 1 fix call (no retry against the same failed head), got %+v", d.FixCalls)
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("expected TransitionState call for Failed")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Failed {
		t.Errorf("last transition To=%v, want Failed", last.To)
	}
}

// TestSelfHeal_FixNoOpUnchangedHead verifies that when d.Fix reports
// Success but leaves the PR's head commit SHA unchanged, selfHeal lands
// failed right away instead of re-polling the identical cached rollup as a
// fresh genuine red (issue #1980). The rollup is scripted FAILURE on every
// poll — standing in for a head that never advances — so a buggy loop that
// trusts the stale rollup would burn all 3 passes instead of stopping after
// the first.
func TestSelfHeal_FixNoOpUnchangedHead(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateFailure, forge.StateFailure, forge.StateFailure, forge.StateFailure,
	})
	// Same SHA before, immediately after, and on the confirm re-read: the
	// fix box exited zero but never pushed a new commit.
	fc.SetHeadCommitSHAs(testPR, []string{"sha-unchanged", "sha-unchanged", "sha-unchanged"})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed after a no-op fix pass", landing)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("expected exactly 1 fix call (no retry against the unchanged head), got %+v", d.FixCalls)
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Failed {
		t.Errorf("last transition To=%v, want Failed", last.To)
	}
}

// TestSelfHeal_FixAdvanceConfirmedAfterTransientSameRead verifies that a
// head-SHA read landing back the same value right after Fix returns (GitHub
// API replication lag momentarily serving the pre-push snapshot) is not
// mistaken for a no-op fix pass: a confirm re-read showing the head did
// advance must let the loop proceed to green, mirroring gateToGreen's own
// confirm-poll pattern for a SUCCESS rollup (issue #1980).
func TestSelfHeal_FixAdvanceConfirmedAfterTransientSameRead(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess})
	// before, immediate-after (stale read, same as before), confirm re-read
	// (shows the real advance).
	fc.SetHeadCommitSHAs(testPR, []string{"sha-a", "sha-a", "sha-b"})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged (a transient same-read must not abort as no-op)", landing)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("expected exactly 1 fix call, got %+v", d.FixCalls)
	}
}

func TestSelfHeal_ErrorStateTriggersFixPass(t *testing.T) {
	c := fixConfig(1)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	// ERROR is genuine red just like FAILURE; fix pass should be triggered.
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateError, forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged after ERROR then SUCCESS with fix pass", landing)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("expected 1 fix call, got %+v", d.FixCalls)
	}
}

func TestSelfHeal_PendingTimeoutNoFix(t *testing.T) {
	c := fixConfig(3)
	c.MergePollTimeout = 0 // expire immediately
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed on PENDING timeout", landing)
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
