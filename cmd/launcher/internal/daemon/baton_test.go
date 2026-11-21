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

	"spindrift.dev/launcher/internal/dispatchkind"
)

// awaitHalt waits for Loop to return, failing the test if any slot starts a
// further child while it waits: the baton passes only after the round's halt
// decision, so no sibling can start one more after a halt.
func awaitHalt(t *testing.T, r *scriptedRunner, done <-chan Halt) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	select {
	case h := <-done:
		return h.String()
	case slot := <-r.started:
		t.Fatalf("awaitHalt: slot %d started a child while Loop was halting", slot)
	case <-deadline:
		t.Fatalf("awaitHalt: Loop did not return within 5s")
	}
	return ""
}

// awaitWG is awaitHalt's counterpart for a test that drives runSlot
// directly against a WaitGroup rather than through Loop's done channel.
func awaitWG(t *testing.T, r *scriptedRunner, wg *sync.WaitGroup) {
	t.Helper()
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	deadline := time.After(5 * time.Second)
	select {
	case <-finished:
	case slot := <-r.started:
		t.Fatalf("awaitWG: slot %d started a child while the pool was halting", slot)
	case <-deadline:
		t.Fatalf("awaitWG: goroutines did not finish within 5s")
	}
}

// assertBatonPassReason fails the test unless events contains at least one
// baton_pass stamped with reason.
func assertBatonPassReason(t *testing.T, events []Event, reason batonReason) {
	t.Helper()
	for _, ev := range events {
		if ev.Event == "baton_pass" && ev.Reason == string(reason) {
			return
		}
	}
	t.Fatalf("events = %v, want a baton_pass with reason %q", eventNames(events), reason)
}

// childStartSlots extracts the slot of every child_start in events, in
// stream order.
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

// siblingSlot is the other slot in the two-slot tests below:
// leadSlot starts out holding the baton, so slot 1 is the one that parks
// on it and the one a release must hand off to.
const siblingSlot = 1

// pinTheFailingHolder parks clk so that a holder whose round fails cannot
// come back around and re-take its own baton, letting the failure tests
// assert exactly which slot started each child.
//
// Waiting for the sibling's baton_hold is not enough: awaitBaton emits it
// *before* its blocking select, so the token can still land in the
// capacity-1 buffer and be re-taken by the failing holder's non-blocking
// receive. A parked clock closes that off structurally: backoffOrHalt
// passes the baton and *then* sleeps out FailureBackoff, which blocks until
// Loop's halt cancels ctx; on waking, haltIfStopping returns the holder
// before it can start anything.
func pinTheFailingHolder(clk *testClock) {
	clk.park()
}

