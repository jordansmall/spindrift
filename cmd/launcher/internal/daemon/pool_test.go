package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/report"
)

// notifyWriter is an Emitter sink that also records each write as a line,
// so a test can block until a specific event has actually been recorded
// rather than guessing at goroutine timing. Write must never block: the
// pool emits while holding p.mu, so a stalled sink would stall every slot.
type notifyWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	lines  []string      // every write, append-only
	cursor int           // index of the next line waitForLine has not consumed
	wake   chan struct{} // cap 1; a dropped send is harmless, the waiter rescans
}

func newNotifyWriter() *notifyWriter {
	return &notifyWriter{wake: make(chan struct{}, 1)}
}

func (w *notifyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	w.lines = append(w.lines, string(p))
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return n, err
}

func (w *notifyWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// waitForLineTimeout is a generous bound: a hang in waitForLine means the
// event a test is synchronizing on was never emitted, a real bug, not a
// timing fluke worth retrying.
const waitForLineTimeout = 5 * time.Second

// waitForLine blocks until a line written after the previous match contains
// substr and returns that line, failing the test if none arrives within
// waitForLineTimeout.
func (w *notifyWriter) waitForLine(t *testing.T, substr string) string {
	t.Helper()
	deadline := time.After(waitForLineTimeout)
	for {
		w.mu.Lock()
		for i := w.cursor; i < len(w.lines); i++ {
			if line := w.lines[i]; strings.Contains(line, substr) {
				w.cursor = i + 1
				w.mu.Unlock()
				return line
			}
		}
		w.cursor = len(w.lines)
		w.mu.Unlock()
		select {
		case <-w.wake:
		case <-deadline:
			t.Fatalf("waitForLine: no line containing %q within %v", substr, waitForLineTimeout)
		}
	}
}

// TestNotifyWriterWriteNeverBlocks pins that Write cannot stall on an
// unread backlog, and that waitForLine still finds successive matches in
// order afterward.
func TestNotifyWriterWriteNeverBlocks(t *testing.T) {
	w := newNotifyWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			fmt.Fprintf(w, "noise %d\n", i)
			if i == 400 || i == 800 {
				fmt.Fprintf(w, "baton_hold %d\n", i)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("notifyWriter.Write blocked with no reader")
	}
	w.waitForLine(t, "baton_hold")
	if got := w.waitForLine(t, "baton_hold"); got != "baton_hold 800\n" {
		t.Fatalf("second waitForLine matched %q, want the second baton_hold", got)
	}
	if got := w.String(); !strings.Contains(got, "noise 999") {
		t.Fatalf("buf lost lines: %q", got)
	}
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

	done := make(chan Halt, 1)
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

	// Every slot halts on release (exit 5 == host-tainted). Whichever
	// slot's RunChild returns first wins the halt race; the others must
	// still be allowed to finish rather than being cut off.
	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 5})
	}

	reason := (<-done).String()
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
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

	reason := Loop(context.Background(), testConfig(1), r, em, clk).String()

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
	em := newTestEmitter(nw)

	cfg := testConfig(slots)
	cfg.FailureBackoff = time.Millisecond
	cfg.BreakerThreshold = threshold
	cfg.BreakerWindow = time.Hour

	done := make(chan Halt, 1)
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
	// (including slot 0/1's own haltIfStopping check) a happens-after view
	// of the halt, so once this line is observed they are guaranteed to
	// notice the halt and return rather than starting a fourth call.
	nw.waitForLine(t, "\"event\":\"halt\"")

	// Slot 0 and 1's restarted children are still in flight (the pool's
	// never-kill-a-started-child invariant), so they must be released for
	// Loop to return at all.
	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	r.releaseSlot(t, 1, ChildResult{Exit: 0})

	reason := (<-done).String()
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
			if req.OnRecord != nil {
				req.OnRecord(Record{Event: report.EventBox, Key: dispatchkey.Issue(fmt.Sprintf("issue-%d", req.Slot))})
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

	done := make(chan Halt, 1)
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

	var h Halt
	select {
	case h = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Loop did not return within 10s of the concurrent release")
	}
	reason := h.String()
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
	em := newTestEmitter(nw)
	cfg := testConfig(slots)

	done := make(chan Halt, 1)
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
	r.releaseSlot(t, 0, ChildResult{Exit: 5})
	nw.waitForLine(t, "\"event\":\"halt\"")

	// Slot 1's restarted child is still in flight; release it so Loop can
	// return.
	r.releaseSlot(t, 1, ChildResult{Exit: 0})

	reason := (<-done).String()
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	for _, ev := range events {
		if ev.Event == "jam" {
			t.Fatalf("events = %v, want no jam event: slot 0 was genuinely running when slot 1 exited none-dispatchable", eventNames(events))
		}
	}
}

// TestPoolExit3SiblingPhasesNotEngaged pins which sibling phases
// do not suppress a jam: a sibling PhaseRunning a butler child never counts
// as engaged, for any reporting kind, and a resolving sibling holds no claim
// yet (issue #3735), so it reads like an idle one, and a backing-off sibling
// holds no claim it could release either (issue #4205). Only a sibling
// running a dispatch/research child counts. The kind's jammed flag is set
// on every row: it records the queue condition, whatever the sibling does.
func TestPoolExit3SiblingPhasesNotEngaged(t *testing.T) {
	tests := []struct {
		name          string
		siblingPhase  Phase
		siblingKind   Kind // only used when siblingPhase == PhaseRunning
		reportingKind Kind
		wantJam       bool
	}{
		{"running butler sibling, dispatch reports", PhaseRunning, KindOf(dispatchkind.Butler), KindOf(dispatchkind.Work), true},
		{"running butler sibling, research reports", PhaseRunning, KindOf(dispatchkind.Butler), KindOf(dispatchkind.Research), true},
		{"running research sibling, dispatch reports", PhaseRunning, KindOf(dispatchkind.Research), KindOf(dispatchkind.Work), false},
		{"resolving sibling (no kind), dispatch reports", PhaseResolving, "", KindOf(dispatchkind.Work), true},
		{"backing-off sibling (no kind), dispatch reports", PhaseBackingOff, "", KindOf(dispatchkind.Work), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := &testClock{}
			nw := newNotifyWriter()
			em := newTestEmitter(nw)
			cfg := testConfig(2)
			cfg.Kinds = []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research), KindOf(dispatchkind.Butler)}
			p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, em, clk)
			defer p.cancel()

			if tt.siblingPhase == PhaseRunning {
				startChildAs(p, 1, tt.siblingKind, "rev1")
			} else {
				p.setPhase(1, tt.siblingPhase)
			}
			p.noteWaitResult(0, tt.reportingKind, "rev1", true, slotFlight{})

			events := decodeEvents(t, bytes.NewBufferString(nw.String()))
			gotJam := countEvents(eventNames(events), "jam") == 1
			if gotJam != tt.wantJam {
				t.Fatalf("events = %v, want jam=%v", eventNames(events), tt.wantJam)
			}
			if !p.st.sched.View(tt.reportingKind, clk.Now()).Jammed {
				t.Fatalf("kind %v jammed = false, want true on every exit 3 regardless of sibling phase", tt.reportingKind)
			}
		})
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
	em := newTestEmitter(nw)
	cfg := testConfig(slots)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Halt, 1)
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
	// return via haltIfStopping without ever starting a third RunChild call
	// this test never scripts a release for.
	cancel()
	reason := (<-done).String()
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

// TestPoolExit3WithSiblingResolvingReportsJam pins that a sibling blocked
// inside ResolveTip does not suppress the jam alarm (issue #3735): it holds
// no claim yet (the claim happens inside the child), so it cannot be what
// left this slot's read none-dispatchable — no different from an idle
// sibling. ResolveTip's single flight also makes sibling resolving windows
// overlap by construction, so counting them would mask jams in lockstep.
//
// Call 1/2 are the two slots' round-1 fetches, racing each other in either
// order: the baton no longer gates ResolveTip (issue #3625's structural
// fix moved acquisition to after the resolve), so both round-1 fetches now
// run concurrently and the hook below treats them identically either way.
// Call 3, the one the hook actually cares about, is still deterministically
// slot 0's second-round fetch: slot 0 is released into its second round
// below, and slot 1's own kind stays gated (noteWaitResult) after its
// exit-3, none-dispatchable result — with clk parked, slot 1's idleSleep
// blocks rather than spinning the virtual clock forward and slipping into
// a genuine, unreleased second RunChild call, so it can never issue a
// competing fetch of its own before the test ends.
func TestPoolExit3WithSiblingResolvingReportsJam(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	r.announceEachSlot()

	resolving := make(chan struct{})
	var closeOnce sync.Once
	r.onResolve = func(ctx context.Context, call int) error {
		if call == 3 {
			closeOnce.Do(func() { close(resolving) })
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	clk := &testClock{}
	clk.park()
	nw := newNotifyWriter()
	em := newTestEmitter(nw)
	cfg := testConfig(slots)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Halt, 1)
	go func() {
		done <- Loop(ctx, cfg, r, em, clk)
	}()

	seen := map[int]bool{}
	for i := 0; i < slots; i++ {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}

	// Slot 0 restarts for its second round and gets stuck on that round's
	// fetch (call 3, hooked above) — resolving, not idle.
	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	<-resolving

	// Slot 1 exits none-dispatchable with slot 0 resolving: this must
	// report jam, not idle.
	r.releaseSlot(t, 1, ChildResult{Exit: 3})
	nw.waitForLine(t, "\"event\":\"jam\"")

	// End the test: cancelling ctx releases slot 0's blocked fetch (the
	// hook itself selects on ctx.Done()) and any wait slot 1 has since
	// parked in, so both slots return via haltIfStopping without either one
	// ever needing a scripted result this test does not provide.
	cancel()
	reason := (<-done).String()
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want it to name context-cancelled", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	for _, ev := range events {
		if ev.Event == "idle" && ev.Slot != nil && *ev.Slot == 1 {
			t.Fatalf("events = %v, want no idle for slot 1: slot 0 was only resolving when slot 1 exited none-dispatchable", eventNames(events))
		}
	}
}

