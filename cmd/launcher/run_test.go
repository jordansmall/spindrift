package main

import (
	"errors"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// TestRun_EmptyQueue_ReturnsErrQueueEmpty asserts run's orchestration logic
// (as opposed to the bootstrap prologue) runs correctly against a
// fake-populated launchContext, with no ISSUE_NUMBER and no dispatchable
// issues in the fake forge.
func TestRun_EmptyQueue_ReturnsErrQueueEmpty(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	err := run(lc)

	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("run(lc) = %v, want errQueueEmpty", err)
	}
}

// TestRunExitCode_EmptyQueue_ReturnsExitCode2 asserts the run-to-exit-code
// translation (errQueueEmpty -> 2) that main previously did inline, against
// a fake-populated launchContext -- no bootstrap, no real config or runtime.
func TestRunExitCode_EmptyQueue_ReturnsExitCode2(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 2 {
		t.Errorf("runExitCode(lc) = %d, want 2 (errQueueEmpty)", got)
	}
}

// TestRunExitCode_QueueMaxJobsZero_NoneDispatchable_ReturnsExitCode3 is the
// end-to-end regression test for #522/#477: with MAX_JOBS unset (0, the
// uncapped drain default) the queue path no longer loops dispatchWaves
// waiting for a blocker — a batch with nothing currently dispatchable exits
// straight to code 3.
func TestRunExitCode_QueueMaxJobsZero_NoneDispatchable_ReturnsExitCode3(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "1",
		Body:   "## Blocked by\n- #2",
		Labels: []string{c.label},
	})
	fc.SetIssue(forge.Issue{Number: "2", State: "OPEN"}) // blocker, not yet complete
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 3 {
		t.Errorf("runExitCode(lc) = %d, want 3 (ErrOpenNoneDispatchable)", got)
	}
}

// TestSelectiveDispatchExitCode_ZeroSelected_ReturnsExitCode3 is the
// regression test for #524's acceptance criterion: zero selected with
// issues held (here, everything overlap-deferred) exits 3, matching the
// queue path's ErrOpenNoneDispatchable translation, instead of the generic
// exit 1 every other selective-dispatch error uses.
func TestSelectiveDispatchExitCode_ZeroSelected_ReturnsExitCode3(t *testing.T) {
	c := baseConfig()
	c.label = "agent-trigger"
	c.overlapGate = "defer"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{c.label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{c.inProgressLabel},
	})

	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := selectiveDispatchExitCode(lc, []string{"10"}, true); got != 3 {
		t.Errorf("selectiveDispatchExitCode(lc, [10], true) = %d, want 3 (ErrOpenNoneDispatchable)", got)
	}
}

// TestRunExitCode_ContinuousDispatch_EmptyQueue_ReturnsExitCode2 verifies
// that CONTINUOUS_DISPATCH mode preserves exit-2 semantics unchanged
// (#527 AC): an empty queue exits the same way whether or not continuous
// mode is enabled.
func TestRunExitCode_ContinuousDispatch_EmptyQueue_ReturnsExitCode2(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	dir := tempLogDir(t)
	fc := forge.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 2 {
		t.Errorf("runExitCode(lc) = %d, want 2 (errQueueEmpty)", got)
	}
}

// TestRunExitCode_ContinuousDispatch_Fresh_DispatchesAndReturns0 verifies
// that with CONTINUOUS_DISPATCH enabled and the freshness probe reporting
// not-applicable (RUNTIME=bwrap, which never blocks a refill), a
// dispatchable issue launches and the run exits 0 — continuous mode wired
// end-to-end through run/runExitCode.
func TestRunExitCode_ContinuousDispatch_Fresh_DispatchesAndReturns0(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runtime = "bwrap"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})

	fr := runner.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 0 {
		t.Errorf("runExitCode(lc) = %d, want 0", got)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Errorf("RunCalls: got %v, want exactly issue 1", fr.RunCalls)
	}
}