// TestPoolBatonSerializesDiscoveryOnColdStart pins issue #3684's AC1: at
// most one child is in its discovery phase at any moment, on the first
// wave too. Only the pre-assigned holder (slot 0) starts discovery; once it
// announces a claim (live, via OnRecord's box record), the baton passes to
// exactly one waiting slot, never both at once.
func TestPoolBatonSerializesDiscoveryOnColdStart(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	nw := newNotifyWriter()
	em := newTestEmitter(nw)

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	// Only the initial holder (slot 0) starts discovery at first.
	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("first slot to start = %d, want 0 (the initial baton holder)", got)
	}

	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	// Not a timing race: a slot parked in awaitBaton cannot reach
	// StartChild until the baton reaches it.
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
	// Every pass above is claim-driven, so every one must say so —
	// assertBatonPassReason only checks for one.
	for _, ev := range events {
		if ev.Event == "baton_pass" && ev.Reason != string(batonPassClaimed) {
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
// baton (see newPool), so the stream carries no baton_hold or baton_pass.
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
// The holder's ResolveTip blocks on ctx.Done(), so it never passes the baton
// on its own; once a sibling reports baton_hold, ctx is cancelled and Loop
// must still return.
func TestPoolBatonCancellationNeverDeadlocksAWaitingSlot(t *testing.T) {
	const slots = 2
	entered := make(chan struct{})
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		onResolve: func(ctx context.Context, call int) error {
			if call == 1 {
				close(entered)
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
	}
	clk := &testClock{}
	nw := newNotifyWriter()
	em := newTestEmitter(nw)

	ctx, cancel := context.WithCancel(context.Background())
	cfg := testConfig(slots)
	p, pctx := newPool(ctx, cfg, r, em, clk)
	defer p.cancel()

	// leadSlot is launched alone and confirmed blocked in its (call 1)
	// resolve before siblingSlot starts: the baton does not gate ResolveTip
	// (issue #3625), so the slots would otherwise race for call 1 and the
	// loser would never park on the baton.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, leadSlot, cfg, p)
	}()
	<-entered

	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, siblingSlot, cfg, p)
	}()

	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if reason := p.haltReason().String(); !strings.Contains(reason, "context-cancelled") {
			t.Fatalf("halt reason = %q, want it to name context-cancelled", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Loop never returned: a slot parked in awaitBaton deadlocked on cancellation")
	}
}

// One test per baton release path follows (issue #3684's release-path AC):
// a two-slot pool whose holder (leadSlot) ends its round by exactly one
// release path, asserting the sibling's child starts and the stream carries
// that path's baton_pass reason. A missed pass leaves the sibling parked
// forever, indistinguishable from a hang.

// TestPoolBatonPassesOnQueueEmptyChildEnd pins the post-StartChild release for
// a queue-empty child (exit 2), so one slot's dry queue cannot stall its
// sibling's discovery.
//
// The clock is parked so the holder cannot re-take its own baton (there is
// no FIFO waiter queue, issue #3684): the sibling picked its Kind before
// awaitBaton, while still ungated, but the holder re-picks after exit 2
// gates the lone Kind (issue #3541) and blocks in idleSleep until Loop's
// halt. The next start is therefore the sibling's, deterministically.
func TestPoolBatonPassesOnQueueEmptyChildEnd(t *testing.T) {
	const slots = 2
	cfg := testConfig(slots)
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	clk.park()
	nw := newNotifyWriter()
	em := newTestEmitter(nw)

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	if got := r.awaitStart(t); got != leadSlot {
		t.Fatalf("first slot to start = %d, want %d (the initial baton holder)", got, leadSlot)
	}
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	r.releaseSlot(t, leadSlot, ChildResult{Exit: 2})

	if got := r.awaitStart(t); got != siblingSlot {
		t.Fatalf("next slot to start = %d, want %d: the sibling parked on the baton, not the holder re-acquiring", got, siblingSlot)
	}

	r.releaseSlot(t, siblingSlot, ChildResult{Exit: 5})
	reason := awaitHalt(t, r, done)
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassChildEnded)
}

// startParkedSiblingPool starts Loop on a parked clock and returns once the
// sibling is parked on baton_hold — the setup every issue #4365 test shares:
// a pass ahead of the holder's halt wakes that sibling deterministically.
func startParkedSiblingPool(t *testing.T, r *scriptedRunner, cfg Config) <-chan Halt {
	t.Helper()
	clk := &testClock{}
	clk.park()
	nw := newNotifyWriter()
	em := newTestEmitter(nw)

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()
	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	return done
}