// TestPoolResolvingSlotResetsToIdleOnCancel pins the reset half of phase
// tracking (issue #3623): a slot that returns from inside the resolving
// span (here, ctx cancellation landing mid-fetch) must not go on
// publishing "resolving" in the status file after it has left runSlot
// entirely.
func TestPoolResolvingSlotResetsToIdleOnCancel(t *testing.T) {
	const slots = 2
	dir, sw := statusDir(t)
	r := &scriptedRunner{revisions: []string{"rev1"}}
	resolving := make(chan struct{})
	var closeOnce sync.Once
	r.onResolve = func(ctx context.Context, call int) error {
		closeOnce.Do(func() { close(resolving) })
		<-ctx.Done()
		return ctx.Err()
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	cfg := testConfig(slots)
	cfg.Status = sw

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Halt, 1)
	go func() {
		done <- Loop(ctx, cfg, r, em, clk)
	}()

	<-resolving
	cancel()
	reason := (<-done).String()
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want it to name context-cancelled", reason)
	}

	st := readStatus(t, dir)
	if len(st.Slots) != slots {
		t.Fatalf("slots = %d, want %d", len(st.Slots), slots)
	}
	if st.Slots[0].Phase != PhaseIdle {
		t.Fatalf("slot 0 phase = %q, want %q after returning from the resolving span on cancellation", st.Slots[0].Phase, PhaseIdle)
	}
}

// TestPoolExit3WithSiblingBackingOffReportsJam pins that a sibling asleep
// out a failure backoff does not suppress the jam alarm: every backoffOrHalt
// entry leaves no claim the sibling could release (issue #4205). Slot 0's
// first child fails the RunChild seam itself (runErrAt/runErr), landing it in
// backoffOrHalt's parked Sleep; slot 1's own fetch is held at the onResolve
// hook until that Sleep is confirmed entered (the sleepSignal below), so
// slot 1's later exit-3 is guaranteed to land while slot 0 is genuinely
// backing off, never racing the two into some other interleaving.
//
// leadSlot is launched alone first and confirmed resolved (call 1) before
// siblingSlot is launched: the baton no longer gates ResolveTip (issue
// #3625's structural fix), so both slots' round-1 resolves would otherwise
// race for call 1/2, and runErrAt=1 (a global RunChild-call index, the same
// convention as call) needs slot 0's own RunChild to land on index 1
// deterministically too.
func TestPoolExit3WithSiblingBackingOffReportsJam(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		runErrAt:  1,
		runErr:    errors.New("run-boom"),
		results:   []ChildResult{{}, {Exit: 3}},
	}
	backingOff := make(chan struct{})
	leadResolved := make(chan struct{})
	var leadResolvedOnce sync.Once
	r.onResolve = func(ctx context.Context, call int) error {
		if call == 1 {
			leadResolvedOnce.Do(func() { close(leadResolved) })
			return nil
		}
		select {
		case <-backingOff:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
	clk := &testClock{sleepSignal: make(chan struct{}, slots+1)}
	clk.park()
	nw := newNotifyWriter()
	em := newTestEmitter(nw)
	cfg := testConfig(slots)

	ctx, cancel := context.WithCancel(context.Background())
	p, pctx := newPool(ctx, cfg, r, em, clk)
	defer p.cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, leadSlot, cfg, p)
	}()
	<-leadResolved

	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, siblingSlot, cfg, p)
	}()

	// Slot 0's seam error lands it in backoffOrHalt's parked Sleep; slot 1
	// stays held at the hook (call 2, its own fetch) until this fires, so
	// there is exactly one sleepSignal to drain before slot 1 is let
	// through.
	<-clk.sleepSignal
	close(backingOff)

	nw.waitForLine(t, "\"event\":\"jam\"")

	cancel()
	wg.Wait()
	reason := p.haltReason().String()
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want it to name context-cancelled", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	for _, ev := range events {
		if ev.Event != "jam" {
			continue
		}
		if ev.Slot == nil || *ev.Slot != siblingSlot || ev.Kind != KindOf(dispatchkind.Work) {
			t.Fatalf("jam event = %+v, want slot %d kind %q: slot %d was only backing off when slot %d exited none-dispatchable", ev, siblingSlot, KindOf(dispatchkind.Work), leadSlot, siblingSlot)
		}
	}
}

// TestIdleSleepClampsSliceToRemainingWait pins idleSleep's own clamp (issue
// #3625 folded pollSlices's slicing loop into idleSleep, one slice per
// call): when less than a full IdleFloor remains before a jammed kind's
// deadline, the slice must shrink to what's left rather than overshoot it,
// and resolveTip must report false — nothing remains to resolve for. That is
// a legal config — IdleCap need not be a multiple of IdleFloor, and a
// deadline need not land on an IdleFloor boundary — but the shipped 5m/30m
// pair always divides evenly, so nothing else in this package reaches the
// clamp.
func TestIdleSleepClampsSliceToRemainingWait(t *testing.T) {
	r := &scriptedRunner{}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.IdleFloor = 3 * time.Millisecond
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	// One no-work jammed result gates dispatch until now+3ms (IdleFloor);
	// advancing 2ms leaves exactly 1ms before that deadline, less than a
	// full slice.
	p.markNoWork(KindOf(dispatchkind.Work), clk.Now(), true)
	clk.advanceBy(2 * time.Millisecond)

	resolveTip := p.idleSleep(pctx, 0)

	want := time.Millisecond
	if clk.waitCount() != 1 || clk.waits()[0] != want {
		t.Fatalf("waits = %v, want a single slice of %v (clamped to what remained, not the 3ms floor)", clk.waits(), want)
	}
	if resolveTip {
		t.Fatalf("resolveTip = true, want false: nothing remained after the clamped slice")
	}
}

// probeWriter is an Emitter sink that runs probe, if set, on every Write
// before delegating to the embedded buffer — letting a test observe what
// held true at the exact moment an event reached the stream, rather than
// only after the fact once the whole write is done.
type probeWriter struct {
	bytes.Buffer
	probe func()
}

func (w *probeWriter) Write(p []byte) (int, error) {
	if w.probe != nil {
		w.probe()
	}
	return w.Buffer.Write(p)
}

// setupAwakeWindowRace builds the two-slot-or-more Awake-window race both
// TestPoolAwakeWindowClosingEmitsExactlyOneCloseAndOpenAcrossSlots and
// TestPoolAwakeWindowCloseOrderedBeforeOpenAcrossSlots stage: several slots
// all parking on the same closed window, released at once by one clock
// step. hook, if non-nil, runs after newPool but before the slot
// goroutines start, so a caller can reach into the pool it just built and
// the sink it emits through — e.g. to point the sink's probe at p.mu. It
// returns the decoded event stream once every awaitWindow goroutine has
// returned.
func setupAwakeWindowRace(t *testing.T, slots int, hook func(p *pool, pw *probeWriter)) []Event {
	t.Helper()
	pw := &probeWriter{}
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	r := &scriptedRunner{revisions: []string{"rev1"}}
	// clk: testClock's step mode, unlike an additive advance (unsound once
	// several Sleep calls race the same shared clock), only changes now on
	// step and releases every Sleep blocked so far at once, modeling "all
	// slots are asleep waiting for the same instant" with no additive
	// artifact.
	clk := &testClock{now: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC), sleepSignal: make(chan struct{}, slots)}
	clk.park()
	em := newTestEmitter(pw)

	cfg := testConfig(slots)
	cfg.Awake = win
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()
	if hook != nil {
		hook(p, pw)
	}

	var wg sync.WaitGroup
	wg.Add(slots)
	for s := 0; s < slots; s++ {
		go func(s int) {
			defer wg.Done()
			p.awaitWindow(pctx, s)
		}(s)
	}

	clk.awaitSleep(t, slots)
	clk.step(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)) // window opens, releases every parked Sleep at once
	wg.Wait()

	return decodeEvents(t, &pw.Buffer)
}

