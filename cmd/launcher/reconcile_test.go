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

// --- reconcileStranded tests --------------------------------------------------

func TestReconcileStranded_GreenPRMergesAndCompletes(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()

	// Issue on the in-progress label with a green open non-draft PR.
	fc.SetIssue(forge.Issue{Number: "5", Labels: []string{c.inProgressLabel}})
	branch := c.branchPrefix + "5"
	fc.SetPR(branch, forge.PR{URL: testReconcilePR, IsDraft: false})
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	reconcileStranded(c, fc, t.TempDir(), nil)

	if fc.Merged != testReconcilePR {
		t.Errorf("expected green stranded PR to be merged; fc.Merged=%q", fc.Merged)
	}
	if len(fc.SwapCalls) == 0 {
		t.Fatal("expected SwapLabel call for completeLabel")
	}
	if last := fc.SwapCalls[len(fc.SwapCalls)-1]; last.Add != c.completeLabel {
		t.Errorf("last swap add=%q, want %q", last.Add, c.completeLabel)
	}
}

func TestReconcileStranded_RedFollowsSelfHeal(t *testing.T) {
	c := reconcileConfig()
	c.maxFixAttempts = 0
	fc := forge.NewFake()

	fc.SetIssue(forge.Issue{Number: "5", Labels: []string{c.inProgressLabel}})
	branch := c.branchPrefix + "5"
	fc.SetPR(branch, forge.PR{URL: testReconcilePR, IsDraft: false})
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StateFailure})

	reconcileStranded(c, fc, t.TempDir(), nil)

	if fc.Merged != "" {
		t.Errorf("expected no merge on red CI; fc.Merged=%q", fc.Merged)
	}
	if len(fc.SwapCalls) == 0 {
		t.Fatal("expected SwapLabel call for failedLabel")
	}
	if last := fc.SwapCalls[len(fc.SwapCalls)-1]; last.Add != c.failedLabel {
		t.Errorf("last swap add=%q, want %q", last.Add, c.failedLabel)
	}
}

func TestReconcileStranded_DraftPRSkipped(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()

	fc.SetIssue(forge.Issue{Number: "5", Labels: []string{c.inProgressLabel}})
	branch := c.branchPrefix + "5"
	fc.SetPR(branch, forge.PR{URL: testReconcilePR, IsDraft: true})

	reconcileStranded(c, fc, t.TempDir(), nil)

	if fc.Merged != "" {
		t.Errorf("draft PR must not be merged; fc.Merged=%q", fc.Merged)
	}
	if len(fc.SwapCalls) != 0 {
		t.Errorf("draft PR must not trigger label churn; got %v", fc.SwapCalls)
	}
}

func TestReconcileStranded_NoPRSkipped(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()

	// In-progress issue with no PR registered.
	fc.SetIssue(forge.Issue{Number: "5", Labels: []string{c.inProgressLabel}})

	reconcileStranded(c, fc, t.TempDir(), nil)

	if fc.Merged != "" {
		t.Errorf("no-PR issue must not be merged; fc.Merged=%q", fc.Merged)
	}
	if len(fc.SwapCalls) != 0 {
		t.Errorf("no-PR issue must not trigger label churn; got %v", fc.SwapCalls)
	}
}

// --- adoptAndGate tests -------------------------------------------------------

func TestAdoptAndGate_RedFollowsSelfHeal(t *testing.T) {
	c := reconcileConfig()
	c.maxFixAttempts = 0 // no fix passes — just mark failed
	fc := forge.NewFake()
	iss := issue{number: "77", title: "Test issue"}

	fc.SetIssue(forge.Issue{Number: "77", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StateFailure})

	adoptAndGate(c, fc, iss, testReconcilePR, func(pass int) error { return nil }, nil)

	if fc.Merged != "" {
		t.Errorf("expected no merge on red CI; fc.Merged=%q", fc.Merged)
	}
	if len(fc.SwapCalls) == 0 {
		t.Fatal("expected SwapLabel call for failedLabel")
	}
	if last := fc.SwapCalls[len(fc.SwapCalls)-1]; last.Add != c.failedLabel {
		t.Errorf("last swap add=%q, want %q", last.Add, c.failedLabel)
	}
}

