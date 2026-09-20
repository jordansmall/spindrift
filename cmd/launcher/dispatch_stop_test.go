// This file drives run and selectiveListDispatch through the same
// installStopSignal seam continuous_stop_test.go uses for
// runContinuousDispatch (#3522), so there is one way to exercise operator
// shutdown in launcher tests rather than several.
package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/testutil"
	"spindrift.dev/launcher/internal/waves"
)

// TestRunExitCode_SignalledStop_NoBoxLaunched pins the base case: a stop
// closed before run's wave ever starts means no Box launches, and the
// caller learns why through exitSignalledStop.
func TestRunExitCode_SignalledStop_NoBoxLaunched(t *testing.T) {
	withClosedStopSignal(t)

	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 1
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != exitSignalledStop {
		t.Errorf("runExitCode(lc) = %d, want %d (waves.ErrSignalledStop)", got, exitSignalledStop)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls: got %+v, want none (a pre-closed stop must claim nothing)", fc.TransitionStateCalls)
	}
}

// A pre-closed stop must win over errQueueEmpty: run's own empty-queue
// branch would otherwise exit 2 before waves.Dispatch is ever reached, a
// return dispatchWave's Gate never gets a chance to observe (#3522).
func TestRunExitCode_SignalledStop_WinsOverEmptyQueue(t *testing.T) {
	withClosedStopSignal(t)

	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels) // no open issues
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != exitSignalledStop {
		t.Errorf("runExitCode(lc) = %d, want %d -- must not flatten into exit 2", got, exitSignalledStop)
	}
}

// A pre-closed stop must also win over ErrOpenNoneDispatchable: drainMaxJobs
// selects zero issues without ever calling dispatchWave, so its own Gate
// check is never reached for an all-blocked batch (#3522).
func TestRunExitCode_SignalledStop_WinsOverAllBlocked(t *testing.T) {
	withClosedStopSignal(t)

	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 1
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

	if got := runExitCode(lc); got != exitSignalledStop {
		t.Errorf("runExitCode(lc) = %d, want %d -- must not flatten into exit 3", got, exitSignalledStop)
	}
}

// TestRunExitCode_SignalledStop_WinsOverClaimedBlocked pins the reviewer's
// exact BLOCKING finding on #3522: ISSUE_NUMBER=42 with #42 blocked writes
// the blocked marker and returns a bare nil from drainMaxJobs's OriginClaimed
// branch, a return the old signalledOr wrapper -- applied only to
// waves.Dispatch's non-nil returns -- could never catch. A Stop already
// closed by the time Dispatch returns must still exit exitSignalledStop, and
// must never reach the dispatch completion banner or reconcileAfterDispatch.
func TestRunExitCode_SignalledStop_WinsOverClaimedBlocked(t *testing.T) {
	withClosedStopSignal(t)

	c := baseConfig()
	c.issueNumber = "42"
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}, Body: "## Blocked by\n- #7"})
	fc.SetIssue(forge.Issue{Number: "7", State: "OPEN"})
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	var got int
	out := testutil.CaptureStdout(t, func() {
		got = runExitCode(lc)
	})
	if got != exitSignalledStop {
		t.Errorf("runExitCode(lc) = %d, want %d -- must not flatten into exit 0", got, exitSignalledStop)
	}
	if strings.Contains(out, "all agents finished") {
		t.Errorf("output must not print the dispatch completion banner after a signalled stop; got:\n%s", out)
	}
}

// An abort (second signal, no prior stop observed at this seam) exits the
// same code as a lone stop.
func TestRunExitCode_SignalledAbort_ExitsSameCodeAsStop(t *testing.T) {
	withClosedAbortSignal(t)

	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels) // no open issues
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != exitSignalledStop {
		t.Errorf("runExitCode(lc) = %d, want %d", got, exitSignalledStop)
	}
}

// selectiveDispatchExitCode must map a pre-closed stop to exitSignalledStop
// ahead of its own 3 (ErrOpenNoneDispatchable) and 1 (default) verdicts, and
// must not print anything to stderr for it -- a requested stop is not a
// failure (mirrors runExitCode's own silence on ErrSignalledStop).
func TestSelectiveDispatchExitCode_SignalledStop(t *testing.T) {
	withClosedStopSignal(t)

	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 1
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	var got int
	stderr := captureStderrFile(t, func() {
		got = selectiveDispatchExitCode(lc, []string{"1"}, true)
	})
	if got != exitSignalledStop {
		t.Errorf("selectiveDispatchExitCode(lc) = %d, want %d", got, exitSignalledStop)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty (a requested stop is not a failure)", stderr)
	}
}

// The abort counterpart of the stop case above.
func TestSelectiveDispatchExitCode_SignalledAbort(t *testing.T) {
	withClosedAbortSignal(t)

	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 1
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := selectiveDispatchExitCode(lc, []string{"1"}, true); got != exitSignalledStop {
		t.Errorf("selectiveDispatchExitCode(lc) = %d, want %d", got, exitSignalledStop)
	}
}

