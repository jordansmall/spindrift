package daemon

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// notifyWriter is an Emitter sink that also publishes each write on a
// channel, so a test can block until a specific event has actually been
// recorded rather than guessing at goroutine timing.
type notifyWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	lines chan string
}

func newNotifyWriter() *notifyWriter {
	return &notifyWriter{lines: make(chan string, 64)}
}

func (w *notifyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	w.mu.Unlock()
	w.lines <- string(p)
	return n, err
}

func (w *notifyWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// waitForLine blocks until a written line contains substr, failing the
// test if none arrives within a generous bound (a hang here means the
// event this test is synchronizing on was never emitted, a real bug, not
// a timing fluke worth retrying).
func (w *notifyWriter) waitForLine(t *testing.T, substr string) {
	t.Helper()
	for i := 0; i < 64; i++ {
		select {
		case line := <-w.lines:
			if strings.Contains(line, substr) {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("waitForLine: no line containing %q within 5s", substr)
		}
	}
	t.Fatalf("waitForLine: no line containing %q within %d lines", substr, 64)
}

// TestPoolRunsSlotsConcurrentlyAndNeverAbandonsAStartedChild pins the two
// guarantees a pool of slots exists for: with Slots: N and every slot's
// discovery released at once (r.onStart below announces each slot
// immediately, so none ever parks on the baton), N children really do run
// at once (not N sequential calls that merely look concurrent from the
// outside), and
// once every slot's child has started, halting one slot still lets every
// sibling's already-started child return and emit its own child_finish
// rather than being abandoned mid-run.
func TestPoolRunsSlotsConcurrentlyAndNeverAbandonsAStartedChild(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	// onStart restores this test's concurrent first wave: without it, only
	// the pre-assigned initial baton holder would start until it claims
	// something.
	r.announceEachSlot()
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	done := make(chan string, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	// Wait for all three slots to be in flight together before releasing
	// any of them — this is what makes "peak == slots" prove real overlap
	// rather than a lucky race.
	seen := map[int]bool{}
	for i := 0; i < slots; i++ {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}
	if peak := r.peakConcurrency(); peak != slots {
		t.Fatalf("peak concurrency = %d, want %d: slots did not overlap", peak, slots)
	}

	// Every slot halts on release (exit 7 == signalled-stop). Whichever
	// slot's RunChild returns first wins the halt race; the others must
	// still be allowed to finish rather than being cut off.
	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 7})
	}

	reason := <-done
	if !strings.Contains(reason, "signalled-stop") {
		t.Fatalf("halt reason = %q, want it to name signalled-stop", reason)
	}

	events := decodeEvents(t, &buf)
	starts, finishes, halts := map[int]int{}, map[int]int{}, 0
	for _, ev := range events {
		switch ev.Event {
		case "child_start":
			if ev.Slot == nil {
				t.Fatalf("child_start event missing slot: %+v", ev)
			}
			starts[*ev.Slot]++
		case "child_finish":
			if ev.Slot == nil {
				t.Fatalf("child_finish event missing slot: %+v", ev)
			}
			finishes[*ev.Slot]++
		case "halt":
			halts++
			if ev.Slot != nil {
				t.Errorf("halt event carries slot %v, want unstamped (pool-level)", *ev.Slot)
			}
		}
	}
	if halts != 1 {
		t.Fatalf("halt events = %d, want exactly 1", halts)
	}
	for s := 0; s < slots; s++ {
		if starts[s] != 1 {
			t.Errorf("slot %d child_start count = %d, want 1", s, starts[s])
		}
		if finishes[s] != 1 {
			t.Errorf("slot %d child_finish count = %d, want 1: a started child must never be abandoned", s, finishes[s])
		}
	}
}

// TestPoolSlots1RunsOneChildAtATime asserts Slots: 1 drives exactly
// one child at a time — the existing single-slot loop tests already pin
// this behaviour via Config{Slots: 1}, so this test just names the
// invariant explicitly at the pool layer.
func TestPoolSlots1RunsOneChildAtATime(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk)

	if r.runCount() != 2 {
		t.Fatalf("run calls = %d, want 2", r.runCount())
	}
	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want it to name host-tainted", reason)
	}
}

