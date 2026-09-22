package daemon

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// TestLoopClosedStopHaltsWithNoChildStarted covers the "already stopped
// before Loop was even called" edge of the operator-stop latch: a
// pre-closed Config.Stop must halt deterministically before any slot ever
// reaches RunChild, not merely once the background watcher happens to be
// scheduled.
func TestLoopClosedStopHaltsWithNoChildStarted(t *testing.T) {
	stop := make(chan struct{})
	close(stop)

	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Stop = stop

	h := Loop(context.Background(), cfg, r, em, clk)

	if h.Class != HaltOperatorStop {
		t.Fatalf("halt class = %v, want HaltOperatorStop", h.Class)
	}
	if got := h.ExitCode(); got != 0 {
		t.Fatalf("exit code = %d, want 0", got)
	}
	if !strings.HasPrefix(h.String(), "context-cancelled:") {
		t.Fatalf("reason = %q, want prefix %q", h.String(), "context-cancelled:")
	}
	if r.runCount() != 0 {
		t.Fatalf("run calls = %d, want 0", r.runCount())
	}
}

// TestLoopClosedStopEndsInProgressResolveTipPromptly closes Stop while
// a slot is blocked inside ResolveTip (standing in for the production
// adapter's git fetch), and asserts the pool's own cancelled context is what
// unblocks it — Loop must not wait out any backoff or idle interval first.
func TestLoopClosedStopEndsInProgressResolveTipPromptly(t *testing.T) {
	stop := make(chan struct{})
	resolving := make(chan struct{})
	unblocked := make(chan struct{})

	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 0}},
		onResolve: func(ctx context.Context, call int) error {
			// Signals the fetch is genuinely in flight before the test
			// closes Stop — otherwise a fast scheduler could have the
			// watcher goroutine halt the pool before runSlot ever reaches
			// ResolveTip, and this test would pass for the wrong
			// reason (no fetch to interrupt at all).
			close(resolving)
			<-ctx.Done()
			close(unblocked)
			return ctx.Err()
		},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Stop = stop

	done := make(chan Halt, 1)
	go func() { done <- Loop(context.Background(), cfg, r, em, clk) }()

	select {
	case <-resolving:
	case <-time.After(5 * time.Second):
		t.Fatalf("ResolveTip was never called")
	}
	close(stop)

	select {
	case <-unblocked:
	case <-time.After(5 * time.Second):
		t.Fatalf("ResolveTip's ctx was never cancelled after Stop closed")
	}

	select {
	case h := <-done:
		if h.Class != HaltOperatorStop {
			t.Fatalf("halt class = %v, want HaltOperatorStop", h.Class)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Loop did not return promptly after Stop closed")
	}
	if r.runCount() != 0 {
		t.Fatalf("run calls = %d, want 0: the fetch never resolved a revision to run a child at", r.runCount())
	}
}

// TestLoopClosedStopEndsInProgressBackoffSleepPromptly covers the other half
// of the "closed Stop ends an in-progress wait promptly" acceptance
// criterion (TestLoopClosedStopEndsInProgressResolveTipPromptly above
// covers the fetch half): a slot parked in the failure backoff sleep must
// unblock the instant Stop closes, not sleep out the rest of
// FailureBackoff. BreakerThreshold defaults to 1000 (testConfig), so this
// one unclassified exit backs the slot off instead of tripping the breaker.
func TestLoopClosedStopEndsInProgressBackoffSleepPromptly(t *testing.T) {
	stop := make(chan struct{})

	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 42}}}
	clk := &testClock{sleepSignal: make(chan struct{}, 1)}
	clk.park()
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Stop = stop

	done := make(chan Halt, 1)
	go func() { done <- Loop(context.Background(), cfg, r, em, clk) }()

	clk.awaitSleep(t, 1)
	close(stop)

	select {
	case h := <-done:
		if h.Class != HaltOperatorStop {
			t.Fatalf("halt class = %v, want HaltOperatorStop", h.Class)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Loop did not return promptly after Stop closed while a slot was parked in the backoff sleep")
	}
}