// Teardown runs on every signalled path, not just a successful one:
// cmdDispatch's defer lc.cleanup() must still fire when run returns
// ErrSignalledStop, since nothing about a signalled exit skips defers.
func TestCmdDispatch_SignalledStop_StillRunsCleanup(t *testing.T) {
	withClosedStopSignal(t)

	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels)
	var cleaned bool
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
		cleanup:      func() { cleaned = true },
	}

	if got := cmdDispatch(lc); got != exitSignalledStop {
		t.Errorf("cmdDispatch(lc) = %d, want %d", got, exitSignalledStop)
	}
	if !cleaned {
		t.Error("lc.cleanup was never called on a signalled one-shot dispatch")
	}
}

// The selective-dispatch counterpart of the cleanup test above.
func TestCmdDispatchSelective_SignalledStop_StillRunsCleanup(t *testing.T) {
	withClosedStopSignal(t)

	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 1
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	var cleaned bool
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
		cleanup:      func() { cleaned = true },
	}

	if got := cmdDispatchSelective(lc, []string{"1"}, true); got != exitSignalledStop {
		t.Errorf("cmdDispatchSelective(lc, ...) = %d, want %d", got, exitSignalledStop)
	}
	if !cleaned {
		t.Error("lc.cleanup was never called on a signalled selective dispatch")
	}
}

// killHook wraps runner.Fake so a test can wait for terminate.Reclaim's Kill
// to actually land before proceeding, mirroring waves/continuous_test.go's
// own killHook (#3521/#3522): in production, Kill terminating the real
// sandbox is what makes a Box's Run() return, an ordering the plain Fake
// cannot reproduce on its own since its Kill only records the call. Shared
// by this file (a blocked RunFunc waiting on kr.killed) and
// recover_stop_test.go (a killSignal chan built here and handed to
// abortDuringSettle too, so it can wait on the same signal).
type killHook struct {
	*runner.Fake
	killed chan string
}

func newKillHook(fr *runner.Fake) *killHook {
	return &killHook{Fake: fr, killed: make(chan string, 1)}
}

func (k *killHook) Kill(name string) error {
	err := k.Fake.Kill(name)
	k.killed <- name
	return err
}

// waitOn fails the test if ch does not receive within 2s.
func waitOn[T any](t *testing.T, ch <-chan T, msg string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
	panic("unreachable")
}

// TestSelectiveListDispatch_ResearchAbort_ReclaimsOntoResearchFamily proves
// applyDispatchKind's label swap (main.go's applyDispatchKind) reaches
// terminate.Reclaim's TransitionState(InProgress, Dispatchable) call: an
// abort mid-flight must move a research issue back onto agent-research, the
// research family's own dispatchable label, never onto the work family's
// ready-for-agent (#3522). withClosedAbortSignal cannot be reused here (its
// channels are pre-closed and hidden inside the helper); this test needs a
// live abort channel it can close only after the Box has actually started,
// so it stubs installStopSignal directly, the same low-level substitution
// withStubbedSignals itself makes.
func TestSelectiveListDispatch_ResearchAbort_ReclaimsOntoResearchFamily(t *testing.T) {
	orig := installStopSignal
	stopCh := make(chan struct{})
	abortCh := make(chan struct{})
	installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) {
		return stopCh, abortCh, func() {}
	}
	t.Cleanup(func() { installStopSignal = orig })

	c := applyDispatchKind(baseConfig(), dispatchKindResearch)
	c.maxParallel = 1
	dir := tempLogDir(t)

	rl := forge.ResearchDispatchLabels()
	fc := forge.NewFake(rl)
	fc.SetIssue(forge.Issue{Number: "1", Title: "first", Labels: []string{c.label}})

	started := make(chan struct{})
	release := make(chan struct{})
	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		close(started)
		<-release
		return nil
	}
	kr := newKillHook(fr)
	f := testFactory(t, dir, kr)
	s := settle.NewFake()

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- selectiveListDispatch(c, fc, fc, capsFor(fc, fc), dir, f, s, []string{"1"}, true, strings.NewReader(""), io.Discard)
	}()

	waitOn(t, started, "issue #1 was never dispatched")
	close(abortCh)
	if got := waitOn(t, kr.killed, "Kill was never called after abort"); got != "agent-issue-1" {
		t.Fatalf("Kill: got %q, want agent-issue-1", got)
	}
	close(release)

	err := waitOn(t, resultCh, "selectiveListDispatch did not return")
	if !errors.Is(err, waves.ErrSignalledStop) {
		t.Fatalf("selectiveListDispatch: got %v, want ErrSignalledStop", err)
	}

	iss, ierr := fc.Issue("1")
	if ierr != nil {
		t.Fatalf("fc.Issue(1): %v", ierr)
	}
	if !containsLabel(iss.Labels, rl.Dispatchable) {
		t.Fatalf("issue #1 labels = %v, want %s (research family's dispatchable label)", iss.Labels, rl.Dispatchable)
	}
	for _, workLabel := range []string{testDispatchLabels.Dispatchable, testDispatchLabels.InProgress, testDispatchLabels.Complete, testDispatchLabels.Failed} {
		if containsLabel(iss.Labels, workLabel) {
			t.Fatalf("issue #1 labels = %v, want no work-family label %q", iss.Labels, workLabel)
		}
	}
	if len(s.FailCalls) != 0 || len(s.SettleCalls) != 0 {
		t.Fatalf("FailCalls=%+v SettleCalls=%+v, want none (abort abandons, never fails or settles)", s.FailCalls, s.SettleCalls)
	}
}