// TestPoolBreakerTripsAtThresholdAcrossSlots pins the breaker's central
// property: it counts failures across the whole pool, not per slot. Slots
// 0 and 1 each fail once and back off (below the threshold of 3, so they
// keep working); slot 2's failure is the third across the pool and trips
// the breaker, halting everything. Already-started siblings (slot 0 and 1's
// post-backoff children) still get to return rather than being abandoned.
func TestPoolBreakerTripsAtThresholdAcrossSlots(t *testing.T) {
	const slots = 3
	const threshold = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	// onStart restores this test's concurrent first wave: every slot
	// announces at once, so none ever parks waiting for the discovery
	// baton.
	r.announceEachSlot()
	clk := &testClock{}
	// nw notifies on every event line as it is written, so the test can
	// block until the pool's halt is actually recorded before releasing
	// slot 0/1's still-in-flight children — without it, those slots can
	// race ahead of the halt and try a third RunChild call this test never
	// scripts a release for.
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	cfg := testConfig(slots)
	cfg.FailureBackoff = time.Millisecond
	cfg.BreakerThreshold = threshold
	cfg.BreakerWindow = time.Hour

	done := make(chan string, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	// All three slots' first child in flight together.
	seen := map[int]bool{}
	for i := 0; i < slots; i++ {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}

	// Slot 0 fails (1st pool-wide failure, below threshold): it backs off
	// and restarts. Awaiting a start again is only possible once that
	// restart's RunChild call is in flight, so it also proves the breaker
	// recorded slot 0's failure before slot 1's is sent below.
	r.releaseSlot(t, 0, ChildResult{Exit: 1})
	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("restart after backoff = slot %d, want slot 0", got)
	}

	// Slot 1 fails (2nd pool-wide failure, still below threshold): same
	// backoff-and-restart.
	r.releaseSlot(t, 1, ChildResult{Exit: 1})
	if got := r.awaitStart(t); got != 1 {
		t.Fatalf("restart after backoff = slot %d, want slot 1", got)
	}

	// Slot 2 fails (3rd pool-wide failure, reaches threshold): the breaker
	// trips instead of slot 2 backing off.
	r.releaseSlot(t, 2, ChildResult{Exit: 1})

	// Wait for the pool's own halt event before releasing slot 0/1's
	// still-in-flight children: the pool's mutex gives every later Lock
	// (including slot 0/1's own stopOnCancel check) a happens-after view
	// of the halt, so once this line is observed they are guaranteed to
	// notice the halt and return rather than starting a fourth call.
	nw.waitForLine(t, "\"event\":\"halt\"")

	// Slot 0 and 1's restarted children are still in flight (the pool's
	// never-kill-a-started-child invariant), so they must be released for
	// Loop to return at all.
	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	r.releaseSlot(t, 1, ChildResult{Exit: 0})

	reason := <-done
	if !strings.Contains(reason, "breaker") {
		t.Errorf("halt reason = %q, want it to name the breaker trip", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	starts, finishes, backoffs := map[int]int{}, map[int]int{}, 0
	var halts, trips int
	for _, ev := range events {
		switch ev.Event {
		case "child_start":
			starts[*ev.Slot]++
		case "child_finish":
			finishes[*ev.Slot]++
		case "backoff":
			backoffs++
		case "halt":
			halts++
		case "breaker_trip":
			trips++
			if ev.Failures == nil || *ev.Failures != threshold {
				t.Errorf("breaker_trip Failures = %v, want %d", ev.Failures, threshold)
			}
			if ev.Slot == nil || *ev.Slot != 2 {
				t.Errorf("breaker_trip Slot = %v, want 2 (the slot whose failure crossed threshold)", ev.Slot)
			}
		}
	}
	if halts != 1 {
		t.Errorf("halt events = %d, want exactly 1", halts)
	}
	if trips != 1 {
		t.Errorf("breaker_trip events = %d, want exactly 1", trips)
	}
	if backoffs != 2 {
		t.Errorf("backoff events = %d, want exactly 2 (slots 0 and 1, below threshold)", backoffs)
	}
	wantStarts := map[int]int{0: 2, 1: 2, 2: 1}
	for s, want := range wantStarts {
		if starts[s] != want {
			t.Errorf("slot %d child_start count = %d, want %d", s, starts[s], want)
		}
		if finishes[s] != want {
			t.Errorf("slot %d child_finish count = %d, want %d: a started child must never be abandoned", s, finishes[s], want)
		}
	}
}

// TestPoolBreakerTripsAtThresholdConcurrently pins the fix for the
// non-atomic crossing: releasing every slot's failure at once (rather
// than one at a time, like the serialized test above) used to let all of
// them observe the breaker already past threshold and each emit its own
// breaker_trip. Run with -race -count=N: the race detector and repeated
// scheduling are what actually exercise the crossing window.
func TestPoolBreakerTripsAtThresholdConcurrently(t *testing.T) {
	const slots = 3
	const threshold = 3

	// started/arrived/release: a test-local barrier releasing every
	// slot's first RunChild call at once, so the concurrent crossing the
	// serialized test above cannot reach really is concurrent.
	started := make(chan int, slots)
	var mu sync.Mutex
	arrived := 0
	release := make(chan struct{})

	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 1}},
		onStart: func(ctx context.Context, req ChildRequest) error {
			// Announce immediately, before ever joining the barrier
			// below: whichever slot currently holds the discovery baton
			// must pass it the instant its own RunChild call begins, or
			// the other slots would still be parked waiting for it and
			// could never join the barrier this test's whole premise
			// depends on.
			if req.OnIssue != nil {
				req.OnIssue(fmt.Sprintf("issue-%d", req.Slot))
			}
			// Non-blocking, because the number of later calls is
			// unbounded: a non-crossing slot races around through its
			// backoff and back into RunChild as many times as the
			// scheduler allows while the crossing slot sits between
			// recordAndCheck and halt. Only the first `slots` sends are
			// ever read, so a blocking send fills the buffer and parks a
			// slot goroutine forever, and Loop never returns.
			select {
			case started <- req.Slot:
			default:
			}
			mu.Lock()
			arrived++
			if arrived == slots {
				close(release)
			}
			mu.Unlock()
			<-release
			return nil
		},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(slots)
	cfg.FailureBackoff = time.Millisecond
	cfg.BreakerThreshold = threshold
	cfg.BreakerWindow = time.Hour

	done := make(chan string, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	// All three slots' first (and, here, only scripted) child in flight
	// together, proving the barrier really did gather all `slots` calls
	// before releasing any of them.
	seen := map[int]bool{}
	for i := 0; i < slots; i++ {
		seen[<-started] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}

	var reason string
	select {
	case reason = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Loop did not return within 10s of the concurrent release")
	}
	if !strings.Contains(reason, "breaker") {
		t.Errorf("halt reason = %q, want it to name the breaker trip", reason)
	}

	events := decodeEvents(t, &buf)
	var halts, trips int
	var tripSlot *int
	for _, ev := range events {
		switch ev.Event {
		case "halt":
			halts++
		case "breaker_trip":
			trips++
			tripSlot = ev.Slot
			if ev.Failures == nil || *ev.Failures != threshold {
				t.Errorf("breaker_trip Failures = %v, want %d", ev.Failures, threshold)
			}
		}
	}
	if trips != 1 {
		t.Fatalf("breaker_trip events = %d, want exactly 1 (this is the atomic-crossing assertion)", trips)
	}
	if halts != 1 {
		t.Errorf("halt events = %d, want exactly 1", halts)
	}
	if tripSlot == nil {
		t.Errorf("breaker_trip slot = nil, want the slot whose failure crossed the threshold")
	}
}

// TestPoolExit3WithSiblingRunningReportsIdleNotJam pins the routine half of
// the occupancy axis: a slot's exit 3 while a sibling is genuinely still
// running (blocked mid-RunChild, not merely between calls) must report
// idle, not jam — the issues this slot found none-dispatchable were
// claimed or overlap-deferred against that very sibling.
func TestPoolExit3WithSiblingRunningReportsIdleNotJam(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	// onStart restores this test's concurrent first wave: every slot
	// announces at once, so none ever parks waiting for the discovery
	// baton.
	r.announceEachSlot()
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })
	cfg := testConfig(slots)

	done := make(chan string, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	// Both slots' first child in flight together.
	seen := map[int]bool{}
	for i := 0; i < slots; i++ {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}

	// Slot 1 exits none-dispatchable while slot 0 is still blocked mid-
	// RunChild (genuinely occupied, not just between calls) — this must
	// report idle.
	r.releaseSlot(t, 1, ChildResult{Exit: 3})
	nw.waitForLine(t, "\"event\":\"idle\"")

	// Slot 1 now loops back and restarts (still nothing wrong with the
	// pool); wait for its restart so the later halt below has a real
	// in-flight child to release rather than racing its own start.
	if got := r.awaitStart(t); got != 1 {
		t.Fatalf("restart after idle = slot %d, want slot 1", got)
	}

	// End the test: halt via slot 0's still-in-flight first child.
	r.releaseSlot(t, 0, ChildResult{Exit: 7})
	nw.waitForLine(t, "\"event\":\"halt\"")

	// Slot 1's restarted child is still in flight; release it so Loop can
	// return.
	r.releaseSlot(t, 1, ChildResult{Exit: 0})

	reason := <-done
	if !strings.Contains(reason, "signalled-stop") {
		t.Fatalf("halt reason = %q, want it to name signalled-stop", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	for _, ev := range events {
		if ev.Event == "jam" {
			t.Fatalf("events = %v, want no jam event: slot 0 was genuinely running when slot 1 exited none-dispatchable", eventNames(events))
		}
	}
}

// TestPoolExit3WithPoolIdleIsAJam pins the jam half of the occupancy axis:
// a slot's exit 3 with every sibling truly parked (asleep in their own
// idle wait, not occupied) must be reported as a jam, carrying the slot
// number, and the daemon keeps going rather than halting on it.
func TestPoolExit3WithPoolIdleIsAJam(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	// onStart restores this test's concurrent first wave: every slot
	// announces at once, so none ever parks waiting for the discovery
	// baton.
	r.announceEachSlot()
	// clk: Sleep must remain provably parked rather than testClock's
	// instant advance racing straight back into a second RunChild call,
	// so "this slot is now asleep" is a real synchronization point this
	// test can wait on via sleepSignal, and the only way any Sleep call
	// ever returns is the pool itself being cancelled below.
	clk := &testClock{sleepSignal: make(chan struct{}, slots)}
	clk.park()
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })
	cfg := testConfig(slots)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		done <- Loop(ctx, cfg, r, em, clk)
	}()

	// Both slots' first child in flight together.
	seen := map[int]bool{}
	for i := 0; i < slots; i++ {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}

	// Slot 1 exits queue-empty and parks in its idle wait — the parked
	// clock's Sleep never returns on its own, so slot 1 is now genuinely,
	// provably not occupied and staying that way.
	r.releaseSlot(t, 1, ChildResult{Exit: 2})
	<-clk.sleepSignal

	// Slot 0 exits none-dispatchable with slot 1 parked and nothing else
	// running: this must report jam, carrying slot 0.
	r.releaseSlot(t, 0, ChildResult{Exit: 3})
	nw.waitForLine(t, "\"event\":\"jam\"")

	// End the test: cancelling the top-level ctx reaches both slots
	// through their gated Sleep call (blocked on ctx.Done()), so both
	// return via stopOnCancel without ever starting a third RunChild call
	// this test never scripts a release for.
	cancel()
	reason := <-done
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want it to name context-cancelled", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	var jams int
	for _, ev := range events {
		if ev.Event == "jam" {
			jams++
			if ev.Slot == nil || *ev.Slot != 0 {
				t.Errorf("jam event slot = %v, want 0", ev.Slot)
			}
			if ev.Reason == "" {
				t.Errorf("jam event reason is empty, want it to say what makes it a jam")
			}
		}
	}
	if jams != 1 {
		t.Fatalf("jam events = %d, want exactly 1", jams)
	}
}

