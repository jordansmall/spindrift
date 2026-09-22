package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	// aliased: two tests below declare a local "report" (a StatusReport)
	// that would otherwise shadow the package name for the rest of their
	// function body.
	reportpkg "spindrift.dev/launcher/internal/report"
)

type runCall struct {
	Kind     Kind
	Revision string
	Slot     int
}

func newTestEmitter(buf *bytes.Buffer) *Emitter {
	return NewEmitter(buf, func() time.Time { return time.Unix(0, 0).UTC() })
}

func decodeEvents(t *testing.T, buf *bytes.Buffer) []Event {
	t.Helper()
	var events []Event
	dec := json.NewDecoder(buf)
	for dec.More() {
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		events = append(events, ev)
	}
	return events
}

func eventNames(events []Event) []string {
	names := make([]string, len(events))
	for i, ev := range events {
		names[i] = ev.Event
	}
	return names
}

// testIdleCap is intentionally far above testIdleFloor: it lets tests that
// only ever hit one no-work check still see the undoubled floor, while
// tests that chain several consecutive no-work checks (and want to see
// growth) are free to assert on the doubling explicitly.
const (
	testIdleFloor = time.Millisecond
	testIdleCap   = time.Hour
)

// testFailureBackoff/testBreakerThreshold/testBreakerWindow are the
// breaker knobs most tests don't care about but Loop now requires to be
// valid (Config's own validation, mirroring Slots). A window far longer
// than any test's virtual clock advances keeps the breaker's window from
// silently aging failures out mid-test; tests that want it to trip build
// their own Config with a small threshold instead.
const (
	testFailureBackoff   = time.Millisecond
	testBreakerThreshold = 1000
	testBreakerWindow    = time.Hour
)

// testConfig builds the Config every test below shares apart from the slot
// count: Kind, IdleFloor/IdleCap, and the three breaker knobs above.
func testConfig(slots int) Config {
	return Config{
		Kinds:            []Kind{KindDispatch},
		IdleFloor:        testIdleFloor,
		IdleCap:          testIdleCap,
		FailureBackoff:   testFailureBackoff,
		BreakerThreshold: testBreakerThreshold,
		BreakerWindow:    testBreakerWindow,
		Slots:            slots,
	}
}

func TestLoopContinueThenHalt(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk).String()

	if r.runCount() != 2 {
		t.Fatalf("run calls = %d, want 2", r.runCount())
	}
	if clk.waitCount() != 0 {
		t.Fatalf("waits = %v, want none (exit 0 continues at once)", clk.waits())
	}
	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want it to name host-tainted", reason)
	}
}

func TestLoopWaitThenHalt(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}, {Exit: 0}, {Exit: 6}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk).String()

	if r.runCount() != 3 {
		t.Fatalf("run calls = %d, want 3", r.runCount())
	}
	if clk.waitCount() != 1 || clk.waits()[0] != testIdleFloor {
		t.Fatalf("waits = %v, want exactly one wait of %v", clk.waits(), testIdleFloor)
	}
	if !strings.Contains(reason, "config-invalid") {
		t.Errorf("halt reason = %q, want it to name config-invalid", reason)
	}
}

// TestLoopExit3SingleSlotIsAJam pins the occupancy-axis contract for a
// single-slot daemon: with Slots: 1 there is never a sibling to explain
// away a "none dispatchable" exit, so every exit 3 is a jam, not routine
// idle — it still waits like exit 2 (below), it just no longer reports
// like it.
func TestLoopExit3SingleSlotIsAJam(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 3}, {Exit: 5}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	if clk.waitCount() != 1 || clk.waits()[0] != testIdleFloor {
		t.Fatalf("waits = %v, want exactly one wait of %v for exit 3", clk.waits(), testIdleFloor)
	}

	events := decodeEvents(t, &buf)
	var sawJam bool
	for _, ev := range events {
		if ev.Event == "idle" {
			t.Errorf("single-slot exit 3 must report jam, not idle: got %+v", ev)
		}
		if ev.Event == "jam" {
			sawJam = true
			if ev.Slot == nil || *ev.Slot != 0 {
				t.Errorf("jam event slot = %v, want 0", ev.Slot)
			}
			if ev.Reason == "" {
				t.Errorf("jam event reason is empty, want it to say what makes it a jam")
			}
		}
	}
	if !sawJam {
		t.Fatalf("events = %v, want a jam event for single-slot exit 3", eventNames(events))
	}
}

// TestLoopExit2NeverEmitsJam pins that the occupancy axis applies only to
// exit 3: an empty queue (exit 2) is empty whatever the siblings are
// doing, so it always reports idle, never jam.
func TestLoopExit2NeverEmitsJam(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}, {Exit: 2}, {Exit: 5}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	events := decodeEvents(t, &buf)
	idleCount := 0
	for _, ev := range events {
		if ev.Event == "jam" {
			t.Fatalf("exit 2 must never emit jam: got %+v", ev)
		}
		if ev.Event == "idle" {
			idleCount++
		}
	}
	if idleCount != 2 {
		t.Errorf("idle events = %d, want 2", idleCount)
	}
}

func TestLoopExit4ContinuesWithoutSleeping(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 4}, {Exit: 5}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	if clk.waitCount() != 0 {
		t.Fatalf("waits = %v, want none for exit 4 (image-stale continues at once)", clk.waits())
	}
	if r.runCount() != 2 {
		t.Fatalf("run calls = %d, want 2", r.runCount())
	}
}

// TestLoopUnknownExitBacksOffThenHalts pins the new contract for an
// unrecognised exit code: it backs the slot off rather than halting the
// pool outright. A follow-up host-tainted exit gives the fake a real halt
// so the test still terminates rather than looping forever on backoffs.
func TestLoopUnknownExitBacksOffThenHalts(t *testing.T) {
	for _, exit := range []int{1, 99} {
		r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: exit}, {Exit: 5}}}
		clk := &testClock{}
		var buf bytes.Buffer
		em := newTestEmitter(&buf)

		reason := Loop(context.Background(), testConfig(1), r, em, clk).String()

		if !strings.Contains(reason, "host-tainted") {
			t.Errorf("exit %d: halt reason = %q, want the follow-up host-tainted halt, not the unclassified exit itself", exit, reason)
		}
		if clk.waitCount() != 1 || clk.waits()[0] != testFailureBackoff {
			t.Errorf("exit %d: waits = %v, want exactly one wait of %v (FailureBackoff)", exit, clk.waits(), testFailureBackoff)
		}

		events := decodeEvents(t, &buf)
		var sawBackoff bool
		for _, ev := range events {
			if ev.Event == "backoff" {
				sawBackoff = true
				if ev.Slot == nil || *ev.Slot != 0 {
					t.Errorf("exit %d: backoff event slot = %v, want 0", exit, ev.Slot)
				}
			}
		}
		if !sawBackoff {
			t.Errorf("exit %d: events = %v, want a backoff event for the unclassified exit", exit, eventNames(events))
		}
	}
}

// TestLoopResolveErrorBacksOffThenHalts pins the new contract: a
// ResolveTip fetch error (a failed git fetch) is exactly the transient blip
// that must not end the night, so it backs off its slot rather than
// halting the pool. A follow-up host-tainted exit gives the fake a real
// halt so the test still terminates.
func TestLoopResolveErrorBacksOffThenHalts(t *testing.T) {
	wantErr := errors.New("boom: no such revision")
	r := &scriptedRunner{revisions: []string{"rev1"}, resolveAt: 1, resolveErr: wantErr, results: []ChildResult{{Exit: 5}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk).String()

	if r.runCount() != 1 {
		t.Fatalf("run calls = %d, want 1: the slot must retry after backing off from the resolve failure", r.runCount())
	}
	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want the follow-up host-tainted halt, not the resolve failure itself", reason)
	}
	if clk.waitCount() != 1 || clk.waits()[0] != testFailureBackoff {
		t.Fatalf("waits = %v, want exactly one wait of %v (FailureBackoff)", clk.waits(), testFailureBackoff)
	}

	events := wantEvents(t, &buf, []string{"backoff", "child_start", "child_finish", "halt"}, "")
	if !strings.Contains(events[0].Reason, wantErr.Error()) {
		t.Errorf("backoff event reason = %q, want it to name %v", events[0].Reason, wantErr)
	}
}

// TestLoopRunChildErrorBacksOffAndEmitsChildFinish pins the new contract: a
// RunChild seam error backs off its slot rather than halting the pool, but
// still emits the child_finish for the child it already started — a
// started child always gets a matching finish. A follow-up host-tainted
// exit gives the fake a real halt so the test still terminates.
func TestLoopRunChildErrorBacksOffAndEmitsChildFinish(t *testing.T) {
	wantErr := errors.New("exec: nix not found")
	r := &scriptedRunner{revisions: []string{"rev1"}, runErrAt: 1, runErr: wantErr, results: []ChildResult{{Exit: 5}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk).String()

	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want the follow-up host-tainted halt, not the run-child failure itself", reason)
	}

	wantEvents(t, &buf, []string{"child_start", "child_finish", "backoff", "child_start", "child_finish", "halt"}, "")
}

