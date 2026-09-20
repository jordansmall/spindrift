package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner scripts ResolveRevision/RunChild for the loop tests: no
// subprocess, no container, no tracker. It records every call it was asked
// for so a test can assert scheduling behaviour, not argv or how the fetch
// was performed.
type fakeRunner struct {
	revisions  []string // one per ResolveRevision call; last value repeats once exhausted
	resolveAt  int      // 1-based call index that returns resolveErr instead of a revision
	resolveErr error

	results  []ChildResult // one per RunChild call, consumed in order
	runErrAt int           // 1-based call index that returns runErr instead of a result
	runErr   error
	// runErrIssues rides alongside runErr, as runner.go's non-ExitError wait
	// failure does when it returns the issues already scanned before the
	// seam itself failed.
	runErrIssues []string

	selfPaths []string // one per SelfPath call; last value repeats once exhausted
	selfErrAt int      // 1-based call index that returns selfErr instead of a path
	selfErr   error

	resolveCalls int
	runCalls     []runCall
	selfCalls    int
}

type runCall struct {
	Kind     Kind
	Revision string
	Slot     int
}

func (f *fakeRunner) ResolveRevision(ctx context.Context) (string, error) {
	f.resolveCalls++
	if f.resolveAt != 0 && f.resolveCalls == f.resolveAt {
		return "", f.resolveErr
	}
	idx := f.resolveCalls - 1
	if idx >= len(f.revisions) {
		idx = len(f.revisions) - 1
	}
	return f.revisions[idx], nil
}

// SelfPath scripts the same way ResolveRevision does: selfPaths consumed in
// order (last value repeating once exhausted), selfErrAt/selfErr standing in
// for a 1-based call index that fails instead.
func (f *fakeRunner) SelfPath(ctx context.Context, revision string) (string, error) {
	f.selfCalls++
	if f.selfErrAt != 0 && f.selfCalls == f.selfErrAt {
		return "", f.selfErr
	}
	if len(f.selfPaths) == 0 {
		return "", nil
	}
	idx := f.selfCalls - 1
	if idx >= len(f.selfPaths) {
		idx = len(f.selfPaths) - 1
	}
	return f.selfPaths[idx], nil
}

func (f *fakeRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	f.runCalls = append(f.runCalls, runCall{Kind: req.Kind, Revision: req.Revision, Slot: req.Slot})
	call := len(f.runCalls)
	if f.runErrAt != 0 && call == f.runErrAt {
		return ChildResult{Issues: f.runErrIssues}, f.runErr
	}
	idx := call - 1
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}
	return f.results[idx], nil
}

// fakeClock is the test Clock: Sleep never actually sleeps, just records the
// durations it was asked to wait so tests run instantly and can assert on
// wait behaviour, and advances a virtual now by each one. Guarded by a
// mutex: a later slice runs several slots concurrently against one clock.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
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
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk)

	if len(r.runCalls) != 2 {
		t.Fatalf("run calls = %d, want 2", len(r.runCalls))
	}
	if len(clk.waits) != 0 {
		t.Fatalf("waits = %v, want none (exit 0 continues at once)", clk.waits)
	}
	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want it to name host-tainted", reason)
	}
}

func TestLoopWaitThenHalt(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}, {Exit: 0}, {Exit: 6}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk)

	if len(r.runCalls) != 3 {
		t.Fatalf("run calls = %d, want 3", len(r.runCalls))
	}
	if len(clk.waits) != 1 || clk.waits[0] != testIdleFloor {
		t.Fatalf("waits = %v, want exactly one wait of %v", clk.waits, testIdleFloor)
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
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 3}, {Exit: 5}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	if len(clk.waits) != 1 || clk.waits[0] != testIdleFloor {
		t.Fatalf("waits = %v, want exactly one wait of %v for exit 3", clk.waits, testIdleFloor)
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
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}, {Exit: 2}, {Exit: 5}}}
	clk := &fakeClock{}
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
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 4}, {Exit: 5}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	if len(clk.waits) != 0 {
		t.Fatalf("waits = %v, want none for exit 4 (image-stale continues at once)", clk.waits)
	}
	if len(r.runCalls) != 2 {
		t.Fatalf("run calls = %d, want 2", len(r.runCalls))
	}
}

func TestLoopExit7Halts(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 7}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk)

	if !strings.Contains(reason, "signalled-stop") {
		t.Errorf("halt reason = %q, want it to name signalled-stop", reason)
	}
}

