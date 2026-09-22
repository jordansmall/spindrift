package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// awaitHalt waits for Loop to return, releasing any further child that
// starts while it drains. passBaton(batonPassChildEnded) fires
// unconditionally, before the releasing call's exit is interpreted and
// before a halt actually lands (runSlot never kills a child it has
// started), so the slot that takes the baton back can legitimately start
// one more round in the gap — a bare <-done would hang on it forever.
func awaitHalt(t *testing.T, r *scriptedRunner, done <-chan Halt) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case h := <-done:
			return h.String()
		case slot := <-r.started:
			r.releaseSlot(t, slot, ChildResult{Exit: 5})
		case <-deadline:
			t.Fatalf("awaitHalt: Loop did not return within 5s")
			return ""
		}
	}
}

// awaitWG is awaitHalt's counterpart for a test that drives runSlot
// directly against a WaitGroup rather than through Loop's done channel —
// the same childEnded-before-halt-interpreted gap applies, so a further
// start must be drained here too rather than left to hang wg.Wait forever.
func awaitWG(t *testing.T, r *scriptedRunner, wg *sync.WaitGroup) {
	t.Helper()
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-finished:
			return
		case slot := <-r.started:
			r.releaseSlot(t, slot, ChildResult{Exit: 5})
		case <-deadline:
			t.Fatalf("awaitWG: goroutines did not finish within 5s")
			return
		}
	}
}

// assertBatonPassReason fails the test unless events contains at least one
// baton_pass stamped with reason — the shared check every release-path test
// below ends on, once its own setup has driven the holder into that one
// path.
func assertBatonPassReason(t *testing.T, events []Event, reason string) {
	t.Helper()
	for _, ev := range events {
		if ev.Event == "baton_pass" && ev.Reason == reason {
			return
		}
	}
	t.Fatalf("events = %v, want a baton_pass with reason %q", eventNames(events), reason)
}

// childStartSlots extracts the slot of every child_start in events, in
// stream order — the shape the two failure tests below assert slot
// identity on.
func childStartSlots(t *testing.T, events []Event) []int {
	t.Helper()
	var slots []int
	for _, ev := range events {
		if ev.Event != "child_start" {
			continue
		}
		if ev.Slot == nil {
			t.Fatalf("child_start event missing slot: %+v", ev)
		}
		slots = append(slots, *ev.Slot)
	}
	return slots
}

// siblingSlot is the other slot in the two-slot failure tests below:
// leadSlot starts out holding the baton, so slot 1 is the one that parks
// on it and the one a release must hand off to.
const siblingSlot = 1

// pinTheFailingHolder parks clk so that a holder whose round fails cannot
// come back around and take its own baton again, which is what lets the
// failure tests below assert exactly which slot started each child.
//
// The obvious synchronisation — wait for the sibling's baton_hold line
// before letting the failure land — does not work: awaitBaton emits
// baton_hold *before* entering its blocking select (pool.go), so the line
// proves only that the sibling emitted, not that it is parked on the
// channel. The token can still land in the capacity-1 buffer and be
// re-taken by the failing holder through awaitBaton's own non-blocking
// receive, which makes the failing slot start the next child instead.
//
// A parked clock closes that off structurally rather than by timing:
// every failure path here reaches backoffOrHalt, which passes the baton
// and *then* sleeps out FailureBackoff. Parked, that sleep blocks until Loop's halt
// cancels ctx, so the failing holder is still sitting in it while the
// sibling runs — and once it does wake, its next haltIfStopping returns it
// rather than letting it start anything.
func pinTheFailingHolder(clk *testClock) {
	clk.park()
}

// awaitSiblingStart drains starts until siblingSlot's own round begins,
// handing each further round leadSlot wins the same again result. It
// returns with siblingSlot's child in flight, for the caller to release.
//
// The two no-work tests below use this rather than pinning the very next
// start, because on a Wait exit the holder loops straight back around
// with nothing to block it — no backoff sleep for pinTheFailingHolder to
// park — and the pool deliberately has no FIFO waiter queue (issue
// #3684), so awaitBaton's non-blocking receive lets a holder that has
// just passed the baton re-take it from the buffer before a sibling that
// has already emitted baton_hold reaches its own blocking receive.
// Asserting "the next child is the sibling's" there would assert fairness
// the design does not claim, and it flaked accordingly. What the design
// does guarantee, and what this pins, is that the sibling is never
// stranded: the baton reaches it.
func awaitSiblingStart(t *testing.T, r *scriptedRunner, again ChildResult) {
	t.Helper()
	const rounds = 8
	for i := 0; i < rounds; i++ {
		slot := r.awaitStart(t)
		if slot == siblingSlot {
			return
		}
		r.releaseSlot(t, slot, again)
	}
	t.Fatalf("slot %d never started across %d rounds: the holder monopolised the baton rather than passing it on", siblingSlot, rounds)
}

