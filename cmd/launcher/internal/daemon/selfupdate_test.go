package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	selfOld = "/nix/store/old-path"
	selfNew = "/nix/store/new-path"
)

func selfUpdateEvents(events []Event) []Event {
	var out []Event
	for _, ev := range events {
		if ev.Event == "self_update" {
			out = append(out, ev)
		}
	}
	return out
}

// prebuildAfterFirstChild scripts r so its prebuild returns result only once
// a child has started, which a one-slot pool can only do if the build left
// the slot free. It gives up after a bound so a regression fails rather than
// hangs.
func prebuildAfterFirstChild(r *scriptedRunner, result error) {
	started := make(chan struct{})
	var once sync.Once
	r.onStart = func(context.Context, ChildRequest) error {
		once.Do(func() { close(started) })
		return nil
	}
	r.onPrebuild = func(ctx context.Context, _ int, _ Tip) error {
		select {
		case <-started:
			return result
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
			return errors.New("no child started during the build")
		}
	}
}

// builtThenHalt asserts a built self_update is immediately followed by the
// one halt, with no child starting after the built event: the pool must stop
// starting work the moment it decides to restart. Draining children may still
// finish.
func builtThenHalt(t *testing.T, events []Event) (built, halt Event) {
	t.Helper()
	for i, ev := range events {
		if ev.Event == "self_update" && ev.Phase == string(SelfUpdateBuilt) {
			after := eventNames(events[i+1:])
			if len(after) == 0 || after[0] != "halt" || countEvents(after, "halt") != 1 || countEvents(after, "child_start") != 0 {
				t.Fatalf("events after built = %v, want the one halt first and no child_start", after)
			}
			return ev, events[i+1]
		}
		if ev.Event == "halt" {
			break
		}
	}
	t.Fatalf("events = %v, want a built self_update directly before the halt", eventNames(events))
	return
}

// eventTap is an io.Writer that signals on ch for each written event line
// naming event, so a test can wait on an event without sleeping.
type eventTap struct {
	w     io.Writer
	event string
	ch    chan struct{}
}

func newEventTap(w io.Writer, event string) *eventTap {
	return &eventTap{w: w, event: event, ch: make(chan struct{}, 16)}
}

func (e *eventTap) Write(b []byte) (int, error) {
	n, err := e.w.Write(b)
	if bytes.Contains(b, []byte(`"event":"`+e.event+`"`)) {
		e.ch <- struct{}{}
	}
	return n, err
}

// A one-slot pool starts a child while the build runs, and the halt follows
// the build's success, not the other way round.
func TestSelfUpdateOneSlotRunsChildDuringBuild(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, selfPaths: []string{selfNew}}
	prebuildAfterFirstChild(r, nil)
	var buf bytes.Buffer
	cfg := testConfig(1)
	cfg.SelfProgram = selfOld

	h := Loop(context.Background(), cfg, r, newTestEmitter(&buf), &testClock{})

	if h.Class != HaltSelfChanged || h.Revision != "rev1" {
		t.Fatalf("halt = %+v, want %v at rev1", h, HaltSelfChanged)
	}
	if r.runCount() < 1 {
		t.Fatalf("run calls = %d, want a child started while the build ran", r.runCount())
	}
	for _, c := range r.calls() {
		if c.Revision != "rev1" {
			t.Fatalf("run calls = %+v, want every child at rev1 under the running build", r.calls())
		}
	}
	got := r.prebuilds()
	if len(got) != 1 || got[0].SelfPath != selfNew || got[0].Revision != "rev1" {
		t.Fatalf("prebuilds = %+v, want one call for rev1 at %s", got, selfNew)
	}
	events := decodeEvents(t, &buf)
	built, halt := builtThenHalt(t, events)
	if built.Revision != "rev1" || built.Reason != "" || built.Slot != nil {
		t.Errorf("self_update = %+v, want built at rev1 with no reason or slot", built)
	}
	if halt.Reason != h.String() {
		t.Errorf("halt event reason = %q, want %q", halt.Reason, h.String())
	}
}

