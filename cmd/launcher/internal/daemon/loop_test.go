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

	resolveCalls int
	runCalls     []runCall
}

type runCall struct {
	Kind     Kind
	Revision string
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

func (f *fakeRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	f.runCalls = append(f.runCalls, runCall{Kind: req.Kind, Revision: req.Revision})
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

const testIdleInterval = time.Millisecond

func TestLoopContinueThenHalt(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

	if len(r.runCalls) != 2 {
		t.Fatalf("run calls = %d, want 2", len(r.runCalls))
	}
	if len(rs.waits) != 0 {
		t.Fatalf("waits = %v, want none (exit 0 continues at once)", rs.waits)
	}
	if !strings.Contains(reason, "host-tainted") {
		t.Errorf("halt reason = %q, want it to name host-tainted", reason)
	}
}

func TestLoopWaitThenHalt(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}, {Exit: 0}, {Exit: 6}}}
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

	if len(r.runCalls) != 3 {
		t.Fatalf("run calls = %d, want 3", len(r.runCalls))
	}
	if len(rs.waits) != 1 || rs.waits[0] != testIdleInterval {
		t.Fatalf("waits = %v, want exactly one wait of %v", rs.waits, testIdleInterval)
	}
	if !strings.Contains(reason, "config-invalid") {
		t.Errorf("halt reason = %q, want it to name config-invalid", reason)
	}
}

func TestLoopExit3WaitsLikeExit2(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 3}, {Exit: 5}}}
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

	if len(rs.waits) != 1 {
		t.Fatalf("waits = %v, want exactly one wait for exit 3", rs.waits)
	}
}

func TestLoopExit4ContinuesWithoutSleeping(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 4}, {Exit: 5}}}
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

	if len(rs.waits) != 0 {
		t.Fatalf("waits = %v, want none for exit 4 (image-stale continues at once)", rs.waits)
	}
	if len(r.runCalls) != 2 {
		t.Fatalf("run calls = %d, want 2", len(r.runCalls))
	}
}

func TestLoopExit7Halts(t *testing.T) {
	r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 7}}}
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

	if !strings.Contains(reason, "signalled-stop") {
		t.Errorf("halt reason = %q, want it to name signalled-stop", reason)
	}
}

func TestLoopUnknownExitHalts(t *testing.T) {
	for _, exit := range []int{1, 99} {
		r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: exit}}}
		rs := &fakeClock{}
		var buf bytes.Buffer
		em := newTestEmitter(&buf)

		reason := Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

		if !strings.Contains(reason, "error") {
			t.Errorf("exit %d: halt reason = %q, want it to name the error outcome", exit, reason)
		}
	}
}

func TestLoopResolveErrorHaltsBeforeAnyChildRuns(t *testing.T) {
	wantErr := errors.New("boom: no such revision")
	r := &fakeRunner{resolveAt: 1, resolveErr: wantErr}
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

	if len(r.runCalls) != 0 {
		t.Fatalf("run calls = %d, want 0: a resolve failure must halt before any child runs", len(r.runCalls))
	}
	if !strings.Contains(reason, wantErr.Error()) {
		t.Errorf("halt reason = %q, want it to name %v", reason, wantErr)
	}

	events := decodeEvents(t, &buf)
	if got := eventNames(events); len(got) != 1 || got[0] != "halt" {
		t.Fatalf("events = %v, want exactly one halt event", got)
	}
}

func TestLoopRunChildErrorHaltsAndEmitsChildFinish(t *testing.T) {
	wantErr := errors.New("exec: nix not found")
	r := &fakeRunner{revisions: []string{"rev1"}, runErrAt: 1, runErr: wantErr}
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

	if !strings.Contains(reason, wantErr.Error()) {
		t.Errorf("halt reason = %q, want it to name %v", reason, wantErr)
	}

	events := decodeEvents(t, &buf)
	names := eventNames(events)
	wantNames := []string{"child_start", "child_finish", "halt"}
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
	r := &fakeRunner{revisions: []string{"rev1"}, runErrAt: 1, runErr: wantErr, runErrIssues: []string{"42"}}
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

	events := decodeEvents(t, &buf)
	names := eventNames(events)
	wantNames := []string{"child_start", "box", "child_finish", "halt"}
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
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

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
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

	if len(r.runCalls) != 4 {
		t.Fatalf("run calls = %d, want 4: the loop must keep going after the queue drains and pick work back up without a restart", len(r.runCalls))
	}
	if len(rs.waits) != 2 {
		t.Fatalf("waits = %v, want exactly two (one per empty-queue exit)", rs.waits)
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
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(context.Background(), Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

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
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

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
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

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
	rs := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, Config{Kind: KindDispatch, IdleInterval: testIdleInterval}, r, em, rs)

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

type cancellingRunner struct {
	cancel   context.CancelFunc
	revision string
	result   ChildResult
	runCalls int
}

func (r *cancellingRunner) ResolveRevision(ctx context.Context) (string, error) {
	return r.revision, nil
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

func (r *cancellingResolveRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	r.runCalls++
	return r.result, nil
}