// TestPoolAwakeWindowClosingEmitsExactlyOneCloseAndOpenAcrossSlots pins the
// edge-triggered contract: with several slots all parking on the same
// closed window, the stream carries exactly one awake_close and one
// awake_open, never one per parked slot.
func TestPoolAwakeWindowClosingEmitsExactlyOneCloseAndOpenAcrossSlots(t *testing.T) {
	const slots = 3
	events := setupAwakeWindowRace(t, slots, nil)

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

// TestPoolAwakeWindowClosingWalkExhaustedCarriesReason pins that
// awaitWindow threads Until's exhausted result through to the awake_close
// event: when the zone-period walk exhausts untilHorizon without finding
// an opening, the event's Reason names that degraded case rather than the
// ordinary "outside the Awake window", and Wait is the untilHorizon
// re-check duration, not a real opening.
func TestPoolAwakeWindowClosingWalkExhaustedCarriesReason(t *testing.T) {
	pw := &probeWriter{}
	w, now := exhaustingWindow(t)
	clk := &testClock{now: now, sleepSignal: make(chan struct{}, 1)}
	clk.park()
	em := newTestEmitter(pw)

	cfg := testConfig(1)
	cfg.Awake = w
	p, pctx := newPool(context.Background(), cfg, &scriptedRunner{revisions: []string{"rev1"}}, em, clk)
	defer p.cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.awaitWindow(pctx, 0)
	}()

	clk.awaitSleep(t, 1)
	p.cancel() // the window never truly opens in this zone; stop the loop after its first close
	wg.Wait()

	events := decodeEvents(t, &pw.Buffer)
	var closes int
	for _, ev := range events {
		if ev.Event != "awake_close" {
			continue
		}
		closes++
		if ev.Reason != walkExhaustedReason {
			t.Fatalf("awake_close reason = %q, want %q", ev.Reason, walkExhaustedReason)
		}
		if ev.Wait != untilHorizon.String() {
			t.Fatalf("awake_close wait = %q, want %q (re-check, not an opening)", ev.Wait, untilHorizon.String())
		}
	}
	if closes != 1 {
		t.Fatalf("awake_close events = %d, want exactly 1 (%v)", closes, eventNames(events))
	}
}

// TestPoolAwakeWindowCloseOrderedBeforeOpenAcrossSlots pins issue #3623's
// core ordering property: mutate holds p.mu across both applying a state
// change and emitting the events it returns, so one mutate's events always
// reach the stream before any later mutate's can. It shares
// setupAwakeWindowRace's two-slot race with the count test above, through
// the ordinary noteAwakeClose/noteAwakeOpen path — neither one holds p.mu
// itself or hand-places a publish any more, so there is no under-lock
// special case left for this ordering to lean on — and adds a probe on the
// emitter's writer that catches an event reaching the stream while p.mu is
// free, the one way this ordering could actually break.
func TestPoolAwakeWindowCloseOrderedBeforeOpenAcrossSlots(t *testing.T) {
	const slots = 2
	events := setupAwakeWindowRace(t, slots, func(p *pool, pw *probeWriter) {
		pw.probe = func() {
			// TryLock reports false whenever p.mu is held by anyone,
			// including this same goroutine (Go mutexes are not
			// reentrant) — so a successful TryLock here means this
			// event's Write reached the wire with p.mu already
			// released, the exact regression mutate's docstring rules
			// out. t.Errorf, not Fatalf: this runs on a slot goroutine.
			if p.mu.TryLock() {
				p.mu.Unlock()
				t.Errorf("event written to the stream while p.mu was not held")
			}
		}
	})

	closeIdx, openIdx := -1, -1
	for i, ev := range events {
		switch ev.Event {
		case "awake_close":
			if closeIdx == -1 {
				closeIdx = i
			}
		case "awake_open":
			if openIdx == -1 {
				openIdx = i
			}
		}
	}
	if closeIdx == -1 || openIdx == -1 {
		t.Fatalf("want exactly one awake_close and one awake_open, got %v", eventNames(events))
	}
	if closeIdx > openIdx {
		t.Fatalf("awake_close at index %d, awake_open at index %d, want close ordered before open (%v)", closeIdx, openIdx, eventNames(events))
	}
}

// TestAwaitWindowReportsAwaitingWindowThenIdle pins noteAwakeClose's phase
// write (issue #3623 review finding): a slot parked on a shut Awake window
// must publish PhaseAwaitingWindow while it waits, not just the awake_close
// event, since it is the sampled phase — not the edge-triggered event — that
// siblingsEngaged and the status file consult. Both edges are checked: the
// slot must come back out of that phase when the window reopens, which is
// noteAwakeOpen's own reset.
func TestAwaitWindowReportsAwaitingWindowThenIdle(t *testing.T) {
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	r := &scriptedRunner{revisions: []string{"rev1"}}
	clk := &testClock{now: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC), sleepSignal: make(chan struct{}, 1)}
	clk.park()
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Awake = win
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.awaitWindow(pctx, 0)
	}()

	clk.awaitSleep(t, 1)
	if got := p.snapshot().Slots[0].Phase; got != PhaseAwaitingWindow {
		t.Fatalf("phase while parked on a shut window = %q, want %q", got, PhaseAwaitingWindow)
	}

	clk.step(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)) // window opens
	wg.Wait()

	if got := p.snapshot().Slots[0].Phase; got != PhaseIdle {
		t.Fatalf("phase after the window opened = %q, want %q", got, PhaseIdle)
	}
}

// TestJamIgnoresSiblingAwaitingWindow pins that a sibling parked on a shut
// Awake window (issue #3623/#3571 review finding) is not listed as engaged
// by siblingsEngaged: it holds no claim, so it reads like an idle sibling.
// Adding that phase to the engaged cases would silently suppress the jam
// alarm for the entire time the pool sits outside its Awake window.
func TestJamIgnoresSiblingAwaitingWindow(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{revisions: []string{"rev1"}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(slots)
	p, _ := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	// Park slot 1 on the shut window directly at the pool seam, without
	// running awaitWindow itself: noteAwakeClose is the one call that
	// writes the awaiting-window phase, and that phase write is exactly
	// what siblingsEngaged must see as unengaged.
	p.noteAwakeClose(1, time.Hour, false)

	p.noteWaitResult(0, KindOf(dispatchkind.Work), "rev1", true, slotFlight{})

	events := decodeEvents(t, &buf)
	foundJam := false
	for _, ev := range events {
		if ev.Event == "jam" {
			foundJam = true
		}
		if ev.Event == "idle" && ev.Slot != nil && *ev.Slot == 0 {
			t.Fatalf("events = %v, want no idle for slot 0: its only sibling was merely awaiting the window, which must not count as engaged", eventNames(events))
		}
	}
	if !foundJam {
		t.Fatalf("events = %v, want a jam event: slot 0 had nothing dispatchable and its only sibling was awaiting the window, not engaged", eventNames(events))
	}
}

// TestBatonParkPublishesIdle pins awaitBaton's baton arm (issue #3625
// review finding): a slot parked waiting for the discovery baton is waiting
// for its own turn, not doing anything, and must read as PhaseIdle in the
// status file -- even though it enters awaitBaton still PhaseResolving, the
// real state at the loop.go call site. It also checks the jam consequence
// end to end: a sibling's none-dispatchable read beside it is a jam, not an
// idle wait. That half no longer guards the reset, since a resolving sibling
// is not engaged either (#3735); only the phase assertion does.
func TestBatonParkPublishesIdle(t *testing.T) {
	const slots = 2
	clk := &testClock{}
	nw := newNotifyWriter()
	em := newTestEmitter(nw)

	cfg := testConfig(slots)
	p, pctx := newPool(context.Background(), cfg, &scriptedRunner{revisions: []string{"rev1"}}, em, clk)
	defer p.cancel()

	p.setPhase(1, PhaseResolving)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.awaitBaton(pctx, 1)
	}()

	// baton_hold is emitted from inside the same mutate that writes the
	// phase, so observing the line proves the phase is already written.
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	if got := p.snapshot().Slots[1].Phase; got != PhaseIdle {
		t.Fatalf("phase while parked on the baton = %q, want %q", got, PhaseIdle)
	}

	p.noteWaitResult(0, KindOf(dispatchkind.Work), "rev1", true, slotFlight{})

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	foundJam := false
	for _, ev := range events {
		if ev.Event == "jam" {
			foundJam = true
		}
		if ev.Event == "idle" && ev.Slot != nil && *ev.Slot == 0 {
			t.Fatalf("events = %v, want no idle for slot 0: its only sibling was merely awaiting the baton, which must not count as engaged", eventNames(events))
		}
	}
	if !foundJam {
		t.Fatalf("events = %v, want a jam event: slot 0 had nothing dispatchable and its only sibling was awaiting the baton, not engaged", eventNames(events))
	}

	p.cancel()
	wg.Wait()
}

