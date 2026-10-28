package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/report"
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
			blockedAt := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
			clk.setNow(blockedAt.Add(-time.Hour))
			var buf bytes.Buffer
			tap := newEventTap(&buf, "self_update")
			var starts int
			r := &scriptedRunner{
				revisions: []string{"rev1"},
				selfPaths: []string{selfNew},
				results:   []ChildResult{{Exit: 0}, {Exit: 5}},
				onPrebuild: func(context.Context, int, Tip) error {
					clk.setNow(blockedAt)
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
			want := SelfUpdate{
				Phase: SelfUpdateBlocked, Revision: "rev1", Reason: wantReason,
				Since: blockedAt.Format(time.RFC3339),
			}
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

// A pool started with a blocked self-update (an image whose in-place exec
// failed) does not rebuild that revision, and rebuilds once the tip moves.
func TestSelfUpdateSeededBlockedRevisionIsNotRebuilt(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1", "rev1", "rev2", "rev2"},
		selfPaths: []string{selfNew},
		results:   []ChildResult{{Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 5}},
		onPrebuild: func(context.Context, int, Tip) error {
			return &PrebuildError{Stage: "build", Err: errors.New("boom")}
		},
	}
	cfg := testConfig(1)
	cfg.SelfProgram = selfOld
	cfg.SelfUpdate = SelfUpdate{Phase: SelfUpdateBlocked, Revision: "rev1", Reason: "exec: boom"}

	Loop(context.Background(), cfg, r, newTestEmitter(&bytes.Buffer{}), &testClock{})

	var revs []string
	for _, tip := range r.prebuilds() {
		revs = append(revs, tip.Revision)
	}
	if got := strings.Join(revs, ","); got != "rev2" {
		t.Fatalf("prebuilt revisions = %s, want only rev2", got)
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
// slots hold a child, waits for the waiting phase, then frees slot 0 so the
// pool has a free slot that must not start a child. Slot 1's child stays
// running and has not reported, which holds the handoff off.
func inPlaceWaiting(t *testing.T, setup ...func(*handoffRig)) *handoffRig {
	t.Helper()
	rig := newHandoffRig(t, 2)
	for _, f := range setup {
		f(rig)
	}
	// Only slot 0 boxes, which frees the baton for slot 1; slot 1 never
	// reports, so the handoff stays held off while it runs.
	rig.r.onStart = func(_ context.Context, req ChildRequest) error {
		if req.Slot == 0 {
			req.OnRecord(Record{Event: report.EventBox, Key: dispatchkey.Issue("issue-0")})
		}
		return nil
	}
	rig.run()
	rig.r.awaitStart(t)
	rig.r.awaitStart(t)
	close(rig.gate)
	waitSelfPhase(t, rig.dir, SelfUpdateWaiting)
	rig.r.releaseSlot(t, 0, ChildResult{Exit: 0})
	return rig
}

// In-place mode: a passing build neither halts nor drains. The status shows
// waiting, and a slot freed meanwhile starts nothing.
func TestSelfUpdateInPlaceWaitsWithoutHaltOrStart(t *testing.T) {
	rig := inPlaceWaiting(t)
	r, buf := rig.r, rig.buf

	rig.stillWaiting(t, "a passing build waits for a clean moment")
	if n := r.runCount(); n != 2 {
		t.Fatalf("run calls = %d, want 2: no child may start while waiting", n)
	}

	close(rig.stop)
	r.releaseSlot(t, 1, ChildResult{Exit: 0})
	rig.wait(t)
	events := decodeEvents(t, buf)
	if n := countEvents(eventNames(events), "child_start"); n != 2 {
		t.Errorf("child_start events = %d, want 2", n)
	}
	su := selfUpdateEvents(events)
	if len(su) != 1 || su[0].Phase != string(SelfUpdateBuilt) {
		t.Errorf("self_update events = %+v, want one built", su)
	}
}

// selfStatus reads the self_update the status file shows right now.
func selfStatus(t *testing.T, dir string) SelfUpdate {
	t.Helper()
	rep, err := ReadStatus(dir)
	if err != nil || rep.Status == nil || rep.Status.SelfUpdate == nil {
		t.Fatalf("ReadStatus = %+v, %v", rep, err)
	}
	return *rep.Status.SelfUpdate
}

var sinceBase = time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

// since is stamped when a real in-place build enters each phase and survives
// a re-publish within the phase: freeing a slot while waiting republishes the
// status without restamping.
func TestSelfUpdateSinceStampedAtEachPhaseKeptAcrossRepublish(t *testing.T) {
	rig := newHandoffRig(t, 2)
	rig.clk.setNow(sinceBase)
	// As in inPlaceWaiting: only slot 0 boxes, so slot 1's unreported child
	// holds the handoff off and the pool stays in waiting.
	rig.r.onStart = func(_ context.Context, req ChildRequest) error {
		if req.Slot == 0 {
			req.OnRecord(Record{Event: report.EventBox, Key: dispatchkey.Issue("issue-0")})
		}
		return nil
	}
	rig.run()
	rig.r.awaitStart(t)
	rig.r.awaitStart(t)

	waitSelfPhase(t, rig.dir, SelfUpdateBuilding)
	if got, want := selfStatus(t, rig.dir).Since, sinceBase.Format(time.RFC3339); got != want {
		t.Fatalf("since at building = %q, want %q", got, want)
	}

	rig.clk.setNow(sinceBase.Add(time.Minute))
	close(rig.gate)
	waitSelfPhase(t, rig.dir, SelfUpdateWaiting)
	waitingSince := sinceBase.Add(time.Minute).Format(time.RFC3339)
	if got := selfStatus(t, rig.dir).Since; got != waitingSince {
		t.Fatalf("since at waiting = %q, want %q", got, waitingSince)
	}

	rig.clk.setNow(sinceBase.Add(time.Hour))
	rig.r.releaseSlot(t, 0, ChildResult{Exit: 0})
	deadline := time.Now().Add(5 * time.Second)
	for {
		rep, err := ReadStatus(rig.dir)
		if err == nil && rep.Status != nil && len(rep.Status.Slots) > 0 && !rep.Status.Slots[0].Busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("status never showed slot 0 freed")
		}
		time.Sleep(time.Millisecond)
	}
	if got := selfStatus(t, rig.dir); got.Phase != SelfUpdateWaiting || got.Since != waitingSince {
		t.Errorf("self_update after a same-phase re-publish = %+v, want waiting since %s", got, waitingSince)
	}

	close(rig.stop)
	rig.r.releaseSlot(t, 1, ChildResult{Exit: 0})
	rig.wait(t)
}

// A stop while waiting drops the phase back to built, which is a phase move
// and so restamps since.
func TestSelfUpdateInPlaceStopWhileWaitingRestampsSince(t *testing.T) {
	rig := inPlaceWaiting(t, func(rig *handoffRig) { rig.clk.setNow(sinceBase) })
	if got, want := selfStatus(t, rig.dir).Since, sinceBase.Format(time.RFC3339); got != want {
		t.Fatalf("since at waiting = %q, want %q", got, want)
	}

	rig.clk.setNow(sinceBase.Add(time.Hour))
	close(rig.stop)
	rig.r.releaseSlot(t, 1, ChildResult{Exit: 0})
	rig.wait(t)

	got := selfStatus(t, rig.dir)
	if want := sinceBase.Add(time.Hour).Format(time.RFC3339); got.Phase != SelfUpdateBuilt || got.Since != want {
		t.Errorf("self_update = %+v, want built since %s", got, want)
	}
}

// A stop while waiting cancels the handoff: an ordinary operator stop that
// drains the running child, with the build left recorded as built.
func TestSelfUpdateInPlaceStopWhileWaitingDrains(t *testing.T) {
	rig := inPlaceWaiting(t)
	r, dir, buf := rig.r, rig.dir, rig.buf

	close(rig.stop)
	select {
	case h := <-rig.done:
		t.Fatalf("pool ended with %+v before its running child finished", h)
	case <-time.After(50 * time.Millisecond):
	}
	r.releaseSlot(t, 1, ChildResult{Exit: 0})
	h := rig.wait(t)

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

// handoffRig is an in-place pool of `slots` held slots whose build passes when
// the test closes gate. announce makes every child box at start; without it a
// child reports nothing until the test fires a record.
type handoffRig struct {
	r    *scriptedRunner
	gate chan struct{}
	dir  string
	done chan Halt
	buf  *bytes.Buffer
	stop chan struct{}
	cfg  Config
	clk  *testClock
}

// newHandoffRig builds the rig without starting Loop, so a test can adjust
// rig.cfg or the runner's hooks first; run starts it.
func newHandoffRig(t *testing.T, slots int) *handoffRig {
	t.Helper()
	rig := &handoffRig{
		r:    &scriptedRunner{revisions: []string{"rev1"}, selfPaths: []string{selfNew}},
		gate: make(chan struct{}),
		dir:  t.TempDir(),
		done: make(chan Halt, 1),
		buf:  &bytes.Buffer{},
		stop: make(chan struct{}),
		clk:  &testClock{},
	}
	rig.r.holdSlots(slots)
	rig.r.onPrebuild = func(ctx context.Context, _ int, _ Tip) error {
		select {
		case <-rig.gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	rig.cfg = testConfig(slots)
	rig.cfg.SelfProgram = selfOld
	rig.cfg.RestartInPlace = true
	rig.cfg.Stop = rig.stop
	rig.cfg.Status = NewStatusWriter(rig.dir, time.Now)
	return rig
}

func (rig *handoffRig) run() {
	go func() {
		rig.done <- Loop(context.Background(), rig.cfg, rig.r, newTestEmitter(rig.buf), rig.clk)
	}()
}

func startHandoffRig(t *testing.T, slots int, announce bool) *handoffRig {
	t.Helper()
	rig := newHandoffRig(t, slots)
	if announce {
		rig.r.announceEachSlot()
	}
	rig.run()
	return rig
}

// wait returns Loop's halt, failing the test when the pool does not end.
func (rig *handoffRig) wait(t *testing.T) Halt {
	t.Helper()
	select {
	case h := <-rig.done:
		return h
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return")
		return Halt{}
	}
}

// stillWaiting fails the test if the pool ends within a short grace period.
func (rig *handoffRig) stillWaiting(t *testing.T, why string) {
	t.Helper()
	select {
	case h := <-rig.done:
		t.Fatalf("pool ended with %+v, want it to keep waiting: %s", h, why)
	case <-time.After(50 * time.Millisecond):
	}
	waitSelfPhase(t, rig.dir, SelfUpdateWaiting)
}

func (r *scriptedRunner) inFlightNow() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inFlight
}

// In-place mode, children running and past discovery: a passing build hands
// off at once, carrying every busy slot, and touches none of the children.
func TestSelfUpdateInPlaceHandsOffBusySlots(t *testing.T) {
	rig := startHandoffRig(t, 2, true)
	rig.r.awaitStart(t)
	rig.r.awaitStart(t)
	close(rig.gate)

	h := rig.wait(t)

	if h.Class != HaltNone || h.Handoff == nil {
		t.Fatalf("halt = %+v, want HaltNone carrying a handoff", h)
	}
	names := eventNames(decodeEvents(t, rig.buf))
	if n := countEvents(names, "halt"); n != 0 {
		t.Errorf("halt events = %d, want none: a handoff is not a halt", n)
	}
	if n := countEvents(names, "child_finish"); n != 0 {
		t.Errorf("child_finish events = %d, want none: the children were not waited on", n)
	}
	if n := rig.r.inFlightNow(); n != 2 {
		t.Errorf("children in flight = %d, want both still running", n)
	}
	if h.Handoff.Stop || h.Handoff.Abort {
		t.Errorf("handoff latch = stop %v abort %v, want open", h.Handoff.Stop, h.Handoff.Abort)
	}
	if len(h.Handoff.Slots) != 2 {
		t.Fatalf("handoff slots = %+v, want 2", h.Handoff.Slots)
	}
	for i, hs := range h.Handoff.Slots {
		rig.r.mu.Lock()
		child := rig.r.handles[i].state
		rig.r.mu.Unlock()
		wantKey := dispatchkey.Issue(fmt.Sprintf("issue-%d", i))
		if hs.Slot != i || hs.Kind != workKind || hs.Revision != "rev1" || hs.Key != wantKey ||
			!slices.Equal(hs.Issues, []string{fmt.Sprintf("issue-%d", i)}) || hs.Child.PID != child.PID ||
			hs.ChildStart == "" || hs.ChildLog == "" {
			t.Errorf("handoff slot %d = %+v, want its own kind, revision, key, issues, child %+v, start and log", i, hs, child)
		}
	}
	rep, err := ReadStatus(rig.dir)
	if err != nil || rep.Status == nil || rep.Status.SelfUpdate == nil {
		t.Fatalf("ReadStatus = %+v, %v", rep, err)
	}
	if got := rep.Status.SelfUpdate.Phase; got != SelfUpdateRestarting {
		t.Errorf("self_update phase = %q, want %q", got, SelfUpdateRestarting)
	}
	for _, sl := range rep.Status.Slots {
		if !sl.Busy {
			t.Errorf("slot %d reads %q after the handoff, want it still busy", sl.Slot, sl.Phase)
		}
	}

	// The children are still collectable: the pool neither killed nor reaped them.
	rig.r.releaseSlot(t, 0, ChildResult{Exit: 4})
	rig.r.mu.Lock()
	h0 := rig.r.handles[0]
	rig.r.mu.Unlock()
	if res, err := h0.Wait(nil); err != nil || res.Exit != 4 {
		t.Errorf("slot 0 child Wait = (%+v, %v), want its released exit", res, err)
	}
}

// The held discovery baton keeps the handoff waiting even when every running
// child has reported something, until the holder boxes and passes it.
func TestSelfUpdateInPlaceHeldBatonHoldsOffHandoff(t *testing.T) {
	rig := startHandoffRig(t, 2, false)
	rig.r.awaitStart(t)
	close(rig.gate)
	rig.stillWaiting(t, "the child has reported nothing")

	rig.r.fireOnRecord(t, 0, Record{Event: report.EventModel, Model: "opus"})
	rig.stillWaiting(t, "the child reported but still holds the discovery baton")

	rig.r.fireOnIssue(t, 0, "42")
	h := rig.wait(t)

	if h.Handoff == nil || len(h.Handoff.Slots) != 1 {
		t.Fatalf("halt = %+v, want a handoff of the one busy slot", h)
	}
	if hs := h.Handoff.Slots[0]; hs.Slot != 0 || hs.Key != dispatchkey.Issue("42") {
		t.Errorf("handoff slot = %+v, want slot 0 on issue 42", hs)
	}
	if n := rig.r.runCount(); n != 1 {
		t.Errorf("run calls = %d, want 1: the free slot must not start a child", n)
	}
	waitSelfPhase(t, rig.dir, SelfUpdateRestarting)
}

// A Chore-keyed child passes the baton as it starts, so the first report is
// the only thing holding the handoff off in a multi-slot pool.
func TestSelfUpdateInPlaceChoreChildReportHoldsOffHandoff(t *testing.T) {
	rig := newHandoffRig(t, 2)
	rig.cfg.Kinds = []Kind{KindOf(dispatchkind.Butler)}
	rig.run()
	rig.r.awaitStart(t)
	rig.r.awaitStart(t)
	close(rig.gate)
	rig.stillWaiting(t, "neither Chore child has reported")

	rig.r.fireOnRecord(t, 0, Record{Event: report.EventModel, Model: "opus"})
	rig.stillWaiting(t, "slot 1's Chore child has not reported")

	rig.r.fireOnRecord(t, 1, Record{Event: report.EventModel, Model: "opus"})
	h := rig.wait(t)

	if h.Class != HaltNone || h.Handoff == nil || len(h.Handoff.Slots) != 2 {
		t.Fatalf("halt = %+v, want a handoff of both busy slots", h)
	}
}

// A halt racing the committed handoff is not a halt: Loop returns a handoff,
// so no halt event may be emitted or latched.
func TestRecordHaltAfterHandoffCommitIsNoOp(t *testing.T) {
	var s state
	s.self.Phase = SelfUpdateRestarting

	if evs := s.recordHalt(Halt{Class: HaltOperatorStop, Detail: "late"}); evs != nil {
		t.Errorf("events = %v, want none after the handoff committed", evs)
	}
	if s.halted {
		t.Error("halt latched after the handoff committed")
	}
}

// A single-slot pool has no baton, so only the first report gates the handoff.
func TestSelfUpdateInPlaceUnreportedChildHoldsOffHandoff(t *testing.T) {
	rig := startHandoffRig(t, 1, false)
	rig.r.awaitStart(t)
	close(rig.gate)
	rig.stillWaiting(t, "the child has not sent its first report")

	rig.r.fireOnRecord(t, 0, Record{Event: report.EventModel, Model: "opus"})
	h := rig.wait(t)

	if h.Class != HaltNone || h.Handoff == nil || len(h.Handoff.Slots) != 1 {
		t.Fatalf("halt = %+v, want a handoff of the one busy slot", h)
	}
	if got := h.Handoff.Slots[0]; got.Child.PID == 0 || got.Key != (dispatchkey.Key{}) {
		t.Errorf("handoff slot = %+v, want the child's pid and no key yet", got)
	}
}

// An image_build record comes from bootstrap, before discovery, so it is not
// the first report that clears a child to hand off.
func TestSelfUpdateInPlaceImageBuildDoesNotClearHandoff(t *testing.T) {
	rig := startHandoffRig(t, 1, false)
	rig.r.awaitStart(t)
	close(rig.gate)
	rig.stillWaiting(t, "the child has not sent its first report")

	rig.r.fireOnRecord(t, 0, Record{Event: report.EventImageBuild, State: string(report.ImageBuildStarted)})
	rig.stillWaiting(t, "the child has only reported an image build")
	rig.r.fireOnRecord(t, 0, Record{Event: report.EventImageBuild, State: string(report.ImageBuildFinished)})
	rig.stillWaiting(t, "the child has only reported an image build")

	rig.r.fireOnRecord(t, 0, Record{Event: report.EventModel, Model: "opus"})
	if h := rig.wait(t); h.Handoff == nil {
		t.Fatalf("halt = %+v, want a handoff once a non-image_build record arrived", h)
	}
}

// The returned document is one a fresh pool resumes from: the slots seat as
// inherited children with the key and kind the predecessor tracked.
func TestSelfUpdateInPlaceHandoffRoundTrips(t *testing.T) {
	rig := startHandoffRig(t, 2, true)
	rig.r.awaitStart(t)
	rig.r.awaitStart(t)
	close(rig.gate)
	h := rig.wait(t)
	if h.Handoff == nil {
		t.Fatalf("halt = %+v, want a handoff", h)
	}
	var wire bytes.Buffer
	if err := EncodeHandoff(&wire, *h.Handoff); err != nil {
		t.Fatalf("EncodeHandoff: %v", err)
	}
	doc, err := DecodeHandoff(&wire)
	if err != nil {
		t.Fatalf("DecodeHandoff: %v", err)
	}

	r2 := &scriptedRunner{}
	r2.holdSlots(2)
	stop := make(chan struct{})
	cfg := testConfig(2)
	cfg.Stop = stop
	cfg.Handoff = &doc
	var buf bytes.Buffer
	done := make(chan Halt, 1)
	go func() { done <- Loop(context.Background(), cfg, r2, newTestEmitter(&buf), &testClock{}) }()
	r2.awaitStart(t)
	r2.awaitStart(t)
	close(stop)
	r2.releaseSlot(t, 0, ChildResult{Exit: 0})
	r2.releaseSlot(t, 1, ChildResult{Exit: 0})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("resumed Loop did not return")
	}

	var inherited []Event
	for _, ev := range decodeEvents(t, &buf) {
		if ev.Event == "child_inherited" {
			inherited = append(inherited, ev)
		}
	}
	if len(inherited) != 2 {
		t.Fatalf("child_inherited events = %+v, want 2", inherited)
	}
	for i, ev := range inherited {
		if *ev.Slot != doc.Slots[i].Slot || ev.Kind != doc.Slots[i].Kind || ev.Key != doc.Slots[i].Key {
			t.Errorf("inherited event %d = %+v, want slot %d kind %s key %v", i, ev, doc.Slots[i].Slot, doc.Slots[i].Kind, doc.Slots[i].Key)
		}
	}
	for i, c := range r2.inheritCalls() {
		if c.PID != doc.Slots[c.Slot].Child.PID {
			t.Errorf("inherit call %d pid = %d, want slot %d's %d", i, c.PID, c.Slot, doc.Slots[c.Slot].Child.PID)
		}
	}
}

// In-place mode, every slot idle: a passing build commits the handoff at once
// from the build's own state change, with nothing to carry. The slots are
// asleep in their idle wait and must return on the commit, not on a timer.
func TestSelfUpdateInPlaceIdlePoolHandsOffAtOnce(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1"}, selfPaths: []string{selfNew},
		results: []ChildResult{{Exit: 2}},
	}
	gate := make(chan struct{})
	r.onPrebuild = func(ctx context.Context, _ int, _ Tip) error {
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	clk := &testClock{}
	clk.park()
	dir := t.TempDir()
	buf := &bytes.Buffer{}
	cfg := testConfig(2)
	cfg.SelfProgram = selfOld
	cfg.RestartInPlace = true
	cfg.Stop = make(chan struct{})
	cfg.Status = NewStatusWriter(dir, time.Now)

	done := make(chan Halt, 1)
	go func() { done <- Loop(context.Background(), cfg, r, newTestEmitter(buf), clk) }()

	// Every child has exited with no work and both slots sleep: the pool is
	// idle, and only the parked clock could end that sleep. Whether the second
	// slot runs a child first is a race: once the first child's queue-empty
	// lands, it idles without running one.
	var runs int
	deadline := time.Now().Add(5 * time.Second)
	for {
		clk.mu.Lock()
		asleep := len(clk.sleepers)
		clk.mu.Unlock()
		runs = r.runCount()
		if asleep == 2 && runs >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slots asleep = %d, runs = %d, want both idle", asleep, runs)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case h := <-done:
		t.Fatalf("pool ended with %+v before the build passed", h)
	default:
	}
	close(gate)

	var h Halt
	select {
	case h = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return after the build passed on an idle pool")
	}
	if h.Class != HaltNone || h.Handoff == nil {
		t.Fatalf("halt = %+v, want HaltNone carrying a handoff", h)
	}
	if len(h.Handoff.Slots) != 0 {
		t.Errorf("handoff slots = %+v, want none: no child was running", h.Handoff.Slots)
	}
	if n := r.runCount(); n != runs {
		t.Errorf("run calls = %d, want %d: a woken slot must not start a child", n, runs)
	}
	events := decodeEvents(t, buf)
	if n := countEvents(eventNames(events), "halt"); n != 0 {
		t.Errorf("halt events = %d, want none: a handoff is not a halt", n)
	}
	rep, err := ReadStatus(dir)
	if err != nil || rep.Status == nil || rep.Status.SelfUpdate == nil {
		t.Fatalf("ReadStatus = %+v, %v", rep, err)
	}
	if got := rep.Status.SelfUpdate.Phase; got != SelfUpdateRestarting {
		t.Errorf("self_update phase = %q, want %q", got, SelfUpdateRestarting)
	}
}