// TestPoolHaltPoolExitPassesNoBatonBeforeHalt pins issue #4365: a holder whose
// child exits with a pool-halting code (5, 6) must not hand the baton to a
// parked sibling before the halt lands, or the sibling starts a child on a
// host the pool has just declared unfit. Exit 7 with Stop closed is not
// covered: the closed Stop halts the woken sibling anyway, so that case
// could not fail.
func TestPoolHaltPoolExitPassesNoBatonBeforeHalt(t *testing.T) {
	for _, tc := range []struct {
		name string
		exit int
		want string
	}{
		{"host-tainted", 5, "host-tainted"},
		{"config-invalid", 6, "config-invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const slots = 2
			r := &scriptedRunner{revisions: []string{"rev1"}}
			r.holdSlots(slots)
			done := startParkedSiblingPool(t, r, testConfig(slots))

			if got := r.awaitStart(t); got != leadSlot {
				t.Fatalf("first slot to start = %d, want %d", got, leadSlot)
			}
			r.releaseSlot(t, leadSlot, ChildResult{Exit: tc.exit})

			if got := awaitHalt(t, r, done); !strings.Contains(got, tc.want) {
				t.Fatalf("halt reason = %q, want it to name %s", got, tc.want)
			}
		})
	}
}

// TestPoolBreakerTripPassesNoBatonBeforeHalt pins issue #4365's breaker
// path: a holder whose failure trips the breaker (BreakerThreshold 1) must
// not hand the baton to the parked sibling before the trip lands.
//
// A held slot cannot return a seam error, so the seam case gates the lead's
// StartChild in onStart instead and scripts exit 5 for any later child: a
// sibling that wrongly runs surfaces as a host-tainted halt, not the
// breaker's.
func TestPoolBreakerTripPassesNoBatonBeforeHalt(t *testing.T) {
	const slots = 2
	for _, tc := range []struct {
		name string
		// setup configures r and returns the step that makes the lead's
		// failure happen once the sibling is parked.
		setup func(r *scriptedRunner) (fail func(t *testing.T))
	}{
		{"unrecognised-exit", func(r *scriptedRunner) func(*testing.T) {
			r.holdSlots(slots)
			return func(t *testing.T) {
				if got := r.awaitStart(t); got != leadSlot {
					t.Fatalf("first slot to start = %d, want %d", got, leadSlot)
				}
				r.releaseSlot(t, leadSlot, ChildResult{Exit: 99})
			}
		}},
		{"seam-error", func(r *scriptedRunner) func(*testing.T) {
			gate := make(chan struct{})
			r.runErrAt = 1
			r.runErr = errors.New("boom")
			r.results = []ChildResult{{Exit: 5}}
			r.onStart = func(ctx context.Context, req ChildRequest) error {
				if req.Slot == leadSlot {
					<-gate
				}
				return nil
			}
			return func(*testing.T) { close(gate) }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptedRunner{revisions: []string{"rev1"}}
			fail := tc.setup(r)
			cfg := testConfig(slots)
			cfg.BreakerThreshold = 1
			done := startParkedSiblingPool(t, r, cfg)

			fail(t)

			if got := awaitHalt(t, r, done); !strings.HasPrefix(got, haltRenderings[HaltBreaker].reason) {
				t.Fatalf("halt = %q, want a breaker halt", got)
			}
			if n := r.runCount(); n != 1 {
				t.Fatalf("StartChild calls = %d, want 1: the sibling must not start a child", n)
			}
		})
	}
}

// TestPoolBatonPassesOnNoneDispatchableChildEnd is the counterpart of
// TestPoolBatonPassesOnQueueEmptyChildEnd for exit 3 (outcomeNoneDispatchable):
// same release site, different outcome label. The clock is parked for the
// same reason: exit 3 gates the kind too.
func TestPoolBatonPassesOnNoneDispatchableChildEnd(t *testing.T) {
	const slots = 2
	cfg := testConfig(slots)
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	clk.park()
	nw := newNotifyWriter()
	em := newTestEmitter(nw)

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	if got := r.awaitStart(t); got != leadSlot {
		t.Fatalf("first slot to start = %d, want %d (the initial baton holder)", got, leadSlot)
	}
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	r.releaseSlot(t, leadSlot, ChildResult{Exit: 3})

	if got := r.awaitStart(t); got != siblingSlot {
		t.Fatalf("next slot to start = %d, want %d: the sibling parked on the baton, not the holder re-acquiring", got, siblingSlot)
	}

	r.releaseSlot(t, siblingSlot, ChildResult{Exit: 5})
	reason := awaitHalt(t, r, done)
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassChildEnded)
}

