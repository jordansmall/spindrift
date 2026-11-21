package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/report"
)

// sweepEvery is distinct from every other duration a test pool sleeps, so
// sweepGateClock can tell the sweeper's wait from a slot's.
const sweepEvery = 7 * time.Minute

// sweepGateClock holds only the sweeper's interval wait until the test sends
// a tick, delegating every other sleep to the embedded testClock.
type sweepGateClock struct {
	*testClock
	tick   chan struct{}
	parked chan struct{} // best-effort signal each time the sweeper starts waiting
}

func newSweepGateClock() *sweepGateClock {
	return &sweepGateClock{testClock: &testClock{}, tick: make(chan struct{}), parked: make(chan struct{}, 1)}
}

func (c *sweepGateClock) Sleep(ctx context.Context, d time.Duration) {
	if d != sweepEvery {
		c.testClock.Sleep(ctx, d)
		return
	}
	select {
	case c.parked <- struct{}{}:
	default:
	}
	select {
	case <-c.tick:
	case <-ctx.Done():
	}
}

// awaitParked waits for the sweeper to be asleep on its interval.
func (c *sweepGateClock) awaitParked(t *testing.T) {
	t.Helper()
	select {
	case <-c.parked:
	case <-time.After(5 * time.Second):
		t.Fatal("sweeper never waited on its interval")
	}
}

// fireTick ends one interval wait.
func (c *sweepGateClock) fireTick(t *testing.T) {
	t.Helper()
	select {
	case c.tick <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("sweeper is not waiting on its interval")
	}
}

type sweepLoop struct {
	r    *scriptedRunner
	clk  *sweepGateClock
	outW *bytes.Buffer
	errW *bytes.Buffer
	done chan Halt
}

// startSweepLoop runs a one-slot pool with held children and a sweep interval;
// each opt may adjust the Config.
func startSweepLoop(t *testing.T, r *scriptedRunner, interval time.Duration, opts ...func(*Config)) *sweepLoop {
	t.Helper()
	r.holdSlots(1)
	cfg := testConfig(1)
	cfg.LateMergeSweepInterval = interval
	for _, o := range opts {
		o(&cfg)
	}
	l := &sweepLoop{r: r, clk: newSweepGateClock(), outW: &bytes.Buffer{}, errW: &bytes.Buffer{}, done: make(chan Halt, 1)}
	em := newTestEmitterErr(l.outW, l.errW)
	go func() { l.done <- Loop(context.Background(), cfg, r, em, l.clk) }()
	return l
}

// finish ends the pool through a host-tainted child exit and returns its halt.
func (l *sweepLoop) finish(t *testing.T) Halt {
	t.Helper()
	l.r.releaseSlot(t, 0, ChildResult{Exit: 5})
	select {
	case h := <-l.done:
		return h
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return")
		return Halt{}
	}
}

// The sweep runs outside the pool: while it is blocked, the only slot finishes
// a child and starts the next. It runs at the tip as soon as the tip resolves,
// then once per interval.
func TestLateMergeSweepHoldsNoSlot(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.onSweep = func(ctx context.Context, _ string) error {
		entered <- struct{}{}
		<-release
		return nil
	}
	l := startSweepLoop(t, r, sweepEvery)

	r.awaitStart(t)
	<-entered

	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	r.awaitStart(t)
	if got := r.sweeps(); !reflect.DeepEqual(got, []string{"rev1"}) {
		t.Fatalf("sweeps = %v, want one at rev1 still running while the slot started a child", got)
	}

	close(release)
	l.clk.awaitParked(t)
	if got := len(r.sweeps()); got != 1 {
		t.Fatalf("sweeps = %d, want no second before the clock advances another interval", got)
	}
	l.clk.fireTick(t)
	<-entered
	if got := len(r.sweeps()); got != 2 {
		t.Fatalf("sweeps = %d, want 2 after a second interval", got)
	}
	if h := l.finish(t); !strings.Contains(h.String(), "host-tainted") {
		t.Errorf("halt = %q, want the child's host-tainted halt", h.String())
	}
}

// A failed sweep, a rate limit included, only warns: no halt, and the pool
// keeps starting children.
func TestLateMergeSweepFailureOnlyWarns(t *testing.T) {
	entered := make(chan struct{}, 1)
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.onSweep = func(context.Context, string) error {
		defer func() { entered <- struct{}{} }()
		return fmt.Errorf("late merges: %w", errors.New("rate limit exceeded"))
	}
	l := startSweepLoop(t, r, sweepEvery)

	r.awaitStart(t)
	<-entered
	l.clk.awaitParked(t) // the warning is written before the sweeper waits again

	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	r.awaitStart(t)
	h := l.finish(t)
	if !strings.Contains(h.String(), "host-tainted") {
		t.Errorf("halt = %q, want only the child's own host-tainted halt", h.String())
	}
	if got := l.errW.String(); !strings.Contains(got, "daemon: late-merge sweep failed: late merges: rate limit exceeded") {
		t.Errorf("errW = %q, want the sweep failure warning", got)
	}
	for _, ev := range decodeEvents(t, bytes.NewBufferString(l.outW.String())) {
		if ev.Event == report.EventBackoff || ev.Event == report.EventBreakerTrip {
			t.Errorf("event %q after a sweep failure, want no backoff or breaker trip", ev.Event)
		}
	}
}