// TestPoolBatonSerializesDiscoveryOnColdStart pins issue #3684's AC1: at
// most one child is in its discovery phase at any moment, on the first
// wave exactly as much as on every refill. On a cold start, only the
// pre-assigned initial holder (slot 0) starts discovery; every other slot
// parks on baton_hold rather than running its own discovery against the
// same tracker snapshot. Once the holder announces a claim (live, via
// OnIssue, not after its child exits), the baton passes to exactly one
// waiting slot — never both at once, which is the whole point of a token
// versus the old one-shot gate that released every sibling together.
func TestPoolBatonSerializesDiscoveryOnColdStart(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	// Only the initial holder (slot 0) starts discovery at first.
	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("first slot to start = %d, want 0 (the initial baton holder)", got)
	}

	// The other two slots park on the baton instead of starting their own
	// discovery: wait for both to report it.
	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	// Structurally guaranteed, not a timing race: a slot parked in
	// awaitBaton cannot reach RunChild until the baton reaches it, so
	// r.started must have nothing further to give yet.
	select {
	case got := <-r.started:
		t.Fatalf("sibling slot %d started before the holder claimed anything, want the pool held", got)
	default:
	}

	// The holder announces a claim while its child is still running: the
	// baton passes at once, but to exactly one waiting slot — the third
	// must stay parked.
	r.fireOnIssue(t, 0, "42")

	firstReleased := r.awaitStart(t)
	if firstReleased == 0 {
		t.Fatalf("first slot released after the claim = %d, want a sibling, not the holder itself", firstReleased)
	}

	select {
	case got := <-r.started:
		t.Fatalf("a second sibling (slot %d) started before the newly-released slot claimed anything, want only one slot released at a time", got)
	default:
	}

	// The newly-released slot announces its own claim: the baton passes
	// again, releasing the third and last slot.
	r.fireOnIssue(t, firstReleased, "43")

	seen := map[int]bool{0: true, firstReleased: true}
	seen[r.awaitStart(t)] = true
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want all %d", seen, slots)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	var holds, passes int
	for _, ev := range events {
		switch ev.Event {
		case "baton_hold":
			holds++
		case "baton_pass":
			passes++
		}
	}
	if holds != slots-1 {
		t.Errorf("baton_hold events = %d, want exactly %d (one per sibling)", holds, slots-1)
	}
	if passes != slots-1 {
		t.Errorf("baton_pass events = %d, want exactly %d (one per claim that released a sibling)", passes, slots-1)
	}
	// Every pass above is claim-driven (fireOnIssue), so the stream's
	// reason must say so on every one of them — not merely on one of
	// them, which is all the shared assertBatonPassReason checks.
	for _, ev := range events {
		if ev.Event == "baton_pass" && ev.Reason != batonPassClaimed {
			t.Errorf("baton_pass reason = %q, want every pass in this test stamped %q", ev.Reason, batonPassClaimed)
		}
	}

	// End the test: release every slot's in-flight child.
	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 5})
	}
	reason := (<-done).String()
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}
}

// TestPoolSingleSlotNeverHoldsABaton pins that a single-slot pool builds no
// baton at all (see newPool): the sole slot has no sibling to serialize
// against, so there is no discovery to stagger and the stream must carry no
// baton_hold or baton_pass event.
func TestPoolSingleSlotNeverHoldsABaton(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	events := decodeEvents(t, &buf)
	for _, ev := range events {
		if ev.Event == "baton_hold" || ev.Event == "baton_pass" {
			t.Fatalf("events = %v, want no baton event for a single-slot pool", eventNames(events))
		}
	}
}