// TestPoolBatonPassesOnUnrecognisedExit pins the same post-StartChild release
// site for an unrecognised exit code (Interpret's Backoff case): the reason
// is batonPassChildEnded, as for a clean no-work exit, since both are "the
// holder's child ended without a claim".
func TestPoolBatonPassesOnUnrecognisedExit(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	// The holder ends in backoffOrHalt; see pinTheFailingHolder.
	pinTheFailingHolder(clk)
	nw := newNotifyWriter()
	em := newTestEmitter(nw)

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

// TestPoolBatonPassesOnSeamFailure pins batonPassChildEnded for a StartChild
// seam error too: a child_start was emitted, so the round did not fail
// before starting one.
func TestPoolBatonPassesOnSeamFailure(t *testing.T) {
	const slots = 2
	wantErr := errors.New("boom")
	nw := newNotifyWriter()
	em := newTestEmitter(nw)
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
		t.Fatal("Loop never returned: a StartChild seam error left the pool deadlocked")
	}
	reason := h.String()
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassChildEnded)
	for _, ev := range events {
		if ev.Event == "baton_pass" && ev.Reason == string(batonPassFailed) {
			t.Fatalf("baton_pass reason = %q, want batonPassChildEnded: a child_start/child_finish pair already reached the stream for this round, so the holder did not fail before starting one", ev.Reason)
		}
	}

	// With the clock parked exactly two children start, in order:
	// leadSlot's seam-failed one, then the sibling's, whose exit 5 halts.
	startSlots := childStartSlots(t, events)
	if len(startSlots) != 2 || startSlots[0] != leadSlot || startSlots[1] != siblingSlot {
		t.Fatalf("child_start slots = %v, want exactly [%d %d]: the sibling parked on the baton must start next, not the failing holder re-acquiring", startSlots, leadSlot, siblingSlot)
	}
}

// TestPoolBatonPassesWhenWindowClosesBeforeChildStart pins the pre-child
// Awake re-check in runSlot: the window can shut between the holder's
// ResolveTip and its child_start, and holding the baton through the whole
// shut span would stall the sibling's discovery.
func TestPoolBatonPassesWhenWindowClosesBeforeChildStart(t *testing.T) {
	const slots = 2
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	clk := &testClock{now: time.Date(2026, 1, 1, 16, 59, 50, 0, time.UTC)}
	clk.park()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		onResolve: func(ctx context.Context, call int) error {
			if call == 1 {
				close(entered)
				<-proceed
				clk.setNow(clk.Now().Add(2 * time.Minute))
			}
			return nil
		},
	}
	nw := newNotifyWriter()
	em := newTestEmitter(nw)

	cfg := testConfig(slots)
	cfg.Awake = win

	ctx, cancel := context.WithCancel(context.Background())
	p, pctx := newPool(ctx, cfg, r, em, clk)
	defer p.cancel()

	// leadSlot must own resolve call 1: the baton does not gate ResolveTip
	// (issue #3625), so both slots would otherwise race for it.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, leadSlot, cfg, p)
	}()
	<-entered

	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, siblingSlot, cfg, p)
	}()

	// The sibling clears awaitWindow (the window is still open at
	// 16:59:50), resolves its own tip, and parks on the baton before the
	// holder's own fetch is allowed to return.
	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	close(proceed)

	// The holder's fetch returns past the window's close (16:59:50 ->
	// 17:01:50): the baton must still pass rather than hang until 09:00.
	// The sibling parks in awaitWindow too, so baton_pass, not a child
	// start, is the signal.
	nw.waitForLine(t, "\"event\":\"baton_pass\"")
	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop never returned: a window close mid-resolve left the pool deadlocked")
	}
	reason := p.haltReason().String()
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