// TestAwaitWindowPublishesOnEveryIteration pins issue #3623's tradeoff:
// every noteAwakeClose/noteAwakeOpen call goes through mutate now, and
// mutate publishes unconditionally, so a second iteration that still finds
// the window shut (no awake_close/awake_open transition, nothing new to
// report) still writes the status file — there is no special case left
// that skips a write just because nothing changed.
func TestAwaitWindowPublishesOnEveryIteration(t *testing.T) {
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

	// The first ResolveTip is the seam immediately after awaitWindow
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
	// transition (awakeShut was already true), still writes — mutate
	// publishes every call, not just the ones that flip a flag.
	clk.step(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	clk.awaitSleep(t, 1)
	afterSecondShutIteration := readStatus(t, dir).Time
	if afterSecondShutIteration == afterClose {
		t.Fatalf("status Time did not change on a non-transition iteration, want a write anyway (mutate publishes unconditionally)")
	}

	// Advance past the reopening: the transition back to open must still
	// publish.
	clk.step(time.Date(2026, 1, 1, 22, 0, 0, 0, time.UTC))
	<-done
	if afterOpen == "" {
		t.Fatalf("ResolveTip was never reached, so the awake_open publish went unobserved")
	}
	if afterOpen == afterSecondShutIteration {
		t.Fatalf("status Time did not change on the awake_open transition")
	}
}

// TestNoteTipMovedStampsJammedKinds pins tip_moved's Kinds field (issue
// #3541 review finding) to exactly the kinds jammed at the moment the tip
// moved, in cfg.Kinds order, whether that's one kind or several — and
// proves a queue-empty (non-jammed) kind is never included and keeps its
// gate deadline untouched. noteTipMoved is
// the pool method issue #3625 pulled pollSlices's mutate body into verbatim,
// so this test moved with it rather than driving the fetch loop that used
// to call it.
func TestNoteTipMovedStampsJammedKinds(t *testing.T) {
	cases := []struct {
		name       string
		jam        []Kind
		queueEmpty []Kind
		want       []Kind
	}{
		{"only dispatch jammed", []Kind{KindOf(dispatchkind.Work)}, nil, []Kind{KindOf(dispatchkind.Work)}},
		{"both kinds jammed", []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)}, nil, []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)}},
		{"queue-empty research left gated", []Kind{KindOf(dispatchkind.Work)}, []Kind{KindOf(dispatchkind.Research)}, []Kind{KindOf(dispatchkind.Work)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptedRunner{}
			clk := &testClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			cfg := dualKindConfig(1, 0)
			p, _ := newPool(context.Background(), cfg, r, em, clk)
			defer p.cancel()

			for _, k := range tc.jam {
				p.markNoWork(k, clk.Now(), true)
			}
			readyAt := func(k Kind) time.Time {
				until, _ := p.gateOf(k, clk.Now())
				return until
			}
			gateBefore := map[Kind]time.Time{}
			for _, k := range tc.queueEmpty {
				p.markNoWork(k, clk.Now(), false)
				gateBefore[k] = readyAt(k)
				if gateBefore[k].IsZero() {
					t.Fatalf("queue-empty gate for %v was never set", k)
				}
			}

			p.noteTipMoved(0, "rev2")

			for _, k := range tc.queueEmpty {
				if got := readyAt(k); !got.Equal(gateBefore[k]) {
					t.Fatalf("queue-empty %v readyAt = %v after tip move, want untouched %v", k, got, gateBefore[k])
				}
			}

			events := decodeEvents(t, &buf)
			var tipMoved *Event
			for i := range events {
				if events[i].Event != "tip_moved" {
					continue
				}
				if tipMoved != nil {
					t.Fatalf("tip_moved emitted more than once, want exactly 1: %v", eventNames(events))
				}
				tipMoved = &events[i]
			}
			if tipMoved == nil {
				t.Fatalf("no tip_moved event, want exactly 1: %v", eventNames(events))
			}
			if tipMoved.Revision != "rev2" {
				t.Fatalf("tip_moved revision = %q, want rev2", tipMoved.Revision)
			}
			if !reflect.DeepEqual(tipMoved.Kinds, tc.want) {
				t.Fatalf("tip_moved kinds = %v, want %v", tipMoved.Kinds, tc.want)
			}
		})
	}
}

// TestIdleSleepFirstJamWaitResolvesNothingExtra pins the opportunistic-
// resolve threshold: a slot's very first no-work wait, grown to exactly one
// IdleFloor, must not ask for an opportunistic resolve — only a wait that
// outlives its first IdleFloor slice earns one.
func TestIdleSleepFirstJamWaitResolvesNothingExtra(t *testing.T) {
	r := &scriptedRunner{}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	p.markNoWork(KindOf(dispatchkind.Work), clk.Now(), true) // first no-work result: wait = IdleFloor exactly

	if resolveTip := p.idleSleep(pctx, 0); resolveTip {
		t.Fatalf("resolveTip = true, want false: a first no-work wait of exactly one IdleFloor must resolve nothing extra")
	}
	if r.resolveCount() != 0 {
		t.Fatalf("resolveCalls = %d, want 0", r.resolveCount())
	}
}

// TestResolveOpportunisticFailureIsNoChangeObserved pins how a failed
// opportunistic resolve must be handled: treated as no change
// observed, not fed to the breaker and not stamped with any event — only the
// iteration's own post-pickKind resolve reports a genuinely broken fetch.
func TestResolveOpportunisticFailureIsNoChangeObserved(t *testing.T) {
	r := &scriptedRunner{resolveAt: 1, resolveErr: errors.New("boom")}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	tip, ok := p.resolveOpportunistic(pctx, 0)
	if ok {
		t.Fatalf("resolveOpportunistic ok = true, want false on a failed resolve")
	}
	if tip != (Tip{}) {
		t.Fatalf("tip = %+v, want the zero value on failure", tip)
	}
	events := decodeEvents(t, &buf)
	for _, ev := range events {
		if ev.Event == "backoff" || ev.Event == "breaker_trip" || ev.Event == "tip_moved" {
			t.Fatalf("events = %v, want no backoff/breaker_trip/tip_moved event from a swallowed opportunistic failure", eventNames(events))
		}
	}
}

// TestResolveTipPostPickKindSiteReportsMoved pins the #3625 review finding's
// fix directly at the pool seam: resolveTip is now the one method both
// idleSleep's opportunistic call and runSlot's post-pickKind fetch go
// through, so a Moved tip observed at *either* site reports noteTipMoved.
// Before the fix, loop.go's post-pickKind ResolveTip call read Tip.Moved
// and discarded it — this test drives that exact call shape (a resolve made
// after a kind is already known runnable, the same site runSlot's
// post-pickKind p.resolveTip call used to own alone) and asserts the
// jammed sibling kind's gate still clears.
func TestResolveTipPostPickKindSiteReportsMoved(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev2"}, moved: []bool{true}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := dualKindConfig(1, 0)
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	p.markNoWork(KindOf(dispatchkind.Work), clk.Now(), true) // dispatch jammed; research untouched, still runnable

	tip, err := p.resolveTip(pctx, 0)
	if err != nil {
		t.Fatalf("resolveTip error = %v, want nil", err)
	}
	if tip.Revision != "rev2" {
		t.Fatalf("tip.Revision = %q, want rev2", tip.Revision)
	}

	events := decodeEvents(t, &buf)
	var tipMoved *Event
	for i := range events {
		if events[i].Event == "tip_moved" {
			tipMoved = &events[i]
		}
	}
	if tipMoved == nil {
		t.Fatalf("events = %v, want a tip_moved event from the post-pickKind resolve site", eventNames(events))
	}
	if !reflect.DeepEqual(tipMoved.Kinds, []Kind{KindOf(dispatchkind.Work)}) {
		t.Fatalf("tip_moved kinds = %v, want [dispatch]", tipMoved.Kinds)
	}
	if p.st.sched.View(KindOf(dispatchkind.Work), clk.Now()).Jammed {
		t.Fatalf("dispatch still jammedNow after a post-pickKind resolve observed Moved=true, want the gate cleared")
	}
}

// TestNoteTipMovedNoEventWithNothingJammed pins the event-stream guarantee
// the #3625 fix's gating exists to preserve: resolveTip now reports every
// Moved tip, including the ordinary per-iteration fetch made while nothing
// is jammed (dispatch and research both idle no-work-free) — that resolve
// must not grow a tip_moved event just because noteTipMoved was reached.
// Before this gate, folding the post-pickKind site onto noteTipMoved would
// have fired tip_moved on every advance of the base branch, not only the
// ones that actually unblocked something.
func TestNoteTipMovedNoEventWithNothingJammed(t *testing.T) {
	r := &scriptedRunner{}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := dualKindConfig(1, 0)
	p, _ := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	p.noteTipMoved(0, "rev2") // no kind jammed

	events := decodeEvents(t, &buf)
	for _, ev := range events {
		if ev.Event == "tip_moved" {
			t.Fatalf("events = %v, want no tip_moved event when noteTipMoved reset nothing", eventNames(events))
		}
	}
}