// TestIdleWaitLastSliceClampsToRemaining pins the clamp at the tail of
// pollSlices's slicing loop: when IdleFloor does not evenly divide the
// requested wait, the final slice must shrink to what's left rather than
// overshoot it. That is a legal config — IdleCap need not be a multiple of
// IdleFloor — but the shipped 5m/30m pair always divides evenly, so nothing
// else in this package reaches the clamp.
func TestIdleWaitLastSliceClampsToRemaining(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.IdleFloor = 3 * time.Millisecond
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	p.pollSlices(pctx, 0, 10*time.Millisecond, "rev1")

	want := []time.Duration{3 * time.Millisecond, 3 * time.Millisecond, 3 * time.Millisecond, time.Millisecond}
	if clk.waitCount() != len(want) {
		t.Fatalf("waits = %v, want %v", clk.waits(), want)
	}
	for i := range want {
		if clk.waits()[i] != want[i] {
			t.Fatalf("waits = %v, want %v", clk.waits(), want)
		}
	}
}

// TestPoolAwakeWindowClosingEmitsExactlyOneCloseAndOpenAcrossSlots pins the
// edge-triggered contract: with several slots all parking on the same
// closed window, the stream carries exactly one awake_close and one
// awake_open, never one per parked slot.
func TestPoolAwakeWindowClosingEmitsExactlyOneCloseAndOpenAcrossSlots(t *testing.T) {
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	// clk: testClock's step mode, unlike an additive advance (unsound once
	// several Sleep calls race the same shared clock), only changes now on
	// step and releases every Sleep blocked so far at once, modeling "all
	// three slots are asleep waiting for the same instant" with no
	// additive artifact.
	clk := &testClock{now: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC), sleepSignal: make(chan struct{}, slots)}
	clk.park()
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(slots)
	cfg.Awake = win
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	var wg sync.WaitGroup
	wg.Add(slots)
	for s := 0; s < slots; s++ {
		go func(s int) {
			defer wg.Done()
			p.awaitWindow(pctx, s)
		}(s)
	}

	clk.awaitSleep(t, slots)
	clk.step(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)) // window opens, releases all three
	wg.Wait()

	events := decodeEvents(t, &buf)
	closes, opens := 0, 0
	for _, ev := range events {
		switch ev.Event {
		case "awake_close":
			closes++
		case "awake_open":
			opens++
		}
	}
	if closes != 1 {
		t.Fatalf("awake_close events = %d, want exactly 1 (%v)", closes, eventNames(events))
	}
	if opens != 1 {
		t.Fatalf("awake_open events = %d, want exactly 1 (%v)", opens, eventNames(events))
	}
}

