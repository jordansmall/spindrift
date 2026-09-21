package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPoolColdStartGatesDiscoveryToTheLeadingSlot pins issue #3634's central
// property: on a cold start, only the leading slot (slot 0) runs discovery
// until it has claimed an issue — every sibling must park rather than run
// its own discovery against the same tracker snapshot, which is what let
// two slots pick the same issue before this gate existed. Once the leader
// announces a claim (live, via OnIssue, not after its child exits), the
// gate opens and the rest of the pool starts its own first wave at once.
func TestPoolColdStartGatesDiscoveryToTheLeadingSlot(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	done := make(chan string, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	// Only the leader (slot 0) starts discovery at first.
	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("first slot to start = %d, want 0 (the leader)", got)
	}

	// The other two slots park on the gate instead of starting their own
	// discovery: wait for both to report it.
	nw.waitForLine(t, "\"event\":\"gate_hold\"")
	nw.waitForLine(t, "\"event\":\"gate_hold\"")

	// Structurally guaranteed, not a timing race: a slot parked in
	// awaitStartGate cannot reach RunChild until the gate closes, so
	// r.started must have nothing further to give yet.
	select {
	case got := <-r.started:
		t.Fatalf("sibling slot %d started before the leader claimed anything, want the pool held", got)
	default:
	}

	// The leader announces a claim while its child is still running: the
	// gate opens at once, and the rest of the pool starts its own first
	// wave.
	r.fireOnIssue(t, 0, "42")

	seen := map[int]bool{0: true}
	for i := 0; i < slots-1; i++ {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started after the claim = %v, want all %d", seen, slots)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	var holds, opens int
	var openReason string
	for _, ev := range events {
		switch ev.Event {
		case "gate_hold":
			holds++
		case "gate_open":
			opens++
			openReason = ev.Reason
		}
	}
	if holds != 2 {
		t.Errorf("gate_hold events = %d, want exactly 2 (slots 1 and 2)", holds)
	}
	if opens != 1 {
		t.Fatalf("gate_open events = %d, want exactly 1", opens)
	}
	if !strings.Contains(openReason, "claimed") {
		t.Errorf("gate_open reason = %q, want it to name the leader's claim", openReason)
	}

	// End the test: release every slot's in-flight child.
	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 7})
	}
	reason := <-done
	if !strings.Contains(reason, "signalled-stop") {
		t.Fatalf("halt reason = %q, want it to name signalled-stop", reason)
	}
}