// TestPoolBatonCancellationNeverDeadlocksAWaitingSlot pins issue #3684's
// AC5: a slot parked in awaitBaton must never hang past ctx cancellation.
// The initial holder's own ResolveTip call (guaranteed to be call 1:
// every sibling is still parked in awaitBaton, so none has reached
// ResolveTip yet) blocks on ctx.Done() rather than returning, so the
// holder never starts a child and never passes the baton on its own; once
// a sibling has reported its own baton_hold, the ctx is cancelled and Loop
// must still return rather than leaving that sibling stuck on <-p.baton
// forever.
func TestPoolBatonCancellationNeverDeadlocksAWaitingSlot(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		onResolve: func(ctx context.Context, call int) error {
			if call == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
	}
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Halt, 1)
	go func() {
		done <- Loop(ctx, testConfig(slots), r, em, clk)
	}()

	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	cancel()

	select {
	case h := <-done:
		if !strings.Contains(h.String(), "context-cancelled") {
			t.Fatalf("halt reason = %q, want it to name context-cancelled", h.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Loop never returned: a slot parked in awaitBaton deadlocked on cancellation")
	}
}

// --- one test per baton release path (issue #3684's release-path AC) ---
//
// Every test below shares one shape: a two-slot pool where the initial
// holder (slot 0, leadSlot) ends its round by exactly one release path, and
// the assertion is that the pool moves rather than stalls on it — the
// waiting sibling's own child goes on to start, and the stream carries a
// baton_pass stamped with the reason that path owns. A pool that forgot to
// pass the baton on any one of these would leave the sibling parked on
// baton_hold forever, indistinguishable from a genuine hang.

// TestPoolBatonPassesOnQueueEmptyChildEnd pins the post-RunChild release for
// a queue-empty child (exit 2): the holder's round ends having found no work
// at all, and the baton must still move — a pool that held it through every
// empty-queue result would let one slot's dry queue stall its sibling's
// discovery too.
//
// cfg carries a second Kind, Research, alongside Dispatch: exit 2 gates
// Dispatch's own kindBackoff (loop.go's Wait case), and that backoff is
// pool-wide, not per-slot (issue #3541) — with only one configured Kind the
// sibling's very next pickKind would find it gated too and bounce the baton
// straight back rather than starting its own child, which is not the
// property this test pins. A second, ungated Kind gives the sibling
// somewhere to go.
func TestPoolBatonPassesOnQueueEmptyChildEnd(t *testing.T) {
	const slots = 2
	cfg := testConfig(slots)
	cfg.Kinds = []Kind{KindDispatch, KindResearch}
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	if got := r.awaitStart(t); got != leadSlot {
		t.Fatalf("first slot to start = %d, want %d (the initial baton holder)", got, leadSlot)
	}
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	r.releaseSlot(t, leadSlot, ChildResult{Exit: 2})

	awaitSiblingStart(t, r, ChildResult{Exit: 2})

	r.releaseSlot(t, siblingSlot, ChildResult{Exit: 5})
	reason := awaitHalt(t, r, done)
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassChildEnded)
}

// TestPoolBatonPassesOnNoneDispatchableChildEnd is
// TestPoolBatonPassesOnQueueEmptyChildEnd's sibling for exit 3: work
// existed but every issue found was claimed or overlap-deferred, a
// different outcome label (outcomeNoneDispatchable) than exit 2's, but the
// same post-RunChild release site — the baton must move on both alike, not
// just the plain empty-queue case.
//
// cfg carries a second Kind for the same reason as
// TestPoolBatonPassesOnQueueEmptyChildEnd: exit 3 gates Dispatch's own
// pool-wide kindBackoff too, so a lone Kind would bounce the baton straight
// back to the holder instead of letting the sibling start its own child.
func TestPoolBatonPassesOnNoneDispatchableChildEnd(t *testing.T) {
	const slots = 2
	cfg := testConfig(slots)
	cfg.Kinds = []Kind{KindDispatch, KindResearch}
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	if got := r.awaitStart(t); got != leadSlot {
		t.Fatalf("first slot to start = %d, want %d (the initial baton holder)", got, leadSlot)
	}
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	r.releaseSlot(t, leadSlot, ChildResult{Exit: 3})

	awaitSiblingStart(t, r, ChildResult{Exit: 3})

	r.releaseSlot(t, siblingSlot, ChildResult{Exit: 5})
	reason := awaitHalt(t, r, done)
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassChildEnded)
}

// TestPoolBatonPassesOnUnrecognisedExit pins the same post-RunChild release
// site for an unrecognised exit code (Interpret's default case, Backoff):
// the child ended abnormally rather than announcing anything, and the
// release must still be honest about it — batonPassChildEnded, the same
// reason a clean no-work exit gets, since from the baton's point of view
// both are "the holder's child ended without a claim".
func TestPoolBatonPassesOnUnrecognisedExit(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	// An unrecognised exit is Interpret's Backoff case, so the holder ends
	// this round in backoffOrHalt exactly as the two failure tests below
	// do — park the clock for the same reason, to pin which slot starts
	// next.
	pinTheFailingHolder(clk)
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	if got := r.awaitStart(t); got != leadSlot {
		t.Fatalf("first slot to start = %d, want %d (the initial baton holder)", got, leadSlot)
	}
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	// 99 is not among Interpret's recognised codes (0, 2-7): it falls to
	// the default case, Backoff.
	r.releaseSlot(t, leadSlot, ChildResult{Exit: 99})

	next := r.awaitStart(t)
	if next != siblingSlot {
		t.Fatalf("next slot to start = %d, want %d: the sibling parked on the baton, not the holder re-acquiring", next, siblingSlot)
	}

	r.releaseSlot(t, next, ChildResult{Exit: 5})
	reason := awaitHalt(t, r, done)
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassChildEnded)
}