// A failed prebuild, at either stage, never halts: the pool keeps running
// children under the old build and both the event and the status file name
// the failure.
func TestSelfUpdateFailureBlocksWithoutHalting(t *testing.T) {
	for _, stage := range []string{"build", "smoke-test"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			clk := &testClock{}
			var buf bytes.Buffer
			tap := newEventTap(&buf, "self_update")
			var starts int
			r := &scriptedRunner{
				revisions: []string{"rev1"},
				selfPaths: []string{selfNew},
				results:   []ChildResult{{Exit: 0}, {Exit: 5}},
				onPrebuild: func(context.Context, int, Tip) error {
					return &PrebuildError{Stage: PrebuildStage(stage), Err: errors.New("boom")}
				},
				// Child 2 starts only after the verdict landed, so the exit 5
				// that halts the pool cannot cancel the build first.
				onStart: func(context.Context, ChildRequest) error {
					if starts++; starts == 2 {
						<-tap.ch
					}
					return nil
				},
			}
			cfg := testConfig(1)
			cfg.SelfProgram = selfOld
			cfg.Status = NewStatusWriter(dir, clk.Now)

			h := Loop(context.Background(), cfg, r, newTestEmitter(tap), clk)

			if h.Class == HaltSelfChanged {
				t.Fatalf("halt class = %v, want no self-changed halt after a failed prebuild", h.Class)
			}
			calls := r.calls()
			if len(calls) != 2 || calls[0].Revision != "rev1" || calls[1].Revision != "rev1" {
				t.Fatalf("run calls = %+v, want two children at rev1", calls)
			}
			wantReason := stage + ": boom"
			su := selfUpdateEvents(decodeEvents(t, &buf))
			if len(su) != 1 || su[0].Phase != string(SelfUpdateBlocked) || su[0].Revision != "rev1" || su[0].Reason != wantReason {
				t.Fatalf("self_update events = %+v, want one blocked at rev1: %q", su, wantReason)
			}
			report, err := ReadStatus(dir)
			if err != nil || report.Status == nil {
				t.Fatalf("ReadStatus: %v, %v", report.Status, err)
			}
			want := SelfUpdate{Phase: SelfUpdateBlocked, Revision: "rev1", Reason: wantReason}
			if got := report.Status.SelfUpdate; got == nil || *got != want {
				t.Fatalf("status self_update = %+v, want %+v", got, want)
			}
		})
	}
}

// A revision that blocked is not rebuilt on later iterations, and a moved
// tip is: its build may differ.
func TestSelfUpdateBlockedRevisionRetriedOnlyWhenTipMoves(t *testing.T) {
	var buf bytes.Buffer
	tap := newEventTap(&buf, "self_update")
	r := &scriptedRunner{
		revisions: []string{"rev1", "rev1", "rev2", "rev2"},
		selfPaths: []string{selfNew},
		results:   []ChildResult{{Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 5}},
		onPrebuild: func(context.Context, int, Tip) error {
			return &PrebuildError{Stage: "build", Err: errors.New("boom")}
		},
		// Resolutions 3 and 4 wait for the previous verdict, so each sees
		// the settled phase rather than a build still in flight.
		onResolve: func(_ context.Context, call int) error {
			if call >= 3 {
				<-tap.ch
			}
			return nil
		},
	}
	cfg := testConfig(1)
	cfg.SelfProgram = selfOld

	Loop(context.Background(), cfg, r, newTestEmitter(tap), &testClock{})

	if r.runCount() != 4 {
		t.Fatalf("run calls = %d, want 4: every iteration starts a child under the old build", r.runCount())
	}
	var revs []string
	for _, tip := range r.prebuilds() {
		revs = append(revs, tip.Revision)
	}
	if got := strings.Join(revs, ","); got != "rev1,rev2" {
		t.Fatalf("prebuilt revisions = %s, want rev1,rev2", got)
	}
	if n := len(selfUpdateEvents(decodeEvents(t, &buf))); n != 2 {
		t.Fatalf("self_update events = %d, want 2 (one blocked per revision)", n)
	}
}