// TestPoolBatonColdStartInsideShutWindowGatesDiscoveryAtOpen pins that,
// apart from the initial holder's first round, a slot never parks in
// awaitWindow holding the baton: acquisition happens only after awaitWindow
// returns, so a cold start inside a shut window carries no baton_hold at all
// for the whole shut span. Once the window reopens, discovery is serialized
// as in any other round: the holder (holding the token from newPool) starts
// at once, and a sibling waits for its claim.
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
	em := newTestEmitter(nw)

	cfg := testConfig(slots)
	cfg.Awake = win

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	// Draining exactly `slots` sleep entries proves all three, holder
	// included, parked in awaitWindow before reaching awaitBaton.
	clk.awaitSleep(t, slots)

	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	for _, ev := range events {
		if ev.Event == "baton_hold" {
			t.Fatalf("events include baton_hold while every slot is still parked on the shut window, want none: the carve-out is that only the pre-assigned holder may hold the baton while parked outside awaitBaton, and here no slot — holder included — has reached awaitBaton at all")
		}
	}

	// Reopen the window: the holder already holds the token; the two
	// siblings park on baton_hold.
	clk.step(time.Date(2026, 1, 1, 9, 0, 1, 0, time.UTC))

	if got := r.awaitStart(t); got != leadSlot {
		t.Fatalf("first slot to start = %d, want %d (the initial baton holder, released by the window reopening alone)", got, leadSlot)
	}
	nw.waitForLine(t, "\"event\":\"baton_hold\"")
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	// Not a timing race: both siblings are parked in awaitBaton.
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
// for a failure before any child starts (the holder's first ResolveTip):
// holding the baton through a per-slot backoff would stall the sibling's
// discovery.
//
// The baton does not gate ResolveTip (issue #3625), so a bare resolveAt=1
// script would race both slots for call 1. leadSlot runs alone and is held
// in onResolve until observed there; only then is siblingSlot launched.
func TestPoolBatonPassesOnPreChildFailure(t *testing.T) {
	const slots = 2
	wantErr := errors.New("boom")
	nw := newNotifyWriter()
	em := newTestEmitter(nw)
	holderEntered := make(chan struct{})
	proceed := make(chan struct{})
	r := &scriptedRunner{
		revisions:  []string{"rev1"},
		resolveAt:  1,
		resolveErr: wantErr,
		results:    []ChildResult{{Exit: 5}},
		onResolve: func(ctx context.Context, call int) error {
			if call == 1 {
				close(holderEntered)
				<-proceed
			}
			return nil
		},
	}
	clk := &testClock{}
	pinTheFailingHolder(clk)

	cfg := testConfig(slots)
	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, leadSlot, cfg, p)
	}()

	<-holderEntered

	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, siblingSlot, cfg, p)
	}()

	close(proceed)

	done := make(chan Halt, 1)
	go func() {
		wg.Wait()
		done <- p.haltReason()
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

	// With the clock parked, leadSlot never gets a second round, so the
	// only child that starts is siblingSlot's.
	startSlots := childStartSlots(t, events)
	if len(startSlots) != 1 || startSlots[0] != siblingSlot {
		t.Fatalf("child_start slots = %v, want exactly [%d]: the sibling parked on the baton must be the one to start, not the failing holder", startSlots, siblingSlot)
	}
}

// TestPoolBatonPassesWhenNoKindIsRunnable pins the pickKind-fails release
// (batonPassIdle): a holder whose pickKind comes up empty, because the
// pool's only kind is gated (issue #3541), must not sleep out its idle wait
// holding the baton.
//
// The kind is seeded gated before either slot starts, rather than driven by
// a real no-work exit, so both slots deterministically see it gated without
// depending on when the sibling's pickKind lands relative to the holder's
// release.
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
	p.markNoWork(KindOf(dispatchkind.Work), clk.Now(), false)
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

	// Past the seeded deadline, whichever slot wins the freed baton starts
	// a child.
	clk.step(clk.Now().Add(time.Hour))

	started := r.awaitStart(t)
	r.releaseSlot(t, started, ChildResult{Exit: 5})
	awaitWG(t, r, &wg)
}

