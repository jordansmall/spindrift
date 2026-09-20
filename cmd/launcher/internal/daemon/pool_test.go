package daemon

import (
	"bytes"
	"context"
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

// blockingRunner is the pool concurrency tests' Runner: RunChild for a
// given slot blocks until the test sends that slot's result on its release
// channel, so a test can hold several slots' children open at once and
// observe how many are truly in flight together, then release them and
// check every one that started also finished. It is keyed by req.Slot
// (unlike loop_test.go's fakeRunner, which scripts one shared call
// sequence): several slots call RunChild concurrently here, so a shared
// call-index counter would hand results to whichever slot happened to call
// next rather than the slot the test meant.
type blockingRunner struct {
	revision string

	mu       sync.Mutex
	inFlight map[int]bool
	peak     int

	started chan int
	release map[int]chan ChildResult
}

func newBlockingRunner(revision string, slots int) *blockingRunner {
	release := make(map[int]chan ChildResult, slots)
	for s := 0; s < slots; s++ {
		release[s] = make(chan ChildResult, 1)
	}
	return &blockingRunner{
		revision: revision,
		inFlight: make(map[int]bool),
		started:  make(chan int, slots),
		release:  release,
	}
}

func (r *blockingRunner) ResolveRevision(ctx context.Context) (string, error) {
	return r.revision, nil
}

// SelfPath is unused by every test using blockingRunner (Config.SelfProgram
// stays empty), but must exist to satisfy Runner.
func (r *blockingRunner) SelfPath(ctx context.Context, revision string) (string, error) {
	return "", nil
}

func (r *blockingRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	r.mu.Lock()
	r.inFlight[req.Slot] = true
	if n := len(r.inFlight); n > r.peak {
		r.peak = n
	}
	r.mu.Unlock()

	r.started <- req.Slot
	result := <-r.release[req.Slot]

	r.mu.Lock()
	delete(r.inFlight, req.Slot)
	r.mu.Unlock()

	return result, nil
}

func (r *blockingRunner) peakConcurrency() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

// TestPoolRunsSlotsConcurrentlyAndNeverAbandonsAStartedChild pins the two
// guarantees a pool of slots exists for: with Slots: N, N children really
// do run at once (not N sequential calls that merely look concurrent from
// the outside), and once every slot's child has started, halting one slot
// still lets every sibling's already-started child return and emit its own
// child_finish rather than being abandoned mid-run.
func TestPoolRunsSlotsConcurrentlyAndNeverAbandonsAStartedChild(t *testing.T) {
	const slots = 3
	r := newBlockingRunner("rev1", slots)
	clk := &fakeClock{}
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
		seen[<-r.started] = true
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
		r.release[s] <- ChildResult{Exit: 7}
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
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk)

	if len(r.runCalls) != 2 {
		t.Fatalf("run calls = %d, want 2", len(r.runCalls))
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
	r := newBlockingRunner("rev1", slots)
	clk := &fakeClock{}
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
		seen[<-r.started] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}

	// Slot 0 fails (1st pool-wide failure, below threshold): it backs off
	// and restarts. Reading r.started again is only possible once that
	// restart's RunChild call is in flight, so it also proves the breaker
	// recorded slot 0's failure before slot 1's is sent below.
	r.release[0] <- ChildResult{Exit: 1}
	if got := <-r.started; got != 0 {
		t.Fatalf("restart after backoff = slot %d, want slot 0", got)
	}

	// Slot 1 fails (2nd pool-wide failure, still below threshold): same
	// backoff-and-restart.
	r.release[1] <- ChildResult{Exit: 1}
	if got := <-r.started; got != 1 {
		t.Fatalf("restart after backoff = slot %d, want slot 1", got)
	}

	// Slot 2 fails (3rd pool-wide failure, reaches threshold): the breaker
	// trips instead of slot 2 backing off.
	r.release[2] <- ChildResult{Exit: 1}

	// Wait for the pool's own halt event before releasing slot 0/1's
	// still-in-flight children: the pool's mutex gives every later Lock
	// (including slot 0/1's own stopOnCancel check) a happens-after view
	// of the halt, so once this line is observed they are guaranteed to
	// notice the halt and return rather than starting a fourth call.
	nw.waitForLine(t, "\"event\":\"halt\"")

	// Slot 0 and 1's restarted children are still in flight (the pool's
	// never-kill-a-started-child invariant), so they must be released for
	// Loop to return at all.
	r.release[0] <- ChildResult{Exit: 0}
	r.release[1] <- ChildResult{Exit: 0}

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

// barrierFailRunner is TestPoolBreakerTripsAtThresholdConcurrently's
// Runner: the first `slots` RunChild calls all park on one channel and
// return together the instant the last of them arrives, so every slot
// hits backoffOrHalt at (as near as the scheduler allows) the same
// instant — the genuinely concurrent crossing the serialized test above
// cannot reach. Any later call (a non-crossing slot racing back around
// before it notices the halt) finds the channel already closed and
// returns immediately, so the test never needs to script a release for
// it.
type barrierFailRunner struct {
	revision string
	slots    int

	started chan int

	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func newBarrierFailRunner(revision string, slots int) *barrierFailRunner {
	return &barrierFailRunner{revision: revision, slots: slots, started: make(chan int, slots*8), release: make(chan struct{})}
}

func (r *barrierFailRunner) ResolveRevision(ctx context.Context) (string, error) {
	return r.revision, nil
}

// SelfPath is unused by every test using barrierFailRunner (Config.SelfProgram
// stays empty), but must exist to satisfy Runner.
func (r *barrierFailRunner) SelfPath(ctx context.Context, revision string) (string, error) {
	return "", nil
}

func (r *barrierFailRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	r.started <- req.Slot
	r.mu.Lock()
	r.arrived++
	if r.arrived == r.slots {
		close(r.release)
	}
	r.mu.Unlock()
	<-r.release
	return ChildResult{Exit: 1}, nil
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
	r := newBarrierFailRunner("rev1", slots)
	clk := &fakeClock{}
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
		seen[<-r.started] = true
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

// gateClock is the Clock for tests that need a slot to remain provably
// parked in its idle wait, rather than fakeClock's instant advance racing
// straight back into a second RunChild call. Sleep blocks on ctx.Done()
// (recording the wait first, like fakeClock does) so "this slot is now
// asleep" is a real synchronization point a test can wait on via
// sleeping, and the only way any Sleep call ever returns is the pool
// itself being cancelled — which is exactly how these tests end the test,
// via the top-level ctx, so no slot ever loops back into a RunChild call
// this test never scripts a release for.
type gateClock struct {
	mu       sync.Mutex
	waits    []time.Duration
	sleeping chan struct{}
}

func newGateClock() *gateClock {
	return &gateClock{sleeping: make(chan struct{}, 8)}
}

func (c *gateClock) Sleep(ctx context.Context, d time.Duration) {
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.mu.Unlock()
	select {
	case c.sleeping <- struct{}{}:
	default:
	}
	<-ctx.Done()
}

func (c *gateClock) Now() time.Time {
	return time.Unix(0, 0).UTC()
}

// TestPoolExit3WithSiblingRunningReportsIdleNotJam pins the routine half of
// the occupancy axis: a slot's exit 3 while a sibling is genuinely still
// running (blocked mid-RunChild, not merely between calls) must report
// idle, not jam — the issues this slot found none-dispatchable were
// claimed or overlap-deferred against that very sibling.
func TestPoolExit3WithSiblingRunningReportsIdleNotJam(t *testing.T) {
	const slots = 2
	r := newBlockingRunner("rev1", slots)
	clk := &fakeClock{}
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
		seen[<-r.started] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}

	// Slot 1 exits none-dispatchable while slot 0 is still blocked mid-
	// RunChild (genuinely occupied, not just between calls) — this must
	// report idle.
	r.release[1] <- ChildResult{Exit: 3}
	nw.waitForLine(t, "\"event\":\"idle\"")

	// Slot 1 now loops back and restarts (still nothing wrong with the
	// pool); wait for its restart so the later halt below has a real
	// in-flight child to release rather than racing its own start.
	if got := <-r.started; got != 1 {
		t.Fatalf("restart after idle = slot %d, want slot 1", got)
	}

	// End the test: halt via slot 0's still-in-flight first child.
	r.release[0] <- ChildResult{Exit: 7}
	nw.waitForLine(t, "\"event\":\"halt\"")

	// Slot 1's restarted child is still in flight; release it so Loop can
	// return.
	r.release[1] <- ChildResult{Exit: 0}

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
	r := newBlockingRunner("rev1", slots)
	clk := newGateClock()
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
		seen[<-r.started] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}

	// Slot 1 exits queue-empty and parks in its idle wait — gateClock's
	// Sleep never returns on its own, so slot 1 is now genuinely,
	// provably not occupied and staying that way.
	r.release[1] <- ChildResult{Exit: 2}
	<-clk.sleeping

	// Slot 0 exits none-dispatchable with slot 1 parked and nothing else
	// running: this must report jam, carrying slot 0.
	r.release[0] <- ChildResult{Exit: 3}
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
// idleWait's slicing loop: when IdleFloor does not evenly divide the
// requested wait, the final slice must shrink to what's left rather than
// overshoot it. That is a legal config — IdleCap need not be a multiple of
// IdleFloor — but the shipped 5m/30m pair always divides evenly, so nothing
// else in this package reaches the clamp.
func TestIdleWaitLastSliceClampsToRemaining(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.IdleFloor = 3 * time.Millisecond
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	p.idleWait(pctx, 0, 10*time.Millisecond, "rev1")

	want := []time.Duration{3 * time.Millisecond, 3 * time.Millisecond, 3 * time.Millisecond, time.Millisecond}
	if len(clk.waits) != len(want) {
		t.Fatalf("waits = %v, want %v", clk.waits, want)
	}
	for i := range want {
		if clk.waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", clk.waits, want)
		}
	}
}

// stepClock is a Clock for testing concurrent parking: unlike fakeClock,
// whose Sleep additively advances a virtual now (fine for one goroutine at
// a time, but unsound once several Sleep calls race the same shared clock
// -- their durations stack instead of overlapping), stepClock only changes
// now when the test calls advance, which also releases every Sleep blocked
// so far at once. That models "N slots are all asleep waiting for the same
// instant" exactly, with no additive artifact.
type stepClock struct {
	mu       sync.Mutex
	now      time.Time
	waits    []time.Duration
	barrier  chan struct{}
	sleeping chan struct{}
}

func newStepClock(now time.Time, slots int) *stepClock {
	return &stepClock{now: now, barrier: make(chan struct{}), sleeping: make(chan struct{}, slots)}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) Sleep(ctx context.Context, d time.Duration) {
	c.mu.Lock()
	c.waits = append(c.waits, d)
	barrier := c.barrier
	c.mu.Unlock()

	c.sleeping <- struct{}{}
	select {
	case <-barrier:
	case <-ctx.Done():
	}
}

// advance sets now and releases every Sleep call parked so far, as if all
// of them woke at once.
func (c *stepClock) advance(now time.Time) {
	c.mu.Lock()
	c.now = now
	old := c.barrier
	c.barrier = make(chan struct{})
	c.mu.Unlock()
	close(old)
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
	r := &fakeRunner{revisions: []string{"rev1"}}
	clk := newStepClock(time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC), slots)
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

	for i := 0; i < slots; i++ {
		select {
		case <-clk.sleeping:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for slot %d to park on the closed window", i)
		}
	}
	clk.advance(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)) // window opens, releases all three
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