// TestLoopRunChildErrorStillEmitsAnnouncedBoxes covers the non-ExitError
// wait-failure path (runner.go): a child can report a box record over its
// pipe and only then have the seam itself fail (a wait failure that is no
// ExitError). That box must still reach the durable stream, so a box event
// lands before child_finish even on the error path — reported live via
// OnRecord, from onStart, since a seam-failed call never reaches a
// scripted ChildResult at all.
func TestLoopRunChildErrorStillEmitsAnnouncedBoxes(t *testing.T) {
	wantErr := errors.New("wait: signal: killed")
	var calls int
	r := &scriptedRunner{
		revisions: []string{"rev1"}, runErrAt: 1, runErr: wantErr,
		results: []ChildResult{{Exit: 5}},
		onStart: func(ctx context.Context, req ChildRequest) error {
			calls++
			if calls == 1 && req.OnRecord != nil {
				req.OnRecord(Record{Event: reportpkg.EventBox, Issue: "42"})
			}
			return nil
		},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	events := wantEvents(t, &buf, []string{"child_start", "box", "child_finish", "backoff", "child_start", "child_finish", "halt"}, "")
	if events[1].Issue != "42" {
		t.Errorf("box event Issue = %q, want %q", events[1].Issue, "42")
	}
}

func TestLoopEventStreamSequenceAndFields(t *testing.T) {
	var calls int
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 0}, {Exit: 5}},
		onStart: func(ctx context.Context, req ChildRequest) error {
			calls++
			if calls == 1 && req.OnRecord != nil {
				req.OnRecord(Record{Event: reportpkg.EventBox, Issue: "10"})
				req.OnRecord(Record{Event: reportpkg.EventBox, Issue: "11"})
			}
			return nil
		},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	events := wantEvents(t, &buf, []string{"child_start", "box", "box", "child_finish", "child_start", "child_finish", "halt"}, "")

	box1, box2 := events[1], events[2]
	if box1.Issue != "10" || box1.Revision != "rev1" {
		t.Errorf("first box event = %+v, want issue 10 at rev1", box1)
	}
	if box2.Issue != "11" || box2.Revision != "rev1" {
		t.Errorf("second box event = %+v, want issue 11 at rev1", box2)
	}

	finish1 := events[3]
	if finish1.Exit == nil || *finish1.Exit != 0 || finish1.Outcome != "dispatched" {
		t.Errorf("first child_finish = %+v, want exit 0 outcome dispatched", finish1)
	}
	finish2 := events[5]
	if finish2.Exit == nil || *finish2.Exit != 5 || finish2.Outcome != "host-tainted" {
		t.Errorf("second child_finish = %+v, want exit 5 outcome host-tainted", finish2)
	}

	halt := events[len(events)-1]
	if !strings.Contains(halt.Reason, "host-tainted") {
		t.Errorf("halt event reason = %q, want it to name host-tainted", halt.Reason)
	}
}

func TestLoopKeepsGoingAfterQueueDrainsThenPicksUpWork(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 2}, {Exit: 2}, {Exit: 0}, {Exit: 5}},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk).String()

	if r.runCount() != 4 {
		t.Fatalf("run calls = %d, want 4: the loop must keep going after the queue drains and pick work back up without a restart", r.runCount())
	}
	if clk.waitCount() != 2 {
		t.Fatalf("waits = %v, want exactly two (one per empty-queue exit)", clk.waits())
	}
	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want it to name host-tainted", reason)
	}
}

func TestLoopPinsEachChildToTheResolvedRevisionEvenWhenItChanges(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1", "rev2", "rev3"},
		results:   []ChildResult{{Exit: 0}, {Exit: 0}, {Exit: 5}},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	want := []runCall{
		{Kind: KindDispatch, Revision: "rev1"},
		{Kind: KindDispatch, Revision: "rev2"},
		{Kind: KindDispatch, Revision: "rev3"},
	}
	if fmt.Sprint(r.calls()) != fmt.Sprint(want) {
		t.Fatalf("run calls = %v, want %v: each child must be pinned to that iteration's resolved revision", r.calls(), want)
	}
}

func TestLoopCancelledContextHaltsBeforeStartingNewWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, testConfig(1), r, em, clk).String()

	if r.runCount() != 0 {
		t.Fatalf("run calls = %d, want 0: an already-cancelled ctx must halt before starting any child", r.runCount())
	}
	if !strings.Contains(reason, "context") {
		t.Errorf("halt reason = %q, want it to name the cancellation", reason)
	}
}

// TestLoopCancelledDuringResolveTipStillHaltsAtTheNextAdmission pins
// issue #3626's collapse to one admission check: a ctx cancelled mid-fetch
// (a bare caller cancel, not cfg.Stop — no ChildRequest.Stop exists to
// forward it to a running child) is no longer re-checked between
// ResolveTip and RunChild, so this iteration's child still starts and
// runs to completion; the loop only notices the cancellation back at the
// top of its next iteration, and still halts with the context-cancelled
// reason rather than ever reaching the breaker.
func TestLoopCancelledDuringResolveTipStillHaltsAtTheNextAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 0}},
		onResolve: func(ctx context.Context, call int) error {
			cancel()
			return nil
		},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, testConfig(1), r, em, clk).String()

	if r.runCount() != 1 {
		t.Fatalf("run calls = %d, want 1: nothing re-checks ctx between ResolveTip and RunChild anymore", r.runCount())
	}
	wantEvents(t, &buf, []string{"child_start", "child_finish", "halt"}, "the child that was already admitted still runs and finishes before the halt")
	if !strings.Contains(reason, "context") {
		t.Errorf("halt reason = %q, want it to name the cancellation", reason)
	}
}

func TestLoopNeverAbandonsAStartedChild(t *testing.T) {
	// A Runner whose RunChild cancels ctx mid-run, as the production
	// adapter's process-exit path will (next slice) — the loop must still
	// wait for the result and emit that child's child_finish before it
	// re-checks ctx and halts, never killing the child itself.
	ctx, cancel := context.WithCancel(context.Background())
	// RunChild cancelling ctx then returning result — the running child
	// must still be waited on and its child_finish emitted, never abandoned.
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 0}},
		onStart: func(ctx context.Context, req ChildRequest) error {
			cancel()
			return nil
		},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, testConfig(1), r, em, clk).String()

	if r.runCount() != 1 {
		t.Fatalf("run calls = %d, want 1", r.runCount())
	}
	wantEvents(t, &buf, []string{"child_start", "child_finish", "halt"}, "the started child's child_finish must still be emitted")
	if !strings.Contains(reason, "context") {
		t.Errorf("halt reason = %q, want it to name the cancellation", reason)
	}
}

// TestLoopSelfEvalCtxCancelledIsNotABreakerFailure asserts that ResolveTip's
// self-eval half, when its ctx is cancelled out from under it (an operator
// SIGTERM racing the evaluation), reports a plain context-cancelled halt,
// not a breaker failure: with MAX_PARALLEL >= the breaker threshold, several
// slots hitting this on one SIGTERM could otherwise trip the breaker and
// turn a clean stop's exit 0 into exit 1 (issue #3543).
func TestLoopSelfEvalCtxCancelledIsNotABreakerFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newCancelErrRunner(cancel, phaseSelfEval, "rev1")
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/old-path"

	reason := Loop(ctx, cfg, r, em, clk).String()

	assertCancelledStopNoBreaker(t, reason, decodeEvents(t, &buf))
}

// TestLoopFetchCtxCancelledIsNotABreakerFailure is
// TestLoopSelfEvalCtxCancelledIsNotABreakerFailure's sibling for
// ResolveTip's fetch half in runSlot (loop.go), which has had this same
// hazard since before this diff.
func TestLoopFetchCtxCancelledIsNotABreakerFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newCancelErrRunner(cancel, phaseFetch, "")
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, testConfig(1), r, em, clk).String()

	assertCancelledStopNoBreaker(t, reason, decodeEvents(t, &buf))
}

// assertCancelledStopNoBreaker fails unless Loop stopped on the cancellation
// itself and left none of the bookkeeping a real seam failure would. Takes
// the already-decoded events, not the raw buffer: a caller that also wants
// to assert on event names/order (e.g. child_finish presence) must decode
// once and reuse the slice, since decodeEvents drains the buffer.
func assertCancelledStopNoBreaker(t *testing.T, reason string, events []Event) {
	t.Helper()
	if !strings.HasPrefix(reason, "context-cancelled:") {
		t.Fatalf("halt reason = %q, want prefix %q", reason, "context-cancelled:")
	}
	for _, e := range events {
		if e.Event == "backoff" || e.Event == "breaker_trip" {
			t.Fatalf("events = %v, want no backoff/breaker_trip event on an ordinary operator stop", events)
		}
	}
}