// A blocked entry does not outlive the mismatch: once a resolved tip builds
// to the running path again, the status file stops reporting it.
func TestSelfUpdateBlockedClearedWhenTipMatchesRunningBuild(t *testing.T) {
	dir := t.TempDir()
	clk := &testClock{}
	var buf bytes.Buffer
	tap := newEventTap(&buf, "self_update")
	var starts int
	var atSecondChild StatusReport
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		selfPaths: []string{selfNew, selfOld},
		results:   []ChildResult{{Exit: 0}, {Exit: 5}},
		onPrebuild: func(context.Context, int, Tip) error {
			return &PrebuildError{Stage: "build", Err: errors.New("boom")}
		},
		onResolve: func(_ context.Context, call int) error {
			if call == 2 {
				<-tap.ch
			}
			return nil
		},
		onStart: func(context.Context, ChildRequest) error {
			if starts++; starts == 2 {
				atSecondChild, _ = ReadStatus(dir)
			}
			return nil
		},
	}
	cfg := testConfig(1)
	cfg.SelfProgram = selfOld
	cfg.Status = NewStatusWriter(dir, clk.Now)

	Loop(context.Background(), cfg, r, newTestEmitter(tap), clk)

	if atSecondChild.Status == nil {
		t.Fatal("no status read at the second child")
	}
	if got := atSecondChild.Status.SelfUpdate; got != nil {
		t.Fatalf("status self_update after the tip matched again = %+v, want none", got)
	}
}

// A slot that finds some build already recorded as built or in flight does
// not start another, even for a newer tip: only the build's own goroutine
// halts, and only for the revision it built.
func TestSelfUpdateRecordedBuildIsNotRebuiltForNewerTip(t *testing.T) {
	for _, phase := range []SelfUpdatePhase{SelfUpdateBuilding, SelfUpdateBuilt} {
		t.Run(string(phase), func(t *testing.T) {
			r := &scriptedRunner{}
			var buf bytes.Buffer
			cfg := testConfig(1)
			cfg.SelfProgram = selfOld
			p, _ := newPool(context.Background(), cfg, r, newTestEmitter(&buf), &testClock{})
			defer p.cancel()
			want := SelfUpdate{Phase: phase, Revision: "rev1"}
			p.mutate(func(s *state) []Event {
				s.self = want
				return nil
			})

			p.selfUpdate(context.Background(), cfg.Kinds[0], Tip{Revision: "rev2", SelfPath: selfNew})
			p.bg.Wait()

			if n := r.prebuildCount(); n != 0 {
				t.Fatalf("prebuild calls = %d, want 0", n)
			}
			if h := p.haltReason(); h.Class == HaltSelfChanged {
				t.Fatalf("halt = %+v, want none: rev2 was never built", h)
			}
			if p.st.self != want {
				t.Fatalf("self = %+v, want %+v unchanged", p.st.self, want)
			}
		})
	}
}

// A slot that resolves a newer tip while a build is landing halts the pool
// at the revision that was built, never at the newer one.
func TestSelfUpdateHaltNamesBuiltRevisionNotNewerTip(t *testing.T) {
	var buf bytes.Buffer
	tap := newEventTap(&buf, "self_update")
	release := make(chan struct{})
	r := &scriptedRunner{
		revisions: []string{"rev1", "rev2"},
		selfPaths: []string{selfNew},
		results:   []ChildResult{{Exit: 0}},
		onPrebuild: func(ctx context.Context, _ int, _ Tip) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		// Resolution 2 sees rev2 as the build for rev1 lands.
		onResolve: func(_ context.Context, call int) error {
			if call == 2 {
				close(release)
				<-tap.ch
			}
			return nil
		},
	}
	cfg := testConfig(1)
	cfg.SelfProgram = selfOld

	h := Loop(context.Background(), cfg, r, newTestEmitter(tap), &testClock{})

	if h.Class != HaltSelfChanged || h.Revision != "rev1" {
		t.Fatalf("halt = %+v, want %v at rev1", h, HaltSelfChanged)
	}
	if got := r.prebuilds(); len(got) != 1 || got[0].Revision != "rev1" {
		t.Fatalf("prebuilds = %+v, want only rev1", got)
	}
	builtThenHalt(t, decodeEvents(t, &buf))
}

