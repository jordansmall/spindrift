package daemon

import (
	"bytes"
	"context"
	"fmt"
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
		done <- Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval, FailureBackoff: testFailureBackoff, BreakerThreshold: testBreakerThreshold, BreakerWindow: testBreakerWindow, Slots: slots}, r, em, clk)
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

// TestPoolSlots1ReproducesTodaysBehaviour asserts Slots: 1 drives exactly
// one child at a time — the existing single-slot loop tests already pin
// this behaviour via Config{Slots: 1}, so this test just names the
// invariant explicitly at the pool layer.
func TestPoolSlots1ReproducesTodaysBehaviour(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval, FailureBackoff: testFailureBackoff, BreakerThreshold: testBreakerThreshold, BreakerWindow: testBreakerWindow, Slots: 1}, r, em, clk)

	if len(r.runCalls) != 2 {
		t.Fatalf("run calls = %d, want 2", len(r.runCalls))
	}
	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want it to name host-tainted", reason)
	}
}

// TestLoopRejectsNonPositiveSlots asserts Loop treats a zero or negative
// pool size as a real, halt-shaped config error rather than silently
// running no children — a zero-slot daemon that looks healthy while doing
// nothing is the worst outcome.
func TestLoopRejectsNonPositiveSlots(t *testing.T) {
	for _, slots := range []int{0, -1} {
		t.Run(fmt.Sprintf("slots=%d", slots), func(t *testing.T) {
			r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
			clk := &fakeClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			reason := Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval, FailureBackoff: testFailureBackoff, BreakerThreshold: testBreakerThreshold, BreakerWindow: testBreakerWindow, Slots: slots}, r, em, clk)

			if len(r.runCalls) != 0 {
				t.Fatalf("run calls = %d, want 0: a non-positive slot count must halt before any child runs", len(r.runCalls))
			}
			if !strings.Contains(reason, "config-invalid") {
				t.Errorf("halt reason = %q, want it to name config-invalid", reason)
			}
			events := decodeEvents(t, &buf)
			if names := eventNames(events); len(names) != 1 || names[0] != "halt" {
				t.Fatalf("events = %v, want exactly one halt event", names)
			}
		})
	}
}

// TestLoopRejectsInvalidBreakerConfig mirrors
// TestLoopRejectsNonPositiveSlots for the breaker knobs this slice adds: a
// non-positive threshold or window, or a negative backoff, is a config
// error Loop rejects up front rather than a zero-value default that would
// make the breaker's real threshold invisible.
func TestLoopRejectsInvalidBreakerConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"zero threshold", Config{Kind: KindDispatch, IdleInterval: testIdleInterval, FailureBackoff: testFailureBackoff, BreakerThreshold: 0, BreakerWindow: testBreakerWindow, Slots: 1}},
		{"negative threshold", Config{Kind: KindDispatch, IdleInterval: testIdleInterval, FailureBackoff: testFailureBackoff, BreakerThreshold: -1, BreakerWindow: testBreakerWindow, Slots: 1}},
		{"zero window", Config{Kind: KindDispatch, IdleInterval: testIdleInterval, FailureBackoff: testFailureBackoff, BreakerThreshold: testBreakerThreshold, BreakerWindow: 0, Slots: 1}},
		{"negative window", Config{Kind: KindDispatch, IdleInterval: testIdleInterval, FailureBackoff: testFailureBackoff, BreakerThreshold: testBreakerThreshold, BreakerWindow: -time.Minute, Slots: 1}},
		{"negative backoff", Config{Kind: KindDispatch, IdleInterval: testIdleInterval, FailureBackoff: -time.Millisecond, BreakerThreshold: testBreakerThreshold, BreakerWindow: testBreakerWindow, Slots: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
			clk := &fakeClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			reason := Loop(context.Background(), tc.cfg, r, em, clk)

			if len(r.runCalls) != 0 {
				t.Fatalf("run calls = %d, want 0: an invalid breaker config must halt before any child runs", len(r.runCalls))
			}
			if !strings.Contains(reason, "config-invalid") {
				t.Errorf("halt reason = %q, want it to name config-invalid", reason)
			}
		})
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

	cfg := Config{Kind: KindDispatch, IdleInterval: testIdleInterval, FailureBackoff: time.Millisecond, BreakerThreshold: threshold, BreakerWindow: time.Hour, Slots: slots}

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