// TestLoopCtxCancelledIsNotABreakerFailure covers two RunChild-result races
// against an operator's SIGTERM tearing the loop's ctx down mid-run, neither
// of which must spend a breaker failure — both must halt on the
// cancellation instead of reaching backoffOrHalt.
func TestLoopCtxCancelledIsNotABreakerFailure(t *testing.T) {
	tests := []struct {
		name   string
		result ChildResult
		err    error
	}{
		// This issue's slice: a child forwarded SIGTERM that dies on the
		// default disposition before installing its own handler reports an
		// unclassified exit (e.g. 143), same as any other unrecognised code.
		{name: "unclassified exit", result: ChildResult{Exit: 143}},
		// The RunChild seam-error branch: the child's own wait can fail as
		// an operator SIGTERM tears it down.
		{name: "RunChild seam error", err: errors.New("wait: signal: killed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			r := &scriptedRunner{
				revisions: []string{"rev1"},
				results:   []ChildResult{tt.result},
				onStart: func(ctx context.Context, req ChildRequest) error {
					cancel()
					return nil
				},
			}
			if tt.err != nil {
				r.runErrAt = 1
				r.runErr = tt.err
			}
			clk := &testClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			reason := Loop(ctx, testConfig(1), r, em, clk).String()

			events := wantEvents(t, &buf, []string{"child_start", "child_finish", "halt"}, "the started child's child_finish must still be emitted")
			assertCancelledStopNoBreaker(t, reason, events)
		})
	}
}

// cancelPhase names which half of one ResolveTip call an operator SIGTERM
// lands inside — the fetch half or the self-eval half — not a separate
// Runner seam: ResolveTip is the only seam onto ResolveTip, and both halves
// run inside that one call.
type cancelPhase string

const (
	phaseFetch    cancelPhase = "fetch"
	phaseSelfEval cancelPhase = "self-eval"
)

// newCancelErrRunner models an operator SIGTERM landing inside one phase of
// ResolveTip: the phase cancelIn names cancels the parent ctx and returns
// its ctx.Err(), and every phase the loop must not reach afterwards errors
// loudly rather than handing back a zero value the loop would read as a
// real answer.
func newCancelErrRunner(cancel context.CancelFunc, cancelIn cancelPhase, revision string) *scriptedRunner {
	r := &scriptedRunner{revisions: []string{revision}}
	r.onResolve = func(ctx context.Context, call int) error {
		if cancelIn == phaseFetch {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	r.onSelf = func(ctx context.Context, call int, revision string) error {
		if cancelIn == phaseSelfEval {
			cancel()
			return ctx.Err()
		}
		return fmt.Errorf("must not be called: the %s phase cancels before the self check runs", cancelIn)
	}
	r.onStart = func(ctx context.Context, req ChildRequest) error {
		return fmt.Errorf("must not be called: a cancelled %s must halt before a child ever starts", cancelIn)
	}
	return r
}

// TestLoopRejectsNonPositiveSlots asserts Loop treats a zero or negative
// pool size as a real, halt-shaped config error rather than silently
// running no children — a zero-slot daemon that looks healthy while doing
// nothing is the worst outcome.
func TestLoopRejectsNonPositiveSlots(t *testing.T) {
	for _, slots := range []int{0, -1} {
		t.Run(fmt.Sprintf("slots=%d", slots), func(t *testing.T) {
			r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
			clk := &testClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			reason := Loop(context.Background(), testConfig(slots), r, em, clk).String()

			if r.runCount() != 0 {
				t.Fatalf("run calls = %d, want 0: a non-positive slot count must halt before any child runs", r.runCount())
			}
			if !strings.Contains(reason, "config-invalid") {
				t.Errorf("halt reason = %q, want it to name config-invalid", reason)
			}
			wantEvents(t, &buf, []string{"halt"}, "a config-invalid pool halts before any child runs, so the halt is the only event there is to emit")
		})
	}
}

// TestLoopRejectsInvalidBreakerConfig mirrors
// TestLoopRejectsNonPositiveSlots for the breaker knobs this slice adds: a
// non-positive threshold or window, or a negative backoff, is a config
// error Loop rejects up front rather than a zero-value default that would
// make the breaker's real threshold invisible.
func TestLoopRejectsInvalidBreakerConfig(t *testing.T) {
	// Each case is the valid single-slot config with exactly one knob made
	// invalid, so what the case rejects is the only thing it states.
	invalid := func(mutate func(*Config)) Config {
		cfg := testConfig(1)
		mutate(&cfg)
		return cfg
	}
	cases := []struct {
		name string
		cfg  Config
	}{
		{"zero threshold", invalid(func(c *Config) { c.BreakerThreshold = 0 })},
		{"negative threshold", invalid(func(c *Config) { c.BreakerThreshold = -1 })},
		{"zero window", invalid(func(c *Config) { c.BreakerWindow = 0 })},
		{"negative window", invalid(func(c *Config) { c.BreakerWindow = -time.Minute })},
		{"negative backoff", invalid(func(c *Config) { c.FailureBackoff = -time.Millisecond })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
			clk := &testClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			reason := Loop(context.Background(), tc.cfg, r, em, clk).String()

			if r.runCount() != 0 {
				t.Fatalf("run calls = %d, want 0: an invalid breaker config must halt before any child runs", r.runCount())
			}
			if !strings.Contains(reason, "config-invalid") {
				t.Errorf("halt reason = %q, want it to name config-invalid", reason)
			}
		})
	}
}

// TestLoopRejectsInvalidKindsConfig mirrors TestLoopRejectsInvalidBreakerConfig
// for the Kinds/ResearchReservation knobs issue #3541 adds: an empty Kinds, an
// unknown or duplicate kind, or a ResearchReservation outside [0, Slots] is a
// config error Loop rejects up front.
func TestLoopRejectsInvalidKindsConfig(t *testing.T) {
	invalid := func(mutate func(*Config)) Config {
		cfg := testConfig(2)
		mutate(&cfg)
		return cfg
	}
	cases := []struct {
		name string
		cfg  Config
	}{
		{"empty kinds", invalid(func(c *Config) { c.Kinds = nil })},
		{"unknown kind", invalid(func(c *Config) { c.Kinds = []Kind{Kind("bogus")} })},
		{"duplicate kind", invalid(func(c *Config) { c.Kinds = []Kind{KindDispatch, KindDispatch} })},
		{"negative reservation", invalid(func(c *Config) { c.ResearchReservation = -1 })},
		{"reservation above slots", invalid(func(c *Config) { c.ResearchReservation = 3 })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
			clk := &testClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			reason := Loop(context.Background(), tc.cfg, r, em, clk).String()

			if r.runCount() != 0 {
				t.Fatalf("run calls = %d, want 0: an invalid kinds config must halt before any child runs", r.runCount())
			}
			if !strings.Contains(reason, "config-invalid") {
				t.Errorf("halt reason = %q, want it to name config-invalid", reason)
			}
		})
	}
}

// TestLoopIdleBackoffGrowsAndCapsAcrossConsecutiveNoWork pins the pool-wide
// growth this slice adds: consecutive no-work checks (exit 2) each double
// the wait from the last one, bounded by IdleCap, rather than repeating the
// same fixed interval forever.
func TestLoopIdleBackoffGrowsAndCapsAcrossConsecutiveNoWork(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{
		{Exit: 2}, {Exit: 2}, {Exit: 2}, {Exit: 2}, {Exit: 2}, {Exit: 5},
	}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.IdleFloor = time.Millisecond
	cfg.IdleCap = 4 * time.Millisecond

	Loop(context.Background(), cfg, r, em, clk)

	want := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond}
	if fmt.Sprint(clk.waits()) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v", clk.waits(), want)
	}
}

// TestLoopIdleBackoffResetsOnDispatch pins the reset rule this slice adds:
// a check that actually dispatches (exit 0) is evidence the queue was not
// really idle, so the very next no-work check must wait the floor again,
// not the grown value the streak was on before the dispatch.
func TestLoopIdleBackoffResetsOnDispatch(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{
		{Exit: 2}, {Exit: 2}, {Exit: 0}, {Exit: 2}, {Exit: 5},
	}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	want := []time.Duration{testIdleFloor, 2 * testIdleFloor, testIdleFloor}
	if fmt.Sprint(clk.waits()) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v (dispatch between the two no-work streaks resets to the floor)", clk.waits(), want)
	}
}

