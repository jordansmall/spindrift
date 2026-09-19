package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/settle"
)

const testReconcilePR = "https://github.com/owner/repo/pull/77"

func reconcileConfig() config {
	c := baseConfig()
	c.branchPrefix = "agent/issue-"
	c.maxFixAttempts = 3
	return c
}

// The assertion to settle.WorkSettler always succeeds because every
// recoverByNumber test in this file goes through dispatchKindWork.
func newWorkSettle(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge) settle.WorkSettler {
	return testNewSettle(c, it, lw, cf).(settle.WorkSettler)
}

// recoverByNumber is the sole adopt-and-gate path (#600). reconcileStranded,
// the unguarded sweep over every agent-in-progress issue, was removed because
// a bare agent-in-progress label carries no liveness signal (see
// TestRun_DoesNotAdoptLiveRunnersInProgressIssue in run_test.go). An operator
// reaches recoverByNumber only through the explicit agent-recover label.

func TestRecoverByNumber_GreenMergesAndCompletes(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR})
	// The leading PENDING proves this run's own checks registered. Issue
	// #1652's adopted-path gate does not trust an immediate SUCCESS alone.
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), newWorkSettle(c, fc, testWired(fc), fc), "42")

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

// The fake fails the first OpenPRForBranch call with an HTTP 5xx error, and
// recoverByNumber must still adopt and complete the PR on the retry rather
// than propagate the transient error (#2323).
func TestRecoverByNumber_RetriesTransientPRLookupError(t *testing.T) {
	c := reconcileConfig()
	c.transientRetryMax = 2
	c.transientBackoffSecs = 0
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR})
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	fc.OpenPRForBranchErrs = []error{errors.New("HTTP 502: Bad Gateway"), nil}

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), newWorkSettle(c, fc, testWired(fc), fc), "42")

	if err != nil {
		t.Errorf("expected nil error after retrying transient PR lookup error; got %v", err)
	}
	if fc.Merged != testReconcilePR {
		t.Errorf("expected PR to be merged; fc.Merged=%q", fc.Merged)
	}
}

// This guards the off-by-one in #2323. transientRetryMax counts retries, not
// total attempts, matching dispatch/retry.go's transientCount check, so a max
// of 1 must still allow one retry rather than degrade to none.
func TestRecoverByNumber_RetryMaxOneStillRetriesOnce(t *testing.T) {
	c := reconcileConfig()
	c.transientRetryMax = 1
	c.transientBackoffSecs = 0
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR})
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	fc.OpenPRForBranchErrs = []error{errors.New("HTTP 502: Bad Gateway"), nil}

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), newWorkSettle(c, fc, testWired(fc), fc), "42")

	if err != nil {
		t.Errorf("expected nil error after one retry with transientRetryMax=1; got %v", err)
	}
	if fc.Merged != testReconcilePR {
		t.Errorf("expected PR to be merged; fc.Merged=%q", fc.Merged)
	}
}

// The adopt-and-gate path always issues an idempotent MarkReady before
// merging, never gated on draft detection, because a stranded PR may be draft
// or not and the shared SettleAdopted path treats both identically (#2408).
func TestRecoverByNumber_AdoptedPRAlwaysCallsMarkReadyThenMergesAndCompletes(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR})
	// The leading PENDING proves this run's own checks registered. Issue
	// #1652's adopted-path gate does not trust an immediate SUCCESS alone.
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), newWorkSettle(c, fc, testWired(fc), fc), "42")

	if err != nil {
		t.Errorf("expected nil error on adopted-PR path; got %v", err)
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
	if len(fc.MarkReadyCalls) == 0 {
		t.Error("expected MarkReady to be called unconditionally on the adopt path")
	}
}