// TestChildRequestCarriesStopThenAbortInOrder pins that runSlot re-exposes
// cfg.Stop and cfg.Abort on every ChildRequest: a double selecting on both
// must see Stop close, then Abort close, in that order.
func TestChildRequestCarriesStopThenAbortInOrder(t *testing.T) {
	stop := make(chan struct{})
	abort := make(chan struct{})
	started := make(chan struct{})
	order := make(chan string, 2)

	r := &scriptedRunner{
		// Exit 7 (signalled-stop) is HaltPool in Interpret's table: runSlot
		// halts the pool and returns right after this one RunChild call, so
		// there is no retry to race the order captured below against a
		// second onStart invocation.
		results: []ChildResult{{Exit: 7}},
		onStart: func(ctx context.Context, req ChildRequest) error {
			close(started)
			s, a := req.Stop, req.Abort
			for i := 0; i < 2; i++ {
				select {
				case <-s:
					order <- "stop"
					s = nil
				case <-a:
					order <- "abort"
					a = nil
				}
			}
			return nil
		},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Stop = stop
	cfg.Abort = abort

	done := make(chan Halt, 1)
	go func() { done <- Loop(context.Background(), cfg, r, em, clk) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("child never started")
	}

	close(stop)
	if got := <-order; got != "stop" {
		t.Fatalf("first channel seen = %q, want stop", got)
	}
	close(abort)
	if got := <-order; got != "abort" {
		t.Fatalf("second channel seen = %q, want abort", got)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Loop did not return after Stop and Abort both closed")
	}
}

// TestChildRequestBothAlreadyClosedSeenImmediately drives the public Loop
// seam, not newPool/runSlot directly: with the mid-iteration admission
// checks gone (issue #3626), a slot parked in ResolveTip when both
// latches close goes on to start its child anyway — the very race
// ChildRequest.Stop/Abort's per-child forwarding exists for — so this is
// now reachable without reaching around Loop's own Stop watcher.
func TestChildRequestBothAlreadyClosedSeenImmediately(t *testing.T) {
	stop := make(chan struct{})
	abort := make(chan struct{})
	resolving := make(chan struct{})

	var seenStop, seenAbort bool
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 5}},
		onResolve: func(ctx context.Context, call int) error {
			if call == 1 {
				close(resolving)
				// Park here until both latches close, so the child this
				// resolve unblocks into starts with both already closed.
				<-stop
				<-abort
			}
			return nil
		},
		onStart: func(ctx context.Context, req ChildRequest) error {
			select {
			case <-req.Stop:
				seenStop = true
			default:
			}
			select {
			case <-req.Abort:
				seenAbort = true
			default:
			}
			return nil
		},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Stop = stop
	cfg.Abort = abort

	done := make(chan Halt, 1)
	go func() { done <- Loop(context.Background(), cfg, r, em, clk) }()

	select {
	case <-resolving:
	case <-time.After(5 * time.Second):
		t.Fatalf("ResolveTip was never called")
	}
	close(stop)
	close(abort)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Loop did not return after the child started and exited")
	}

	if !seenStop || !seenAbort {
		t.Fatalf("seenStop=%v seenAbort=%v, want both true", seenStop, seenAbort)
	}
}