// TestLoopRejectsInvalidIdleConfig mirrors TestLoopRejectsInvalidBreakerConfig
// for the idle-backoff knobs this slice adds: a non-positive floor or a cap
// below the floor is a config error Loop rejects up front.
func TestLoopRejectsInvalidIdleConfig(t *testing.T) {
	invalid := func(mutate func(*Config)) Config {
		cfg := testConfig(1)
		mutate(&cfg)
		return cfg
	}
	cases := []struct {
		name string
		cfg  Config
	}{
		{"zero floor", invalid(func(c *Config) { c.IdleFloor = 0 })},
		{"negative floor", invalid(func(c *Config) { c.IdleFloor = -time.Millisecond })},
		{"cap below floor", invalid(func(c *Config) { c.IdleCap = c.IdleFloor - time.Nanosecond })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
			clk := &testClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			reason := Loop(context.Background(), tc.cfg, r, em, clk).String()

			if r.runCount() != 0 {
				t.Fatalf("run calls = %d, want 0: an invalid idle config must halt before any child runs", r.runCount())
			}
			if !strings.Contains(reason, "config-invalid") {
				t.Errorf("halt reason = %q, want it to name config-invalid", reason)
			}
		})
	}
}

// TestLoopTipMovedShortCircuitsNoneDispatchableWait pins the asymmetric
// short-circuit: a none-dispatchable wait is sliced into IdleFloor-sized
// polls, and a tip that moves mid-wait ends the wait early, resets the
// backoff, and the very next child runs at the new revision.
func TestLoopTipMovedShortCircuitsNoneDispatchableWait(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1", "rev1", "rev1", "rev1", "rev2"},
		moved:     []bool{false, false, false, false, true},
		results:   []ChildResult{{Exit: 3}, {Exit: 3}, {Exit: 3}, {Exit: 5}},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	// Three no-work checks grow the wait to floor, 2*floor, 4*floor; the
	// third one's poll sees the moved tip after its first slice, so it
	// contributes only one more floor-sized sleep instead of riding out
	// the rest of its 4*floor wait.
	want := []time.Duration{testIdleFloor, testIdleFloor, testIdleFloor, testIdleFloor}
	if fmt.Sprint(clk.waits()) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v (tip-moved cuts the third wait to a single floor slice)", clk.waits(), want)
	}

	events := decodeEvents(t, &buf)
	var tipMoved *Event
	for i := range events {
		if events[i].Event == "tip_moved" {
			if tipMoved != nil {
				t.Fatalf("events = %v, want exactly one tip_moved", eventNames(events))
			}
			tipMoved = &events[i]
		}
	}
	if tipMoved == nil {
		t.Fatalf("events = %v, want a tip_moved event", eventNames(events))
	}
	if tipMoved.Revision != "rev2" {
		t.Errorf("tip_moved revision = %q, want %q", tipMoved.Revision, "rev2")
	}
	if tipMoved.Slot == nil || *tipMoved.Slot != 0 {
		t.Errorf("tip_moved slot = %v, want 0", tipMoved.Slot)
	}
	if tipMoved.Reason == "" {
		t.Errorf("tip_moved reason is empty, want it to say a merge can unblock a jammed queue")
	}

	if r.runCount() != 4 {
		t.Fatalf("run calls = %d, want 4", r.runCount())
	}
	if r.calls()[3].Revision != "rev2" {
		t.Errorf("run calls[3].Revision = %q, want %q: the next child must run at the moved tip", r.calls()[3].Revision, "rev2")
	}
}

// TestLoopNoneDispatchableWaitSleepsFullWaitWhenTipNeverMoves pins the other
// half of the short-circuit: with no tip movement, the grown wait is slept
// out completely, in floor-sized slices, and no tip_moved event appears.
func TestLoopNoneDispatchableWaitSleepsFullWaitWhenTipNeverMoves(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 3}, {Exit: 3}, {Exit: 3}, {Exit: 5}},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	want := []time.Duration{
		testIdleFloor,                // 1st check: wait = floor, one slice
		testIdleFloor, testIdleFloor, // 2nd check: wait = 2*floor, two slices
		testIdleFloor, testIdleFloor, testIdleFloor, testIdleFloor, // 3rd check: wait = 4*floor, four slices
	}
	if fmt.Sprint(clk.waits()) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v (full grown wait slept in floor-sized slices)", clk.waits(), want)
	}

	events := decodeEvents(t, &buf)
	for _, ev := range events {
		if ev.Event == "tip_moved" {
			t.Fatalf("events = %v, want no tip_moved when the tip never moves", eventNames(events))
		}
	}
}

// TestLoopQueueEmptyWaitIgnoresTipMoved pins the asymmetric half of the
// short-circuit that costs nothing: a queue-empty wait (exit 2) never
// polls mid-wait, however far the backoff has grown, and ResolveTip is
// called no more than the once-per-iteration the loop already does.
func TestLoopQueueEmptyWaitIgnoresTipMoved(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1", "rev2", "rev3", "rev4"},
		results:   []ChildResult{{Exit: 2}, {Exit: 2}, {Exit: 2}, {Exit: 5}},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	want := []time.Duration{testIdleFloor, 2 * testIdleFloor, 4 * testIdleFloor}
	if fmt.Sprint(clk.waits()) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v: exactly one Sleep per wait, whole, never sliced", clk.waits(), want)
	}

	if r.resolveCount() != 4 {
		t.Errorf("resolveCalls = %d, want 4: exactly one ResolveTip call per iteration, no mid-wait poll", r.resolveCount())
	}

	events := decodeEvents(t, &buf)
	for _, ev := range events {
		if ev.Event == "tip_moved" {
			t.Fatalf("events = %v, want no tip_moved for a queue-empty wait", eventNames(events))
		}
	}
}

// TestLoopTipMovedResetsBackoffForNextWait pins that a tip-moved
// short-circuit is treated the same as any other real-work observation: the
// very next no-work wait starts back at the floor, not wherever the
// short-circuited streak left off.
func TestLoopTipMovedResetsBackoffForNextWait(t *testing.T) {
	r := &scriptedRunner{
		// A trailing explicit false, not left to the "last value repeats"
		// convention: resolveTip (pool.go, issue #3625) now reports a
		// Moved tip from the post-pickKind site too, not only the
		// opportunistic one, so a 6th resolve call that repeated moved's
		// last scripted value (true) would observe the still-jammed gate
		// a second time and fire a second tip_moved this test does not
		// intend to exercise.
		revisions: []string{"rev1", "rev1", "rev1", "rev1", "rev2", "rev2"},
		moved:     []bool{false, false, false, false, true, false},
		results:   []ChildResult{{Exit: 3}, {Exit: 3}, {Exit: 3}, {Exit: 3}, {Exit: 5}},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	// Checks 1-3 grow floor -> 2*floor -> 4*floor, but the 3rd short-circuits
	// after one slice on the moved tip. The 4th check, right after, must be
	// back at a bare floor wait (one slice), not a continuation of the grown
	// streak.
	want := []time.Duration{testIdleFloor, testIdleFloor, testIdleFloor, testIdleFloor, testIdleFloor}
	if fmt.Sprint(clk.waits()) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v (backoff reset after the tip-moved short-circuit)", clk.waits(), want)
	}

	events := decodeEvents(t, &buf)
	tipMovedCount := 0
	for _, ev := range events {
		if ev.Event == "tip_moved" {
			tipMovedCount++
		}
	}
	if tipMovedCount != 1 {
		t.Fatalf("tip_moved events = %d, want exactly 1", tipMovedCount)
	}
}

// TestLoopTipMovedResolvesOnceNotFetchThenRefetch is the headline regression
// tripwire for #3579: the slot that observes a mid-wait tip move must reuse
// that same resolution for the child it runs next, not fetch it again at the
// top of the following iteration. Two consecutive no-work checks grow the
// wait past one IdleFloor, so the second's idleSleep asks for an
// opportunistic resolve; that resolve is scripted to report the move
// straight away, so pickKind finds dispatch runnable again in the very same
// iteration and must run with the tip already in hand — one resolution per
// run, not the fetch-then-refetch pair there was before this slice.
func TestLoopTipMovedResolvesOnceNotFetchThenRefetch(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1", "rev1", "rev2"},
		moved:     []bool{false, false, true},
		results:   []ChildResult{{Exit: 3}, {Exit: 3}, {Exit: 5}},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	if r.runCount() != 3 {
		t.Fatalf("run calls = %d, want 3", r.runCount())
	}
	if got := r.calls()[2].Revision; got != "rev2" {
		t.Fatalf("run calls[2].Revision = %q, want rev2: the child that follows an observed move must run at the resolution that observed it", got)
	}
	if r.resolveCount() != 3 {
		t.Fatalf("resolveCalls = %d, want 3: one resolution per run, not a fetch-then-refetch pair (#3579)", r.resolveCount())
	}
}