// TestPoolColdStartGateStaysReleasedAcrossLaterIterations pins issue
// #3634's AC2 in its concrete, testable form: "once the first slot
// resolves, the remaining slots fill without further gating; steady-state
// behaviour is unchanged". The first pass gates the two siblings exactly
// like TestPoolColdStartGatesDiscoveryToTheLeadingSlot above; this test
// then drives every slot through two more iterations each and checks that
// none of them emits a further gate_hold — awaitStartGate's steady-state
// path is a non-blocking select on an already-closed channel, so a later
// iteration parking there would be a bug, not routine gating.
func TestPoolColdStartGateStaysReleasedAcrossLaterIterations(t *testing.T) {
	const slots = 3
	const passes = 3 // the gated first pass, plus two ungated ones
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	done := make(chan string, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	// First pass: leader alone, siblings gated, exactly as the test above.
	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("first slot to start = %d, want 0 (the leader)", got)
	}
	for i := 0; i < slots-1; i++ {
		nw.waitForLine(t, "\"event\":\"gate_hold\"")
	}
	r.fireOnIssue(t, 0, "42")

	seen := map[int]bool{0: true}
	for i := 0; i < slots-1; i++ {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started after the claim = %v, want all %d", seen, slots)
	}

	// Later passes: every slot's child exits 0 (dispatched, Continue), so
	// each slot loops straight back into its next RunChild with no wait
	// and no gate in the way.
	for pass := 1; pass < passes; pass++ {
		for s := 0; s < slots; s++ {
			r.releaseSlot(t, s, ChildResult{Exit: 0})
		}
		seen = map[int]bool{}
		for i := 0; i < slots; i++ {
			seen[r.awaitStart(t)] = true
		}
		if len(seen) != slots {
			t.Fatalf("pass %d: distinct slots started = %v, want all %d", pass, seen, slots)
		}
	}

	// End the test: release every slot's final in-flight child.
	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 7})
	}
	reason := <-done
	if !strings.Contains(reason, "signalled-stop") {
		t.Fatalf("halt reason = %q, want it to name signalled-stop", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	var holds, opens int
	for _, ev := range events {
		switch ev.Event {
		case "gate_hold":
			holds++
		case "gate_open":
			opens++
		}
	}
	if holds != slots-1 {
		t.Errorf("gate_hold events = %d, want exactly %d (one per sibling, on the first pass only)", holds, slots-1)
	}
	if opens != 1 {
		t.Errorf("gate_open events = %d, want exactly 1", opens)
	}
}

// TestPoolColdStartWithNoClaimReleasesAfterFinishReports mirrors the test
// above for the leader's other outcome: no work found. The gate must still
// open once the leader's child returns, even though nothing was ever
// claimed — but the release now lands at the top of the leader's next
// iteration, not inline right after RunChild, so it must land strictly
// after the leader's own child_finish (and any box events) rather than
// racing ahead of the events its reason names.
func TestPoolColdStartWithNoClaimReleasesAfterFinishReports(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	// clk: once the leader's exit-2 backs the pool's one configured kind
	// off, every slot that reaches pickKind finds it gated and parks in
	// its own idle wait — a parked testClock's Sleep never returns on its
	// own, so that park is this test's synchronization point for "the
	// sibling ran its own discovery cycle", not just "the sibling started
	// a child" (which single-kind exhaustion makes nothing left to
	// start). Never calling wake(): the only way any Sleep call ever
	// returns here is the top-level ctx being cancelled below.
	clk := &testClock{now: time.Unix(0, 0).UTC(), sleepSignal: make(chan struct{}, 8)}
	clk.park()
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		done <- Loop(ctx, testConfig(slots), r, em, clk)
	}()

	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("first slot to start = %d, want 0 (the leader)", got)
	}
	nw.waitForLine(t, "\"event\":\"gate_hold\"")

	select {
	case got := <-r.started:
		t.Fatalf("sibling slot %d started before the leader's first child finished, want the pool held", got)
	default:
	}

	// The leader's first child finds no work and exits without ever
	// calling OnIssue: the gate must still open on its return, letting the
	// sibling — previously blocked in awaitStartGate — run its own
	// pickKind instead of staying parked on a gate that is now the only
	// thing standing between it and ever running.
	r.releaseSlot(t, 0, ChildResult{Exit: 2})

	// The leader's own next pickKind call observes the dispatch backoff it
	// just set synchronously, in the same goroutine, so it always idles.
	select {
	case <-clk.sleepSignal:
	case <-time.After(5 * time.Second):
		t.Fatalf("the leader never reached its own idle wait after releasing the gate")
	}

	// The sibling's own pickKind call, in a different goroutine, races the
	// leader's markNoWork against openStartGate (both fire from the same
	// finish site, but only one is that call's own next step): it either
	// also finds dispatch already gated (idles) or still finds it
	// runnable (starts a child). Either one proves the gate released it —
	// this is not a hang risk either way, so accept whichever arrives.
	select {
	case s := <-r.started:
		if s != 1 {
			t.Fatalf("unexpected slot %d started a child", s)
		}
		r.releaseSlot(t, 1, ChildResult{Exit: 2})
	case <-clk.sleepSignal:
	case <-time.After(5 * time.Second):
		t.Fatalf("the sibling never unblocked from the gate")
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	var opens, openIdx, childFinishIdx int
	var openReason string
	childFinishIdx = -1
	for i, ev := range events {
		switch ev.Event {
		case "gate_open":
			opens++
			openIdx = i
			openReason = ev.Reason
		case "child_finish":
			if childFinishIdx == -1 {
				childFinishIdx = i
			}
		}
	}
	if opens != 1 {
		t.Fatalf("gate_open events = %d, want exactly 1", opens)
	}
	if !strings.Contains(openReason, "resolved without a claim") {
		t.Errorf("gate_open reason = %q, want it to say the round resolved without a claim", openReason)
	}
	if childFinishIdx == -1 {
		t.Fatalf("events = %v, want at least one child_finish", eventNames(events))
	}
	if openIdx <= childFinishIdx {
		t.Errorf("gate_open at index %d does not follow the leader's own child_finish at index %d: the release must land on the next iteration, after the finish it reports on", openIdx, childFinishIdx)
	}

	cancel()
	reason := <-done
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want it to name context-cancelled", reason)
	}
}