// TestPoolBatonPassesOnSeamFailure pins the same post-RunChild release site
// for a RunChild seam error (the child could not even be started/waited on,
// not a real exit code at all): the release still fires there, before
// backoffOrHalt's own passBaton call, so the reason on the stream honestly
// says a child ended rather than claiming the round failed before starting
// one.
func TestPoolBatonPassesOnSeamFailure(t *testing.T) {
	const slots = 2
	wantErr := errors.New("boom")
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		runErrAt:  1,
		runErr:    wantErr,
		results:   []ChildResult{{Exit: 5}},
	}
	clk := &testClock{}
	pinTheFailingHolder(clk)

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	var h Halt
	select {
	case h = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop never returned: a RunChild seam error left the pool deadlocked")
	}
	reason := h.String()
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassChildEnded)
	for _, ev := range events {
		if ev.Event == "baton_pass" && ev.Reason == batonPassFailed {
			t.Fatalf("baton_pass reason = %q, want batonPassChildEnded: a child_start/child_finish pair already reached the stream for this round, so the holder did not fail before starting one", ev.Reason)
		}
	}

	// runErrAt=1 seam-errors leadSlot's own RunChild call, so the round
	// that follows is siblingSlot's — the only other configured slot
	// (slots==2) — and with the clock parked exactly two children start,
	// in exactly that order: leadSlot's seam-failed one, then the
	// sibling's, whose exit 5 halts the pool.
	startSlots := childStartSlots(t, events)
	if len(startSlots) != 2 || startSlots[0] != leadSlot || startSlots[1] != siblingSlot {
		t.Fatalf("child_start slots = %v, want exactly [%d %d]: the sibling parked on the baton must start next, not the failing holder re-acquiring", startSlots, leadSlot, siblingSlot)
	}
}

// TestPoolBatonPassesWhenWindowClosesBeforeChildStart pins the pre-child
// Awake re-check in runSlot: the window can shut between the holder's
// ResolveTip fetch and its own child_start, and the holder is about to
// loop back into awaitWindow for the whole shut span — holding the baton
// through that would stall the sibling's own discovery for no reason.
func TestPoolBatonPassesWhenWindowClosesBeforeChildStart(t *testing.T) {
	const slots = 2
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	clk := &testClock{now: time.Date(2026, 1, 1, 16, 59, 50, 0, time.UTC)}
	clk.park()
	proceed := make(chan struct{})
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		onResolve: func(ctx context.Context, call int) error {
			if call == 1 {
				// call 1 is guaranteed the holder's own: the sibling is
				// still parked on the baton and has not reached
				// ResolveTip yet.
				<-proceed
				clk.setNow(clk.Now().Add(2 * time.Minute))
			}
			return nil
		},
	}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	cfg := testConfig(slots)
	cfg.Awake = win

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Halt, 1)
	go func() {
		done <- Loop(ctx, cfg, r, em, clk)
	}()

	// The sibling clears awaitWindow (the window is still open at
	// 16:59:50) and parks on the baton before the holder's fetch is
	// allowed to return.
	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	close(proceed)

	// The holder's fetch returns past the window's close (16:59:50 ->
	// 17:01:50): the baton must still pass, on the holder's next
	// iteration, rather than the pool hanging on it until the window
	// reopens at 09:00. With a shut window the sibling parks straight
	// back into awaitWindow too, so a child starting is not the right
	// signal here — the baton_pass event, seen below, is.
	nw.waitForLine(t, "\"event\":\"baton_pass\"")
	cancel()

	var h Halt
	select {
	case h = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop never returned: a window close mid-resolve left the pool deadlocked")
	}
	reason := h.String()
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want it to name context-cancelled", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassWindowClosed)
	for _, ev := range events {
		if ev.Event == "child_start" {
			t.Fatalf("events include child_start, want none: the window was shut before either slot could start a child")
		}
	}
}