// TestPoolBatonPassesWhenHolderStopsBeforeResolving pins runSlot's own
// deferred passBaton (batonPassStopped): a holder that stops before any
// other release site — here, an operator cancellation inside its first
// ResolveTip — must still release the baton on its way out.
//
// Only leadSlot's goroutine runs: this pins the holder's stamped reason, not
// a sibling's unblocking (TestPoolBatonCancellationNeverDeadlocksAWaitingSlot
// covers that). cfg still configures two slots so newPool allocates a baton
// at all.
func TestPoolBatonPassesWhenHolderStopsBeforeResolving(t *testing.T) {
	const slots = 2
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	r := &scriptedRunner{
		onResolve: func(rctx context.Context, call int) error {
			if call == 1 {
				close(entered)
				<-rctx.Done()
				return rctx.Err()
			}
			return nil
		},
	}
	clk := &testClock{}
	nw := newNotifyWriter()
	em := newTestEmitter(nw)

	cfg := testConfig(slots)
	p, pctx := newPool(ctx, cfg, r, em, clk)
	defer p.cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runSlot(pctx, leadSlot, cfg, p)
	}()

	<-entered
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runSlot never returned: a holder that stopped before resolving anything left its own baton unpassed")
	}

	reason := p.haltReason().String()
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want it to name context-cancelled", reason)
	}

	// backoffOrHalt's haltIfStopping check (issue #3626) catches the
	// cancellation before it touches the baton, so the pass is runSlot's
	// deferred batonPassStopped, not backoffOrHalt's batonPassFailed.
	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassStopped)
}