// TestLoopTipMovedFollowsMovedFlagNotRevisionDiff pins that tip_moved is
// driven by Tip.Moved alone, never a revision-string diff: Moved and
// Revision are scripted to disagree in both directions, and the event must
// follow the flag each time.
func TestLoopTipMovedFollowsMovedFlagNotRevisionDiff(t *testing.T) {
	cases := []struct {
		name      string
		revisions []string
		moved     []bool
		wantMoved bool
	}{
		{
			name:      "moved true with an unchanged revision still fires",
			revisions: []string{"rev1", "rev1", "rev1"},
			moved:     []bool{false, false, true},
			wantMoved: true,
		},
		{
			name:      "a changed revision with moved false stays silent",
			revisions: []string{"rev1", "rev1", "rev2"},
			moved:     []bool{false, false, false},
			wantMoved: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptedRunner{
				revisions: tc.revisions,
				moved:     tc.moved,
				results:   []ChildResult{{Exit: 3}, {Exit: 3}, {Exit: 5}},
			}
			clk := &testClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			Loop(context.Background(), testConfig(1), r, em, clk)

			events := decodeEvents(t, &buf)
			var tipMoved *Event
			for i := range events {
				if events[i].Event == "tip_moved" {
					tipMoved = &events[i]
				}
			}
			if tc.wantMoved && tipMoved == nil {
				t.Fatalf("events = %v, want a tip_moved event", eventNames(events))
			}
			if !tc.wantMoved && tipMoved != nil {
				t.Fatalf("events = %v, want no tip_moved event", eventNames(events))
			}
		})
	}
}

// TestLoopIdleWaitSwallowsMidWaitPollFailure pins resolveOpportunistic's
// failure branch: a ResolveTip error during the opportunistic resolve
// idleSleep's resolveTip return asks for is not the loop's own
// per-iteration fetch, so it must be swallowed as "no change
// observed" rather than routed to backoffOrHalt (which would trip the
// pool-wide breaker over a transient fetch blip).
//
// Iteration 1: ResolveTip call #1 (top of loop) -> exit 3 -> wait = floor;
// idleSleep sleeps one slice and returns resolveTip = false (nothing
// remained). Iteration 2: call #2 (top of loop) -> exit 3 -> wait = 2*floor;
// idleSleep sleeps slice 1 and returns resolveTip = true. Iteration 3: the
// opportunistic resolve this asked for is call #3, the one set to fail --
// swallowed, phase reset, pickKind still gated -> idleSleep sleeps slice 2
// and returns resolveTip = false. Iteration 4: call #4 (top of loop, dispatch
// now runnable) -> exit 5 -> halt. FailureBackoff is set apart from the
// floor so a stray failure-backoff sleep (a regression routing the
// opportunistic failure to backoffOrHalt) would be unmistakable in
// clk.waits().
func TestLoopIdleWaitSwallowsMidWaitPollFailure(t *testing.T) {
	r := &scriptedRunner{
		revisions:  []string{"rev1"},
		resolveAt:  3,
		resolveErr: errors.New("boom: transient fetch failure"),
		results:    []ChildResult{{Exit: 3}, {Exit: 3}, {Exit: 5}},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.FailureBackoff = 7 * time.Millisecond

	reason := Loop(context.Background(), cfg, r, em, clk).String()

	want := []time.Duration{testIdleFloor, testIdleFloor, testIdleFloor}
	if fmt.Sprint(clk.waits()) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v: the failed mid-wait poll must neither cut the wait short nor add a failure backoff", clk.waits(), want)
	}

	events := decodeEvents(t, &buf)
	for _, ev := range events {
		if ev.Event == "backoff" || ev.Event == "breaker_trip" || ev.Event == "tip_moved" {
			t.Fatalf("events = %v, want no backoff/breaker_trip/tip_moved event", eventNames(events))
		}
	}

	if strings.Contains(reason, "breaker") {
		t.Errorf("halt reason = %q, want it not to name the breaker (halts on exit 5, not a tripped breaker)", reason)
	}

	if r.runCount() != 3 {
		t.Fatalf("run calls = %d, want 3", r.runCount())
	}
}

// TestLoopTipMovedFiresDespiteSelfEvalErrorOnSameResolve pins the reviewer
// finding on resolveTip (pool.go, issue #3625 review round 2): a resolution
// that observes Moved=true must report it even when that same call's
// self-eval half fails and wraps the result in *SelfEvalError. The runner's
// baseline is already advanced by the time a *SelfEvalError is returned
// (resolveTipOnce, double_test.go), so an err == nil guard on the report
// would drop the move for good — no later resolve can ever re-observe it.
//
// The script mirrors TestLoopIdleWaitSwallowsMidWaitPollFailure's jam setup
// (two exit-3 results gate dispatch, growing its backoff), but the
// opportunistic resolve idleSleep asks for (call #3) is scripted with
// Moved=true and a self-eval error rather than a plain fetch error. With the
// fix, resolveTip reports the move before resolveOpportunistic swallows the
// error, so noteTipMoved resets dispatch's backoff mid-iteration: pickKind
// finds it runnable at once, and the ordinary post-pickKind resolve (call
// #4) proceeds in that same loop pass rather than idleSleep sleeping out a
// second slice. That collapses the wait sequence to two Sleep calls instead
// of three, which is the observable this test pins alongside the event
// itself: the pre-fix guard leaves dispatch jammed through call #3, costing
// a third slice before the natural elapse at call #4 unblocks it anyway.
func TestLoopTipMovedFiresDespiteSelfEvalErrorOnSameResolve(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		moved:     []bool{false, false, true, false},
		selfPaths: []string{"/nix/store/same-path"},
		selfErrAt: 3,
		selfErr:   errors.New("eval boom"),
		results:   []ChildResult{{Exit: 3}, {Exit: 3}, {Exit: 5}},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/same-path"

	reason := Loop(context.Background(), cfg, r, em, clk).String()

	events := decodeEvents(t, &buf)
	var tipMoved *Event
	for i := range events {
		if events[i].Event == "tip_moved" {
			tipMoved = &events[i]
		}
	}
	if tipMoved == nil {
		t.Fatalf("events = %v, want a tip_moved event despite the self-eval error on the same resolve", eventNames(events))
	}
	if len(tipMoved.Kinds) != 1 || tipMoved.Kinds[0] != KindDispatch {
		t.Fatalf("tip_moved.Kinds = %v, want [%v]: the jammed kind's backoff must be the one reset", tipMoved.Kinds, KindDispatch)
	}

	want := []time.Duration{testIdleFloor, testIdleFloor}
	if fmt.Sprint(clk.waits()) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v: the reset must unblock dispatch mid-iteration, not cost a third idle slice", clk.waits(), want)
	}

	if r.runCount() != 3 {
		t.Fatalf("run calls = %d, want 3", r.runCount())
	}
	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want it to name host-tainted", reason)
	}
}

// TestLoopAwakeWindowClosedAtStartSleepsFullSpanThenStarts pins the
// sleep-to-next-opening shape: a daemon started outside its window starts no
// child, sleeps exactly the whole remaining span in one Sleep call (not an
// idle-backoff-sized poll), and reports the transition as awake_close then
// awake_open before the first child_start.
func TestLoopAwakeWindowClosedAtStartSleepsFullSpanThenStarts(t *testing.T) {
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}}
	clk := &testClock{now: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Awake = win

	Loop(context.Background(), cfg, r, em, clk)

	if clk.waitCount() != 1 {
		t.Fatalf("waits = %v, want exactly one sleep to the next opening, not a poll loop", clk.waits())
	}
	if clk.waits()[0] != time.Hour {
		t.Fatalf("waits = %v, want the one wait = 1h, the whole remaining span to 09:00", clk.waits())
	}

	wantEventPrefix(t, &buf, []string{"awake_close", "awake_open", "child_start"},
		"the daemon parks for the shut window, reopens, and only then starts its first child")
}

