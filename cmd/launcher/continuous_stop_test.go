package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
)

// withStubbedSignals overrides the installStopSignal seam so
// runContinuousDispatch wires waves.Config.Stop and waves.Config.Abort to
// channels that are already in the requested closed/open state, standing in
// for "the operator's first/second signal already arrived" without
// registering a real handler or sending a real signal (#3520, #3521).
func withStubbedSignals(t *testing.T, stopClosed, abortClosed bool) {
	t.Helper()
	orig := installStopSignal
	stopCh := make(chan struct{})
	abortCh := make(chan struct{})
	if stopClosed {
		close(stopCh)
	}
	if abortClosed {
		close(abortCh)
	}
	installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) {
		return stopCh, abortCh, func() {}
	}
	t.Cleanup(func() { installStopSignal = orig })
}

// withClosedStopSignal stubs a first-signal-only world: stop is closed,
// abort stays open (#3520).
func withClosedStopSignal(t *testing.T) {
	t.Helper()
	withStubbedSignals(t, true, false)
}

// withClosedAbortSignal stubs a second-signal world: both stop and abort are
// closed, matching what a real second Ctrl-C delivers -- stopsignal.Relay
// always closes stop on the first signal before it ever closes abort on the
// second (#3521).
func withClosedAbortSignal(t *testing.T) {
	t.Helper()
	withStubbedSignals(t, true, true)
}

// A pre-closed stop channel must win over the ordinary empty-queue verdict:
// with no open issues at all, runContinuousDispatch would otherwise exit 2
// (errQueueEmpty), but RunContinuous observes the stop on its very first
// refill call, ahead of ever discovering the queue is empty (#3520).
func TestRunExitCode_ContinuousDispatch_SignalledStop_WinsOverEmptyQueue(t *testing.T) {
	withClosedStopSignal(t)

	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
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
		t.Errorf("runExitCode(lc) = %d, want %d (waves.ErrSignalledStop) -- must not flatten into exit 2", got, exitSignalledStop)
	}
}

// The same precedence holds against the blocked-issue verdict (exit 3, not
// just the empty-queue one): an open issue exists but is blocked, which
// would ordinarily surface ErrOpenNoneDispatchable, but a pre-closed stop
// still wins (#3520).
func TestRunExitCode_ContinuousDispatch_SignalledStop_WinsOverAllBlocked(t *testing.T) {
	withClosedStopSignal(t)

	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
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
		t.Errorf("runExitCode(lc) = %d, want %d (waves.ErrSignalledStop) -- must not flatten into exit 3", got, exitSignalledStop)
	}
}

// A pre-closed abort channel (the second-signal escalation, #3521) must exit
// exitSignalledStop the same as a pre-closed stop alone: RunContinuous's
// observeAbort reclaims in-flight issues and returns the same
// waves.ErrSignalledStop a graceful drain does, so a driving loop like
// dogfood.sh sees one exit code for "the operator asked to stop" regardless
// of which escalation level actually fired.
func TestRunExitCode_ContinuousDispatch_SignalledAbort_ExitsSameCodeAsStop(t *testing.T) {
	withClosedAbortSignal(t)

	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
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
		t.Errorf("runExitCode(lc) = %d, want %d (waves.ErrSignalledStop) -- an abort exits the same code as a graceful drain", got, exitSignalledStop)
	}
}