// TestLoopExit7WithStopOpenBacksOffAndCounts pins issue #3626's other half:
// exit 7 while Stop is still open means someone else signalled that child,
// not the operator — an unclassified failure like any other, so it counts
// in the breaker rather than halting the pool outright. BreakerThreshold: 2
// scripts two such exits so the first is asserted as a genuine backoff (the
// slot actually sleeps and refills) before the second crosses the
// threshold — BreakerThreshold: 1 (as this test used to run it) trips on
// the first exit, before any backoff sleep, so it could never assert that
// half.
func TestLoopExit7WithStopOpenBacksOffAndCounts(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 7}, {Exit: 7}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.BreakerThreshold = 2

	h := Loop(context.Background(), cfg, r, em, clk)

	if clk.waitCount() != 1 || clk.waits()[0] != testFailureBackoff {
		t.Fatalf("waits = %v, want exactly one wait of %v: the first exit 7 must back the slot off and refill, not trip the breaker itself", clk.waits(), testFailureBackoff)
	}
	if h.Class != HaltBreaker {
		t.Fatalf("halt class = %v, want HaltBreaker: exit 7 with Stop open must count as a breaker failure, not an operator stop", h.Class)
	}
	events := decodeEvents(t, &buf)
	found := false
	for _, ev := range events {
		if ev.Event == "breaker_trip" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want a breaker_trip event", eventNames(events))
	}
}

// TestLoopExit7WithStopClosedHaltsAsOperatorStop pins issue #3626's
// headline case: exit 7 keeps its pre-#3626 meaning — an operator stop,
// exit 0 — but only while Stop was already closed when the child exited.
// Drives newPool/runSlot directly, not Loop: Loop's own background Stop
// watcher would otherwise race this same close and can win it with its own
// generic HaltOperatorStop, halting with the watcher's reason instead of
// this child's own HaltChildSignalled one — a race, not a bug, but not
// what this test means to pin.
func TestLoopExit7WithStopClosedHaltsAsOperatorStop(t *testing.T) {
	stop := make(chan struct{})
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 7}},
		onStart:   func(ctx context.Context, req ChildRequest) error { close(stop); return nil },
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Stop = stop

	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	runSlot(pctx, 0, cfg, p)
	h := p.haltReason()

	if h.Class != HaltChildSignalled {
		t.Fatalf("halt class = %v, want HaltChildSignalled", h.Class)
	}
	if got := h.ExitCode(); got != 0 {
		t.Fatalf("exit code = %d, want 0", got)
	}
}

// TestLoopUnknownExitWithStopClosedNeverCounted pins the breaker carve-out
// (issue #3595, now by construction): an unrecognised exit that lands while
// Stop is already closed is an ordinary shutdown, not evidence of a
// systemic fault, so it never reaches the breaker — BreakerThreshold: 1
// means any count at all would trip it, so no breaker_trip proves it was
// skipped. Drives newPool/runSlot directly, not Loop, for the same
// watcher-race reason as TestLoopExit7WithStopClosedHaltsAsOperatorStop
// above.
func TestLoopUnknownExitWithStopClosedNeverCounted(t *testing.T) {
	stop := make(chan struct{})
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 42}},
		onStart:   func(ctx context.Context, req ChildRequest) error { close(stop); return nil },
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.BreakerThreshold = 1
	cfg.Stop = stop

	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	runSlot(pctx, 0, cfg, p)
	h := p.haltReason()

	if h.Class != HaltOperatorStop {
		t.Fatalf("halt class = %v, want HaltOperatorStop", h.Class)
	}
	events := decodeEvents(t, &buf)
	for _, ev := range events {
		if ev.Event == "breaker_trip" {
			t.Fatalf("events = %v, want no breaker_trip: an unknown exit while Stop is already closed must never count", eventNames(events))
		}
	}
}

// TestLoopUnknownExitWithStopOpenCounted pins the carve-out's other edge:
// with Stop still open, an unrecognised exit is an ordinary unclassified
// failure and does count — BreakerThreshold: 1 trips on this one alone.
func TestLoopUnknownExitWithStopOpenCounted(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 42}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.BreakerThreshold = 1

	h := Loop(context.Background(), cfg, r, em, clk)

	if h.Class != HaltBreaker {
		t.Fatalf("halt class = %v, want HaltBreaker", h.Class)
	}
	events := decodeEvents(t, &buf)
	found := false
	for _, ev := range events {
		if ev.Event == "breaker_trip" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want a breaker_trip event", eventNames(events))
	}
}