// TestAwaitWindowSkipsPublishOnNonTransitionIteration pins that awaitWindow
// only writes the status file on a real awake_close/awake_open transition
// (review finding pool.go:169-180): a second iteration that still finds the
// window shut, with nothing new to report, must perform no additional
// write, even though noteAwakeClose runs again on every iteration.
func TestAwaitWindowSkipsPublishOnNonTransitionIteration(t *testing.T) {
	win, err := ParseWindow("22:00-06:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	// clk: see TestPoolAwakeWindowClosingEmitsExactlyOneCloseAndOpenAcrossSlots
	// for why step mode, not an additive advance, is required here.
	clk := &testClock{now: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC), sleepSignal: make(chan struct{}, 1)}
	clk.park()

	// A counting now func: every StatusWriter.Publish call advances the
	// stamped Time by one, whatever the sampled Status otherwise says, so
	// reading Time back after each parking tells write-count apart from
	// content equality (which a repeated "still shut" write would share).
	// Loop publishes from a slot goroutine, but StatusWriter.Publish calls
	// the now func under its own mutex, so the counter needs no guard.
	var writes int
	dir := t.TempDir()
	sw := NewStatusWriter(dir, func() time.Time {
		writes++
		return time.Unix(int64(writes), 0).UTC()
	})

	cfg := testConfig(1)
	cfg.Awake = win
	cfg.Status = sw
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The first ResolveRevision is the seam immediately after awaitWindow
	// returns, and nothing publishes between the awake_open publish and
	// it, so the Time captured here is the one that transition wrote.
	// Cancelling from the same hook halts Loop, keeping every later
	// publish out of the reading; the cancel precedes the read so that a
	// read failure, which ends this goroutine where it stands, still
	// leaves Loop a way out instead of wedging the test until the package
	// timeout.
	var afterOpen string
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.onResolve = func(hookCtx context.Context, _ int) error {
		cancel()
		afterOpen = readStatus(t, dir).Time
		return hookCtx.Err()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		Loop(ctx, cfg, r, em, clk)
	}()

	clk.awaitSleep(t, 1) // first iteration: noteAwakeClose observes the transition
	afterClose := readStatus(t, dir).Time

	// Advance to a still-shut instant: a second iteration, still no
	// transition (awakeShut was already true), must publish nothing new.
	clk.step(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	clk.awaitSleep(t, 1)
	afterSecondShutIteration := readStatus(t, dir).Time
	if afterSecondShutIteration != afterClose {
		t.Fatalf("status Time changed on a non-transition iteration: got %q, want unchanged %q", afterSecondShutIteration, afterClose)
	}

	// Advance past the reopening: the transition back to open must still
	// publish.
	clk.step(time.Date(2026, 1, 1, 22, 0, 0, 0, time.UTC))
	<-done
	if afterOpen == "" {
		t.Fatalf("ResolveRevision was never reached, so the awake_open publish went unobserved")
	}
	if afterOpen == afterSecondShutIteration {
		t.Fatalf("status Time did not change on the awake_open transition")
	}
}

// TestPollSlicesTipMovedStampsJammedKinds pins tip_moved's Kinds field
// (issue #3541 review finding) to exactly the kinds jammed at the moment the
// tip moved, in cfg.Kinds order, whether that's one kind or several — and
// proves a queue-empty (non-jammed) kind is never included.
func TestPollSlicesTipMovedStampsJammedKinds(t *testing.T) {
	cases := []struct {
		name string
		jam  []Kind
		want []Kind
	}{
		{"only dispatch jammed", []Kind{KindDispatch}, []Kind{KindDispatch}},
		{"both kinds jammed", []Kind{KindDispatch, KindResearch}, []Kind{KindDispatch, KindResearch}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptedRunner{revisions: []string{"rev2"}}
			clk := &testClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			cfg := dualKindConfig(1, 0)
			p, pctx := newPool(context.Background(), cfg, r, em, clk)
			defer p.cancel()

			for _, k := range tc.jam {
				p.kinds[k].markNoWork(clk.Now(), true)
			}

			p.pollSlices(pctx, 0, 2*cfg.IdleFloor, "rev1")

			events := decodeEvents(t, &buf)
			var tipMoved *Event
			for i := range events {
				if events[i].Event == "tip_moved" {
					tipMoved = &events[i]
				}
			}
			if tipMoved == nil {
				t.Fatalf("events = %v, want a tip_moved event", eventNames(events))
			}
			if !reflect.DeepEqual(tipMoved.Kinds, tc.want) {
				t.Fatalf("tip_moved kinds = %v, want %v", tipMoved.Kinds, tc.want)
			}
		})
	}
}

// TestSlotOrderDerivesFromKinds pins slotOrder to the kinds argument rather
// than a hardcoded two-kind pair (issue #3541 review finding): a slot below
// the reservation puts KindResearch first and keeps every other kind in its
// given relative order, a slot at or above the reservation keeps kinds in
// its given order with KindResearch moved last, and the input slice must
// never be mutated.
func TestSlotOrderDerivesFromKinds(t *testing.T) {
	const kindOther Kind = "other"

	tests := []struct {
		name        string
		kinds       []Kind
		reservation int
		slot        int
		want        []Kind
	}{
		{
			name:        "single kind unchanged",
			kinds:       []Kind{KindDispatch},
			reservation: 0,
			slot:        0,
			want:        []Kind{KindDispatch},
		},
		{
			name:        "two kinds below reservation prefers research",
			kinds:       []Kind{KindDispatch, KindResearch},
			reservation: 1,
			slot:        0,
			want:        []Kind{KindResearch, KindDispatch},
		},
		{
			name:        "two kinds at reservation prefers work",
			kinds:       []Kind{KindDispatch, KindResearch},
			reservation: 1,
			slot:        1,
			want:        []Kind{KindDispatch, KindResearch},
		},
		{
			name:        "three kinds below reservation keeps non-research relative order",
			kinds:       []Kind{kindOther, KindDispatch, KindResearch},
			reservation: 1,
			slot:        0,
			want:        []Kind{KindResearch, kindOther, KindDispatch},
		},
		{
			// A reserved slot in a research-less set must not invent a
			// preference for a kind the pool has no backoff entry for —
			// pickKind would nil-deref on it.
			name:        "no research kind below reservation keeps given order",
			kinds:       []Kind{KindDispatch, kindOther},
			reservation: 1,
			slot:        0,
			want:        []Kind{KindDispatch, kindOther},
		},
		{
			name:        "three kinds at reservation keeps given order with research last",
			kinds:       []Kind{KindResearch, kindOther, KindDispatch},
			reservation: 1,
			slot:        1,
			want:        []Kind{kindOther, KindDispatch, KindResearch},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := append([]Kind(nil), tc.kinds...)

			got := slotOrder(tc.kinds, tc.reservation, tc.slot)

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("slotOrder(%v, %d, %d) = %v, want %v", tc.kinds, tc.reservation, tc.slot, got, tc.want)
			}
			if !reflect.DeepEqual(tc.kinds, orig) {
				t.Fatalf("slotOrder mutated its kinds argument: got %v, want %v", tc.kinds, orig)
			}
		})
	}
}

// shutWindow builds an Awake window guaranteed shut at now: opening two
// hours out and closing three, so a test can drive State off a real window
// rather than poking the edge-triggered awakeShut flag directly.
func shutWindow(t *testing.T, now time.Time) *Window {
	t.Helper()
	return windowFromOffsets(t, now, 2*time.Hour, 3*time.Hour)
}

// windowFromOffsets formats and parses an Awake window spanning open (now +
// openIn) to shut (now + shutIn), the spec-formatting and ParseWindow call
// shutWindow and openWindowClosingSoon otherwise duplicated verbatim; only
// the two offsets differ between them.
func windowFromOffsets(t *testing.T, now time.Time, openIn, shutIn time.Duration) *Window {
	t.Helper()
	open := now.Add(openIn).UTC()
	shut := now.Add(shutIn).UTC()
	spec := fmt.Sprintf("%02d:%02d-%02d:%02d UTC", open.Hour(), open.Minute(), shut.Hour(), shut.Minute())
	w, err := ParseWindow(spec)
	if err != nil {
		t.Fatalf("ParseWindow(%q): %v", spec, err)
	}
	return w
}