// TestLoopAwakeWindowWraparoundOpenStartsImmediately pins the wraparound
// case (a window crossing midnight) and the "already open at start" case
// together: at 23:00 inside a 22:00-06:00 window, the daemon starts its
// first child at once, with no awake_close/awake_open pair at all.
func TestLoopAwakeWindowWraparoundOpenStartsImmediately(t *testing.T) {
	win, err := ParseWindow("22:00-06:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}}
	clk := &testClock{now: time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Awake = win

	Loop(context.Background(), cfg, r, em, clk)

	if clk.waitCount() != 0 {
		t.Fatalf("waits = %v, want none: the window is already open at start", clk.waits())
	}
	if r.runCount() != 1 {
		t.Fatalf("run calls = %d, want 1", r.runCount())
	}
	for _, ev := range decodeEvents(t, &buf) {
		if ev.Event == "awake_close" || ev.Event == "awake_open" {
			t.Fatalf("events = %v, want no awake_close/awake_open when the window starts open", eventNames(decodeEvents(t, &buf)))
		}
	}
}

// TestLoopAwakeWindowClosesWhileChildRunsFinishesThenParks pins the "a Box
// running when the window closes finishes, and so does its Settle" criterion
// at the loop layer: a child already in flight when the window's close time
// passes still gets its child_finish, and only the *next* iteration parks
// (awake_close) rather than a second child starting straight away.
func TestLoopAwakeWindowClosesWhileChildRunsFinishesThenParks(t *testing.T) {
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	clk := &testClock{now: time.Date(2026, 1, 1, 16, 0, 0, 0, time.UTC)}
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 3}, {Exit: 5}},
		// RunChild advances the shared clock, simulating a Box that
		// outlasts the Awake window: the window can close mid-run without
		// anything in the loop noticing until the child itself returns.
		onStart: func(ctx context.Context, req ChildRequest) error {
			clk.advanceBy(2 * time.Hour) // 16:00 -> 18:00, past the 17:00 close
			return nil
		},
	}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Awake = win

	Loop(context.Background(), cfg, r, em, clk)

	if r.runCount() != 2 {
		t.Fatalf("run calls = %d, want 2: the in-flight child finishes, and a second starts once the window reopens", r.runCount())
	}

	wantEvents(t, &buf, []string{"child_start", "child_finish", "awake_close", "awake_open", "child_start", "child_finish", "halt"}, "no idle/jam step consumed for the wait the closed window owns")
}

// TestLoopAwakeWindowClosesDuringResolveTipParksInsteadOfStarting pins
// a blocking review finding on runSlot: awaitWindow only decides the
// window is open once, and ResolveTip's git fetch can outlast that
// decision, so runSlot re-checks the window after ResolveTip returns.
// A child must never start once the fetch comes back outside the window --
// the slot should park (awake_close/awake_open) and try again, not launch
// straight away.
func TestLoopAwakeWindowClosesDuringResolveTipParksInsteadOfStarting(t *testing.T) {
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	clk := &testClock{now: time.Date(2026, 1, 1, 16, 58, 0, 0, time.UTC)}
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 5}},
		// ResolveTip advances the shared clock, simulating a fetch that
		// spans the window's close: the decision to run was made while
		// still open, but time has moved on by the time the revision comes
		// back. Guarded to call 1 only, so a slot that parks and retries
		// sees a steady clock on its second pass.
		onResolve: func(ctx context.Context, call int) error {
			if call == 1 {
				clk.advanceBy(4 * time.Minute) // 16:58 -> 17:02, past the 17:00 close
			}
			return nil
		},
	}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Awake = win

	Loop(context.Background(), cfg, r, em, clk)

	if r.runCount() != 1 {
		t.Fatalf("run calls = %d, want 1: the closed-window fetch must not start a child, only the retry after the slot parks", r.runCount())
	}

	wantEvents(t, &buf, []string{"awake_close", "awake_open", "child_start", "child_finish", "halt"}, "no child_start until the slot has parked and reopened")
}

// TestLoopSelfChangeHaltsAtIterationBoundary asserts the loop halts before
// starting a child when ResolveTip's Tip.SelfPath reports the daemon's own build
// changed at the fetched tip, and that the halt reason names it.
func TestLoopSelfChangeHaltsAtIterationBoundary(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, selfPaths: []string{"/nix/store/new-path"}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/old-path"

	h := Loop(context.Background(), cfg, r, em, clk)

	if h.Class != HaltSelfChanged {
		t.Fatalf("halt class = %v, want %v", h.Class, HaltSelfChanged)
	}
	if r.runCount() != 0 {
		t.Fatalf("run calls = %d, want 0: the halt must land before a child is launched", r.runCount())
	}

	events := wantEvents(t, &buf, []string{"halt"}, "exactly one halt event")
	if events[0].Reason != h.String() {
		t.Errorf("halt event reason = %q, want %q", events[0].Reason, h.String())
	}
}

// TestLoopSelfChangeNeverReExecs asserts that once a self-change halt has
// fired, Loop returns without ever calling RunChild again — no re-exec, no
// further iteration, whatever ran before the halt is all that ever runs.
func TestLoopSelfChangeNeverReExecs(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1", "rev2"},
		results:   []ChildResult{{Exit: 0}},
		selfPaths: []string{"/nix/store/old-path", "/nix/store/new-path"},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/old-path"

	h := Loop(context.Background(), cfg, r, em, clk)

	if h.Class != HaltSelfChanged {
		t.Fatalf("halt class = %v, want %v", h.Class, HaltSelfChanged)
	}
	if r.runCount() != 1 {
		t.Fatalf("run calls = %d, want 1: the first iteration's matching self-path should run its child, the second iteration's mismatch must halt before any further child", r.runCount())
	}
}

// TestLoopSelfPathMatchDoesNotHalt asserts a SelfPath result equal to
// Config.SelfProgram is a no-op: the loop proceeds to run children as
// normal.
func TestLoopSelfPathMatchDoesNotHalt(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 0}, {Exit: 5}},
		selfPaths: []string{"/nix/store/same-path"},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/same-path"

	h := Loop(context.Background(), cfg, r, em, clk)

	if h.Class == HaltSelfChanged {
		t.Fatalf("halt class = %v, want no self-changed halt: the self path matched", h.Class)
	}
	if r.runCount() != 2 {
		t.Fatalf("run calls = %d, want 2: a matching self path must not stop children from running", r.runCount())
	}
}

// TestLoopEmptySelfProgramSkipsCheck asserts Config.SelfProgram == "" never
// evaluates the self path at all — the check is fully disabled, not merely
// non-halting.
func TestLoopEmptySelfProgramSkipsCheck(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	if r.selfCount() != 0 {
		t.Fatalf("selfCalls = %d, want 0: an empty SelfProgram must never evaluate the self path", r.selfCount())
	}
}

// TestLoopEmptySelfProgramNeverHaltsEvenWithSelfPathReported asserts the
// comparison itself is skipped, not merely made harmless: even when the
// runner's Tip carries a SelfPath (this slice's scriptedRunner serves the
// self half whenever a test scripts selfPaths, whatever cfg.SelfProgram is),
// an empty Config.SelfProgram must never halt on it.
func TestLoopEmptySelfProgramNeverHaltsEvenWithSelfPathReported(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 0}, {Exit: 5}},
		selfPaths: []string{"/nix/store/whatever-path"},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	h := Loop(context.Background(), testConfig(1), r, em, clk)

	if h.Class == HaltSelfChanged {
		t.Fatalf("halt class = %v, want no self-changed halt: Config.SelfProgram is empty", h.Class)
	}
	if r.runCount() != 2 {
		t.Fatalf("run calls = %d, want 2: an empty SelfProgram must never stop children from running", r.runCount())
	}
}

// TestLoopSelfChangeHaltDetailFormat pins the exact detail string a
// self-build mismatch halts with — the same "daemon build at %s is %s,
// running %s" grammar checkSelfBuild used before this slice folded it into
// runSlot's own field comparison.
func TestLoopSelfChangeHaltDetailFormat(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, selfPaths: []string{"/nix/store/new-path"}}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/old-path"

	h := Loop(context.Background(), cfg, r, em, clk)

	want := "daemon build at rev1 is /nix/store/new-path, running /nix/store/old-path"
	if h.Detail != want {
		t.Errorf("halt detail = %q, want %q", h.Detail, want)
	}
}

// TestLoopResolveTipErrorClassification is the regression tripwire that
// keeps the halt class an operator's exit code depends on distinguishable
// between ResolveTip's two error shapes. A *SelfEvalError backs off under
// the self-build reason and carries the resolved revision on the backoff
// event (the fetch half succeeded); a plain fetch error backs off under
// "resolve-revision:" with no revision at all (nothing was ever resolved).
func TestLoopResolveTipErrorClassification(t *testing.T) {
	tests := []struct {
		name         string
		r            *scriptedRunner
		wantPrefix   string
		wantRevision string
	}{
		{
			name: "self eval error",
			r: &scriptedRunner{
				revisions: []string{"rev1"},
				results:   []ChildResult{{Exit: 5}},
				selfErrAt: 1,
				selfErr:   errors.New("eval boom"),
			},
			wantPrefix:   "self-build:",
			wantRevision: "rev1",
		},
		{
			name: "fetch error",
			r: &scriptedRunner{
				revisions:  []string{"rev1"},
				results:    []ChildResult{{Exit: 5}},
				resolveAt:  1,
				resolveErr: errors.New("fetch boom"),
			},
			wantPrefix:   "resolve-revision:",
			wantRevision: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := &testClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			cfg := testConfig(1)
			cfg.SelfProgram = "/nix/store/same-path"
			cfg.FailureBackoff = 5 * time.Millisecond

			Loop(context.Background(), cfg, tt.r, em, clk)

			events := decodeEvents(t, &buf)
			var backoff *Event
			for i := range events {
				if events[i].Event == "backoff" {
					backoff = &events[i]
					break
				}
			}
			if backoff == nil {
				t.Fatalf("events = %v, want a backoff event", eventNames(events))
			}
			if !strings.HasPrefix(backoff.Reason, tt.wantPrefix) {
				t.Errorf("backoff reason = %q, want prefix %q", backoff.Reason, tt.wantPrefix)
			}
			if backoff.Revision != tt.wantRevision {
				t.Errorf("backoff revision = %q, want %q", backoff.Revision, tt.wantRevision)
			}
		})
	}
}