// TestLoopMultiSlotSiblingResolveClearsJam pins the invariant the #3625
// review finding actually turns on: Tip.Moved is Runner-global
// (cmd/launcher/daemon/runner.go:52-57, lastRevision/haveLastRevision), so
// exactly one resolution ever observes a given move — whichever slot that
// is must report it, or the move is lost for the whole pool. Two slots,
// dispatch seeded jammed before either starts and research left runnable,
// so pickKind hands both slots research (the only runnable kind, whichever
// preference order slotOrder gives them) and both reach the post-pickKind
// resolve concurrently — the baton no longer gates ResolveTip (issue
// #3625's structural fix moved acquisition past it). Both resolutions are
// scripted to observe the same Moved=true/rev2 answer, modelling the
// shared result concurrent callers of a coalesced flight would get in
// production; only one of the two noteTipMoved calls this produces should
// find dispatch still jammed and actually reset it, so exactly one
// tip_moved event should reach the stream.
func TestLoopMultiSlotSiblingResolveClearsJam(t *testing.T) {
	const slots = 2
	cfg := dualKindConfig(slots, 0)
	r := &scriptedRunner{revisions: []string{"rev2"}, moved: []bool{true}}
	r.holdSlots(slots)
	// announceEachSlot: without it, the first slot to reach RunChild would
	// never pass the baton onward (that happens on the child's first
	// announced issue, runSlot's ChildRequest.OnRecord callback's
	// passBaton(batonPassClaimed) call), so the second slot would stay
	// parked in awaitBaton forever and this test's "both slots really ran"
	// premise would never be reached.
	r.announceEachSlot()
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()
	p.markNoWork(KindOf(dispatchkind.Work), clk.Now(), true) // dispatch jammed before either slot starts; research left runnable

	var wg sync.WaitGroup
	wg.Add(slots)
	for s := 0; s < slots; s++ {
		go func(s int) {
			defer wg.Done()
			runSlot(pctx, s, cfg, p)
		}(s)
	}

	// Both slots reach RunChild — the pool really did run two slots, not
	// one slot and a sibling that never got there.
	seen := map[int]bool{}
	for i := 0; i < slots; i++ {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}

	events := decodeEvents(t, &buf)
	var tipMovedCount int
	var tipMoved *Event
	for i := range events {
		if events[i].Event == "tip_moved" {
			tipMovedCount++
			tipMoved = &events[i]
		}
	}
	if tipMovedCount != 1 {
		t.Fatalf("tip_moved events = %d, want exactly 1: %v", tipMovedCount, eventNames(events))
	}
	if !reflect.DeepEqual(tipMoved.Kinds, []Kind{KindOf(dispatchkind.Work)}) {
		t.Fatalf("tip_moved kinds = %v, want [dispatch]", tipMoved.Kinds)
	}

	// pickKind, not a bare p.st read, is the lock-respecting way to observe
	// dispatch's gate cleared.
	if kind, ok := p.pickKind(); !ok || kind != KindOf(dispatchkind.Work) {
		t.Fatalf("pickKind() after the resolve = (%q, %v), want (dispatch, true): dispatch's gate must be cleared", kind, ok)
	}

	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 5}) // host-tainted: halts the whole pool cleanly
	}
	awaitWG(t, r, &wg)
}

// TestSlotOrderDerivesFromKinds pins slotOrder to the kinds argument rather
// than a hardcoded two-kind pair (issue #3541 review finding): preferReserved
// puts KindOf(dispatchkind.Research) first and keeps every other kind in its
// given relative order, otherwise kinds keep their given order with
// KindOf(dispatchkind.Research) moved last, and the input slice must never be
// mutated.
func TestSlotOrderDerivesFromKinds(t *testing.T) {
	const kindOther Kind = "other"

	tests := []struct {
		name           string
		kinds          []Kind
		preferReserved bool
		want           []Kind
	}{
		{
			name:           "single kind unchanged",
			kinds:          []Kind{KindOf(dispatchkind.Work)},
			preferReserved: false,
			want:           []Kind{KindOf(dispatchkind.Work)},
		},
		{
			name:           "two kinds below reservation prefers research",
			kinds:          []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)},
			preferReserved: true,
			want:           []Kind{KindOf(dispatchkind.Research), KindOf(dispatchkind.Work)},
		},
		{
			name:           "two kinds at reservation prefers work",
			kinds:          []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)},
			preferReserved: false,
			want:           []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)},
		},
		{
			name:           "three kinds below reservation keeps non-research relative order",
			kinds:          []Kind{kindOther, KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)},
			preferReserved: true,
			want:           []Kind{KindOf(dispatchkind.Research), kindOther, KindOf(dispatchkind.Work)},
		},
		{
			// Preferring research in a research-less set must not invent
			// a kind the pool has no backoff entry for — Schedule.Decide's
			// kind lookup would nil-deref on it.
			name:           "no research kind below reservation keeps given order",
			kinds:          []Kind{KindOf(dispatchkind.Work), kindOther},
			preferReserved: true,
			want:           []Kind{KindOf(dispatchkind.Work), kindOther},
		},
		{
			name:           "three kinds at reservation keeps given order with research last",
			kinds:          []Kind{KindOf(dispatchkind.Research), kindOther, KindOf(dispatchkind.Work)},
			preferReserved: false,
			want:           []Kind{kindOther, KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := append([]Kind(nil), tc.kinds...)

			got := slotOrder(tc.kinds, tc.preferReserved)

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("slotOrder(%v, %v) = %v, want %v", tc.kinds, tc.preferReserved, got, tc.want)
			}
			if !reflect.DeepEqual(tc.kinds, orig) {
				t.Fatalf("slotOrder mutated its kinds argument: got %v, want %v", tc.kinds, orig)
			}
		})
	}
}

// TestSlotOrderIdleTierAlwaysLast pins the three-tier ordering issue #3878
// adds: the butler (dispatchkind.PriorityIdle) is tried last on every slot,
// reservation or not, and never trades places with the reserved kind —
// only reserved-vs-normal moves with the reservation, exactly as it did
// before the idle tier existed.
func TestSlotOrderIdleTierAlwaysLast(t *testing.T) {
	tests := []struct {
		name           string
		kinds          []Kind
		preferReserved bool
		want           []Kind
	}{
		{
			name:           "below reservation: reserved, normal, idle",
			kinds:          []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research), KindOf(dispatchkind.Butler)},
			preferReserved: true,
			want:           []Kind{KindOf(dispatchkind.Research), KindOf(dispatchkind.Work), KindOf(dispatchkind.Butler)},
		},
		{
			name:           "at/above reservation: normal, reserved, idle",
			kinds:          []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research), KindOf(dispatchkind.Butler)},
			preferReserved: false,
			want:           []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research), KindOf(dispatchkind.Butler)},
		},
		{
			name:           "no reservation at all: normal, idle",
			kinds:          []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Butler)},
			preferReserved: false,
			want:           []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Butler)},
		},
		{
			name:           "idle given first in config keeps tier, not position",
			kinds:          []Kind{KindOf(dispatchkind.Butler), KindOf(dispatchkind.Research), KindOf(dispatchkind.Work)},
			preferReserved: true,
			want:           []Kind{KindOf(dispatchkind.Research), KindOf(dispatchkind.Work), KindOf(dispatchkind.Butler)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := slotOrder(tc.kinds, tc.preferReserved)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("slotOrder(%v, %v) = %v, want %v", tc.kinds, tc.preferReserved, got, tc.want)
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
			// first ResolveTip call happens after that publish and
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
						KindOf(dispatchkind.Work):     {{Exit: 2}},
						KindOf(dispatchkind.Research): {{Exit: 2}},
					},
				}
				cfg := testConfig(1)
				cfg.Kinds = []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)}
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
			// kind still routes idleSleep's first slice through a
			// clk.Sleep call (even though idleSleep no longer loops
			// through several slices itself), so the same onSleep hook
			// still lands on the instant both kinds are gated.
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
						KindOf(dispatchkind.Work):     {{Exit: 3}},
						KindOf(dispatchkind.Research): {{Exit: 2}},
					},
				}
				cfg := testConfig(1)
				cfg.Kinds = []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)}
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
			// clock past the window's close and republishes via OnRecord
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
						req.OnRecord(Record{Event: report.EventBox, Key: dispatchkey.Issue("x")})
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
			// No case above exhausts the Awake walk, the one non-halted
			// source of Reason (see the *ReasonWalkExhausted tests).
			if tc.want != StateHalted && st.Reason != "" {
				t.Fatalf("reason = %q, want empty outside StateHalted", st.Reason)
			}
		})
	}
}

// TestPoolSnapshotReasonWalkExhausted pins that snapshotLocked surfaces
// Until's walk-exhausted degraded case (awake.go) in Status.Reason regardless
// of which non-halted State it lands on: the window really is shut (or a
// child is still running under a since-shut window) and NextCheck only names
// a re-check instant, not a real reopening, so Reason is how a reader tells
// the two apart (issue #3590).
func TestPoolSnapshotReasonWalkExhausted(t *testing.T) {
	tests := []struct {
		name  string
		start bool
		want  State
	}{
		{name: "asleep", start: false, want: StateAsleep},
		{name: "working outranks asleep", start: true, want: StateWorking},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, now := exhaustingWindow(t)
			clk := &testClock{now: now}
			cfg := testConfig(1)
			cfg.Awake = w
			var buf bytes.Buffer
			p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), clk)
			if tc.start {
				p.startChild(0, KindOf(dispatchkind.Work), "rev1")
			}
			snap := p.snapshot()

			if snap.State != tc.want {
				t.Fatalf("state = %q, want %q", snap.State, tc.want)
			}
			if snap.Reason != walkExhaustedReason {
				t.Fatalf("reason = %q, want %q", snap.Reason, walkExhaustedReason)
			}
			wantNextCheck := now.Add(untilHorizon).UTC().Format(time.RFC3339)
			if len(snap.Checks) == 0 {
				t.Fatalf("checks is empty, want at least one kind's nextCheck pinned")
			}
			for _, kc := range snap.Checks {
				if kc.NextCheck != wantNextCheck {
					t.Fatalf("kind %q nextCheck = %q, want %q (the re-check instant, not a real reopening)", kc.Kind, kc.NextCheck, wantNextCheck)
				}
			}
		})
	}
}

