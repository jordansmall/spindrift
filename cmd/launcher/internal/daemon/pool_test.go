package daemon

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

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
		done <- Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval, Slots: slots}, r, em, clk)
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

	reason := Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval, Slots: 1}, r, em, clk)

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

			reason := Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval, Slots: slots}, r, em, clk)

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