// TestPoolBatonColdStartInsideShutWindowGatesDiscoveryAtOpen pins runSlot's
// comment above p.awaitBaton (loop.go): apart from the pre-assigned initial
// holder's very first round, a slot never parks in awaitWindow holding the
// baton — acquisition happens only after awaitWindow returns. A cold start
// wholly inside a shut Awake window puts every slot, the holder included,
// through awaitWindow first: none of them has reached awaitBaton yet, so
// the stream must carry no baton_hold at all for the whole shut span, not
// just none from the holder. Once the window reopens, discovery resumes
// exactly as serialized as any other round: the holder (already holding
// the token from newPool) starts at once, and a sibling's baton_hold only
// clears once the holder's own claim passes it on.
func TestPoolBatonColdStartInsideShutWindowGatesDiscoveryAtOpen(t *testing.T) {
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
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	cfg := testConfig(slots)
	cfg.Awake = win

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	// Every slot's first window read finds it shut — the holder included —
	// so all three park in clk.Sleep from inside awaitWindow, none having
	// reached awaitBaton yet. Draining exactly `slots` sleep entries proves
	// all three, not just the first, actually got there.
	clk.awaitSleep(t, slots)

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	for _, ev := range events {
		if ev.Event == "baton_hold" {
			t.Fatalf("events include baton_hold while every slot is still parked on the shut window, want none: the carve-out is that only the pre-assigned holder may hold the baton while parked outside awaitBaton, and here no slot — holder included — has reached awaitBaton at all")
		}
	}

	// Reopen the window: every slot's Sleep returns at once. The holder
	// proceeds straight through (it already holds the token), the two
	// siblings each miss the non-blocking receive and park on baton_hold.
	clk.step(time.Date(2026, 1, 1, 9, 0, 1, 0, time.UTC))

	if got := r.awaitStart(t); got != leadSlot {
		t.Fatalf("first slot to start = %d, want %d (the initial baton holder, released by the window reopening alone)", got, leadSlot)
	}
	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	// Structurally guaranteed, not a timing race: both siblings are parked
	// in awaitBaton and cannot reach RunChild until the holder claims
	// something, so r.started must have nothing further to give yet.
	select {
	case got := <-r.started:
		t.Fatalf("a sibling slot %d started before the holder claimed anything, want the pool held through the reopen", got)
	default:
	}

	r.fireOnIssue(t, leadSlot, "42")

	winner := r.awaitStart(t)
	if winner == leadSlot {
		t.Fatalf("slot released after the claim = %d, want a sibling, not the holder itself", winner)
	}

	events = decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassClaimed)

	// End the test: release every slot's in-flight child (the third never
	// got released above, so it is still parked on baton_hold).
	r.releaseSlot(t, leadSlot, ChildResult{Exit: 5})
	r.releaseSlot(t, winner, ChildResult{Exit: 5})
	reason := awaitHalt(t, r, done)
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}
}

// TestPoolBatonPassesOnPreChildFailure pins the backoffOrHalt release site
// for a failure that lands before any child ever starts (here, the
// holder's very first ResolveTip): the holder is about to back off
// alone, and holding the pool through that backoff would stall the
// sibling's own discovery on a problem backoffOrHalt already handles
// per-slot. Call 1 is guaranteed the holder's own: the sibling is still
// parked on the baton and has not reached ResolveTip yet.
func TestPoolBatonPassesOnPreChildFailure(t *testing.T) {
	const slots = 2
	wantErr := errors.New("boom")
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &scriptedRunner{
		revisions:  []string{"rev1"},
		resolveAt:  1,
		resolveErr: wantErr,
		results:    []ChildResult{{Exit: 5}},
	}
	clk := &testClock{}
	pinTheFailingHolder(clk)

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	var h Halt
	select {
	case h = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop never returned: a holder that failed before starting a child left the pool deadlocked")
	}
	reason := h.String()
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassFailed)

	// resolveAt=1 fails leadSlot before it ever reaches child_start, so
	// the only child that starts at all is siblingSlot's: with the clock
	// parked, leadSlot never gets a second round in which to start one of
	// its own.
	startSlots := childStartSlots(t, events)
	if len(startSlots) != 1 || startSlots[0] != siblingSlot {
		t.Fatalf("child_start slots = %v, want exactly [%d]: the sibling parked on the baton must be the one to start, not the failing holder", startSlots, siblingSlot)
	}
}