// TestPoolSnapshotSlots pins that snapshot names each slot's kind, revision
// and in-flight issues from occupancy, leaving an unoccupied slot bare.
// Driven against a 2-slot pool: slot 0 is the pre-assigned initial baton
// holder (pool.go's leadSlot), so it alone occupies on the first round
// while slot 1 parks in awaitBaton.
//
// The baton no longer gates ResolveTip (issue #3625's structural fix), so
// both slots' round-1 resolves now race for call index 1/2, and a bare
// call>=2 script would risk pinning slot 0 itself — the pre-assigned
// holder that never explicitly acquires the baton on its first round — to
// the blocked branch, leaving nothing to ever pass a baton nobody
// explicitly took: a genuine deadlock, not just a stale assumption. slot 0
// is launched alone first and confirmed resolved (call 1) before slot 1 is
// launched, so slot 1's own resolve is deterministically call 2 and parks
// forever on ctx.Done(), which keeps slot 1 from ever reaching occupy
// regardless of scheduling.
//
// Slot 0 then stays parked in that same call 1 until slot 1 has entered
// its own resolve: resolveTip (pool.go) publishes PhaseResolving before
// calling ResolveTip, so that hold is what orders slot 1's phase ahead of
// the status read onStart makes. Without it slot 0 can reach onStart
// before slot 1's goroutine is scheduled at all, snapshotting slot 1 as
// still idle.
func TestPoolSnapshotSlots(t *testing.T) {
	dir, sw := statusDir(t)
	clk := &testClock{}
	var st *Status
	var readErr error
	leadEntered := make(chan struct{})
	var leadEnteredOnce sync.Once
	siblingResolving := make(chan struct{})
	var siblingResolvingOnce sync.Once
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		onResolve: func(ctx context.Context, call int) error {
			if call == 1 {
				leadEnteredOnce.Do(func() { close(leadEntered) })
				<-siblingResolving
				return nil
			}
			siblingResolvingOnce.Do(func() { close(siblingResolving) })
			<-ctx.Done()
			return ctx.Err()
		},
		onStart: func(ctx context.Context, req ChildRequest) error {
			req.OnRecord(Record{Event: report.EventBox, Key: dispatchkey.Issue("7")})
			st, readErr = readStatusErr(dir)
			return nil
		},
	}
	r.holdSlots(2)
	cfg := testConfig(2)
	cfg.Status = sw
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()
	p.publishInitial()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, leadSlot, cfg, p)
	}()
	<-leadEntered

	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, siblingSlot, cfg, p)
	}()

	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("started slot = %d, want 0 (the initial baton holder)", got)
	}
	r.releaseSlot(t, 0, ChildResult{Exit: 5}) // host-tainted: halts the pool, which cancels the ctx onResolve is parked on
	wg.Wait()

	if readErr != nil {
		t.Fatalf("%v", readErr)
	}
	if st == nil {
		t.Fatalf("onStart never captured a status")
	}
	if len(st.Slots) != 2 {
		t.Fatalf("slots = %d, want 2", len(st.Slots))
	}
	if !st.Slots[0].Busy || st.Slots[0].Kind != KindOf(dispatchkind.Work) || st.Slots[0].Revision != "rev1" {
		t.Fatalf("slot 0 = %+v, want busy dispatch@rev1", st.Slots[0])
	}
	if st.Slots[0].Phase != PhaseRunning {
		t.Fatalf("slot 0 phase = %q, want %q", st.Slots[0].Phase, PhaseRunning)
	}
	if !reflect.DeepEqual(st.Slots[0].Issues, []string{"7"}) {
		t.Fatalf("slot 0 issues = %v, want [7]", st.Slots[0].Issues)
	}
	if st.Slots[1].Busy {
		t.Fatalf("slot 1 = %+v, want unoccupied", st.Slots[1])
	}
	// Slot 1 is parked inside its own ResolveTip call for the whole run
	// (see this test's own doc): resolving now happens before the baton is
	// ever acquired, so it never reaches awaitBaton at all.
	if st.Slots[1].Phase != PhaseResolving {
		t.Fatalf("slot 1 phase = %q, want %q", st.Slots[1].Phase, PhaseResolving)
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

	p.startChild(0, KindOf(dispatchkind.Work), "rev1")
	p.noteBox(0, KindOf(dispatchkind.Work), "rev1", Record{Event: report.EventBox, Key: dispatchkey.Issue("42")})

	snap := p.snapshot()
	if !reflect.DeepEqual(snap.Slots[0].Issues, []string{"42"}) {
		t.Fatalf("issues = %v, want [42]", snap.Slots[0].Issues)
	}
	if snap.Slots[0].Chore != "" {
		t.Fatalf("chore = %q, want empty: an issue-keyed slot must never carry a Chore", snap.Slots[0].Chore)
	}

	p.noteBox(0, KindOf(dispatchkind.Work), "rev1", Record{Event: report.EventBox, Key: dispatchkey.Issue("99")}) // mutate the pool's copy after snapshotting

	if !reflect.DeepEqual(snap.Slots[0].Issues, []string{"42"}) {
		t.Fatalf("snapshot issues changed after mutating pool state: got %v, want [42]", snap.Slots[0].Issues)
	}
}

// TestPoolNoteBoxDedupesRepeatIssueInStatusButNotInEvents pins the fix for
// a repeat-issue regression: a child that boxes the same issue twice (a
// fix-pass box after its own initial box) must publish that issue once in
// the slot's status Issues list — restoring origin/main's dedupe semantics
// — while the box event itself still fires once per record, since a
// fix-pass box is a real thing that happened and the stream must show
// both phases (issue #3627's review finding).
func TestPoolNoteBoxDedupesRepeatIssueInStatusButNotInEvents(t *testing.T) {
	clk := &testClock{}
	cfg := testConfig(1)
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, em, clk)

	p.startChild(0, KindOf(dispatchkind.Work), "rev1")
	p.noteBox(0, KindOf(dispatchkind.Work), "rev1", Record{Event: report.EventBox, Key: dispatchkey.Issue("123"), Phase: "initial"})
	p.noteBox(0, KindOf(dispatchkind.Work), "rev1", Record{Event: report.EventBox, Key: dispatchkey.Issue("123"), Phase: "fix-pass-1"})

	snap := p.snapshot()
	if !reflect.DeepEqual(snap.Slots[0].Issues, []string{"123"}) {
		t.Fatalf("issues = %v, want [123] (deduped)", snap.Slots[0].Issues)
	}

	events := wantEvents(t, &buf, []string{"child_start", "box", "box"}, "")
	if events[1].Key != dispatchkey.Issue("123") || events[1].Phase != "initial" {
		t.Errorf("first box event = %+v, want issue 123 phase initial", events[1])
	}
	if events[2].Key != dispatchkey.Issue("123") || events[2].Phase != "fix-pass-1" {
		t.Errorf("second box event = %+v, want issue 123 phase fix-pass-1", events[2])
	}

	if got := p.flightClaim(0); got != dispatchkey.Issue("123") {
		t.Errorf("flightClaim(0) = %v, want issue 123 (the most recently boxed issue)", got)
	}
}

// TestPoolSnapshotPublishesLatestBoxPhaseAsPass pins that a running slot's
// status carries the phase of its child's most recent box record, and that
// neither a finished child nor a fresh one on the same slot inherits it.
func TestPoolSnapshotPublishesLatestBoxPhaseAsPass(t *testing.T) {
	clk := &testClock{}
	cfg := testConfig(1)
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, em, clk)
	work := KindOf(dispatchkind.Work)
	box := func(phase string) {
		p.noteBox(0, work, "rev1", Record{Event: report.EventBox, Key: dispatchkey.Issue("123"), Phase: phase})
	}
	pass := func() string { return p.snapshot().Slots[0].Pass }
	start := func() {
		t.Helper()
		if _, _, ok := p.startChild(0, work, "rev1"); !ok {
			t.Fatal("startChild refused the slot")
		}
		if ph := p.snapshot().Slots[0].Phase; ph != PhaseRunning {
			t.Fatalf("phase after startChild = %q, want %q", ph, PhaseRunning)
		}
	}

	start()
	if got := pass(); got != "" {
		t.Fatalf("pass before first box = %q, want empty", got)
	}
	box("initial")
	if got := pass(); got != "initial" {
		t.Fatalf("pass after first box = %q, want initial", got)
	}
	box("fix-pass-1")
	if got := pass(); got != "fix-pass-1" {
		t.Fatalf("pass after later box = %q, want fix-pass-1", got)
	}

	p.finishChild(0)
	if got := pass(); got != "" {
		t.Fatalf("pass after finishChild = %q, want empty", got)
	}

	start()
	if got := pass(); got != "" {
		t.Fatalf("pass on new child before its first box = %q, want empty", got)
	}
}