// A zero interval runs no sweep at all.
func TestLateMergeSweepZeroIntervalRunsNone(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}

	Loop(context.Background(), testConfig(1), r, newTestEmitter(&bytes.Buffer{}), &testClock{})

	if got := r.sweeps(); len(got) != 0 {
		t.Fatalf("sweeps = %v, want none", got)
	}
}

// Every Loop restart sweeps at once, without waiting an interval: a daemon
// restarting more often than the interval must still sweep.
func TestLateMergeSweepRunsRightAfterEachRestart(t *testing.T) {
	for run := 1; run <= 2; run++ {
		entered := make(chan struct{}, 1)
		r := &scriptedRunner{revisions: []string{"rev1"}}
		r.onSweep = func(context.Context, string) error {
			entered <- struct{}{}
			return nil
		}
		l := startSweepLoop(t, r, sweepEvery)
		r.awaitStart(t)
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("run %d: no sweep without a tick", run)
		}
		if got := r.sweeps(); !reflect.DeepEqual(got, []string{"rev1"}) {
			t.Fatalf("run %d: sweeps = %v, want one at rev1", run, got)
		}
		l.finish(t)
	}
}

// A pool halt only stops new sweeps: the one in flight keeps its context (its
// interval stamp is already claimed) and Loop waits it out.
func TestLateMergeSweepSurvivesPoolHalt(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	ctxErr := make(chan error, 1)
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.onSweep = func(ctx context.Context, _ string) error {
		entered <- struct{}{}
		<-release
		ctxErr <- ctx.Err()
		return ctx.Err()
	}
	l := startSweepLoop(t, r, sweepEvery)
	r.awaitStart(t)
	<-entered

	r.releaseSlot(t, 0, ChildResult{Exit: 5})
	select {
	case h := <-l.done:
		t.Fatalf("Loop returned (%q) while a sweep was in flight", h.String())
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-ctxErr; err != nil {
		t.Errorf("sweep ctx err = %v after the pool halted, want nil", err)
	}
	select {
	case <-l.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return after the sweep finished")
	}
	if got := l.errW.String(); strings.Contains(got, "late-merge sweep failed") {
		t.Errorf("errW = %q, want no sweep warning", got)
	}
}

// The hard-abort latch cancels an in-flight sweep.
func TestLateMergeSweepAbortCancelsInFlightSweep(t *testing.T) {
	entered := make(chan struct{}, 1)
	ended := make(chan error, 1)
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.onSweep = func(ctx context.Context, _ string) error {
		entered <- struct{}{}
		<-ctx.Done()
		ended <- ctx.Err()
		return ctx.Err()
	}
	abort := make(chan struct{})
	l := startSweepLoop(t, r, sweepEvery, func(c *Config) { c.Abort = abort })
	r.awaitStart(t)
	<-entered

	close(abort)
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("sweep ctx err = %v, want canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("abort did not cancel the sweep")
	}
	l.finish(t)
	if got := l.errW.String(); strings.Contains(got, "late-merge sweep failed") {
		t.Errorf("errW = %q, want no warning for an aborted sweep", got)
	}
}

// A committed in-place handoff cancels an in-flight sweep and Loop waits for
// it to return, so no un-reaped sweep process outlives the exec.
func TestLateMergeSweepInPlaceHandoffCancelsAndWaitsForSweep(t *testing.T) {
	rig := newHandoffRig(t, 1)
	rig.cfg.LateMergeSweepInterval = sweepEvery
	entered := make(chan struct{}, 1)
	cancelled := make(chan struct{})
	exit := make(chan struct{})
	var returned atomic.Bool
	rig.r.onSweep = func(ctx context.Context, _ string) error {
		entered <- struct{}{}
		<-ctx.Done()
		close(cancelled)
		<-exit
		returned.Store(true)
		return ctx.Err()
	}
	errW := &bytes.Buffer{}
	rig.r.announceEachSlot()
	go func() {
		rig.done <- Loop(context.Background(), rig.cfg, rig.r, newTestEmitterErr(rig.buf, errW), rig.clk)
	}()
	rig.r.awaitStart(t)
	<-entered

	close(rig.gate)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("handoff did not cancel the sweep")
	}

	close(exit)
	h := rig.wait(t)
	if !returned.Load() {
		t.Error("Loop returned before the sweep function did")
	}
	if h.Handoff == nil {
		t.Fatalf("halt = %+v, want a handoff", h)
	}
	if got := errW.String(); strings.Contains(got, "late-merge sweep failed") {
		t.Errorf("errW = %q, want no warning for a cancelled sweep", got)
	}
}