// A halt that cancels the build mid-flight leaves no verdict behind, and the
// status file shows the build in flight until then.
func TestSelfUpdateCancelledBuildLeavesNoVerdict(t *testing.T) {
	dir := t.TempDir()
	clk := &testClock{}
	var status StatusReport
	var statusErr error
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		selfPaths: []string{selfNew},
		results:   []ChildResult{{Exit: 5}},
		onPrebuild: func(ctx context.Context, _ int, _ Tip) error {
			<-ctx.Done()
			return ctx.Err()
		},
		onStart: func(context.Context, ChildRequest) error {
			status, statusErr = ReadStatus(dir)
			return nil
		},
	}
	cfg := testConfig(1)
	cfg.SelfProgram = selfOld
	cfg.Status = NewStatusWriter(dir, clk.Now)
	var buf bytes.Buffer

	h := Loop(context.Background(), cfg, r, newTestEmitter(&buf), clk)

	if h.Class == HaltSelfChanged {
		t.Fatalf("halt class = %v, want the child's own halt", h.Class)
	}
	if statusErr != nil || status.Status == nil {
		t.Fatalf("ReadStatus during a running child: %v, %v", status.Status, statusErr)
	}
	if got := status.Status.SelfUpdate; got == nil || got.Phase != SelfUpdateBuilding || got.Revision != "rev1" {
		t.Fatalf("status self_update during build = %+v, want building at rev1", got)
	}
	if n := len(selfUpdateEvents(decodeEvents(t, &buf))); n != 0 {
		t.Fatalf("self_update events = %d, want none: a cancelled build is no verdict", n)
	}
	report, err := ReadStatus(dir)
	if err != nil || report.Status == nil {
		t.Fatalf("final ReadStatus: %v, %v", report.Status, err)
	}
	if got := report.Status.SelfUpdate; got != nil {
		t.Fatalf("final status self_update = %+v, want none after a cancelled build", got)
	}
}

// An operator stop during the build records no verdict either.
func TestSelfUpdateStopDuringBuildRecordsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		selfPaths: []string{selfNew},
	}
	prebuildAfterFirstChild(r, nil)
	build := r.onPrebuild
	r.onPrebuild = func(c context.Context, call int, tip Tip) error {
		_ = build(c, call, tip)
		cancel()
		return &PrebuildError{Stage: "build", Err: context.Canceled}
	}
	var buf bytes.Buffer
	cfg := testConfig(1)
	cfg.SelfProgram = selfOld

	h := Loop(ctx, cfg, r, newTestEmitter(&buf), &testClock{})

	if h.Class != HaltOperatorStop {
		t.Fatalf("halt class = %v, want %v", h.Class, HaltOperatorStop)
	}
	events := decodeEvents(t, &buf)
	if n := len(selfUpdateEvents(events)); n != 0 {
		t.Fatalf("self_update events = %d, want none: a cancelled build is no verdict", n)
	}
	if n := countEvents(eventNames(events), "halt"); n != 1 {
		t.Fatalf("halt events = %d, want exactly 1", n)
	}
}

// waitSelfPhase polls the status file until its self_update shows phase.
func waitSelfPhase(t *testing.T, dir string, phase SelfUpdatePhase) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rep, err := ReadStatus(dir); err == nil && rep.Status != nil &&
			rep.Status.SelfUpdate != nil && rep.Status.SelfUpdate.Phase == phase {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("status self_update never reached %q", phase)
}