// TestPoolSnapshotPublishesLatestModelRecord pins that a running slot's
// status carries its child's latest model record, that a later model record
// replaces it and a later box record clears it, and that neither a finished
// child nor a fresh one on the same slot inherits it. Each record is also
// emitted as a model event.
func TestPoolSnapshotPublishesLatestModelRecord(t *testing.T) {
	clk := &testClock{}
	cfg := testConfig(1)
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, em, clk)
	work := KindOf(dispatchkind.Work)
	key := dispatchkey.Issue("123")
	model := func(id, role string) {
		p.noteModel(0, work, "rev1", Record{Event: report.EventModel, Key: key, Model: id, ModelRole: role})
	}
	got := func() (string, string) {
		s := p.snapshot().Slots[0]
		return s.Model, s.ModelRole
	}
	want := func(step, id, role string) {
		t.Helper()
		if m, r := got(); m != id || r != role {
			t.Fatalf("%s: model, role = %q, %q; want %q, %q", step, m, r, id, role)
		}
	}
	start := func() {
		t.Helper()
		if _, _, ok := p.startChild(0, work, "rev1"); !ok {
			t.Fatal("startChild refused the slot")
		}
	}

	start()
	want("before any model record", "", "")
	model("claude-sonnet-5-5", "worker")
	want("after model record", "claude-sonnet-5-5", "worker")
	model("claude-opus-5-5", "")
	want("after later model record", "claude-opus-5-5", "")
	p.noteBox(0, work, "rev1", Record{Event: report.EventBox, Key: key, Phase: "fix-pass-1"})
	want("after box record", "", "")
	model("claude-opus-5-5", "reviewer")
	want("after model record in new pass", "claude-opus-5-5", "reviewer")

	p.finishChild(0)
	want("after finishChild", "", "")
	start()
	want("new child before its first model record", "", "")

	events := wantEvents(t, &buf, []string{"child_start", "model", "model", "box", "model", "child_start"}, "")
	if ev := events[1]; ev.Model != "claude-sonnet-5-5" || ev.ModelRole != "worker" || ev.Key != key || ev.Kind != work || ev.Slot == nil || *ev.Slot != 0 {
		t.Errorf("first model event = %+v", ev)
	}
	if ev := events[2]; ev.Model != "claude-opus-5-5" || ev.ModelRole != "" {
		t.Errorf("second model event = %+v", ev)
	}
}

// TestPoolNoteModelDroppedWhenSlotNotRunning pins noteBox's race guard for
// model records: one reported after the slot cleared changes nothing and
// emits nothing.
func TestPoolNoteModelDroppedWhenSlotNotRunning(t *testing.T) {
	clk := &testClock{}
	cfg := testConfig(1)
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, em, clk)
	p.noteModel(0, KindOf(dispatchkind.Work), "rev1", Record{Event: report.EventModel, Key: dispatchkey.Issue("1"), Model: "m"})
	if s := p.snapshot().Slots[0]; s.Model != "" {
		t.Errorf("model on idle slot = %q, want empty", s.Model)
	}
	if buf.Len() != 0 {
		t.Errorf("events = %q, want none", buf.String())
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
	cfg.Kinds = []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, em, clk)

	snap := p.snapshot()
	if !reflect.DeepEqual(snap.Kinds, []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)}) {
		t.Fatalf("kinds = %v, want [dispatch research]", snap.Kinds)
	}

	cfg.Kinds[0] = "mutated" // mutate the caller's slice after snapshotting

	if !reflect.DeepEqual(snap.Kinds, []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)}) {
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
			KindOf(dispatchkind.Research): {{Exit: 2}},
		},
		results: []ChildResult{{Exit: 0}},
		onStart: func(ctx context.Context, req ChildRequest) error {
			if req.Kind == KindOf(dispatchkind.Work) {
				once.Do(func() {
					cancel()
					st = readStatus(t, dir)
				})
			}
			return nil
		},
	}
	cfg := testConfig(1)
	cfg.Kinds = []Kind{KindOf(dispatchkind.Research), KindOf(dispatchkind.Work)}
	cfg.ResearchReservation = 1
	cfg.Status = sw
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	Loop(ctx, cfg, r, em, clk)

	if st == nil {
		t.Fatalf("onStart never captured a status")
	}
	if len(st.Checks) != 2 || st.Checks[0].Kind != KindOf(dispatchkind.Research) || st.Checks[1].Kind != KindOf(dispatchkind.Work) {
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

	wait := p.markNoWork(KindOf(dispatchkind.Work), clk.Now(), false)
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
// reviewer's rejected fix that would key jammedNow off state.siblingsEngaged
// — the predicate runSlot only reaches through markNoWork — instead of
// markNoWork's own outcome flag: it drives the real runSlot call site rather
// than calling markNoWork directly, so a future regression back to that
// rejected predicate would fail this test. Slot 1 has a research child
// running when slot 0's dispatch check returns exit 3 —
// siblingsEngaged is true at that exact instant — and the dispatch kind's
// jammed flag must still flip true, unaffected by occupancy. It must also
// stay true once the sibling clears, so the pool-level state reads jammed,
// never waiting: an open, none-dispatchable queue must never be reported as
// "every queue empty". Stays off Loop for the same reason it drives runSlot
// by hand rather than a full pool: it needs slot 1's occupancy faked in
// directly (p.startChild) at an exact instant relative to slot 0's own exit-3,
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
	em := newTestEmitter(nw)
	cfg := testConfig(slots)
	cfg.Kinds = []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)}
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	// Research starts already gated (idle, not jammed): with two kinds
	// configured, an ungated research would have slot 0's own loop retry
	// it the moment dispatch backs off, rather than parking in idleSleep —
	// this test's synchronization point.
	p.markNoWork(KindOf(dispatchkind.Research), clk.Now(), false)
	startChildAs(p, 1, KindOf(dispatchkind.Research), "rev1")

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

	if !p.st.sched.View(KindOf(dispatchkind.Work), clk.Now()).Jammed {
		t.Fatalf("dispatch jammedNow = false with a sibling occupied, want true (exit 3 alone gates this)")
	}

	// A busy slot outranks jammed/waiting in snapshot's precedence, so the
	// sibling has to clear before the state under test is observable.
	p.finishChild(1)

	if !p.st.sched.View(KindOf(dispatchkind.Work), clk.Now()).Jammed {
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
			KindOf(dispatchkind.Work):     {{Exit: 3}},
			KindOf(dispatchkind.Research): {{Exit: 2}},
		},
	}
	cfg := testConfig(1)
	cfg.Kinds = []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)}
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
		case KindOf(dispatchkind.Work):
			dispatch = kc
		case KindOf(dispatchkind.Research):
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
// "daemon: status file write failed" diagnostic to the emitter's errW
// rather than silently vanishing, and must never panic or halt the daemon —
// mirrors TestEmitterEncodeFailureReportsDiagnosticAndDoesNotPanic's precedent
// for the sibling failure path. Driven through Loop's own up-front publish
// (loop.go, before any child runs) rather than a hand-called p.publish(),
// per issue #3620 slice 7a. The write is made to fail by pointing
// NewStatusWriter at a directory that does not exist, so os.CreateTemp
// fails; chmod 0500 is avoided because the Nix check sandbox may run as
// root, where mode bits do not deny writes.
func TestPoolPublishReportsWriteFailureDiagnostic(t *testing.T) {
	clk := &testClock{}
	cfg := testConfig(1)
	cfg.Status = NewStatusWriter(filepath.Join(t.TempDir(), "no-such-dir"), time.Now)
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}} // host-tainted: halts promptly
	var buf, errBuf bytes.Buffer
	em := newTestEmitterErr(&buf, &errBuf)

	Loop(context.Background(), cfg, r, em, clk) // must not panic despite the write failure

	if got := errBuf.String(); !strings.Contains(got, "daemon: status file write failed") {
		t.Fatalf("errW = %q, want it to contain %q", got, "daemon: status file write failed")
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
			gate:      []Kind{KindOf(dispatchkind.Work)},
			want:      []string{reopen, reopen},
			wantState: StateAsleep,
		},
		{
			name:      "a backoff deadline past the reopening wins",
			window:    "22:00-06:00 UTC",
			awakeShut: true,
			floor:     longFloor,
			gate:      []Kind{KindOf(dispatchkind.Work)},
			want:      []string{farLater, reopen},
			wantState: StateAsleep,
		},
		{
			name:      "open window leaves each kind's own deadline",
			window:    "06:00-22:00 UTC",
			floor:     longFloor,
			gate:      []Kind{KindOf(dispatchkind.Work)},
			want:      []string{farLater, ""},
			wantState: StateChecking,
		},
		{
			name:      "nil always-awake window leaves each kind's own deadline",
			window:    "",
			floor:     longFloor,
			gate:      []Kind{KindOf(dispatchkind.Work)},
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
			cfg.Kinds = []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)}
			cfg.Awake = w
			if tc.floor > 0 {
				cfg.IdleFloor = tc.floor
				cfg.IdleCap = tc.floor
			}
			var buf bytes.Buffer
			p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), clk)
			p.st.awakeShut = tc.awakeShut
			for _, k := range tc.gate {
				p.markNoWork(k, clk.Now(), false)
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

// startChildAs marks slot running as kind exactly, bypassing startChild's
// re-choice, for tests that need a sibling of a specific kind in flight
// whatever the pool's reservation or backoff state would pick.
func startChildAs(p *pool, slot int, kind Kind, revision string) {
	p.mutate(func(s *state) []Event {
		s.slots[slot] = slotState{phase: PhaseRunning, flight: slotFlight{kind: kind, revision: revision}}
		return nil
	})
}

// TestPoolStartChildChoosesKindFromLiveReservedCount pins issue #3582: the
// research floor is a live count of running research children, not a
// per-slot preference, so a free slot picks research whenever fewer than
// ResearchReservation research children are running, whichever slot it is.
func TestPoolStartChildChoosesKindFromLiveReservedCount(t *testing.T) {
	work, research := KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)
	tests := []struct {
		name        string
		reservation int
		sibling     Kind // kind of the running child on slot 0; "" for none
		want        Kind
	}{
		{"no research running, floor of 1: research", 1, work, research},
		{"research already running, floor of 1 met: work", 1, research, work},
		{"floor of 0: work even with no research running", 0, work, work},
		{"floor of 2, one research running: research again", 2, research, research},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := dualKindConfig(3, tt.reservation)
			var buf bytes.Buffer
			p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), &testClock{})
			defer p.cancel()
			if tt.sibling != "" {
				startChildAs(p, 0, tt.sibling, "rev1")
			}

			if got, _, _ := p.startChild(1, work, "rev1"); got != tt.want {
				t.Fatalf("startChild chose %q, want %q", got, tt.want)
			}
			if got := p.snapshot().Slots[1].Kind; got != tt.want {
				t.Fatalf("slot 1 flight kind = %q, want the chosen %q", got, tt.want)
			}
		})
	}
}