// TestPoolBatonSerializesRefillsAfterTheFirstWave pins issue #3684: the baton
// applies to *every* refill, not just the cold start — two slots finishing
// in the same tick still discover one after the other. Claims are driven via
// fireOnIssue, not announceEachSlot, so the test controls when each reaches
// the pool.
func TestPoolBatonSerializesRefillsAfterTheFirstWave(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	nw := newNotifyWriter()
	em := newTestEmitter(nw)

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	first := r.awaitStart(t)
	if first != leadSlot {
		t.Fatalf("first slot to start = %d, want %d (the initial baton holder)", first, leadSlot)
	}
	// Both siblings must park before the first claim, or one can grab the
	// token via the non-blocking receive with no hold, under-counting the
	// wave1 holds the refill is measured against.
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

	// Exit 0, not a no-work exit: a no-work exit gates the single Kind
	// (issue #3541) and would entangle an idleSleep round-trip with the
	// property pinned here.
	r.releaseSlot(t, first, ChildResult{Exit: 0})
	r.releaseSlot(t, second, ChildResult{Exit: 0})

	winner := r.awaitStart(t)
	if winner != first && winner != second {
		t.Fatalf("first replacement to start = %d, want one of the two released slots (%d, %d)", winner, first, second)
	}

	// The winner silently took wave1's spare token; wait for the loser's
	// fresh baton_hold so the select below sees it genuinely parked.
	nw.waitForLine(t, "\"event\":\"baton_hold\"")

	// Not a timing race: the loser is parked on awaitBaton.
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

// TestPoolOneKindPoolBehavesAsBefore pins issue #3684's AC2: a multi-slot
// work-only pool still fills every slot — the baton serializes discovery,
// never starves a slot.
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
		if call.Kind != KindOf(dispatchkind.Work) {
			t.Fatalf("StartChild call %+v used kind %q, want only %q for a one-kind pool", call, call.Kind, KindOf(dispatchkind.Work))
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

// TestPoolBatonEventStreamOrderingAndReasons pins issue #3684's AC6: every
// baton_hold and baton_pass names its slot and a non-empty reason, and a
// held slot's first child_start lands after some baton_pass. Not
// necessarily the next one — awaitBaton's non-blocking receive lets a
// race winner log baton_hold *after* the pass that freed a different
// sibling — so no global hold-before-pass ordering is asserted.
func TestPoolBatonEventStreamOrderingAndReasons(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	clk := &testClock{}
	nw := newNotifyWriter()
	em := newTestEmitter(nw)

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
// baton_pass rows of docs/reference.md's event table to pool.go's reason
// constants, both ways; nothing else catches either side drifting.
// Constants are parsed from pool.go so a new batonPass* must be documented,
// and the docs side is narrowed to the two table rows so a reason quoted
// elsewhere cannot stand in for the row.
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
	declared := regexp.MustCompile(`(?m)^\t(baton(?:Pass|Hold)\w*)(?:\s+batonReason)?\s+=\s+"([^"]*)"$`).FindAllStringSubmatch(string(src), -1)
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

// startBatonSibling starts runSlot for slot on its own goroutine, tracked by
// wg, for the tests below that stage a holder and a sibling one at a time.
func startBatonSibling(ctx context.Context, wg *sync.WaitGroup, slot int, cfg Config, p *pool) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(ctx, slot, cfg, p)
	}()
}

// TestPoolButlerStartSkipsTheBatonWhileWorkHoldsIt pins issue #4583: a Chore-
// keyed kind selects no tracker issue and its races are settled by the
// Ledger, so a butler start never waits behind a work child still in its
// discovery phase.
func TestPoolButlerStartSkipsTheBatonWhileWorkHoldsIt(t *testing.T) {
	cfg := triKindConfig(2, 0)
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(2)
	clk := &testClock{}
	nw := newNotifyWriter()
	p, pctx := newPool(context.Background(), cfg, r, newTestEmitter(nw), clk)
	defer p.cancel()
	p.markNoWork(KindOf(dispatchkind.Research), clk.Now(), false)

	var wg sync.WaitGroup
	startBatonSibling(pctx, &wg, 0, cfg, p)
	if slot := r.awaitStart(t); slot != 0 {
		t.Fatalf("started slot = %d, want 0 (the initial baton holder)", slot)
	}
	// Slot 0's work child holds the baton: it has announced no Box.
	p.markNoWork(KindOf(dispatchkind.Work), clk.Now(), false)

	startBatonSibling(pctx, &wg, 1, cfg, p)
	if slot := r.awaitStart(t); slot != 1 {
		t.Fatalf("started slot = %d, want 1", slot)
	}
	calls := r.calls()
	if len(calls) != 2 || calls[0].Kind != KindOf(dispatchkind.Work) || calls[1].Kind != KindOf(dispatchkind.Butler) {
		t.Fatalf("calls = %v, want work on slot 0 then butler on slot 1", calls)
	}
	if strings.Contains(nw.String(), "\"event\":\"baton_hold\"") {
		t.Fatal("a butler slot emitted baton_hold, want the butler start to skip the baton")
	}

	haltThenRelease(t, r, pctx, 1, 0)
	awaitWG(t, r, &wg)
}

// TestPoolIssueKeyedStartsStillWaitOnEachOthersBaton pins the other half of
// issue #4583: work and research keep sharing the baton, so two children never
// race for one issue and research never runs against work on one issue.
func TestPoolIssueKeyedStartsStillWaitOnEachOthersBaton(t *testing.T) {
	work, research := KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)
	for _, tc := range []struct {
		name            string
		holder, sibling Kind
	}{
		{"work start waits on a research holder", research, work},
		{"research start waits on a work holder", work, research},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := dualKindConfig(2, 0)
			r := &scriptedRunner{revisions: []string{"rev1"}}
			r.holdSlots(2)
			clk := &testClock{}
			nw := newNotifyWriter()
			p, pctx := newPool(context.Background(), cfg, r, newTestEmitter(nw), clk)
			defer p.cancel()
			// Back the sibling's kind off so the holder's first pick is
			// the holder kind; then swap, so the sibling re-decides to its own kind.
			p.markNoWork(tc.sibling, clk.Now(), false)

			var wg sync.WaitGroup
			startBatonSibling(pctx, &wg, 0, cfg, p)
			if slot := r.awaitStart(t); slot != 0 {
				t.Fatalf("started slot = %d, want 0 (the initial baton holder)", slot)
			}
			p.markNoWork(tc.holder, clk.Now(), false)
			p.resetKind(tc.sibling)

			startBatonSibling(pctx, &wg, 1, cfg, p)
			nw.waitForLine(t, "\"event\":\"baton_hold\"")
			select {
			case got := <-r.started:
				t.Fatalf("slot %d started while the other kind's child held the baton", got)
			default:
			}

			r.fireOnIssue(t, 0, "42")
			if slot := r.awaitStart(t); slot != 1 {
				t.Fatalf("started slot = %d, want 1 once the baton passed", slot)
			}
			if calls := r.calls(); len(calls) != 2 || calls[0].Kind != tc.holder || calls[1].Kind != tc.sibling {
				t.Fatalf("calls = %v, want %s then %s", calls, tc.holder, tc.sibling)
			}

			haltThenRelease(t, r, pctx, 0, 1)
			awaitWG(t, r, &wg)
		})
	}
}