// TestLoopSelfPathErrorBacksOff asserts a SelfPath error is treated like any
// other unclassified iteration-boundary failure: this slot backs off
// (reason prefixed self-build:) and retries, rather than halting the pool.
func TestLoopSelfPathErrorBacksOff(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 5}},
		selfPaths: []string{"/nix/store/same-path"},
		selfErrAt: 1,
		selfErr:   errors.New("eval boom"),
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/same-path"
	cfg.FailureBackoff = 5 * time.Millisecond

	h := Loop(context.Background(), cfg, r, em, clk)

	if h.Class == HaltSelfChanged {
		t.Fatalf("halt class = %v, want no self-changed halt: SelfPath only errored once, then matched", h.Class)
	}
	if r.selfCount() != 2 {
		t.Fatalf("selfCalls = %d, want 2: the errored call plus the retry", r.selfCount())
	}
	if r.runCount() != 1 {
		t.Fatalf("run calls = %d, want 1: the slot must retry after backing off", r.runCount())
	}

	events := decodeEvents(t, &buf)
	found := false
	for _, ev := range events {
		if ev.Event == "backoff" {
			found = true
			if !strings.HasPrefix(ev.Reason, "self-build:") {
				t.Errorf("backoff reason = %q, want prefix %q", ev.Reason, "self-build:")
			}
		}
	}
	if !found {
		t.Fatalf("events = %v, want a backoff event for the SelfPath error", eventNames(events))
	}
}

// TestLoopSelfChangeDrainsRunningChild asserts the "drains running children
// rather than killing them" criterion for the self-change halt specifically:
// with one slot's child already running when a sibling slot's self-check
// halts the pool, the running child's child_finish is still emitted and
// neither slot returns until that child has actually returned. It drives
// runSlot against a newPool rather than Loop so the two slots can be
// launched in a pinned order, for the reason below; Loop adds nothing over
// that here beyond the goroutine fan-out this does itself.
//
// leadSlot must win the matching (call 1) path, not siblingSlot: leadSlot
// is newPool's pre-assigned baton holder (pool.go), and awaitBaton now
// sits after the resolve/self-check, right before the child start (issue
// #3625's structural fix). If leadSlot instead landed on call 2 — the
// branch that blocks on `started`, which only a child actually starting
// can close — it would sit in self-eval forever waiting for a child that
// can never start: starting requires the baton, and leadSlot cannot pass
// a baton it is holding while blocked inside its own self-eval. That is a
// genuine deadlock, but purely a test-construction one:
// self-eval never depends cross-slot in production, only this fixture's
// own `started` handshake does — so the fix is pinning call order via a
// staggered launch, the same pattern the baton release-path tests use,
// rather than leaving it to a shared call-index race.
func TestLoopSelfChangeDrainsRunningChild(t *testing.T) {
	const matchPath = "/nix/store/old-path"
	const newPath = "/nix/store/new-path"
	leadEntered := make(chan struct{}) // closed by onSelf once leadSlot's call 1 lands
	started := make(chan struct{})     // closed by onStart the moment the child starts
	release := make(chan struct{})     // closed by the test to let RunChild return
	var startOnce sync.Once
	var leadEnteredOnce sync.Once

	r := &scriptedRunner{
		revisions: []string{"rev1"},
		selfPaths: []string{matchPath, newPath},
		results:   []ChildResult{{Exit: 0}},
		onSelf: func(ctx context.Context, call int, revision string) error {
			if call == 1 {
				leadEnteredOnce.Do(func() { close(leadEntered) })
				return nil
			}
			// Not first: wait for the other slot's child to actually be
			// running before reporting the mismatch, so the halt this
			// triggers is guaranteed to race a genuinely in-flight child,
			// not an imagined one.
			<-started
			return nil
		},
		onStart: func(ctx context.Context, req ChildRequest) error {
			startOnce.Do(func() { close(started) })
			<-release
			return nil
		},
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(2)
	cfg.SelfProgram = matchPath

	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()

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

	<-started      // the long-running child is confirmed running
	close(release) // let it finish now that it is known to have been running

	wg.Wait()
	h := p.haltReason()

	if h.Class != HaltSelfChanged {
		t.Fatalf("halt class = %v, want %v", h.Class, HaltSelfChanged)
	}
	if r.runCount() != 1 {
		t.Fatalf("run calls = %d, want 1: only the already-running child ever runs", r.runCount())
	}

	events := decodeEvents(t, &buf)
	names := eventNames(events)
	haltCount := 0
	sawFinish := false
	for _, n := range names {
		switch n {
		case "halt":
			haltCount++
		case "child_finish":
			sawFinish = true
		}
	}
	if haltCount != 1 {
		t.Fatalf("halt events = %d, want exactly 1: events = %v", haltCount, names)
	}
	if !sawFinish {
		t.Fatalf("events = %v, want a child_finish for the drained child", names)
	}
}

// TestLoopPublishesLiveStatus drives a Loop run with Config.Status pointed
// at a temp dir and asserts the status file names the in-flight child's
// kind/revision/issues while RunChild is still running, and that a valid
// status file survives the run (issue #3545).
func TestLoopPublishesLiveStatus(t *testing.T) {
	dir := t.TempDir()
	clk := &testClock{}
	sw := NewStatusWriter(dir, func() time.Time { return time.Unix(0, 0).UTC() })

	// report/probeErr are read back from inside onStart, while the child is
	// still "running" from the pool's point of view, so the assertion below
	// is genuinely about the in-flight file, not the one left behind after
	// the child returns.
	var report StatusReport
	var probeErr error
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 5}}, // host-tainted: Loop halts promptly after this call
		onStart: func(ctx context.Context, req ChildRequest) error {
			req.OnRecord(Record{Event: reportpkg.EventBox, Issue: "42"})
			report, probeErr = ReadStatus(dir)
			return nil
		},
	}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Status = sw

	reason := Loop(context.Background(), cfg, r, em, clk).String()
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	if probeErr != nil {
		t.Fatalf("ReadStatus during RunChild: %v", probeErr)
	}
	if report.Status == nil {
		t.Fatalf("status was nil while the child was still running")
	}
	st := report.Status
	if st.State != StateWorking {
		t.Fatalf("in-flight state = %q, want %q", st.State, StateWorking)
	}
	if len(st.Slots) != 1 || !st.Slots[0].Busy || st.Slots[0].Kind != KindDispatch || st.Slots[0].Revision != "rev1" {
		t.Fatalf("in-flight slot = %+v, want busy dispatch@rev1", st.Slots)
	}
	if !reflect.DeepEqual(st.Slots[0].Issues, []string{"42"}) {
		t.Fatalf("in-flight issues = %v, want [42]", st.Slots[0].Issues)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus after Loop returned: %v", err)
	}
	if report.Status == nil {
		t.Fatalf("status file missing after Loop finished")
	}
}

// TestLoopBoxSettledAndUnknownRecords drives one child through a box
// record (with phase), an unknown record, and a settled record over
// OnRecord (issue #3627), and pins: the box event carries phase and lands
// the issue in the live status file's slot while the child is still
// running; the unknown record produces neither an event nor an error; the
// settled event carries issue/state/note; and child_finish carries the
// issue the child claimed.
func TestLoopBoxSettledAndUnknownRecords(t *testing.T) {
	dir := t.TempDir()
	clk := &testClock{}
	sw := NewStatusWriter(dir, func() time.Time { return time.Unix(0, 0).UTC() })

	var report StatusReport
	var probeErr error
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 5}}, // host-tainted: Loop halts promptly after this call
		onStart: func(ctx context.Context, req ChildRequest) error {
			req.OnRecord(Record{Event: reportpkg.EventBox, Issue: "42", Phase: "initial"})
			// A live status read here is genuinely about the in-flight
			// file, not the one left behind after the child returns.
			report, probeErr = ReadStatus(dir)
			// An event this daemon build doesn't recognise must produce
			// neither an event nor an error — never crash the slot
			// goroutine mid-run.
			req.OnRecord(Record{Event: "heartbeat", Issue: "99"})
			req.OnRecord(Record{Event: reportpkg.EventSettled, Issue: "42", State: "complete", Note: "merged clean"})
			return nil
		},
	}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Status = sw

	reason := Loop(context.Background(), cfg, r, em, clk).String()
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}

	if probeErr != nil {
		t.Fatalf("ReadStatus during RunChild: %v", probeErr)
	}
	if report.Status == nil {
		t.Fatalf("status was nil while the child was still running")
	}
	if !reflect.DeepEqual(report.Status.Slots[0].Issues, []string{"42"}) {
		t.Fatalf("in-flight issues = %v, want [42] (the heartbeat record must not appear)", report.Status.Slots[0].Issues)
	}

	events := wantEvents(t, &buf, []string{"child_start", "box", "settled", "child_finish", "halt"}, "")

	box := events[1]
	if box.Issue != "42" || box.Phase != "initial" {
		t.Errorf("box event = %+v, want issue 42 phase initial", box)
	}
	settled := events[2]
	if settled.Issue != "42" || settled.State != "complete" || settled.Note != "merged clean" {
		t.Errorf("settled event = %+v, want issue 42 state complete note %q", settled, "merged clean")
	}
	finish := events[3]
	if finish.Issue != "42" {
		t.Errorf("child_finish event Issue = %q, want %q", finish.Issue, "42")
	}
}

