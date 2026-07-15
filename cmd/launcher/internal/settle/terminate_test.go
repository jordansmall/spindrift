package settle

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/terminate"
)

// TestGateToGreen_TerminatedAbandonsWithoutTransition verifies that a
// termination marked before gateToGreen's first poll makes it bail
// immediately, without ever confirming green or swapping agent-complete —
// ADR 0024's "abandons the settle wherever it stands" applied to the CI-watch
// phase.
func TestGateToGreen_TerminatedAbandonsWithoutTransition(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	reg.Mark("1")

	got := s.gateToGreen("1", testPR)

	if got != gateAbandoned {
		t.Errorf("gateToGreen = %v, want gateAbandoned", got)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called after termination; got %+v", fc.TransitionStateCalls)
	}
}

// TestMergeImmediate_TerminatedStopsRebaseRetry verifies a termination marked
// before mergeImmediate's first attempt stops it from ever calling Merge or
// Rebase — the merge-gate phase of "abandons the settle wherever it stands."
func TestMergeImmediate_TerminatedStopsRebaseRetry(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	s := New(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	reg.Mark("1")

	err := s.mergeImmediate("1", testPR, dispatch.NewFake())

	if !errors.Is(err, errAbandoned) {
		t.Errorf("mergeImmediate err = %v, want errAbandoned", err)
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called after termination; fc.Merged=%q", fc.Merged)
	}
	if len(fc.RebasedURLs) != 0 {
		t.Errorf("Rebase must not be called after termination; got %v", fc.RebasedURLs)
	}
}

// terminatingDispatcher wraps a dispatch.Fake so its Fix call marks num
// terminated after returning — simulating Terminate reaping the fix-pass Box
// mid-flight (the caller observes Fix's own failure result, then notices the
// termination on its next loop iteration).
type terminatingDispatcher struct {
	*dispatch.Fake
	reg *terminate.Registry
	num string
}

func (d terminatingDispatcher) Fix(pass int, ciFailureSummary string) dispatch.Result {
	res := d.Fake.Fix(pass, ciFailureSummary)
	d.reg.Mark(d.num)
	return res
}

// TestSelfHeal_TerminatedDuringFixPass_StopsRetryLoop verifies that a
// termination landing while a fix-pass Box is running (observed here as Fix
// returning, then the registry being marked) stops selfHeal from dispatching
// a second fix pass or re-polling CI — it abandons on the very next
// checkpoint instead of continuing the attempt loop.
func TestSelfHeal_TerminatedDuringFixPass_StopsRetryLoop(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	// Genuine red on every poll if the loop were ever allowed to continue —
	// proves termination is what stops it, not exhausted fix attempts.
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateFailure, forge.StateFailure, forge.StateFailure, forge.StateFailure,
	})
	s := New(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	d := terminatingDispatcher{Fake: dispatch.NewFake(), reg: reg, num: "1"}

	landing := s.selfHeal(d, "1", testPR)

	if landing != landingAbandoned {
		t.Errorf("selfHeal = %v, want landingAbandoned", landing)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("want exactly 1 fix call (termination stops the retry loop), got %d: %+v", len(d.FixCalls), d.FixCalls)
	}
}

// TestSettle_AbandonedSkipsUsageComment verifies Settle's "ready" branch
// posts no usage comment when selfHeal reports landingAbandoned — Terminate
// already recorded its own comment; a second, unrelated comment from the
// orphaned settle goroutine would be noise the operator never asked for.
func TestSettle_AbandonedSkipsUsageComment(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	reg.Mark("1")

	d := dispatch.NewFake()
	s.Settle(d, "1", dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "1", Landing: testPR, Status: "ready", Note: "ok"},
	})

	if len(fc.CommentCalls) != 0 {
		t.Errorf("no comment expected after termination; got %+v", fc.CommentCalls)
	}
}