// TestPoolHolderStartingButlerPassesTheBatonAtOnce pins that a holder starting
// a Chore-keyed child passes the baton at the start, so a butler run never
// holds the pool until its Box record.
func TestPoolHolderStartingButlerPassesTheBatonAtOnce(t *testing.T) {
	cfg := triKindConfig(2, 0)
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(2)
	clk := &testClock{}
	nw := newNotifyWriter()
	p, pctx := newPool(context.Background(), cfg, r, newTestEmitter(nw), clk)
	defer p.cancel()
	p.markNoWork(KindOf(dispatchkind.Work), clk.Now(), false)
	p.markNoWork(KindOf(dispatchkind.Research), clk.Now(), false)

	var wg sync.WaitGroup
	startBatonSibling(pctx, &wg, 0, cfg, p)
	if slot := r.awaitStart(t); slot != 0 {
		t.Fatalf("started slot = %d, want 0", slot)
	}
	p.resetKind(KindOf(dispatchkind.Work))

	// No Box record from the butler child: the sibling's start proves the
	// baton passed at the child's start.
	startBatonSibling(pctx, &wg, 1, cfg, p)
	if slot := r.awaitStart(t); slot != 1 {
		t.Fatalf("started slot = %d, want 1", slot)
	}
	events := decodeEvents(t, bytes.NewBufferString(nw.String()))
	assertBatonPassReason(t, events, batonPassChoreKeyed)
	for _, ev := range events {
		if ev.Event == "baton_hold" {
			t.Fatalf("baton_hold event %+v, want none: the baton was already passed", ev)
		}
	}

	haltThenRelease(t, r, pctx, 1, 0)
	awaitWG(t, r, &wg)
}

// haltThenRelease ends a two-slot test: haltSlot's child exits host-tainted
// and the pool's halt lands before other's child releases, so other cannot
// loop around and start another child on a pool that has not yet halted.
func haltThenRelease(t *testing.T, r *scriptedRunner, pctx context.Context, haltSlot, other int) {
	t.Helper()
	r.releaseSlot(t, haltSlot, ChildResult{Exit: 5})
	select {
	case <-pctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("pool did not halt within 5s of the host-tainted exit")
	}
	r.releaseSlot(t, other, ChildResult{Exit: 0})
}