// statusDir builds a temp status directory and a StatusWriter pointed at it
// with a fixed clock, the wiring TestPoolSnapshotState's Loop-driven cases
// and TestPoolPublishReportsWriteFailureDiagnostic both need.
func statusDir(t *testing.T) (string, *StatusWriter) {
	t.Helper()
	dir := t.TempDir()
	sw := NewStatusWriter(dir, func() time.Time { return time.Unix(0, 0).UTC() })
	return dir, sw
}

// readStatusErr is readStatus's non-fatal twin, for hooks that run on a
// pool slot goroutine rather than the test's own: t.Fatalf there runs
// runtime.Goexit on the slot goroutine, not the test, so the failure needs
// to travel back as a plain error and get asserted on the test goroutine.
func readStatusErr(dir string) (*Status, error) {
	report, err := ReadStatus(dir)
	if err != nil {
		return nil, fmt.Errorf("ReadStatus(%s): %w", dir, err)
	}
	if report.Status == nil {
		return nil, fmt.Errorf("ReadStatus(%s): status is nil", dir)
	}
	return report.Status, nil
}

// readStatus reads dir's status file, failing the test on a read error or a
// nil Status — ReadStatus itself treats a missing status file as "no error,
// nil Status" (see its own doc), which no caller here should ever hit.
func readStatus(t *testing.T, dir string) *Status {
	t.Helper()
	st, err := readStatusErr(dir)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return st
}

// openWindowClosingSoon builds an Awake window guaranteed open at now but
// due to close in closesIn, the mirror image of shutWindow: it lets a test
// start a child while the window is genuinely open and only later advance
// the clock past the close, so "a child already running when the window
// closes is never touched" (Config.Awake's own doc) has something real to
// prove against.
func openWindowClosingSoon(t *testing.T, now time.Time, closesIn time.Duration) *Window {
	t.Helper()
	return windowFromOffsets(t, now, -time.Hour, closesIn)
}

// TestPoolSnapshotState pins snapshot's State precedence (issue #3545):
// halted outranks everything, working outranks asleep (a child started
// before the window closed is still running), then jammed vs waiting is
// distinguished by whether any gated kind is jammedNow, and checking is the
// default when nothing is gated and nothing is running. Five of the six
// cases drive Loop and read the live status file at a deterministic instant
// (a hook or a parked clock), per issue #3620 slice 7a; the "asleep from a
// shut window" case builds a pool directly instead, because it pins the
// instant before any Loop hook can reach — see that case's own comment.
func TestPoolSnapshotState(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, dir string, sw *StatusWriter) *Status
		want State
	}{
		{
			// Loop's up-front publish (loop.go) fires before any slot
			// starts a child and before any kind has ever backed off; the
			// first ResolveRevision call happens after that publish and
			// before anything else has changed, so reading there catches
			// exactly that instant.
			name: "checking when nothing gated and nothing running",
			run: func(t *testing.T, dir string, sw *StatusWriter) *Status {
				clk := &testClock{}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var once sync.Once
				var st *Status
				r := &scriptedRunner{
					revisions: []string{"rev1"},
					onResolve: func(ctx context.Context, call int) error {
						once.Do(func() {
							// cancel() before the read (see TestAwaitWindowSkipsPublishOnNonTransitionIteration's
							// r.onResolve): a readStatus failure must still
							// unblock Loop rather than Goexit past the cancel.
							cancel()
							st = readStatus(t, dir)
						})
						return nil
					},
				}
				cfg := testConfig(1)
				cfg.Status = sw
				var buf bytes.Buffer
				em := newTestEmitter(&buf)
				Loop(ctx, cfg, r, em, clk)
				return st
			},
			want: StateChecking,
		},
		{
			// A single slot cycling both kinds exit-2s each in turn until
			// pickKind finds every kind gated and calls idleSleep: that is
			// the first (and only) instant clk.Sleep is entered, so the
			// onSleep hook reads exactly there, after the second kind's
			// own emit has already republished with both gated.
			name: "waiting when every kind gated queue-empty",
			run: func(t *testing.T, dir string, sw *StatusWriter) *Status {
				clk := &testClock{}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var once sync.Once
				var st *Status
				clk.onSleep = func() {
					once.Do(func() {
						cancel()
						st = readStatus(t, dir)
					})
				}
				r := &scriptedRunner{
					revisions: []string{"rev1"},
					byKind: map[Kind][]ChildResult{
						KindDispatch: {{Exit: 2}},
						KindResearch: {{Exit: 2}},
					},
				}
				cfg := testConfig(1)
				cfg.Kinds = []Kind{KindDispatch, KindResearch}
				cfg.Status = sw
				var buf bytes.Buffer
				em := newTestEmitter(&buf)
				Loop(ctx, cfg, r, em, clk)
				return st
			},
			want: StateWaiting,
		},
		{
			// Same shape as "waiting" above, but dispatch exits 3
			// (none-dispatchable, and the pool's only slot, so it is a
			// jam) while research exits 2 (queue-empty). A jammed gated
			// kind routes idleSleep through pollSlices instead of a bare
			// Sleep, but pollSlices's first slice is still a clk.Sleep
			// call, so the same onSleep hook still lands on the instant
			// both kinds are gated.
			name: "jammed when every kind gated and one jammed",
			run: func(t *testing.T, dir string, sw *StatusWriter) *Status {
				clk := &testClock{}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var once sync.Once
				var st *Status
				clk.onSleep = func() {
					once.Do(func() {
						cancel()
						st = readStatus(t, dir)
					})
				}
				r := &scriptedRunner{
					revisions: []string{"rev1"},
					byKind: map[Kind][]ChildResult{
						KindDispatch: {{Exit: 3}},
						KindResearch: {{Exit: 2}},
					},
				}
				cfg := testConfig(1)
				cfg.Kinds = []Kind{KindDispatch, KindResearch}
				cfg.Status = sw
				var buf bytes.Buffer
				em := newTestEmitter(&buf)
				Loop(ctx, cfg, r, em, clk)
				return st
			},
			want: StateJammed,
		},
		{
			// The one case that builds a pool directly rather than driving
			// Loop: what it pins is precisely the instant no Loop hook can
			// reach. State must derive from the window itself, not the
			// edge-triggered awakeShut flag, or it would disagree with
			// nextCheck (already naming the reopening) and read checking
			// instead. awaitWindow flips awakeShut inside noteAwakeClose
			// (pool.go) before it ever calls clk.Sleep, so a read from the
			// clock's onSleep hook is already past the flip and would pass
			// against a flag-derived state too. Loop's own up-front publish
			// is before the flip, but nothing the double can hook fires
			// between the two.
			name: "asleep from a shut window even before any slot parks",
			run: func(t *testing.T, _ string, _ *StatusWriter) *Status {
				clk := &testClock{}
				cfg := testConfig(1)
				cfg.Awake = shutWindow(t, clk.Now())
				var buf bytes.Buffer
				p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), clk)
				snap := p.snapshot()
				return &snap
			},
			want: StateAsleep,
		},
		{
			// The window is open when the child starts (so it really did
			// start under it), and onStart — while the child is still
			// "running" from the pool's point of view — advances the
			// clock past the window's close and republishes via OnIssue
			// before reading, so the read genuinely observes a running
			// child under a since-shut window, not one that merely never
			// closed.
			name: "working outranks asleep",
			run: func(t *testing.T, dir string, sw *StatusWriter) *Status {
				clk := &testClock{now: time.Unix(0, 0).UTC()}
				var st *Status
				var readErr error
				r := &scriptedRunner{
					revisions: []string{"rev1"},
					results:   []ChildResult{{Exit: 5}}, // host-tainted: halts promptly after the read
					onStart: func(ctx context.Context, req ChildRequest) error {
						clk.advanceBy(10 * time.Minute)
						req.OnIssue("x")
						// onStart runs on a pool slot goroutine, not this
						// test's own: a fatal read here would Goexit that
						// goroutine mid-RunChild, and Loop's wg.Wait (called
						// below, on this test's goroutine) would then hang
						// rather than surface the failure. Capture the error
						// and assert it once Loop has returned instead.
						st, readErr = readStatusErr(dir)
						return nil
					},
				}
				cfg := testConfig(1)
				cfg.Awake = openWindowClosingSoon(t, clk.Now(), 5*time.Minute)
				cfg.Status = sw
				var buf bytes.Buffer
				em := newTestEmitter(&buf)
				Loop(context.Background(), cfg, r, em, clk)
				if readErr != nil {
					t.Fatalf("%v", readErr)
				}
				return st
			},
			want: StateWorking,
		},
		{
			name: "halted outranks everything",
			run: func(t *testing.T, dir string, sw *StatusWriter) *Status {
				clk := &testClock{}
				r := &scriptedRunner{
					revisions: []string{"rev1"},
					results:   []ChildResult{{Exit: 5}}, // host-tainted halt
				}
				cfg := testConfig(1)
				cfg.Status = sw
				var buf bytes.Buffer
				em := newTestEmitter(&buf)
				Loop(context.Background(), cfg, r, em, clk)
				return readStatus(t, dir)
			},
			want: StateHalted,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, sw := statusDir(t)
			st := tc.run(t, dir, sw)
			if st == nil {
				t.Fatalf("run never captured a status")
			}
			if st.State != tc.want {
				t.Fatalf("state = %q, want %q", st.State, tc.want)
			}
			if tc.want == StateHalted && !strings.Contains(st.Reason, "host-tainted") {
				t.Fatalf("reason = %q, want it to name host-tainted", st.Reason)
			}
		})
	}
}