// TestPoolSingleSlotNeverGates pins that a single-slot pool builds no gate
// at all (see newPool): the sole slot is both leader and everyone, so
// there is no wave to stagger and the stream must carry no gate event.
func TestPoolSingleSlotNeverGates(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	events := decodeEvents(t, &buf)
	for _, ev := range events {
		if ev.Event == "gate_hold" || ev.Event == "gate_open" {
			t.Fatalf("events = %v, want no gate event for a single-slot pool", eventNames(events))
		}
	}
}

// TestPoolLeaderFailureBeforeChildStillReleasesTheGate pins the
// backoffOrHalt release site: a leader that fails before ever starting a
// child (here, its very first ResolveRevision) must still release every
// sibling, not strand the whole pool through its own backoff. The first
// ResolveRevision call is guaranteed to be the leader's own — every sibling
// is still parked in awaitStartGate at that point — so scripting call 1 to
// fail deterministically targets the leader alone.
func TestPoolLeaderFailureBeforeChildStillReleasesTheGate(t *testing.T) {
	const slots = 2
	wantErr := errors.New("boom")
	r := &scriptedRunner{
		revisions:  []string{"rev1"},
		resolveAt:  1,
		resolveErr: wantErr,
		results:    []ChildResult{{Exit: 7}},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	done := make(chan string, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	var reason string
	select {
	case reason = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop never returned: a leader that fails before starting a child left the pool deadlocked")
	}
	if !strings.Contains(reason, "signalled-stop") {
		t.Fatalf("halt reason = %q, want it to name signalled-stop", reason)
	}

	events := decodeEvents(t, &buf)
	var opens, childStarts int
	var openReason string
	for _, ev := range events {
		switch ev.Event {
		case "gate_open":
			opens++
			openReason = ev.Reason
		case "child_start":
			childStarts++
		}
	}
	if opens != 1 {
		t.Fatalf("gate_open events = %d, want exactly 1", opens)
	}
	if !strings.Contains(openReason, "first discovery round failed") {
		t.Errorf("gate_open reason = %q, want it to name the leader's pre-child failure", openReason)
	}
	if childStarts == 0 {
		t.Fatalf("child_start events = %d, want at least 1: the sibling must go on to run its own child rather than staying parked", childStarts)
	}
}

// TestPoolLeaderPostChildFailureReleasesTheGateWithHonestReason covers the
// path the reviewer found uncovered (issue #3634 review): a leader whose
// first child actually starts and only then fails — an unrecognised exit
// code (loop.go's Backoff case) or a RunChild seam error — must still
// release the gate, and the reason it stamps must not claim the leader
// "failed before starting a child" when a child manifestly did: that
// child's own child_start/child_finish pair already reached the stream
// before the release. The first RunChild call is guaranteed to be the
// leader's own — every sibling is still parked in awaitStartGate at that
// point — so scripting call 1 to fail targets the leader alone.
//
// Deliberately asserts nothing about where each gate_hold lands relative
// to gate_open, unlike the tests above whose leader parks long enough for
// every sibling to have reached the gate first. This leader fails as fast
// as the fake can script it, so a sibling can still be between
// awaitStartGate's non-blocking receive and its own gate_hold emit when
// the release fires — a hold that trails the open, which openStartGate's
// emit-before-close ordering allows by design.
func TestPoolLeaderPostChildFailureReleasesTheGateWithHonestReason(t *testing.T) {
	tests := []struct {
		name string
		r    *scriptedRunner
	}{
		{
			name: "unrecognised exit code",
			r: &scriptedRunner{
				revisions: []string{"rev1"},
				results:   []ChildResult{{Exit: 99}, {Exit: 7}},
			},
		},
		{
			name: "RunChild seam error",
			r: &scriptedRunner{
				revisions: []string{"rev1"},
				runErrAt:  1,
				runErr:    errors.New("boom"),
				results:   []ChildResult{{Exit: 7}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const slots = 2
			clk := &testClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			done := make(chan string, 1)
			go func() {
				done <- Loop(context.Background(), testConfig(slots), tc.r, em, clk)
			}()

			var reason string
			select {
			case reason = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Loop never returned: a leader whose first child failed left the pool deadlocked")
			}
			if !strings.Contains(reason, "signalled-stop") {
				t.Fatalf("halt reason = %q, want it to name signalled-stop", reason)
			}

			events := decodeEvents(t, &buf)
			var opens int
			var openReason string
			openIdx, childStartIdx, childFinishIdx := -1, -1, -1
			for i, ev := range events {
				switch ev.Event {
				case "gate_open":
					opens++
					openReason = ev.Reason
					openIdx = i
				case "child_start":
					if childStartIdx == -1 {
						childStartIdx = i
					}
				case "child_finish":
					if childFinishIdx == -1 {
						childFinishIdx = i
					}
				}
			}
			if opens != 1 {
				t.Fatalf("gate_open events = %d, want exactly 1", opens)
			}
			if strings.Contains(openReason, "before starting a child") {
				t.Errorf("gate_open reason = %q, falsely claims the leader failed before starting a child even though a child_start/child_finish pair already reached the stream", openReason)
			}
			if childStartIdx == -1 || childFinishIdx == -1 {
				t.Fatalf("events = %v, want at least one child_start/child_finish pair", eventNames(events))
			}
			if openIdx <= childFinishIdx {
				t.Errorf("gate_open at index %d does not follow the leader's own child_finish at index %d", openIdx, childFinishIdx)
			}
		})
	}
}

// TestPoolLeaderStopBeforeResolvingReleasesTheGate pins the deferred
// release in runSlot (gateOpenStopped): a leader that stops — here, an
// operator cancellation landing inside its very first ResolveRevision,
// before it ever gets to start a child — must still open the gate on its
// way out, or every sibling parked in awaitStartGate would hang forever.
func TestPoolLeaderStopBeforeResolvingReleasesTheGate(t *testing.T) {
	const slots = 3
	ctx, cancel := context.WithCancel(context.Background())
	// onResolve blocks on proceed (so the test can first confirm every
	// sibling is genuinely parked on the gate) and then cancels the pool
	// itself, modelling an operator SIGTERM landing before the leader's
	// first discovery round ever resolves. onStart must never be reached
	// down this path — a cancelled pool halts before starting any child.
	proceed := make(chan struct{})
	r := &scriptedRunner{
		onResolve: func(ctx context.Context, call int) error {
			<-proceed
			cancel()
			return ctx.Err()
		},
		onStart: func(ctx context.Context, req ChildRequest) error {
			return fmt.Errorf("must not be called: a cancelled pool must halt before any child starts")
		},
	}
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	done := make(chan string, 1)
	go func() {
		done <- Loop(ctx, testConfig(slots), r, em, clk)
	}()

	// Confirm every sibling is genuinely parked on the gate before letting
	// the leader observe the cancellation, or the leader could win the
	// race and this test would pass without ever exercising a held sibling.
	for i := 0; i < slots-1; i++ {
		nw.waitForLine(t, "\"event\":\"gate_hold\"")
	}
	close(proceed)

	var reason string
	select {
	case reason = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop never returned: a leader that stopped before resolving anything left a sibling parked")
	}
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want it to name context-cancelled", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	var holds, opens int
	var openReason string
	for _, ev := range events {
		switch ev.Event {
		case "gate_hold":
			holds++
		case "gate_open":
			opens++
			openReason = ev.Reason
		}
	}
	if holds != slots-1 {
		t.Errorf("gate_hold events = %d, want %d (one per held slot)", holds, slots-1)
	}
	if opens != 1 {
		t.Fatalf("gate_open events = %d, want exactly 1", opens)
	}
	if !strings.Contains(openReason, "stopped before its first discovery round resolved") {
		t.Errorf("gate_open reason = %q, want it to name the leader stopping first", openReason)
	}
}

// TestPoolColdStartEventStreamOrderingAndReasons pins the operator-facing
// contract issue #3634 exists for: "an operator can tell a gated start from
// a stalled one" from the event stream alone. One gate_hold per held slot,
// exactly one gate_open, every held slot's own first child_start strictly
// after gate_open, and both event kinds carry a non-empty reason.
func TestPoolColdStartEventStreamOrderingAndReasons(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	done := make(chan string, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("first slot to start = %d, want 0 (the leader)", got)
	}
	nw.waitForLine(t, "\"event\":\"gate_hold\"")
	nw.waitForLine(t, "\"event\":\"gate_hold\"")

	r.fireOnIssue(t, 0, "42")

	seen := map[int]bool{0: true}
	for i := 0; i < slots-1; i++ {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started after the claim = %v, want all %d", seen, slots)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))

	var holds, opens, openIdx int
	var openReason string
	firstChildStart := map[int]int{}
	for i, ev := range events {
		switch ev.Event {
		case "gate_hold":
			holds++
			if ev.Reason == "" {
				t.Errorf("gate_hold event has empty reason: %+v", ev)
			}
		case "gate_open":
			opens++
			openIdx = i
			openReason = ev.Reason
		case "child_start":
			if ev.Slot == nil {
				t.Fatalf("child_start event missing slot: %+v", ev)
			}
			if _, ok := firstChildStart[*ev.Slot]; !ok {
				firstChildStart[*ev.Slot] = i
			}
		}
	}
	if holds != slots-1 {
		t.Fatalf("gate_hold events = %d, want %d (one per held slot)", holds, slots-1)
	}
	if opens != 1 {
		t.Fatalf("gate_open events = %d, want exactly 1", opens)
	}
	if openReason == "" {
		t.Fatalf("gate_open reason is empty, want a non-empty operator-facing reason")
	}
	for slot, idx := range firstChildStart {
		if slot == leadSlot {
			// The leader's own first child_start predates the gate
			// entirely — it never waits on its own gate.
			continue
		}
		if idx <= openIdx {
			t.Errorf("slot %d's first child_start at index %d does not follow gate_open at index %d", slot, idx, openIdx)
		}
	}

	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 7})
	}
	<-done
}

// TestPoolLeaderWindowClosesDuringResolveReleasesTheGate pins the
// reviewer's blocking finding on issue #3634: a daemon started at
// 16:59:50 with an Awake window of 09:00-17:00 lets every slot clear
// awaitWindow before the window shuts, so the siblings park on the gate,
// but the leader's own ResolveRevision fetch outlasts the window's close.
// The leader's first iteration then ends via the bare continue at the
// Awake re-check, never calling RunChild and never opening the gate
// directly — the siblings must not stay pinned on the gate for the shut
// window's whole remaining span; the top-of-loop release is what frees
// them, on the leader's very next iteration. With the fix disabled, the
// leader instead parks in awaitWindow for the whole shut span (the
// blocking clock's Sleep never returns on its own) and no gate_open ever
// fires — the failure this pins is the missing event, not a wrong reason.
func TestPoolLeaderWindowClosesDuringResolveReleasesTheGate(t *testing.T) {
	const slots = 3
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	// clk: like pool_test.go's parked testClock, Sleep blocks on
	// ctx.Done() rather than advancing, so a slot parked in awaitWindow
	// stays parked for the shut window's whole span, exactly as a real
	// clock would — but unlike a fixed-Now gate clock, this one reads a
	// mutable now that the leader's onResolve hook advances past the
	// window's close once its own fetch returns.
	clk := &testClock{now: time.Date(2026, 1, 1, 16, 59, 50, 0, time.UTC)}
	clk.park()
	// proceed lets the test hold the leader there until every sibling is
	// confirmed parked on the gate; returning advances the clock past the
	// window's close, so runSlot's post-fetch Awake check takes the bare
	// continue without ever starting a child. Every call after that first
	// one — the leader's own retries and any sibling's, once the gate
	// opens — blocks on ctx.Done() instead of resolving, so the pool goes
	// quiescent rather than spinning once the test is ready to assert.
	proceed := make(chan struct{})
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		onResolve: func(ctx context.Context, call int) error {
			if call == 1 {
				<-proceed
				clk.advanceBy(2 * time.Minute)
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		},
		onStart: func(ctx context.Context, req ChildRequest) error {
			return fmt.Errorf("must not be called: the window shut before the leader's fetch returned")
		},
	}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	cfg := testConfig(slots)
	cfg.Awake = win

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		done <- Loop(ctx, cfg, r, em, clk)
	}()

	// Every sibling clears awaitWindow (the window is open at 16:59:50) and
	// parks on the gate before the leader's fetch is allowed to return.
	for i := 0; i < slots-1; i++ {
		nw.waitForLine(t, "\"event\":\"gate_hold\"")
	}
	close(proceed)

	// The leader's fetch returns past the window's close (16:59:50 ->
	// 17:01:50): the gate must still open, on the leader's next iteration,
	// rather than the pool hanging until the window reopens at 09:00.
	nw.waitForLine(t, "\"event\":\"gate_open\"")
	cancel()

	reason := <-done
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want it to name context-cancelled", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	var holdIdx []int
	var opens, openIdx int
	var openReason string
	for i, ev := range events {
		switch ev.Event {
		case "gate_hold":
			holdIdx = append(holdIdx, i)
		case "gate_open":
			opens++
			openIdx = i
			openReason = ev.Reason
		}
	}
	if len(holdIdx) != slots-1 {
		t.Fatalf("gate_hold events = %d, want %d (one per held slot)", len(holdIdx), slots-1)
	}
	if opens != 1 {
		t.Fatalf("gate_open events = %d, want exactly 1", opens)
	}
	// Pairing invariant, not just a count: every gate_hold must actually be
	// followed by the one gate_open, not merely coexist with it.
	for _, hi := range holdIdx {
		if openIdx <= hi {
			t.Errorf("gate_open at index %d does not follow gate_hold at index %d", openIdx, hi)
		}
	}
	if !strings.Contains(openReason, "resolved without a claim") {
		t.Errorf("gate_open reason = %q, want it to say the round resolved without a claim", openReason)
	}
}

