package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

const testReconcilePR = "https://github.com/owner/repo/pull/77"

// reconcileConfig returns a config suitable for reconcile tests.
func reconcileConfig() config {
	c := baseConfig()
	c.branchPrefix = "agent/issue-"
	c.maxFixAttempts = 3
	return c
}

// --- recoverByNumber tests ----------------------------------------------------
//
// recoverByNumber is the sole adopt-and-gate path (#600): reconcileStranded,
// the unguarded automatic sweep over every agent-in-progress issue, was
// removed because a bare agent-in-progress label carries no liveness signal
// (see TestRun_DoesNotAdoptLiveRunnersInProgressIssue in run_test.go).
// recoverByNumber is reached only via the operator's explicit agent-recover
// label (.github/workflows/agent-recover.yml -> `spindrift recover <n>`).

func TestRecoverByNumber_GreenMergesAndCompletes(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR, IsDraft: false})
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, dir, testFactory(t, dir, nil), newSettle(c, fc, fc), "42")

	if err != nil {
		t.Errorf("expected nil error on green path; got %v", err)
	}
	if fc.Merged != testReconcilePR {
		t.Errorf("expected PR to be merged; fc.Merged=%q", fc.Merged)
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("expected TransitionState call for completeLabel")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Complete {
		t.Errorf("last transition To=%v, want Complete", last.To)
	}
}

func TestRecoverByNumber_DraftPRSkipped(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR, IsDraft: true})

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, dir, testFactory(t, dir, nil), newSettle(c, fc, fc), "42")

	if err == nil {
		t.Error("expected error for draft PR; got nil")
	}
	if fc.Merged != "" {
		t.Errorf("draft PR must not be merged; fc.Merged=%q", fc.Merged)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("draft PR must not trigger label churn; got %v", fc.TransitionStateCalls)
	}
}

func TestRecoverByNumber_NoPRSkipped(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	// No PR registered for the branch.

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, dir, testFactory(t, dir, nil), newSettle(c, fc, fc), "42")

	if err == nil {
		t.Error("expected error for no-PR case; got nil")
	}
	if fc.Merged != "" {
		t.Errorf("no-PR case must not trigger merge; fc.Merged=%q", fc.Merged)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("no-PR case must not trigger label churn; got %v", fc.TransitionStateCalls)
	}
}

func TestRecoverByNumber_RedFollowsSelfHeal(t *testing.T) {
	c := reconcileConfig()
	c.maxFixAttempts = 0
	fc := forge.NewFake()
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR, IsDraft: false})
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StateFailure})

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, dir, testFactory(t, dir, nil), newSettle(c, fc, fc), "42")

	if err != nil {
		t.Errorf("expected nil error (gate result expressed via labels); got %v", err)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge on red CI; fc.Merged=%q", fc.Merged)
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("expected TransitionState call for failedLabel")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Failed {
		t.Errorf("last transition To=%v, want Failed", last.To)
	}
}