// TestPoolSnapshotSlots pins that snapshot names each slot's kind, revision
// and in-flight issues from occupancy, leaving an unoccupied slot bare.
// Driven through Loop with a 2-slot pool: slot 0 is the pre-assigned
// initial baton holder (pool.go's leadSlot), so it alone resolves and
// occupies on the first round while slot 1 parks in awaitBaton. Announcing
// slot 0's issue via req.OnIssue passes the baton, so slot 1's own
// ResolveRevision call (call index 2 — onResolve is keyed by call, not
// slot, since ResolveRevision carries no slot) is parked forever on
// ctx.Done(), which keeps slot 1 from ever reaching occupy regardless of
// scheduling.
func TestPoolSnapshotSlots(t *testing.T) {
	dir, sw := statusDir(t)
	clk := &testClock{}
	var st *Status
	var readErr error
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		onResolve: func(ctx context.Context, call int) error {
			if call >= 2 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		onStart: func(ctx context.Context, req ChildRequest) error {
			req.OnIssue("7")
			st, readErr = readStatusErr(dir)
			return nil
		},
	}
	r.holdSlots(2)
	cfg := testConfig(2)
	cfg.Status = sw
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	done := make(chan string, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("started slot = %d, want 0 (the initial baton holder)", got)
	}
	r.releaseSlot(t, 0, ChildResult{Exit: 5}) // host-tainted: halts the pool, which cancels the ctx onResolve is parked on
	<-done

	if readErr != nil {
		t.Fatalf("%v", readErr)
	}
	if st == nil {
		t.Fatalf("onStart never captured a status")
	}
	if len(st.Slots) != 2 {
		t.Fatalf("slots = %d, want 2", len(st.Slots))
	}
	if !st.Slots[0].Busy || st.Slots[0].Kind != KindDispatch || st.Slots[0].Revision != "rev1" {
		t.Fatalf("slot 0 = %+v, want busy dispatch@rev1", st.Slots[0])
	}
	if !reflect.DeepEqual(st.Slots[0].Issues, []string{"7"}) {
		t.Fatalf("slot 0 issues = %v, want [7]", st.Slots[0].Issues)
	}
	if st.Slots[1].Busy {
		t.Fatalf("slot 1 = %+v, want unoccupied", st.Slots[1])
	}
}

// TestPoolSnapshotCopiesIssuesSlice pins that a snapshot's Issues slice is a
// copy, not an alias onto the slot's live state: mutating the pool after
// taking the snapshot must never change what was already handed out. Stays
// on direct construction: aliasing is an in-memory property of the Go
// slice header, and a status file round-trips through JSON, so ReadStatus
// can never observe it either way — there is no instant for Loop to reach.
func TestPoolSnapshotCopiesIssuesSlice(t *testing.T) {
	clk := &testClock{}
	cfg := testConfig(1)
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, em, clk)

	p.occupy(0, KindDispatch, "rev1")
	p.noteIssue(0, "42")

	snap := p.snapshot()
	if !reflect.DeepEqual(snap.Slots[0].Issues, []string{"42"}) {
		t.Fatalf("issues = %v, want [42]", snap.Slots[0].Issues)
	}

	p.noteIssue(0, "99") // mutate the pool's copy after snapshotting

	if !reflect.DeepEqual(snap.Slots[0].Issues, []string{"42"}) {
		t.Fatalf("snapshot issues changed after mutating pool state: got %v, want [42]", snap.Slots[0].Issues)
	}
}

// TestPoolSnapshotCopiesKindsSlice pins that snapshot's Kinds is a copy of
// cfg.Kinds, not an alias: mutating the caller's slice after snapshot must
// never change what was already handed out (mirrors
// TestPoolSnapshotCopiesIssuesSlice's contract for the Issues slice above).
// Stays on direct construction for the same reason: this is an in-memory
// aliasing property JSON round-tripping through ReadStatus cannot express.
func TestPoolSnapshotCopiesKindsSlice(t *testing.T) {
	clk := &testClock{}
	cfg := testConfig(1)
	cfg.Kinds = []Kind{KindDispatch, KindResearch}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, em, clk)

	snap := p.snapshot()
	if !reflect.DeepEqual(snap.Kinds, []Kind{KindDispatch, KindResearch}) {
		t.Fatalf("kinds = %v, want [dispatch research]", snap.Kinds)
	}

	cfg.Kinds[0] = "mutated" // mutate the caller's slice after snapshotting

	if !reflect.DeepEqual(snap.Kinds, []Kind{KindDispatch, KindResearch}) {
		t.Fatalf("snapshot kinds changed after mutating caller's slice: got %v, want [dispatch research]", snap.Kinds)
	}
}