// TestRun_DoesNotAdoptLiveRunnersInProgressIssue is the regression test for
// #600: a bare agent-in-progress issue with an open non-draft PR is what a
// live runner's in-flight work looks like from the outside (a second local
// dogfood run, or an overlapping agent-dispatch box) — the same shape a
// crash-stranded issue has. Before #600 the discovered-origin sweep could not
// tell the two apart and force-pushed/merged over the live runner. Simulating
// two "runners" sharing one fake forge: this run must leave that issue's PR
// and labels completely untouched.
func TestRun_DoesNotAdoptLiveRunnersInProgressIssue(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.branchPrefix = "agent/issue-"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = c.branchPrefix
	// Issue #5: another runner's live work — agent-in-progress with an open
	// non-draft PR, no explicit recovery signal.
	fc.SetIssue(forge.Issue{Number: "5", Labels: []string{c.inProgressLabel}})
	fc.SetPR(fc.AgentBranch("5"), forge.PR{URL: "https://github.com/owner/repo/pull/5", IsDraft: false})

	sf := settle.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       sf,
	}

	if err := run(lc); !errors.Is(err, errQueueEmpty) {
		t.Fatalf("run(lc) = %v, want errQueueEmpty (no other issue dispatchable)", err)
	}
	if len(sf.SettleAdoptedCalls) != 0 {
		t.Errorf("expected no SettleAdopted calls on a bare in-progress issue; got %v", sf.SettleAdoptedCalls)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("expected no label churn on issue #5; got %v", fc.TransitionStateCalls)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge; fc.Merged=%q", fc.Merged)
	}
}

// TestRunExitCode_ContinuousDispatch_ImageStale_ReturnsExitCode4 verifies
// the new exit code (#527 AC): with the freshness probe reporting
// rebuild-needed (here, forced by a base-branch fetch that fails — pwd is a
// real git repo whose "origin" remote is unreachable, a transient failure —
// see issue #1579, which carves the pwd-is-not-a-git-repo-at-all case out of
// this same fetch-failure path into a distinct not-applicable verdict, so
// this test must exercise a genuine repo to still land on rebuild-needed), no
// Box launches and the run exits 4.
func TestRunExitCode_ContinuousDispatch_ImageStale_ReturnsExitCode4(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runtime = "podman"
	c.baseBranch = "main"
	dir := tempLogDir(t)
	if err := runGit(dir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := runGit(dir, "remote", "add", "origin", filepath.Join(dir, "does-not-exist.git")); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})

	fr := runner.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 4 {
		t.Errorf("runExitCode(lc) = %d, want 4 (waves.ErrImageStale)", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0 (no Box launches once the probe is stale)", len(fr.RunCalls))
	}
}

// TestRun_DepsOfCheckFailure_HoldsIssueNotDispatched verifies that the batch
// dispatch path (`run`) threads BuildEdges' failed set (#1103) through to the
// wave engine: an issue whose own DepsOf call errored is held for retry, not
// dispatched and not cascade-failed, while an unaffected sibling in the same
// batch still dispatches normally.
func TestRun_DepsOfCheckFailure_HoldsIssueNotDispatched(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.label}})

	fr := runner.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: failDepsOf{Fake: fc, num: "1"},
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	if err := run(lc); err != nil {
		t.Fatalf("run(lc): %v", err)
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "2" {
		t.Fatalf("RunCalls: got %v, want exactly issue 2", fr.RunCalls)
	}

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.failedLabel) {
		t.Errorf("issue 1 must NOT be cascade-failed on a DepsOf check failure; labels=%v", iss1.Labels)
	}
}

// TestRunExitCode_ContinuousDispatch_DepsOfCheckFailure_HoldsIssueNotDispatched
// verifies that CONTINUOUS_DISPATCH's discover closure threads BuildEdges'
// failed set (#1103) through to nextReady exactly as the batch path does: an
// issue whose own DepsOf call errored is held for retry rather than
// dispatched, while an unaffected sibling still dispatches.
func TestRunExitCode_ContinuousDispatch_DepsOfCheckFailure_HoldsIssueNotDispatched(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 2
	c.runtime = "bwrap"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.label}})

	fr := runner.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: failDepsOf{Fake: fc, num: "1"},
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 0 {
		t.Errorf("runExitCode(lc) = %d, want 0", got)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "2" {
		t.Fatalf("RunCalls: got %v, want exactly issue 2", fr.RunCalls)
	}

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.failedLabel) {
		t.Errorf("issue 1 must NOT be cascade-failed on a DepsOf check failure; labels=%v", iss1.Labels)
	}
}