// TestLoopUnknownExitBacksOffThenHalts pins the new contract for an
// unrecognised exit code: it backs the slot off rather than halting the
// pool outright. A follow-up host-tainted exit gives the fake a real halt
// so the test still terminates rather than looping forever on backoffs.
func TestLoopUnknownExitBacksOffThenHalts(t *testing.T) {
	for _, exit := range []int{1, 99} {
		r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: exit}, {Exit: 5}}}
		clk := &fakeClock{}
		var buf bytes.Buffer
		em := newTestEmitter(&buf)

		reason := Loop(context.Background(), testConfig(1), r, em, clk)

		if !strings.Contains(reason, "host-tainted") {
			t.Errorf("exit %d: halt reason = %q, want the follow-up host-tainted halt, not the unclassified exit itself", exit, reason)
		}
		if len(clk.waits) != 1 || clk.waits[0] != testFailureBackoff {
			t.Errorf("exit %d: waits = %v, want exactly one wait of %v (FailureBackoff)", exit, clk.waits, testFailureBackoff)
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
// ResolveRevision error (a failed git fetch) is exactly the transient blip
// that must not end the night, so it backs off its slot rather than
// halting the pool. A follow-up host-tainted exit gives the fake a real
// halt so the test still terminates.
func TestLoopResolveErrorBacksOffThenHalts(t *testing.T) {
	wantErr := errors.New("boom: no such revision")
	r := &fakeRunner{revisions: []string{"rev1"}, resolveAt: 1, resolveErr: wantErr, results: []ChildResult{{Exit: 5}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk)

	if len(r.runCalls) != 1 {
		t.Fatalf("run calls = %d, want 1: the slot must retry after backing off from the resolve failure", len(r.runCalls))
	}
	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want the follow-up host-tainted halt, not the resolve failure itself", reason)
	}
	if len(clk.waits) != 1 || clk.waits[0] != testFailureBackoff {
		t.Fatalf("waits = %v, want exactly one wait of %v (FailureBackoff)", clk.waits, testFailureBackoff)
	}

	events := decodeEvents(t, &buf)
	names := eventNames(events)
	wantNames := []string{"backoff", "child_start", "child_finish", "halt"}
	if fmt.Sprint(names) != fmt.Sprint(wantNames) {
		t.Fatalf("events = %v, want %v", names, wantNames)
	}
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
	r := &fakeRunner{revisions: []string{"rev1"}, runErrAt: 1, runErr: wantErr, results: []ChildResult{{Exit: 5}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk)

	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want the follow-up host-tainted halt, not the run-child failure itself", reason)
	}

	events := decodeEvents(t, &buf)
	names := eventNames(events)
	wantNames := []string{"child_start", "child_finish", "backoff", "child_start", "child_finish", "halt"}
	if len(names) != len(wantNames) {
		t.Fatalf("events = %v, want %v", names, wantNames)
	}
	for i, n := range wantNames {
		if names[i] != n {
			t.Errorf("events[%d] = %q, want %q", i, names[i], n)
		}
	}
}

// TestLoopRunChildErrorStillEmitsAnnouncedBoxes covers the non-ExitError
// wait-failure path (runner.go): RunChild can return Issues alongside an
// error when the child announced Boxes before the seam itself failed. Those
// Boxes must still reach the durable stream, so a box event lands before
// child_finish even on the error path.
func TestLoopRunChildErrorStillEmitsAnnouncedBoxes(t *testing.T) {
	wantErr := errors.New("wait: signal: killed")
	r := &fakeRunner{revisions: []string{"rev1"}, runErrAt: 1, runErr: wantErr, runErrIssues: []string{"42"}, results: []ChildResult{{Exit: 5}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	events := decodeEvents(t, &buf)
	names := eventNames(events)
	wantNames := []string{"child_start", "box", "child_finish", "backoff", "child_start", "child_finish", "halt"}
	if fmt.Sprint(names) != fmt.Sprint(wantNames) {
		t.Fatalf("events = %v, want %v", names, wantNames)
	}
	if events[1].Issue != "42" {
		t.Errorf("box event Issue = %q, want %q", events[1].Issue, "42")
	}
}

func TestLoopEventStreamSequenceAndFields(t *testing.T) {
	r := &fakeRunner{
		revisions: []string{"rev1"},
		results: []ChildResult{
			{Exit: 0, Issues: []string{"10", "11"}},
			{Exit: 5},
		},
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	events := decodeEvents(t, &buf)
	wantNames := []string{"child_start", "box", "box", "child_finish", "child_start", "child_finish", "halt"}
	if got := eventNames(events); fmt.Sprint(got) != fmt.Sprint(wantNames) {
		t.Fatalf("event sequence = %v, want %v", got, wantNames)
	}

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
	r := &fakeRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 2}, {Exit: 2}, {Exit: 0}, {Exit: 5}},
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), testConfig(1), r, em, clk)

	if len(r.runCalls) != 4 {
		t.Fatalf("run calls = %d, want 4: the loop must keep going after the queue drains and pick work back up without a restart", len(r.runCalls))
	}
	if len(clk.waits) != 2 {
		t.Fatalf("waits = %v, want exactly two (one per empty-queue exit)", clk.waits)
	}
	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want it to name host-tainted", reason)
	}
}