// TestPoolLeaderParkedOnShutWindowHoldsTheGateUntilItReopens pins the
// invariant a shut-window park must NOT release the gate (issue #3634): a
// shut window parks every slot, so releasing the leader into it would only
// hand the whole wave to the window's reopening instant — precisely the
// simultaneous-discovery race the gate exists to prevent. A daemon started
// milliseconds before an Awake window's close lets the siblings' own
// window reads land before the close (so they clear awaitWindow and park
// on the gate) while the leader's first read lands after it, parking the
// leader in awaitWindow on its very first iteration. The gate must stay
// held for the whole shut span and only open once the window reopens and
// the leader's first round actually resolves.
//
// The slots are staged by hand rather than through Loop: Loop starts every
// slot at once and the shared Clock has no slot identity, so there is no
// way there to land the leader's Now() after the close and the siblings'
// before it.
func TestPoolLeaderParkedOnShutWindowHoldsTheGateUntilItReopens(t *testing.T) {
	const slots = 3
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	clk := &testClock{now: time.Date(2026, 1, 1, 16, 59, 50, 0, time.UTC)}
	clk.park()
	// r is the leader's Runner once its own window read lands after the
	// siblings' (see below): ResolveRevision's first call fails outright,
	// the simplest way to reach a release (backoffOrHalt), so RunChild is
	// never called at all. Every later call — this slot's own retries, or
	// a sibling's once the gate opens — blocks on ctx.Done() instead, so
	// the stream goes quiescent while the test asserts.
	r := &scriptedRunner{
		onResolve: func(ctx context.Context, call int) error {
			if call == 1 {
				return errors.New("boom")
			}
			<-ctx.Done()
			return ctx.Err()
		},
		onStart: func(ctx context.Context, req ChildRequest) error {
			return fmt.Errorf("must not be called: the leader releases via a resolve failure before RunChild")
		},
	}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	cfg := testConfig(slots)
	cfg.Awake = win

	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	var wg sync.WaitGroup
	for s := 1; s < slots; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runSlot(pctx, s, cfg, p)
		}()
	}
	for i := 0; i < slots-1; i++ {
		nw.waitForLine(t, "\"event\":\"gate_hold\"")
	}

	// Only now does the window shut, so the leader's very first window read
	// — and no sibling's — finds it already closed.
	clk.setNow(time.Date(2026, 1, 1, 17, 0, 5, 0, time.UTC))

	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, leadSlot, cfg, p)
	}()

	// The leader parks on the shut window exactly like the siblings parked
	// on the gate; wait for the awake_close its own park reports.
	nw.waitForLine(t, "\"event\":\"awake_close\"")

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	var holdIdx []int
	for i, ev := range events {
		switch ev.Event {
		case "gate_hold":
			holdIdx = append(holdIdx, i)
		case "gate_open":
			t.Fatalf("gate_open at index %d reached the stream while the leader is still parked on the shut window", i)
		}
	}
	if len(holdIdx) != slots-1 {
		t.Fatalf("gate_hold events = %d, want %d (one per held sibling) before the window reopens", len(holdIdx), slots-1)
	}

	// Now reopen the window: the leader's blocked Sleep returns, its first
	// window read finds it open, and its first discovery round runs (and
	// fails, releasing the gate).
	clk.setNow(time.Date(2026, 1, 2, 9, 0, 5, 0, time.UTC))
	clk.wake()

	nw.waitForLine(t, "\"event\":\"gate_open\"")
	p.cancel()
	wg.Wait()

	if got := r.runCount(); got != 0 {
		t.Errorf("RunChild calls = %d, want 0 (the leader releases via a resolve failure)", got)
	}

	events = decodeEvents(t, bytes.NewBufferString(nw.String()))
	holdIdx = nil
	opens, openIdx := 0, -1
	var openReason string
	for i, ev := range events {
		switch ev.Event {
		case "gate_hold":
			holdIdx = append(holdIdx, i)
		case "gate_open":
			opens++
			openIdx = i
			openReason = ev.Reason
		}
	}
	if len(holdIdx) != slots-1 {
		t.Fatalf("gate_hold events = %d, want %d (one per held slot)", len(holdIdx), slots-1)
	}
	if opens != 1 {
		t.Fatalf("gate_open events = %d, want exactly 1", opens)
	}
	for _, hi := range holdIdx {
		if openIdx <= hi {
			t.Errorf("gate_open at index %d does not follow gate_hold at index %d", openIdx, hi)
		}
	}
	if !strings.Contains(openReason, "first discovery round failed") {
		t.Errorf("gate_open reason = %q, want it to name the leader's failure", openReason)
	}
}