// inPlaceWaiting runs a two-slot in-place pool whose build passes once both
// slots hold a child, waits for the waiting phase, then frees slot 1 so the
// pool has a free slot that must not start a child. Slot 0's child stays
// running. It returns Loop's halt channel and the buffer of events.
func inPlaceWaiting(t *testing.T) (r *scriptedRunner, stop chan struct{}, dir string, done chan Halt, buf *bytes.Buffer) {
	t.Helper()
	r = &scriptedRunner{revisions: []string{"rev1"}, selfPaths: []string{selfNew}}
	r.holdSlots(2)
	r.announceEachSlot()
	gate := make(chan struct{})
	r.onPrebuild = func(ctx context.Context, _ int, _ Tip) error {
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	stop = make(chan struct{})
	dir = t.TempDir()
	buf = &bytes.Buffer{}
	cfg := testConfig(2)
	cfg.SelfProgram = selfOld
	cfg.RestartInPlace = true
	cfg.Stop = stop
	cfg.Status = NewStatusWriter(dir, time.Now)

	done = make(chan Halt, 1)
	go func() { done <- Loop(context.Background(), cfg, r, newTestEmitter(buf), &testClock{}) }()
	r.awaitStart(t)
	r.awaitStart(t)
	close(gate)
	waitSelfPhase(t, dir, SelfUpdateWaiting)
	r.releaseSlot(t, 1, ChildResult{Exit: 0})
	return
}

// In-place mode: a passing build neither halts nor drains. The status shows
// waiting, and a slot freed meanwhile starts nothing.
func TestSelfUpdateInPlaceWaitsWithoutHaltOrStart(t *testing.T) {
	r, stop, dir, done, buf := inPlaceWaiting(t)

	select {
	case h := <-done:
		t.Fatalf("pool ended with %+v while waiting, want it to keep waiting", h)
	case <-time.After(50 * time.Millisecond):
	}
	waitSelfPhase(t, dir, SelfUpdateWaiting)
	if n := r.runCount(); n != 2 {
		t.Fatalf("run calls = %d, want 2: no child may start while waiting", n)
	}

	close(stop)
	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	<-done
	events := decodeEvents(t, buf)
	if n := countEvents(eventNames(events), "child_start"); n != 2 {
		t.Errorf("child_start events = %d, want 2", n)
	}
	su := selfUpdateEvents(events)
	if len(su) != 1 || su[0].Phase != string(SelfUpdateBuilt) {
		t.Errorf("self_update events = %+v, want one built", su)
	}
}

// A stop while waiting cancels the handoff: an ordinary operator stop that
// drains the running child, with the build left recorded as built.
func TestSelfUpdateInPlaceStopWhileWaitingDrains(t *testing.T) {
	r, stop, dir, done, buf := inPlaceWaiting(t)

	close(stop)
	select {
	case h := <-done:
		t.Fatalf("pool ended with %+v before its running child finished", h)
	case <-time.After(50 * time.Millisecond):
	}
	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	h := <-done

	if h.Class != HaltOperatorStop {
		t.Fatalf("halt = %+v, want %v", h, HaltOperatorStop)
	}
	events := decodeEvents(t, buf)
	names := eventNames(events)
	if countEvents(names, "halt") != 1 || countEvents(names, "child_finish") != 2 || countEvents(names, "child_start") != 2 {
		t.Errorf("events = %v, want one halt, two child_start and two child_finish", names)
	}
	rep, err := ReadStatus(dir)
	if err != nil || rep.Status == nil || rep.Status.SelfUpdate == nil {
		t.Fatalf("ReadStatus = %+v, %v", rep, err)
	}
	if got := rep.Status.SelfUpdate.Phase; got != SelfUpdateBuilt {
		t.Errorf("self_update phase = %q, want %q after the handoff was cancelled", got, SelfUpdateBuilt)
	}
}