// TestPoolSnapshotChecksOrderAndNextCheck pins that Checks is built in
// cfg.Kinds order (not the p.kinds map's undefined order) and carries a
// nextCheck only for a gated kind. Driven through Loop with
// ResearchReservation: 1 so the sole slot prefers research first: its
// first iteration gates research (exit 2) and continues, its second picks
// dispatch — the onStart hook reads the status file there, while dispatch
// is occupying the slot and research is already gated from the round
// before.
func TestPoolSnapshotChecksOrderAndNextCheck(t *testing.T) {
	dir, sw := statusDir(t)
	clk := &testClock{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	var st *Status
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindResearch: {{Exit: 2}},
		},
		results: []ChildResult{{Exit: 0}},
		onStart: func(ctx context.Context, req ChildRequest) error {
			if req.Kind == KindDispatch {
				once.Do(func() {
					cancel()
					st = readStatus(t, dir)
				})
			}
			return nil
		},
	}
	cfg := testConfig(1)
	cfg.Kinds = []Kind{KindResearch, KindDispatch}
	cfg.ResearchReservation = 1
	cfg.Status = sw
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	Loop(ctx, cfg, r, em, clk)

	if st == nil {
		t.Fatalf("onStart never captured a status")
	}
	if len(st.Checks) != 2 || st.Checks[0].Kind != KindResearch || st.Checks[1].Kind != KindDispatch {
		t.Fatalf("checks = %+v, want research then dispatch", st.Checks)
	}
	if st.Checks[0].NextCheck == "" {
		t.Fatalf("research nextCheck empty, want gated (non-empty)")
	}
	if st.Checks[1].NextCheck != "" {
		t.Fatalf("dispatch nextCheck = %q, want empty (runnable now)", st.Checks[1].NextCheck)
	}
}

// TestPoolSnapshotElapsedGateReadsAsCheckingNotWaiting pins the fix for the
// finding at backoff.go:98: once a gated kind's deadline has passed, a
// snapshot taken before anything re-runs pickKind must report the kind as
// runnable now (empty NextCheck) and the pool as StateChecking, never a
// stale past NextCheck under StateWaiting — the seven-hours-stale
// awake_open publish from the finding's own reproduction. Stays on direct
// construction: nothing republishes the status file merely because a
// deadline elapsed with no event, so the window between the gate's own
// deadline passing and the slot's next pickKind call — what this test
// pins — has no Loop-driven publish landing inside it for ReadStatus to
// observe.
func TestPoolSnapshotElapsedGateReadsAsCheckingNotWaiting(t *testing.T) {
	clk := &testClock{}
	cfg := testConfig(1)
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, em, clk)

	wait := p.kinds[KindDispatch].markNoWork(clk.Now(), false)
	clk.advanceBy(wait + time.Millisecond) // past the deadline

	snap := p.snapshot()
	if snap.Checks[0].NextCheck != "" {
		t.Fatalf("nextCheck = %q, want empty: the gate elapsed before this snapshot", snap.Checks[0].NextCheck)
	}
	if snap.State != StateChecking {
		t.Fatalf("state = %q, want %q (elapsed gate reads as runnable, not waiting)", snap.State, StateChecking)
	}
}

// TestPoolExit3DuringSiblingOccupancyStaysJammedAfterSiblingClears pins the
// reviewer's rejected fix that would key jammedNow off runSlot's own
// poolJammed predicate instead of markNoWork's outcome flag: it drives the
// real runSlot call site rather than calling markNoWork directly, so a future regression back
// to the rejected poolJammed predicate would fail this test. Slot 1 has a
// research child running when slot 0's dispatch check returns exit 3 —
// siblingsOccupied is true at that exact instant — and the dispatch kind's
// jammed flag must still flip true, unaffected by occupancy. It must also
// stay true once the sibling clears, so the pool-level state reads jammed,
// never waiting: an open, none-dispatchable queue must never be reported as
// "every queue empty". Stays off Loop for the same reason it drives runSlot
// by hand rather than a full pool: it needs slot 1's occupancy faked in
// directly (p.occupy) at an exact instant relative to slot 0's own exit-3,
// with no wait for a real research child to actually be scheduled there —
// an interleaving no Loop hook pins down deterministically across two live
// slots.
func TestPoolExit3DuringSiblingOccupancyStaysJammedAfterSiblingClears(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	// clk: Sleep must remain provably parked (see TestPoolExit3WithPoolIdleIsAJam)
	// so slot 0 parking in its idle wait is a real synchronization point.
	clk := &testClock{sleepSignal: make(chan struct{}, slots)}
	clk.park()
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })
	cfg := testConfig(slots)
	cfg.Kinds = []Kind{KindDispatch, KindResearch}
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	// Research starts already gated (idle, not jammed): with two kinds
	// configured, an ungated research would have slot 0's own loop retry
	// it the moment dispatch backs off, rather than parking in idleSleep —
	// this test's synchronization point.
	p.kinds[KindResearch].markNoWork(clk.Now(), false)
	p.occupy(1, KindResearch, "rev1")

	done := make(chan struct{})
	go func() {
		defer close(done)
		runSlot(pctx, 0, cfg, p)
	}()

	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("started slot = %d, want 0", got)
	}
	r.releaseSlot(t, 0, ChildResult{Exit: 3})
	// Slot 0 parking in its idle wait is the synchronization point:
	// markNoWork has run by then, so the flag read below is settled state
	// rather than a sample taken mid-iteration.
	<-clk.sleepSignal

	if !p.kinds[KindDispatch].jammedNow() {
		t.Fatalf("dispatch jammedNow = false with a sibling occupied, want true (exit 3 alone gates this)")
	}

	// A busy slot outranks jammed/waiting in snapshot's precedence, so the
	// sibling has to clear before the state under test is observable.
	p.unoccupy(1)

	if !p.kinds[KindDispatch].jammedNow() {
		t.Fatalf("dispatch jammedNow = false after the sibling cleared, want still true")
	}
	if s := p.snapshot().State; s != StateJammed {
		t.Fatalf("state = %q, want %q (never %q)", s, StateJammed, StateWaiting)
	}

	p.cancel()
	<-done
}