// TestPoolBatonPassesWhenNoKindIsRunnable pins the pickKind-fails release
// (batonPassIdle): every slot shares one kindBackoff per configured kind
// (issue #3541), so once a no-work result gates the pool's only kind, a
// holder whose own pickKind then comes up empty must not sleep out its idle
// wait holding the baton — that would strand every sibling's discovery on
// the same drought, not just the one kind that is dry.
//
// The kind is seeded gated directly, before either slot starts, rather
// than driven by a real no-work RunChild: passBaton(batonPassChildEnded)
// fires before markNoWork in loop.go's own Wait case, so racing a live
// no-work exit against the sibling's very next pickKind call would leave
// this test's own outcome dependent on which side of that gap the sibling
// happened to land on. Seeding up front removes that gap entirely — both
// slots deterministically see the kind already gated.
func TestPoolBatonPassesWhenNoKindIsRunnable(t *testing.T) {
	const slots = 2
	cfg := testConfig(slots)
	r := &scriptedRunner{}
	clk := &testClock{sleepSignal: make(chan struct{}, slots)}
	clk.park()
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()
	p.markNoWork(KindDispatch, clk.Now(), false)
	r.holdSlots(slots)

	var wg sync.WaitGroup
	wg.Add(slots)
	for s := 0; s < slots; s++ {
		go func(s int) {
			defer wg.Done()
			runSlot(pctx, s, cfg, p)
		}(s)
	}

	// Both slots independently find the seeded kind gated and park in
	// idleSleep's Sleep: the holder on its very first pickKind, the
	// sibling the instant it takes the baton the holder just passed.
	clk.awaitSleep(t, slots)

	events := decodeEvents(t, &buf)
	assertBatonPassReason(t, events, batonPassIdle)

	// Advance past the seeded deadline and release both parked slots:
	// pickKind now finds the kind runnable again, so whichever slot wins
	// the freed baton goes on to start its own child — the pool resumes
	// rather than stalling on the drought.
	clk.step(clk.Now().Add(time.Hour))

	started := r.awaitStart(t)
	r.releaseSlot(t, started, ChildResult{Exit: 5})
	awaitWG(t, r, &wg)
}

// TestPoolBatonPassesWhenHolderStopsBeforeResolving pins runSlot's own
// deferred passBaton (batonPassStopped): a holder that stops before any of
// the other release sites ever resolves — here, an operator cancellation
// landing inside its very first ResolveTip — must still release the
// baton on its way out, or the sibling parked in awaitBaton would hang
// forever on a token nothing else will ever pass. Complements, and does
// not duplicate,
// TestPoolBatonCancellationNeverDeadlocksAWaitingSlot: that test asserts
// the waiting sibling unblocks; this one asserts the holder's own exit
// stamps its own reason.
func TestPoolBatonPassesWhenHolderStopsBeforeResolving(t *testing.T) {
	const slots = 2
	ctx, cancel := context.WithCancel(context.Background())
	proceed := make(chan struct{})
	r := &scriptedRunner{
		onResolve: func(rctx context.Context, call int) error {
			if call == 1 {
				// call 1 is guaranteed the holder's own: the sibling is
				// still parked on the baton and has not reached
				// ResolveTip yet.
				<-proceed
				cancel()
				return rctx.Err()
			}
			return nil
		},
	}
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(ctx, testConfig(slots), r, em, clk)
	}()

	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	close(proceed)

	var h Halt
	select {
	case h = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop never returned: a holder that stopped before resolving anything left the sibling parked")
	}
	reason := h.String()
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want it to name context-cancelled", reason)
	}

	// backoffOrHalt checks haltIfStopping before it ever touches the baton
	// (issue #3626), and this ctx cancellation is exactly what that check
	// catches — so backoffOrHalt returns without passing the baton itself,
	// and it is runSlot's own deferred passBaton that fires as the holder
	// unwinds. The reason is batonPassStopped, not batonPassFailed: the
	// holder returned before its discovery round resolved, so the pass
	// this test observes is runSlot's, not backoffOrHalt's.
	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassStopped)
}