// TestPoolColdStartInsideShutWindowGatesTheWaveAtOpen pins the reviewer's
// blocking finding: an ordinary cold start wholly inside a shut Awake
// window, the leader included, must not let every slot wake into discovery
// together once the window reopens. Before the fix, awaitWindow released
// the gate the instant the leader (the only slot the release actually
// affects) found the window shut — at process start, before any discovery
// ran — so the wave that hits the reopened window is exactly the
// double-Box race the gate exists to prevent.
func TestPoolColdStartInsideShutWindowGatesTheWaveAtOpen(t *testing.T) {
	const slots = 3
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	clk := &testClock{
		now:         time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC),
		sleepSignal: make(chan struct{}, slots),
	}
	clk.park()
	// proceed lets the test hold the leader's first ResolveRevision there
	// so it can assert exactly one call ran before releasing it. Every
	// later call (a sibling's, once the gate opens, or the leader's own
	// next iteration) blocks on ctx.Done() instead, so the stream goes
	// quiescent while the test asserts.
	proceed := make(chan struct{})
	// resolveStarted closes the instant the first call lands, before it
	// blocks on proceed: the leader and the siblings run on independent
	// goroutines with no ordering guarantee between "the leader entered
	// ResolveRevision" and "a sibling reached gate_hold", so the test
	// drains this rather than assuming the former already happened once
	// it observes the latter.
	resolveStarted := make(chan struct{})
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 2}},
		onResolve: func(ctx context.Context, call int) error {
			if call == 1 {
				close(resolveStarted)
				select {
				case <-proceed:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	cfg := testConfig(slots)
	cfg.Awake = win

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		done <- Loop(ctx, cfg, r, em, clk)
	}()

	// Every slot's first window read finds it shut — the leader included —
	// so all three park in awaitWindow rather than any reaching the gate.
	// Drain the entered barrier exactly `slots` times first: Loop starts
	// every slot at once with no per-slot identity to stage them by hand,
	// so without this, mutating now below can race ahead of the leader's
	// own first check.
	for i := 0; i < slots; i++ {
		<-clk.sleepSignal
	}
	nw.waitForLine(t, "\"event\":\"awake_close\"")

	clk.setNow(time.Date(2026, 1, 1, 9, 0, 1, 0, time.UTC))
	clk.wake()

	// The window opens for every slot at once; the siblings clear it and
	// park on the gate.
	for i := 0; i < slots-1; i++ {
		nw.waitForLine(t, "\"event\":\"gate_hold\"")
	}
	<-resolveStarted
	if got := r.resolveCount(); got != 1 {
		t.Fatalf("ResolveRevision calls = %d, want exactly 1 (the leader only) before the gate opens", got)
	}

	close(proceed)
	nw.waitForLine(t, "\"event\":\"gate_open\"")
	cancel()
	<-done

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	var holdIdx []int
	opens, openIdx, openWinIdx := 0, -1, -1
	for i, ev := range events {
		switch ev.Event {
		case "gate_hold":
			holdIdx = append(holdIdx, i)
		case "gate_open":
			opens++
			openIdx = i
		case "awake_open":
			if openWinIdx < 0 {
				openWinIdx = i
			}
		}
	}
	if len(holdIdx) != slots-1 {
		t.Fatalf("gate_hold events = %d, want %d", len(holdIdx), slots-1)
	}
	if opens != 1 {
		t.Fatalf("gate_open events = %d, want exactly 1", opens)
	}
	for _, hi := range holdIdx {
		if openIdx <= hi {
			t.Errorf("gate_open at index %d does not follow gate_hold at index %d", openIdx, hi)
		}
	}
	if openWinIdx < 0 || openWinIdx > holdIdx[0] {
		t.Errorf("awake_open at index %d does not precede the first gate_hold at index %d", openWinIdx, holdIdx[0])
	}
}