// TestPoolSnapshotChecksCarryPerKindJammed pins the standing finding on
// KindCheck.Jammed: which kind is jammed must be recoverable from the
// published Checks, not just the pool-level State. Driven through Loop the
// same way as TestPoolSnapshotState's "jammed when every kind gated and
// one jammed" case: a single slot cycles both kinds to exit-3/exit-2 until
// pickKind finds everything gated and parks in idleSleep, the one instant
// clk.Sleep is entered, so onSleep reads the status file exactly there.
func TestPoolSnapshotChecksCarryPerKindJammed(t *testing.T) {
	dir, sw := statusDir(t)
	clk := &testClock{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	var st *Status
	clk.onSleep = func() {
		once.Do(func() {
			cancel()
			st = readStatus(t, dir)
		})
	}
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 3}},
			KindResearch: {{Exit: 2}},
		},
	}
	cfg := testConfig(1)
	cfg.Kinds = []Kind{KindDispatch, KindResearch}
	cfg.Status = sw
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	Loop(ctx, cfg, r, em, clk)

	if st == nil {
		t.Fatalf("onSleep never captured a status")
	}
	var dispatch, research KindCheck
	for _, kc := range st.Checks {
		switch kc.Kind {
		case KindDispatch:
			dispatch = kc
		case KindResearch:
			research = kc
		}
	}
	if !dispatch.Jammed {
		t.Fatalf("dispatch Jammed = false, want true")
	}
	if research.Jammed {
		t.Fatalf("research Jammed = true, want false")
	}
}

// TestPoolPublishReportsWriteFailureDiagnostic pins the review finding on
// pool.publish: a status write that fails must report the
// "daemon: status file write failed" diagnostic to emitErrW rather than
// silently vanishing, and must never panic or halt the daemon — mirrors
// TestEmitterEncodeFailureReportsDiagnosticAndDoesNotPanic's precedent for
// the sibling failure path. Driven through Loop's own up-front publish
// (loop.go, before any child runs) rather than a hand-called p.publish(),
// per issue #3620 slice 7a. The write is made to fail by pointing
// NewStatusWriter at a directory that does not exist, so os.CreateTemp
// fails; chmod 0500 is avoided because the Nix check sandbox may run as
// root, where mode bits do not deny writes.
func TestPoolPublishReportsWriteFailureDiagnostic(t *testing.T) {
	var errBuf bytes.Buffer
	orig := emitErrW
	emitErrW = &errBuf
	t.Cleanup(func() { emitErrW = orig })

	clk := &testClock{}
	cfg := testConfig(1)
	cfg.Status = NewStatusWriter(filepath.Join(t.TempDir(), "no-such-dir"), time.Now)
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}} // host-tainted: halts promptly
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), cfg, r, em, clk) // must not panic despite the write failure

	if got := errBuf.String(); !strings.Contains(got, "daemon: status file write failed") {
		t.Fatalf("emitErrW = %q, want it to contain %q", got, "daemon: status file write failed")
	}
}

// TestPoolSnapshotNextCheckFloorsOnAwakeWindow pins the review finding on
// pool.snapshot: with the Awake window shut no slot starts a child until
// awaitWindow returns, so every kind's nextCheck must be the later of its
// own backoff deadline and the window's reopening — never empty, which
// means "runnable now". Stays on direct construction: this is snapshot's
// own arithmetic over hand-set state (an explicit awakeShut, a chosen
// backoff floor, per-row gated kinds), including a row that asserts state
// reads asleep even before awakeShut has ever flipped — a combination
// Loop's own edge-triggered noteAwakeClose never produces on its own, so
// there is no instant to drive it to.
func TestPoolSnapshotNextCheckFloorsOnAwakeWindow(t *testing.T) {
	const (
		reopen   = "2026-01-01T22:00:00Z"
		farLater = "2026-01-03T08:00:00Z"
	)
	longFloor := 48 * time.Hour

	tests := []struct {
		name      string
		window    string
		awakeShut bool
		floor     time.Duration
		gate      []Kind
		want      []string
		wantState State
	}{
		{
			name:      "shut window floors every ungated kind",
			window:    "22:00-06:00 UTC",
			awakeShut: true,
			want:      []string{reopen, reopen},
			wantState: StateAsleep,
		},
		{
			// The review fix this test pins: a pool built outside its
			// Awake window and snapshotted before any slot's awaitWindow
			// has parked never flips awakeShut, so state must derive from
			// the window itself (same as nextCheck already does) rather
			// than reading checking while nextCheck names a reopening ten
			// hours out.
			name:      "shut window reports asleep even before awakeShut flips",
			window:    "22:00-06:00 UTC",
			awakeShut: false,
			want:      []string{reopen, reopen},
			wantState: StateAsleep,
		},
		{
			name:      "shut window outranks an earlier backoff deadline",
			window:    "22:00-06:00 UTC",
			awakeShut: true,
			gate:      []Kind{KindDispatch},
			want:      []string{reopen, reopen},
			wantState: StateAsleep,
		},
		{
			name:      "a backoff deadline past the reopening wins",
			window:    "22:00-06:00 UTC",
			awakeShut: true,
			floor:     longFloor,
			gate:      []Kind{KindDispatch},
			want:      []string{farLater, reopen},
			wantState: StateAsleep,
		},
		{
			name:      "open window leaves each kind's own deadline",
			window:    "06:00-22:00 UTC",
			floor:     longFloor,
			gate:      []Kind{KindDispatch},
			want:      []string{farLater, ""},
			wantState: StateChecking,
		},
		{
			name:      "nil always-awake window leaves each kind's own deadline",
			window:    "",
			floor:     longFloor,
			gate:      []Kind{KindDispatch},
			want:      []string{farLater, ""},
			wantState: StateChecking,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, err := ParseWindow(tc.window)
			if err != nil {
				t.Fatalf("ParseWindow(%q) err = %v, want nil", tc.window, err)
			}
			clk := &testClock{now: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)}
			cfg := testConfig(1)
			cfg.Kinds = []Kind{KindDispatch, KindResearch}
			cfg.Awake = w
			if tc.floor > 0 {
				cfg.IdleFloor = tc.floor
				cfg.IdleCap = tc.floor
			}
			var buf bytes.Buffer
			p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), clk)
			p.awakeShut = tc.awakeShut
			for _, k := range tc.gate {
				p.kinds[k].markNoWork(clk.Now(), false)
			}

			snap := p.snapshot()

			if len(snap.Checks) != len(tc.want) {
				t.Fatalf("checks = %+v, want %d entries", snap.Checks, len(tc.want))
			}
			for i, want := range tc.want {
				if snap.Checks[i].NextCheck != want {
					t.Fatalf("checks[%d] (%s) nextCheck = %q, want %q", i, snap.Checks[i].Kind, snap.Checks[i].NextCheck, want)
				}
			}
			if snap.State != tc.wantState {
				t.Fatalf("state = %q, want %q", snap.State, tc.wantState)
			}
		})
	}
}