func TestLoopPinsEachChildToTheResolvedRevisionEvenWhenItChanges(t *testing.T) {
	r := &fakeRunner{
		revisions: []string{"rev1", "rev2", "rev3"},
		results:   []ChildResult{{Exit: 0}, {Exit: 0}, {Exit: 5}},
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	want := []runCall{
		{Kind: KindDispatch, Revision: "rev1"},
		{Kind: KindDispatch, Revision: "rev2"},
		{Kind: KindDispatch, Revision: "rev3"},
	}
	if fmt.Sprint(r.runCalls) != fmt.Sprint(want) {
		t.Fatalf("run calls = %v, want %v: each child must be pinned to that iteration's resolved revision", r.runCalls, want)
	}
}

func TestLoopCancelledContextHaltsBeforeStartingNewWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, testConfig(1), r, em, clk)

	if len(r.runCalls) != 0 {
		t.Fatalf("run calls = %d, want 0: an already-cancelled ctx must halt before starting any child", len(r.runCalls))
	}
	if !strings.Contains(reason, "context") {
		t.Errorf("halt reason = %q, want it to name the cancellation", reason)
	}
}

func TestLoopCancelledDuringResolveRevisionHaltsBeforeStartingNewWork(t *testing.T) {
	// A Runner whose ResolveRevision cancels ctx mid-fetch, as the
	// production adapter's git-fetch path can when the operator sends
	// SIGTERM while a resolve is in flight — the loop must re-check ctx
	// after ResolveRevision returns and halt instead of starting a child
	// that nothing will ever signal.
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancellingResolveRunner{cancel: cancel, revision: "rev1", result: ChildResult{Exit: 0}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, testConfig(1), r, em, clk)

	if r.runCalls != 0 {
		t.Fatalf("run calls = %d, want 0: a ctx cancelled during resolve must halt before starting any child", r.runCalls)
	}
	events := decodeEvents(t, &buf)
	names := eventNames(events)
	wantNames := []string{"halt"}
	if fmt.Sprint(names) != fmt.Sprint(wantNames) {
		t.Fatalf("events = %v, want %v: no child_start once ctx is cancelled mid-resolve", names, wantNames)
	}
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
	r := &cancellingRunner{cancel: cancel, revision: "rev1", result: ChildResult{Exit: 0}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, testConfig(1), r, em, clk)

	if r.runCalls != 1 {
		t.Fatalf("run calls = %d, want 1", r.runCalls)
	}
	events := decodeEvents(t, &buf)
	names := eventNames(events)
	wantNames := []string{"child_start", "child_finish", "halt"}
	if fmt.Sprint(names) != fmt.Sprint(wantNames) {
		t.Fatalf("events = %v, want %v: the started child's child_finish must still be emitted", names, wantNames)
	}
	if !strings.Contains(reason, "context") {
		t.Errorf("halt reason = %q, want it to name the cancellation", reason)
	}
}

// TestLoopSelfPathCtxCancelledIsNotABreakerFailure asserts that a SelfPath
// call whose ctx is cancelled out from under it (an operator SIGTERM racing
// the evaluation) reports a plain context-cancelled halt, not a breaker
// failure: with MAX_PARALLEL >= the breaker threshold, several slots hitting
// this on one SIGTERM could otherwise trip the breaker and turn a clean
// stop's exit 0 into exit 1 (issue #3543).
func TestLoopSelfPathCtxCancelledIsNotABreakerFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelErrRunner{cancel: cancel, cancelIn: seamSelfPath, revision: "rev1"}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/old-path"

	reason := Loop(ctx, cfg, r, em, clk)

	assertCancelledStopNoBreaker(t, reason, &buf)
}

// TestLoopResolveRevisionCtxCancelledIsNotABreakerFailure is
// TestLoopSelfPathCtxCancelledIsNotABreakerFailure's sibling for the
// ResolveRevision error path in runSlot (loop.go), which has had this same
// hazard since before this diff.
func TestLoopResolveRevisionCtxCancelledIsNotABreakerFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelErrRunner{cancel: cancel, cancelIn: seamResolveRevision}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, testConfig(1), r, em, clk)

	assertCancelledStopNoBreaker(t, reason, &buf)
}