// This covers #2225's relayed-branch adoption arm. With no open PR but a
// genuine success self-report recovered from disk, recoverByNumber must open a
// PR on the relayed branch and drive it through the normal merge gate to
// agent-complete instead of reporting no open PR.
func TestRecoverByNumber_RelayedBranchAdoptedMergesAndCompletes(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	// No PR is registered for the branch, so forge.ResolveOpenPR reports
	// res.Found=false and the adopt arm fires.
	fc.CreateDraftPRURL = testReconcilePR
	// The leading PENDING proves this run's own checks registered, matching
	// TestRecoverByNumber_GreenMergesAndCompletes' reasoning (#1652).
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})

	dir := tempLogDir(t)
	logPath := filepath.Join(dispatch.HostLogDirFor(dir), "issue-42.log")
	// The log line is a leading-token near-miss with no full grammar. Its bare
	// word is the generated vocabulary's own "ready", not the removed "success"
	// synonym, because isSuccessSelfReport only recognizes words from
	// outcome.WorkStatuses and "success" was never one (#2981).
	if err := os.WriteFile(logPath, []byte("SPINDRIFT_OUTCOME: ready\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cf := fc.AsGithubReadOnly()
	err := recoverByNumber(c, fc, cf, capsFor(fc, cf), dir, testFactory(t, dir, nil), newWorkSettle(c, fc, testWired(fc), cf), "42")

	if err != nil {
		t.Errorf("expected nil error on relayed-branch adopt path; got %v", err)
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
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	if fc.CreateDraftPRCalls[0].Head != branch {
		t.Errorf("CreateDraftPRCalls[0].Head = %q, want %q", fc.CreateDraftPRCalls[0].Head, branch)
	}
}

// The relayed-branch adoption arm must never fire with neither an open PR nor
// a self-report on disk to recover. The pre-#2225 no-PR path, labels untouched
// and no PR opened, stays exactly unchanged.
func TestRecoverByNumber_NoPRNoSelfReportStillNoOps(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	// No PR and no log written, so tempLogDir's dir stays empty and
	// dispatch.LastSelfReportFromLogs finds nothing.

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), newWorkSettle(c, fc, testWired(fc), fc), "42")

	if err == nil {
		t.Error("expected error for no-PR, no-self-report case; got nil")
	}
	if fc.Merged != "" {
		t.Errorf("no-PR case must not trigger merge; fc.Merged=%q", fc.Merged)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("no-PR case must not trigger label churn; got %v", fc.TransitionStateCalls)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("no-PR, no-self-report case must not open a PR; got %+v", fc.CreateDraftPRCalls)
	}
}

func TestRecoverByNumber_NoPRSkipped(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), newWorkSettle(c, fc, testWired(fc), fc), "42")

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

// This pins #2477's fix. A no-PR, no-self-report recover attempt against an
// issue that was agent-complete before the workflow's host-side claim stripped
// that label must restore agent-complete rather than let the caller's non-nil
// error drive the workflow's blind park-to-agent-failed step.
func TestRecoverByNumber_NoPRRestoresPriorComplete(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	// No PR and no log written, so tempLogDir's dir stays empty and
	// dispatch.LastSelfReportFromLogs finds nothing.
	fc.PriorClaimStates = map[string]forge.DispatchState{"42": forge.Complete}

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), newWorkSettle(c, fc, testWired(fc), fc), "42")

	if err != nil {
		t.Errorf("expected nil error when restoring a prior agent-complete state; got %v", err)
	}
	iss, issErr := fc.Issue("42")
	if issErr != nil {
		t.Fatalf("fc.Issue(42): %v", issErr)
	}
	if !containsLabel(iss.Labels, c.completeLabel) {
		t.Errorf("expected issue to carry %q after restore; got labels %v", c.completeLabel, iss.Labels)
	}
	if containsLabel(iss.Labels, c.failedLabel) {
		t.Errorf("expected issue to not carry %q after restore; got labels %v", c.failedLabel, iss.Labels)
	}
	if len(fc.CommentCalls) < 1 {
		t.Error("expected an explanatory comment to be posted")
	}
}

// recoverFailed restores only a prior agent-complete state, never a prior
// agent-failed one. An issue already agent-failed before the claim must still
// park agent-failed exactly as it did before #2477.
func TestRecoverByNumber_NoPRPriorFailedStillErrors(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	fc.PriorClaimStates = map[string]forge.DispatchState{"42": forge.Failed}

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), newWorkSettle(c, fc, testWired(fc), fc), "42")

	if err == nil {
		t.Error("expected error for no-PR case with prior agent-failed state; got nil")
	}
	iss, issErr := fc.Issue("42")
	if issErr != nil {
		t.Fatalf("fc.Issue(42): %v", issErr)
	}
	if containsLabel(iss.Labels, c.completeLabel) {
		t.Errorf("expected issue to not carry %q; got labels %v", c.completeLabel, iss.Labels)
	}
}

func TestRecoverByNumber_RedFollowsSelfHeal(t *testing.T) {
	c := reconcileConfig()
	c.maxFixAttempts = 0
	fc := forge.NewFake()
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR})
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StateFailure})

	dir := tempLogDir(t)
	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), newWorkSettle(c, fc, testWired(fc), fc), "42")

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