// TestPoolStartChildChoreKeyedNonHolderNeverSwitchesToIssueKeyed pins issue #4583:
// a slot that skipped the baton for a butler provisional kind is not the
// discovering slot, so startChild must keep the butler rather than re-decide
// onto work. The baton holder (leadSlot) discovers anyway and may take work.
func TestPoolStartChildChoreKeyedNonHolderNeverSwitchesToIssueKeyed(t *testing.T) {
	work, butler := KindOf(dispatchkind.Work), KindOf(dispatchkind.Butler)
	tests := []struct {
		name string
		slot int
		want Kind
	}{
		{"non-holder keeps the butler", leadSlot + 1, butler},
		{"baton holder may switch to work", leadSlot, work},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p, _ := newPool(context.Background(), triKindConfig(2, 0), &scriptedRunner{}, newTestEmitter(&buf), &testClock{})
			defer p.cancel()

			if got, _, ok := p.startChild(tt.slot, butler, "rev1"); !ok || got != tt.want {
				t.Fatalf("startChild chose %q (ok=%v), want %q", got, ok, tt.want)
			}
		})
	}
}

// TestPoolStartChildFallsBackToProvisionalKindWhenNothingRunnable pins the
// fallback: a kind runnable at pick time that a sibling gated since still
// starts as the provisional kind rather than as an empty one.
func TestPoolStartChildFallsBackToProvisionalKindWhenNothingRunnable(t *testing.T) {
	work, research := KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)
	clk := &testClock{}
	var buf bytes.Buffer
	p, _ := newPool(context.Background(), dualKindConfig(2, 1), &scriptedRunner{}, newTestEmitter(&buf), clk)
	defer p.cancel()
	p.markNoWork(work, clk.Now(), false)
	p.markNoWork(research, clk.Now(), false)

	if got, _, _ := p.startChild(0, work, "rev1"); got != work {
		t.Fatalf("startChild chose %q, want the provisional %q", got, work)
	}
}

// TestPoolStartChildDeclinesProbedKindWhoseCountZeroed pins ADR 0059: unlike
// an exit-driven kind, a probed kind whose count a sibling zeroed since
// runSlot decided is not started as an empty child. The slot stays idle and
// no child_start is emitted.
func TestPoolStartChildDeclinesProbedKindWhoseCountZeroed(t *testing.T) {
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	var buf bytes.Buffer
	p, _ := newPool(context.Background(), probedConfig(2, time.Hour, workKind), &scriptedRunner{}, newTestEmitter(&buf), clk)
	defer p.cancel()
	p.mutate(func(s *state) []Event {
		s.sched, _ = s.sched.Observe(clk.Now(), DemandProbed{Kind: workKind, Ready: 1})
		s.sched, _ = s.sched.Observe(clk.Now(), ChildDone{Kind: workKind, Result: ChildEmpty})
		return nil
	})

	if kind, _, ok := p.startChild(0, workKind, "rev1"); ok {
		t.Fatalf("startChild started %q, want no start", kind)
	}
	if got := p.snapshot().Slots[0].Phase; got == PhaseRunning {
		t.Fatalf("slot 0 phase = %q, want not running", got)
	}
	if strings.Contains(buf.String(), "child_start") {
		t.Fatalf("child_start emitted for a declined start: %s", buf.String())
	}
}

// TestOccupancyCountsRunningAndStartingPerKind pins that only PhaseRunning
// slots count, and that a running slot with no flight key (no claim reported
// yet) is also Starting.
func TestOccupancyCountsRunningAndStartingPerKind(t *testing.T) {
	work, research := KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)
	keyed := slotFlight{kind: work, key: dispatchkey.Issue("1")}
	s := &state{slots: []slotState{
		{phase: PhaseRunning, flight: slotFlight{kind: work}},
		{phase: PhaseRunning, flight: slotFlight{kind: work}},
		{phase: PhaseRunning, flight: keyed},
		{phase: PhaseRunning, flight: slotFlight{kind: research}},
		{phase: PhaseRunning, flight: slotFlight{kind: research, key: dispatchkey.Issue("2")}},
		{phase: PhaseBackingOff, flight: slotFlight{kind: research}},
		{phase: PhaseIdle},
	}}
	occ := occupancy(s)
	if want := map[Kind]int{work: 3, research: 2}; !reflect.DeepEqual(occ.Running, want) {
		t.Errorf("Running = %v, want %v", occ.Running, want)
	}
	if want := map[Kind]int{work: 2, research: 1}; !reflect.DeepEqual(occ.Starting, want) {
		t.Errorf("Starting = %v, want %v", occ.Starting, want)
	}
}

// TestPoolConcurrentNoWorkUnderOneGateRaisesBackoffOnce: four slots of one
// exit-driven kind start under the same gate and all exit empty. They are one
// observation of the same empty queue, so the kind climbs one backoff step,
// not the four it would if each result escalated (issue #4618). A prior empty
// result puts the gate generation above zero, so a child that failed to carry
// its start gate back would read as stale instead of passing by coincidence.
func TestPoolConcurrentNoWorkUnderOneGateRaisesBackoffOnce(t *testing.T) {
	work := KindOf(dispatchkind.Work)
	const slots = 4
	clk := &testClock{}
	clk.setNow(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	var buf bytes.Buffer
	p, _ := newPool(context.Background(), testConfig(slots), &scriptedRunner{}, newTestEmitter(&buf), clk)
	defer p.cancel()

	if got := p.markNoWork(work, clk.Now(), false); got != testIdleFloor {
		t.Fatalf("first empty result gated for %v, want the floor %v", got, testIdleFloor)
	}
	clk.advanceBy(testIdleFloor)

	for slot := 0; slot < slots; slot++ {
		p.startChild(slot, work, "rev1")
	}
	for slot := 0; slot < slots; slot++ {
		flight := p.flightReport(slot)
		p.finishChild(slot)
		p.noteWaitResult(slot, work, "rev1", false, flight)
	}

	p.mu.Lock()
	v := p.st.sched.View(work, clk.Now())
	p.mu.Unlock()
	if !v.Gated {
		t.Fatal("work not gated after four concurrent empty exits")
	}
	if got, want := v.Until.Sub(clk.Now()), 2*testIdleFloor; got != want {
		t.Fatalf("work gated for %v, want one step up to %v (cap %v)", got, want, testIdleCap)
	}
}

func TestLiveKeysCollectsOnlyRunningSlotsKeys(t *testing.T) {
	work := KindOf(dispatchkind.Work)
	s := &state{slots: []slotState{
		{phase: PhaseRunning, flight: slotFlight{kind: work, key: dispatchkey.Issue("1")}},
		{phase: PhaseRunning, flight: slotFlight{kind: work}},
		{phase: PhaseBackingOff, flight: slotFlight{kind: work, key: dispatchkey.Issue("2")}},
		{phase: PhaseIdle},
	}}
	got := liveKeys(s)
	if want := (dispatchkey.Set{dispatchkey.Issue("1"): true}); !reflect.DeepEqual(got, want) {
		t.Fatalf("liveKeys = %v, want %v: only a running slot's non-zero key", got, want)
	}
}