// assertCancelledStopNoBreaker fails unless Loop stopped on the cancellation
// itself and left none of the bookkeeping a real seam failure would.
func assertCancelledStopNoBreaker(t *testing.T, reason string, buf *bytes.Buffer) {
	t.Helper()
	if !strings.HasPrefix(reason, "context-cancelled:") {
		t.Fatalf("halt reason = %q, want prefix %q", reason, "context-cancelled:")
	}
	events := decodeEvents(t, buf)
	for _, e := range events {
		if e.Event == "backoff" || e.Event == "breaker_trip" {
			t.Fatalf("events = %v, want no backoff/breaker_trip event on an ordinary operator stop", events)
		}
	}
}

type cancelSeam string

const (
	seamResolveRevision cancelSeam = "resolve-revision"
	seamSelfPath        cancelSeam = "self-path"
)

// cancelErrRunner models an operator SIGTERM landing inside one seam: the
// seam cancelIn names cancels the parent ctx and returns its ctx.Err(), and
// every seam the loop must not reach afterwards errors loudly rather than
// handing back a zero value the loop would read as a real answer.
type cancelErrRunner struct {
	cancel   context.CancelFunc
	cancelIn cancelSeam
	revision string
}

func (r *cancelErrRunner) ResolveRevision(ctx context.Context) (string, error) {
	if r.cancelIn == seamResolveRevision {
		r.cancel()
		return "", ctx.Err()
	}
	return r.revision, nil
}

func (r *cancelErrRunner) SelfPath(ctx context.Context, revision string) (string, error) {
	if r.cancelIn == seamSelfPath {
		r.cancel()
		return "", ctx.Err()
	}
	return "", fmt.Errorf("must not be called: the %s seam cancels before the self check runs", r.cancelIn)
}

func (r *cancelErrRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	return ChildResult{}, fmt.Errorf("must not be called: a cancelled %s must halt before a child ever starts", r.cancelIn)
}

type cancellingRunner struct {
	cancel   context.CancelFunc
	revision string
	result   ChildResult
	runCalls int
}

func (r *cancellingRunner) ResolveRevision(ctx context.Context) (string, error) {
	return r.revision, nil
}

func (r *cancellingRunner) SelfPath(ctx context.Context, revision string) (string, error) {
	return "", nil
}

func (r *cancellingRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	r.runCalls++
	r.cancel()
	return r.result, nil
}

type cancellingResolveRunner struct {
	cancel   context.CancelFunc
	revision string
	result   ChildResult
	runCalls int
}

func (r *cancellingResolveRunner) ResolveRevision(ctx context.Context) (string, error) {
	r.cancel()
	return r.revision, nil
}

func (r *cancellingResolveRunner) SelfPath(ctx context.Context, revision string) (string, error) {
	return "", nil
}

func (r *cancellingResolveRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	r.runCalls++
	return r.result, nil
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

			reason := Loop(context.Background(), testConfig(slots), r, em, clk)

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
			r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
			clk := &fakeClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			reason := Loop(context.Background(), tc.cfg, r, em, clk)

			if len(r.runCalls) != 0 {
				t.Fatalf("run calls = %d, want 0: an invalid kinds config must halt before any child runs", len(r.runCalls))
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
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{
		{Exit: 2}, {Exit: 2}, {Exit: 2}, {Exit: 2}, {Exit: 2}, {Exit: 5},
	}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.IdleFloor = time.Millisecond
	cfg.IdleCap = 4 * time.Millisecond

	Loop(context.Background(), cfg, r, em, clk)

	want := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond}
	if fmt.Sprint(clk.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v", clk.waits, want)
	}
}

