package main

import (
	"os"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
)

// withClosedStopSignal overrides the installStopSignal seam so
// runContinuousDispatch wires waves.Config.Stop to a channel that is already
// closed, standing in for "the operator's SIGTERM already arrived" without
// registering a real handler or sending a real signal (#3520).
func withClosedStopSignal(t *testing.T) {
	t.Helper()
	orig := installStopSignal
	ch := make(chan struct{})
	close(ch)
	installStopSignal = func() (<-chan struct{}, func()) { return ch, func() {} }
	t.Cleanup(func() { installStopSignal = orig })
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

// relayStop is notifyStopSignal's internal relay factored out so it can be
// driven from a fake chan os.Signal (#3520): the real notifyStopSignal
// itself is a one-line signal.Notify call plus this relay, and the
// signal.Notify half is trusted stdlib wiring not worth exercising with a
// real SIGTERM to the test binary. These two cases are what the seam
// contract promises: a value on sig closes stop, and cleanup silences the
// relay without ever closing stop on its own.
func TestRelayStop_SignalClosesStop(t *testing.T) {
	sig := make(chan os.Signal, 1)
	stop, cleanup := relayStop(sig)
	defer cleanup()

	sig <- syscall.SIGTERM // a plain channel send, never a real OS signal

	select {
	case <-stop:
	case <-time.After(time.Second):
		t.Fatal("stop never closed after a value arrived on sig")
	}
}

func TestRelayStop_CleanupSilencesRelayWithoutClosingStop(t *testing.T) {
	sig := make(chan os.Signal, 1)
	stop, cleanup := relayStop(sig)
	cleanup()

	select {
	case <-stop:
		t.Fatal("stop closed even though sig never received a value")
	case <-time.After(50 * time.Millisecond):
	}
}