// TestPoolBatonSerializesRefillsAfterTheFirstWave pins issue #3684's own
// sharpest difference from the deleted one-shot gate: the gate released
// every sibling together once, then stopped gating at all. The baton must
// keep applying to *every* refill, not just the cold start — two slots
// finishing in the same tick still discover one after the other, not both
// at once. Every claim below is driven manually via fireOnIssue (not
// announceEachSlot), so the test controls exactly when each replacement's
// own claim reaches the pool rather than racing it against RunChild's own
// entry.
func TestPoolBatonSerializesRefillsAfterTheFirstWave(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	// Drive a genuine first wave: the pre-assigned holder claims, freeing
	// the baton to the next parked sibling, and so on, until all three are
	// in flight together.
	first := r.awaitStart(t)
	if first != leadSlot {
		t.Fatalf("first slot to start = %d, want %d (the initial baton holder)", first, leadSlot)
	}
	// Wait for both siblings to genuinely park before releasing the first
	// claim: without this, one sibling's own first awaitBaton call can
	// still be in flight when the claim lands, letting it grab the token
	// via the non-blocking receive with no hold of its own — leaving the
	// wave1 hold count that the refill's own count is measured against
	// under-counted.
	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	r.fireOnIssue(t, first, "issue-a")
	second := r.awaitStart(t)
	r.fireOnIssue(t, second, "issue-b")
	third := r.awaitStart(t)
	// third's own claim frees the baton with no sibling left to hand it
	// to: a spare token, not a hold, waits in the channel from here.
	r.fireOnIssue(t, third, "issue-c")

	if peak := r.peakConcurrency(); peak != slots {
		t.Fatalf("peakConcurrency = %d, want %d: the first wave never had all three slots in flight at once", peak, slots)
	}

	// Release two of the three in-flight children together: both refill in
	// the same tick. Exit 0 (dispatched, Continue) rather than a no-work
	// exit deliberately: a no-work exit gates this pool's own single Kind
	// through the pool-wide kindBackoff (issue #3541), which would send
	// both slots through an extra idleSleep/batonPassIdle round-trip and
	// entangle that gate's own timing with the property this test pins.
	r.releaseSlot(t, first, ChildResult{Exit: 0})
	r.releaseSlot(t, second, ChildResult{Exit: 0})

	winner := r.awaitStart(t)
	if winner != first && winner != second {
		t.Fatalf("first replacement to start = %d, want one of the two released slots (%d, %d)", winner, first, second)
	}

	// The winner grabbed the spare token wave1 left behind, silently — no
	// hold of its own. The loser finds the channel already empty and
	// parks: wait for its baton_hold (the wave1 pair was already drained
	// above, so this is the refill's own, freshly emitted one) so the
	// select below observes a genuinely parked loser, not a lucky
	// scheduling gap.
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	// Structurally guaranteed, not a timing race: the loser is parked on
	// awaitBaton and cannot reach RunChild until the winner claims
	// something, so r.started must have nothing further to give yet.
	select {
	case got := <-r.started:
		t.Fatalf("a second replacement (slot %d) started before the first announced, want the two refills serialized", got)
	default:
	}

	r.fireOnIssue(t, winner, "issue-d")

	loser := r.awaitStart(t)
	if loser == winner || (loser != first && loser != second) {
		t.Fatalf("second replacement to start = %d, want the other released slot", loser)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	var holds int
	for _, ev := range events {
		if ev.Event == "baton_hold" {
			holds++
		}
	}
	// Two from the first wave's siblings (second, third), one more from
	// the loser's own refill wait: the baton applies past the cold start.
	if holds != slots {
		t.Errorf("baton_hold events = %d, want %d (%d from the first wave's siblings, one more from the refill)", holds, slots, slots-1)
	}

	r.releaseSlot(t, third, ChildResult{Exit: 5})
	r.releaseSlot(t, winner, ChildResult{Exit: 5})
	r.releaseSlot(t, loser, ChildResult{Exit: 5})
	reason := awaitHalt(t, r, done)
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}
}

// TestPoolOneKindPoolBehavesAsBefore pins issue #3684's AC2 for the
// single-Kind half: a multi-slot pool restricted to just KindDispatch
// (testConfig's default) still fills every slot with that one kind — the
// baton only serializes *discovery*, it must never leave a slot starved
// just because there is nothing else to pick.
func TestPoolOneKindPoolBehavesAsBefore(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	r.announceEachSlot()
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	seen := map[int]bool{}
	for i := 0; i < slots; i++ {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want all %d slots used", seen, slots)
	}

	for _, call := range r.calls() {
		if call.Kind != KindDispatch {
			t.Fatalf("RunChild call %+v used kind %q, want only %q for a one-kind pool", call, call.Kind, KindDispatch)
		}
	}

	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 5})
	}
	reason := awaitHalt(t, r, done)
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}
}