func TestAdoptAndGate_GreenMergesAndCompletes(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()
	iss := issue{number: "77", title: "Test issue"}

	fc.SetIssue(forge.Issue{Number: "77", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	var fixCalls []int
	adoptAndGate(c, fc, iss, testReconcilePR, func(pass int) error {
		fixCalls = append(fixCalls, pass)
		return nil
	}, nil)

	if fc.Merged != testReconcilePR {
		t.Errorf("expected PR to be merged; fc.Merged=%q", fc.Merged)
	}
	if len(fixCalls) != 0 {
		t.Errorf("expected no fix calls on green CI, got %v", fixCalls)
	}
	if len(fc.SwapCalls) == 0 {
		t.Fatal("expected SwapLabel call for completeLabel")
	}
	if last := fc.SwapCalls[len(fc.SwapCalls)-1]; last.Add != c.completeLabel {
		t.Errorf("last swap add=%q, want %q", last.Add, c.completeLabel)
	}
}

// --- engageByNumber tests -----------------------------------------------------

func TestEngageByNumber_GreenMergesAndCompletes(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := c.branchPrefix + "42"
	fc.SetPR(branch, forge.PR{URL: testReconcilePR, IsDraft: false})
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	err := engageByNumber(c, fc, t.TempDir(), nil, "42")

	if err != nil {
		t.Errorf("expected nil error on green path; got %v", err)
	}
	if fc.Merged != testReconcilePR {
		t.Errorf("expected PR to be merged; fc.Merged=%q", fc.Merged)
	}
	if len(fc.SwapCalls) == 0 {
		t.Fatal("expected SwapLabel call for completeLabel")
	}
	if last := fc.SwapCalls[len(fc.SwapCalls)-1]; last.Add != c.completeLabel {
		t.Errorf("last swap add=%q, want %q", last.Add, c.completeLabel)
	}
}

func TestEngageByNumber_DraftPRSkipped(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := c.branchPrefix + "42"
	fc.SetPR(branch, forge.PR{URL: testReconcilePR, IsDraft: true})

	err := engageByNumber(c, fc, t.TempDir(), nil, "42")

	if err == nil {
		t.Error("expected error for draft PR; got nil")
	}
	if fc.Merged != "" {
		t.Errorf("draft PR must not be merged; fc.Merged=%q", fc.Merged)
	}
	if len(fc.SwapCalls) != 0 {
		t.Errorf("draft PR must not trigger label churn; got %v", fc.SwapCalls)
	}
}

func TestEngageByNumber_NoPRSkipped(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	// No PR registered for the branch.

	err := engageByNumber(c, fc, t.TempDir(), nil, "42")

	if err == nil {
		t.Error("expected error for no-PR case; got nil")
	}
	if fc.Merged != "" {
		t.Errorf("no-PR case must not trigger merge; fc.Merged=%q", fc.Merged)
	}
	if len(fc.SwapCalls) != 0 {
		t.Errorf("no-PR case must not trigger label churn; got %v", fc.SwapCalls)
	}
}

func TestEngageByNumber_RedFollowsSelfHeal(t *testing.T) {
	c := reconcileConfig()
	c.maxFixAttempts = 0
	fc := forge.NewFake()

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := c.branchPrefix + "42"
	fc.SetPR(branch, forge.PR{URL: testReconcilePR, IsDraft: false})
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StateFailure})

	err := engageByNumber(c, fc, t.TempDir(), nil, "42")

	if err != nil {
		t.Errorf("expected nil error (gate result expressed via labels); got %v", err)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge on red CI; fc.Merged=%q", fc.Merged)
	}
	if len(fc.SwapCalls) == 0 {
		t.Fatal("expected SwapLabel call for failedLabel")
	}
	if last := fc.SwapCalls[len(fc.SwapCalls)-1]; last.Add != c.failedLabel {
		t.Errorf("last swap add=%q, want %q", last.Add, c.failedLabel)
	}
}