// TestLoopNilStatusPublishesNothing pins Config.Status == nil as the
// deliberate opt-out: Loop must run to completion without panicking and
// must never create a status file. The check against dir would be vacuous
// on its own — nothing ties dir to cfg when Status is nil, so no run could
// ever write there — so the second half re-runs the same shape with
// cfg.Status wired to a StatusWriter over that same dir, proving a file
// does land there when Status is non-nil and making the first half's
// absence assertion load-bearing rather than a check against thin air.
func TestLoopNilStatusPublishesNothing(t *testing.T) {
	dir := t.TempDir()
	statusPath := filepath.Join(dir, statusFileName)
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	// cfg.Status is left nil deliberately.
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}}
	Loop(context.Background(), cfg, r, em, clk)

	if _, err := os.Stat(statusPath); !os.IsNotExist(err) {
		t.Fatalf("status file exists at %s despite nil Config.Status: err=%v", dir, err)
	}

	cfg.Status = NewStatusWriter(dir, clk.Now)
	r2 := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}}
	Loop(context.Background(), cfg, r2, em, clk)

	if _, err := os.Stat(statusPath); err != nil {
		t.Fatalf("status file missing at %s once Config.Status was wired: %v", dir, err)
	}
}

// TestLoopInvalidConfigPublishesHaltedStatus pins invalidConfig's Write:
// Loop rejects a bad config before any pool exists, so there is no
// pool.snapshot to publish through, yet a wired Status must still end up
// holding a StateHalted record naming the same reason returned to the
// caller — otherwise a checkout's status file is left at whatever a
// predecessor run last published, under a lock this run now holds.
func TestLoopInvalidConfigPublishesHaltedStatus(t *testing.T) {
	dir := t.TempDir()
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(0) // Slots <= 0 is rejected before a pool is built
	cfg.Status = NewStatusWriter(dir, clk.Now)
	r := &scriptedRunner{}

	reason := Loop(context.Background(), cfg, r, em, clk).String()
	if !strings.Contains(reason, "config-invalid") {
		t.Fatalf("halt reason = %q, want it to name config-invalid", reason)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if report.Status == nil {
		t.Fatalf("status file missing after an invalid-config halt")
	}
	if report.Status.State != StateHalted {
		t.Errorf("state = %q, want %q", report.Status.State, StateHalted)
	}
	if report.Status.Reason != reason {
		t.Errorf("status reason = %q, want %q", report.Status.Reason, reason)
	}
}

// TestLoopCoalescesConcurrentResolvesAcrossSlots is the Loop-seam
// acceptance test for issue #3625's coalescing criterion: three slots
// released together, in one pool, cost the shared Runner one resolve, not
// three.
//
// This is one Loop with Slots: 3, not three independent one-slot Loops:
// the baton no longer gates ResolveTip (issue #3625's structural fix moved
// acquisition past it, to immediately before the child start), so every
// slot's very first round reaches the post-pickKind resolve concurrently
// with no setup needed to force it — that overlap is what makes
// resolveCount() == 1 a claim about Loop's own behaviour rather than about
// the double's single-flight in isolation. Against a Loop that still
// serialized resolves behind the baton, only the leader would ever reach
// holdResolve's park and awaitResolveJoins below would time out waiting
// for joiners that could never arrive, rather than the test passing.
//
// coalesceResolve+holdResolve pins the leader in place until the other two
// have genuinely joined its flight (not merely started their own — a sleep
// here would make the test flaky by construction), then releases it: one
// resolve should answer all three. resolveCount() == 1 is the tripwire —
// three independently resolving slots would have left it at 3.
func TestLoopCoalescesConcurrentResolvesAcrossSlots(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{
		revisions:       []string{"rev1"},
		results:         []ChildResult{{Exit: 5}}, // host-tainted: halts the whole pool once any slot starts a child
		coalesceResolve: true,
	}
	r.holdResolve(slots - 1) // the leader itself never joins its own flight

	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	r.awaitResolveJoins(t, slots-1)
	r.releaseResolve(t)

	<-done

	if got := r.resolveCount(); got != 1 {
		t.Fatalf("resolveCalls = %d, want 1: three slots resolving independently would have made 3", got)
	}
}

// TestLoopCoalescedResolveFailureBacksOffEverySlot is the Loop-seam
// acceptance test for issue #3625 acceptance criterion 6: a fetch error
// surfaced to every caller sharing one flight still leaves each of them
// backing off on its own, same as an uncoalesced failure would. The
// coalescing collapses the fetch itself, not the per-slot backoff that
// follows it.
//
// holdResolve+awaitResolveJoins forces the same genuine three-way overlap
// TestLoopCoalescesConcurrentResolvesAcrossSlots relies on, so resolveErr
// answers all three callers of one shared flight rather than three
// independent resolves that merely happened to fail identically. rearmResolve
// then re-uses the same join count for round 2 as a synchronization
// barrier — all three slots reaching round 2's flight is only possible once
// every slot has finished its own round-1 backoff — before the events
// buffer is read. A follow-up successful resolve (resolveAt is 1-based and
// only matches the first leader) and a host-tainted exit then give the pool
// a real halt, the same way TestLoopResolveErrorBacksOffThenHalts ends its
// single-slot case, so this test terminates rather than looping forever on
// backoffs.
func TestLoopCoalescedResolveFailureBacksOffEverySlot(t *testing.T) {
	const slots = 3
	wantErr := errors.New("boom: no such revision")
	r := &scriptedRunner{
		revisions:       []string{"rev1"},
		resolveAt:       1,
		resolveErr:      wantErr,
		results:         []ChildResult{{Exit: 5}}, // host-tainted: halts the pool once any slot starts a child
		coalesceResolve: true,
	}
	r.holdResolve(slots - 1) // the leader itself never joins its own flight

	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), testConfig(slots), r, em, clk)
	}()

	r.awaitResolveJoins(t, slots-1)

	// Each slot must finish its own backoffOrHalt (and thus emit its own
	// "backoff" event) before it loops back for round 2's resolve, so
	// seeing all three converge on round 2's flight is the synchronization
	// point that proves round 1's backoffs are all done — without it, a
	// fast leader could race ahead into round 2 and beyond before the
	// other two slots recorded anything, which is exactly the -race
	// failure an earlier version of this test hit. rearmResolve installs
	// round 2's hold before releasing round 1's, so no slot can slip
	// through unheld between the two rounds (see rearmResolve's own doc).
	r.rearmResolve(t, slots-1) // round 1's shared flight fails for all three
	r.awaitResolveJoins(t, slots-1)

	// Without coalescing, resolveAt: 1 would only ever fail the one caller
	// that happened to make call #1 — the other two would succeed and run
	// a child straight away, never backing off at all. All three slots
	// reporting their own backoff below is only possible because the one
	// shared flight's error answered all of them.
	events := decodeEvents(t, &buf)
	seen := map[int]bool{}
	for _, ev := range events {
		if ev.Event != "backoff" {
			continue
		}
		if ev.Slot == nil {
			t.Fatalf("backoff event has no slot: %+v", ev)
		}
		if !strings.Contains(ev.Reason, wantErr.Error()) {
			t.Errorf("backoff event slot %d reason = %q, want it to name %v", *ev.Slot, ev.Reason, wantErr)
		}
		seen[*ev.Slot] = true
	}
	if len(seen) != slots {
		t.Fatalf("slots reporting their own backoff = %v, want all %d slots: the shared failure must not merge into fewer backoffs than slots", seen, slots)
	}

	r.releaseResolve(t) // round 2 succeeds (resolveAt matches only call #1); the pool runs a child and halts

	reason := (<-done).String()
	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want the follow-up host-tainted halt, not the shared resolve failure itself", reason)
	}
}