// TestPoolBatonEventStreamOrderingAndReasons pins issue #3684's AC6, the
// operator-facing contract the gate pair of #3634 used to carry: every
// baton_hold and baton_pass names its slot and carries a
// non-empty reason, so an operator can tell which slot is discovering and
// why control changed hands, and a held slot's own first child_start lands
// after some baton_pass. Not necessarily the very next one, though —
// awaitBaton's non-blocking receive lets a slot that wins a race log its
// own baton_hold *after* the baton_pass that actually freed a different
// sibling (see awaitBaton's own doc) — so this stops short of asserting a
// single global hold-before-pass ordering.
func TestPoolBatonEventStreamOrderingAndReasons(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	nw := newNotifyWriter()
	em := NewEmitter(nw, func() time.Time { return time.Unix(0, 0).UTC() })

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	first := r.awaitStart(t)
	if first != leadSlot {
		t.Fatalf("first slot to start = %d, want %d (the initial baton holder)", first, leadSlot)
	}
	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	r.fireOnIssue(t, first, "42")
	second := r.awaitStart(t)
	r.fireOnIssue(t, second, "43")
	third := r.awaitStart(t)

	seen := map[int]bool{first: true, second: true, third: true}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want all %d", seen, slots)
	}

	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 5})
	}
	reason := awaitHalt(t, r, done)
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))

	var passIdxs []int
	firstChildStart := map[int]int{}
	for i, ev := range events {
		switch ev.Event {
		case "baton_hold":
			if ev.Slot == nil {
				t.Fatalf("baton_hold event missing slot: %+v", ev)
			}
			if ev.Reason == "" {
				t.Errorf("baton_hold event has empty reason: %+v", ev)
			}
		case "baton_pass":
			if ev.Slot == nil {
				t.Fatalf("baton_pass event missing slot: %+v", ev)
			}
			if ev.Reason == "" {
				t.Errorf("baton_pass event has empty reason: %+v", ev)
			}
			passIdxs = append(passIdxs, i)
		case "child_start":
			if ev.Slot == nil {
				t.Fatalf("child_start event missing slot: %+v", ev)
			}
			if _, ok := firstChildStart[*ev.Slot]; !ok {
				firstChildStart[*ev.Slot] = i
			}
		}
	}

	if len(passIdxs) == 0 {
		t.Fatalf("events = %v, want at least one baton_pass", eventNames(events))
	}

	for slot, csIdx := range firstChildStart {
		if slot == leadSlot {
			// The pre-assigned initial holder never waits on its own
			// baton: its first child_start can predate every baton_pass.
			continue
		}
		found := false
		for _, pIdx := range passIdxs {
			if pIdx < csIdx {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("slot %d's first child_start at index %d has no baton_pass before it", slot, csIdx)
		}
	}
}

// TestReferenceDocBatonReasonsMatchConstants binds the baton_hold and
// baton_pass rows of docs/reference.md's event table to pool.go's actual
// reason constants. The docs reproduce every one of them verbatim for
// operators reading the event stream, and unlike the marker/parity checks
// elsewhere in this repo nothing else catches one side drifting out from
// under the other — an edit to a constant here or to the prose there would
// otherwise go unnoticed until an operator matched a log line against a
// stale sentence.
//
// Both sides are read from source rather than restated here, following
// TestReferenceDocLabelSnippetMatchesTriageDefaults (main_test.go): the
// constants are parsed out of pool.go, so a newly added batonPass* has to
// be documented rather than silently escaping a hand-maintained list, and
// the docs side is narrowed to the two table rows, so a reason quoted
// anywhere else in a 5000-line file cannot stand in for the row that is
// actually meant to carry it. The reverse direction is checked too: a row
// naming a constant pool.go no longer declares is as stale as a row
// quoting the wrong string.
func TestReferenceDocBatonReasonsMatchConstants(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "reference.md"))
	if err != nil {
		t.Fatalf("read docs/reference.md: %v", err)
	}
	rows := regexp.MustCompile("(?m)^\\| `baton_(?:hold|pass)`.*$").FindAllString(string(raw), -1)
	if len(rows) != 2 {
		t.Fatalf("docs/reference.md event-table rows for baton_hold/baton_pass = %d, want 2", len(rows))
	}
	table := strings.Join(rows, "\n")

	src, err := os.ReadFile("pool.go")
	if err != nil {
		t.Fatalf("read pool.go: %v", err)
	}
	declared := regexp.MustCompile(`(?m)^\t(baton(?:Pass|Hold)\w*)\s+=\s+"([^"]*)"$`).FindAllStringSubmatch(string(src), -1)
	if len(declared) == 0 {
		// Without this the loop below would pass vacuously if the const
		// block were ever reshaped out from under the pattern.
		t.Fatalf("pool.go declares no baton reason constants matching the expected const-block shape")
	}

	names := make(map[string]bool, len(declared))
	for _, d := range declared {
		name, reason := d[1], d[2]
		names[name] = true
		if !strings.Contains(table, reason) {
			t.Errorf("the baton table rows are missing %s's reason string verbatim: %q", name, reason)
		}
		if !strings.Contains(table, "`"+name+"`") {
			t.Errorf("the baton table rows never name %s, so an operator cannot map its string back to code", name)
		}
	}

	for _, m := range regexp.MustCompile("`(baton(?:Pass|Hold)\\w*)`").FindAllStringSubmatch(table, -1) {
		if !names[m[1]] {
			t.Errorf("the baton table rows name %s, which pool.go no longer declares", m[1])
		}
	}
}