// TestLoopIdleBackoffResetsOnDispatch pins the reset rule this slice adds:
// a check that actually dispatches (exit 0) is evidence the queue was not
// really idle, so the very next no-work check must wait the floor again,
// not the grown value the streak was on before the dispatch.
func TestLoopIdleBackoffResetsOnDispatch(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{
		{Exit: 2}, {Exit: 2}, {Exit: 0}, {Exit: 2}, {Exit: 5},
	}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	want := []time.Duration{testIdleFloor, 2 * testIdleFloor, testIdleFloor}
	if fmt.Sprint(clk.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v (dispatch between the two no-work streaks resets to the floor)", clk.waits, want)
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
			r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
			clk := &fakeClock{}
			var buf bytes.Buffer
			em := newTestEmitter(&buf)

			reason := Loop(context.Background(), tc.cfg, r, em, clk)

			if len(r.runCalls) != 0 {
				t.Fatalf("run calls = %d, want 0: an invalid idle config must halt before any child runs", len(r.runCalls))
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
	r := &fakeRunner{
		revisions: []string{"rev1", "rev1", "rev1", "rev1", "rev2"},
		results:   []ChildResult{{Exit: 3}, {Exit: 3}, {Exit: 3}, {Exit: 5}},
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	// Three no-work checks grow the wait to floor, 2*floor, 4*floor; the
	// third one's poll sees the moved tip after its first slice, so it
	// contributes only one more floor-sized sleep instead of riding out
	// the rest of its 4*floor wait.
	want := []time.Duration{testIdleFloor, testIdleFloor, testIdleFloor, testIdleFloor}
	if fmt.Sprint(clk.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v (tip-moved cuts the third wait to a single floor slice)", clk.waits, want)
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

	if len(r.runCalls) != 4 {
		t.Fatalf("run calls = %d, want 4", len(r.runCalls))
	}
	if r.runCalls[3].Revision != "rev2" {
		t.Errorf("run calls[3].Revision = %q, want %q: the next child must run at the moved tip", r.runCalls[3].Revision, "rev2")
	}
}

// TestLoopNoneDispatchableWaitSleepsFullWaitWhenTipNeverMoves pins the other
// half of the short-circuit: with no tip movement, the grown wait is slept
// out completely, in floor-sized slices, and no tip_moved event appears.
func TestLoopNoneDispatchableWaitSleepsFullWaitWhenTipNeverMoves(t *testing.T) {
	r := &fakeRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 3}, {Exit: 3}, {Exit: 3}, {Exit: 5}},
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	want := []time.Duration{
		testIdleFloor,                // 1st check: wait = floor, one slice
		testIdleFloor, testIdleFloor, // 2nd check: wait = 2*floor, two slices
		testIdleFloor, testIdleFloor, testIdleFloor, testIdleFloor, // 3rd check: wait = 4*floor, four slices
	}
	if fmt.Sprint(clk.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v (full grown wait slept in floor-sized slices)", clk.waits, want)
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
// polls mid-wait, however far the backoff has grown, and ResolveRevision is
// called no more than the once-per-iteration the loop already does.
func TestLoopQueueEmptyWaitIgnoresTipMoved(t *testing.T) {
	r := &fakeRunner{
		revisions: []string{"rev1", "rev2", "rev3", "rev4"},
		results:   []ChildResult{{Exit: 2}, {Exit: 2}, {Exit: 2}, {Exit: 5}},
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	want := []time.Duration{testIdleFloor, 2 * testIdleFloor, 4 * testIdleFloor}
	if fmt.Sprint(clk.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v: exactly one Sleep per wait, whole, never sliced", clk.waits, want)
	}

	if r.resolveCalls != 4 {
		t.Errorf("resolveCalls = %d, want 4: exactly one ResolveRevision per iteration, no mid-wait poll", r.resolveCalls)
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
	r := &fakeRunner{
		revisions: []string{"rev1", "rev1", "rev1", "rev1", "rev2"},
		results:   []ChildResult{{Exit: 3}, {Exit: 3}, {Exit: 3}, {Exit: 3}, {Exit: 5}},
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	// Checks 1-3 grow floor -> 2*floor -> 4*floor, but the 3rd short-circuits
	// after one slice on the moved tip. The 4th check, right after, must be
	// back at a bare floor wait (one slice), not a continuation of the grown
	// streak.
	want := []time.Duration{testIdleFloor, testIdleFloor, testIdleFloor, testIdleFloor, testIdleFloor}
	if fmt.Sprint(clk.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v (backoff reset after the tip-moved short-circuit)", clk.waits, want)
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

// TestLoopIdleWaitSwallowsMidWaitPollFailure pins idleWait's mid-wait
// poll-failure branch (cmd/launcher/internal/daemon/pool.go): a
// ResolveRevision error during idleWait's mid-wait poll is
// opportunistic, not the loop's own per-iteration fetch, so it must be
// swallowed as "no change observed" rather than routed to backoffOrHalt
// (which would trip the pool-wide breaker over a transient fetch blip).
//
// Iteration 1: ResolveRevision call #1 (top of loop) -> exit 3 -> wait =
// floor; idleWait sleeps one slice and returns without polling (remaining
// hits 0 exactly). Iteration 2: call #2 (top of loop) -> exit 3 -> wait =
// 2*floor; idleWait sleeps slice 1, then polls -- that's call #3, the one
// set to fail -- swallows it, and sleeps slice 2. Iteration 3: call #4
// (top) -> exit 5 -> halt. FailureBackoff is set apart from the floor so a
// stray failure-backoff sleep (a regression routing the poll failure to
// backoffOrHalt) would be unmistakable in clk.waits.
func TestLoopIdleWaitSwallowsMidWaitPollFailure(t *testing.T) {
	r := &fakeRunner{
		revisions:  []string{"rev1"},
		resolveAt:  3,
		resolveErr: errors.New("boom: transient fetch failure"),
		results:    []ChildResult{{Exit: 3}, {Exit: 3}, {Exit: 5}},
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.FailureBackoff = 7 * time.Millisecond

	reason := Loop(context.Background(), cfg, r, em, clk)

	want := []time.Duration{testIdleFloor, testIdleFloor, testIdleFloor}
	if fmt.Sprint(clk.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v: the failed mid-wait poll must neither cut the wait short nor add a failure backoff", clk.waits, want)
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

	if len(r.runCalls) != 3 {
		t.Fatalf("run calls = %d, want 3", len(r.runCalls))
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
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}}
	clk := &fakeClock{now: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Awake = win

	Loop(context.Background(), cfg, r, em, clk)

	if len(clk.waits) != 1 {
		t.Fatalf("waits = %v, want exactly one sleep to the next opening, not a poll loop", clk.waits)
	}
	if clk.waits[0] != time.Hour {
		t.Fatalf("waits = %v, want the one wait = 1h, the whole remaining span to 09:00", clk.waits)
	}

	names := eventNames(decodeEvents(t, &buf))
	wantPrefix := []string{"awake_close", "awake_open", "child_start"}
	if len(names) < len(wantPrefix) || fmt.Sprint(names[:len(wantPrefix)]) != fmt.Sprint(wantPrefix) {
		t.Fatalf("events = %v, want to start with %v", names, wantPrefix)
	}
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
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}}
	clk := &fakeClock{now: time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Awake = win

	Loop(context.Background(), cfg, r, em, clk)

	if len(clk.waits) != 0 {
		t.Fatalf("waits = %v, want none: the window is already open at start", clk.waits)
	}
	if len(r.runCalls) != 1 {
		t.Fatalf("run calls = %d, want 1", len(r.runCalls))
	}
	for _, ev := range decodeEvents(t, &buf) {
		if ev.Event == "awake_close" || ev.Event == "awake_open" {
			t.Fatalf("events = %v, want no awake_close/awake_open when the window starts open", eventNames(decodeEvents(t, &buf)))
		}
	}
}

// windowAdvancingRunner is a single-slot Runner whose RunChild advances the
// shared fakeClock by advance before returning, simulating a Box that
// outlasts the Awake window: the window can close mid-run without anything
// in the loop noticing until the child itself returns.
type windowAdvancingRunner struct {
	clk      *fakeClock
	revision string
	advance  time.Duration
	results  []ChildResult
	calls    int
}

func (r *windowAdvancingRunner) ResolveRevision(ctx context.Context) (string, error) {
	return r.revision, nil
}

func (r *windowAdvancingRunner) SelfPath(ctx context.Context, revision string) (string, error) {
	return "", nil
}

func (r *windowAdvancingRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	r.clk.mu.Lock()
	r.clk.now = r.clk.now.Add(r.advance)
	r.clk.mu.Unlock()

	r.calls++
	idx := r.calls - 1
	if idx >= len(r.results) {
		idx = len(r.results) - 1
	}
	return r.results[idx], nil
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
	clk := &fakeClock{now: time.Date(2026, 1, 1, 16, 0, 0, 0, time.UTC)}
	r := &windowAdvancingRunner{
		clk:      clk,
		revision: "rev1",
		advance:  2 * time.Hour, // 16:00 -> 18:00, past the 17:00 close
		results:  []ChildResult{{Exit: 3}, {Exit: 5}},
	}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Awake = win

	Loop(context.Background(), cfg, r, em, clk)

	if r.calls != 2 {
		t.Fatalf("run calls = %d, want 2: the in-flight child finishes, and a second starts once the window reopens", r.calls)
	}

	names := eventNames(decodeEvents(t, &buf))
	want := []string{"child_start", "child_finish", "awake_close", "awake_open", "child_start", "child_finish", "halt"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v: no idle/jam step consumed for the wait the closed window owns", names, want)
	}
}

// resolveWindowAdvancingRunner is a single-slot Runner whose ResolveRevision
// advances the shared fakeClock by advance before returning, simulating a
// fetch that spans the window's close: the decision to run was made while
// still open, but time has moved on by the time the revision comes back.
// The advance only fires once, so a slot that parks and retries sees a
// steady clock on its second pass.
type resolveWindowAdvancingRunner struct {
	clk      *fakeClock
	revision string
	advance  time.Duration
	advanced bool
	result   ChildResult
	calls    int
}

func (r *resolveWindowAdvancingRunner) ResolveRevision(ctx context.Context) (string, error) {
	if !r.advanced {
		r.clk.mu.Lock()
		r.clk.now = r.clk.now.Add(r.advance)
		r.clk.mu.Unlock()
		r.advanced = true
	}
	return r.revision, nil
}
func (r *resolveWindowAdvancingRunner) SelfPath(ctx context.Context, revision string) (string, error) {
	return "", nil
}

func (r *resolveWindowAdvancingRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	r.calls++
	return r.result, nil
}

// TestLoopAwakeWindowClosesDuringResolveRevisionParksInsteadOfStarting pins
// a blocking review finding on runSlot: awaitWindow only decides the
// window is open once, and ResolveRevision's git fetch can outlast that
// decision, so runSlot re-checks the window after ResolveRevision returns.
// A child must never start once the fetch comes back outside the window --
// the slot should park (awake_close/awake_open) and try again, not launch
// straight away.
func TestLoopAwakeWindowClosesDuringResolveRevisionParksInsteadOfStarting(t *testing.T) {
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	clk := &fakeClock{now: time.Date(2026, 1, 1, 16, 58, 0, 0, time.UTC)}
	r := &resolveWindowAdvancingRunner{
		clk:      clk,
		revision: "rev1",
		advance:  4 * time.Minute, // 16:58 -> 17:02, past the 17:00 close
		result:   ChildResult{Exit: 5},
	}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.Awake = win

	Loop(context.Background(), cfg, r, em, clk)

	if r.calls != 1 {
		t.Fatalf("run calls = %d, want 1: the closed-window fetch must not start a child, only the retry after the slot parks", r.calls)
	}

	names := eventNames(decodeEvents(t, &buf))
	want := []string{"awake_close", "awake_open", "child_start", "child_finish", "halt"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v: no child_start until the slot has parked and reopened", names, want)
	}
}

// TestLoopSelfChangeHaltsAtIterationBoundary asserts the loop halts before
// starting a child when Runner.SelfPath reports the daemon's own build
// changed at the fetched tip, and that the halt reason names it.
func TestLoopSelfChangeHaltsAtIterationBoundary(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}, selfPaths: []string{"/nix/store/new-path"}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/old-path"

	reason := Loop(context.Background(), cfg, r, em, clk)

	if !strings.HasPrefix(reason, HaltSelfChanged) {
		t.Fatalf("halt reason = %q, want prefix %q", reason, HaltSelfChanged)
	}
	if len(r.runCalls) != 0 {
		t.Fatalf("run calls = %d, want 0: the halt must land before a child is launched", len(r.runCalls))
	}

	events := decodeEvents(t, &buf)
	names := eventNames(events)
	if fmt.Sprint(names) != fmt.Sprint([]string{"halt"}) {
		t.Fatalf("events = %v, want exactly one halt event", names)
	}
	if events[0].Reason != reason {
		t.Errorf("halt event reason = %q, want %q", events[0].Reason, reason)
	}
}

// TestLoopSelfChangeNeverReExecs asserts that once a self-change halt has
// fired, Loop returns without ever calling RunChild again — no re-exec, no
// further iteration, whatever ran before the halt is all that ever runs.
func TestLoopSelfChangeNeverReExecs(t *testing.T) {
	r := &fakeRunner{
		revisions: []string{"rev1", "rev2"},
		results:   []ChildResult{{Exit: 0}},
		selfPaths: []string{"/nix/store/old-path", "/nix/store/new-path"},
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/old-path"

	reason := Loop(context.Background(), cfg, r, em, clk)

	if !strings.HasPrefix(reason, HaltSelfChanged) {
		t.Fatalf("halt reason = %q, want prefix %q", reason, HaltSelfChanged)
	}
	if len(r.runCalls) != 1 {
		t.Fatalf("run calls = %d, want 1: the first iteration's matching self-path should run its child, the second iteration's mismatch must halt before any further child", len(r.runCalls))
	}
}

// TestLoopSelfPathMatchDoesNotHalt asserts a SelfPath result equal to
// Config.SelfProgram is a no-op: the loop proceeds to run children as
// normal.
func TestLoopSelfPathMatchDoesNotHalt(t *testing.T) {
	r := &fakeRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 0}, {Exit: 5}},
		selfPaths: []string{"/nix/store/same-path"},
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/same-path"

	reason := Loop(context.Background(), cfg, r, em, clk)

	if strings.HasPrefix(reason, HaltSelfChanged) {
		t.Fatalf("halt reason = %q, want no self-changed halt: the self path matched", reason)
	}
	if len(r.runCalls) != 2 {
		t.Fatalf("run calls = %d, want 2: a matching self path must not stop children from running", len(r.runCalls))
	}
}

// TestLoopEmptySelfProgramSkipsCheck asserts Config.SelfProgram == "" never
// calls Runner.SelfPath at all — the check is fully disabled, not merely
// non-halting.
func TestLoopEmptySelfProgramSkipsCheck(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), testConfig(1), r, em, clk)

	if r.selfCalls != 0 {
		t.Fatalf("selfCalls = %d, want 0: an empty SelfProgram must never call SelfPath", r.selfCalls)
	}
}

// TestLoopSelfPathErrorBacksOff asserts a SelfPath error is treated like any
// other unclassified iteration-boundary failure: this slot backs off
// (reason prefixed self-build:) and retries, rather than halting the pool.
func TestLoopSelfPathErrorBacksOff(t *testing.T) {
	r := &fakeRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 5}},
		selfPaths: []string{"/nix/store/same-path"},
		selfErrAt: 1,
		selfErr:   errors.New("eval boom"),
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(1)
	cfg.SelfProgram = "/nix/store/same-path"
	cfg.FailureBackoff = 5 * time.Millisecond

	reason := Loop(context.Background(), cfg, r, em, clk)

	if strings.HasPrefix(reason, HaltSelfChanged) {
		t.Fatalf("halt reason = %q, want no self-changed halt: SelfPath only errored once, then matched", reason)
	}
	if r.selfCalls != 2 {
		t.Fatalf("selfCalls = %d, want 2: the errored call plus the retry", r.selfCalls)
	}
	if len(r.runCalls) != 1 {
		t.Fatalf("run calls = %d, want 1: the slot must retry after backing off", len(r.runCalls))
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

// drainSelfRunner is a hand-rolled Runner for
// TestLoopSelfChangeDrainsRunningChild: one slot's RunChild blocks on a
// channel the test controls (a long-running child), while the other slot's
// SelfPath call — deliberately made to wait until the child has actually
// started — reports a changed build and halts the pool. Whichever slot's
// SelfPath call lands first "wins" the matching path and goes on to run the
// child; call order between the two goroutines is otherwise unconstrained,
// so the test does not assume which slot number plays which role.
type drainSelfRunner struct {
	mu        sync.Mutex
	selfCalls int
	runCalls  int

	started   chan struct{} // closed by RunChild the moment it starts
	release   chan struct{} // closed by the test to let RunChild return
	matchPath string
	newPath   string
}

func (r *drainSelfRunner) ResolveRevision(ctx context.Context) (string, error) {
	return "rev1", nil
}

func (r *drainSelfRunner) SelfPath(ctx context.Context, revision string) (string, error) {
	r.mu.Lock()
	r.selfCalls++
	first := r.selfCalls == 1
	r.mu.Unlock()
	if first {
		return r.matchPath, nil
	}
	// Not first: wait for the other slot's child to actually be running
	// before reporting the mismatch, so the halt this triggers is
	// guaranteed to race a genuinely in-flight child, not an imagined one.
	<-r.started
	return r.newPath, nil
}

func (r *drainSelfRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	r.mu.Lock()
	r.runCalls++
	r.mu.Unlock()
	close(r.started)
	<-r.release
	return ChildResult{Exit: 0}, nil
}

// TestLoopSelfChangeDrainsRunningChild asserts the "drains running children
// rather than killing them" criterion for the self-change halt specifically:
// with one slot's child already running when a sibling slot's self-check
// halts the pool, the running child's child_finish is still emitted and
// Loop only returns once that child has actually returned.
func TestLoopSelfChangeDrainsRunningChild(t *testing.T) {
	r := &drainSelfRunner{
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		matchPath: "/nix/store/old-path",
		newPath:   "/nix/store/new-path",
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := testConfig(2)
	cfg.SelfProgram = "/nix/store/old-path"

	done := make(chan string, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	<-r.started      // the long-running child is confirmed running
	close(r.release) // let it finish now that it is known to have been running

	reason := <-done

	if !strings.HasPrefix(reason, HaltSelfChanged) {
		t.Fatalf("halt reason = %q, want prefix %q", reason, HaltSelfChanged)
	}
	if r.runCalls != 1 {
		t.Fatalf("run calls = %d, want 1: only the already-running child ever runs", r.runCalls)
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
