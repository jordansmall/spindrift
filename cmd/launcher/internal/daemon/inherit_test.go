package daemon

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/report"
)

var (
	butlerKind = KindOf(dispatchkind.Butler)
)

// inheritedWork is a work slot whose child already boxed issue 42.
func inheritedWork(slot, pid int) HandoffSlot {
	return HandoffSlot{
		Slot: slot, Kind: workKind, Revision: "rev1",
		ChildStart: "2026-10-11T10:00:00Z", ChildStartN: 1,
		ChildLog: "logs/work.log",
		Key:      dispatchkey.Issue("42"), Issues: []string{"42"},
		Pass: "implement", Model: "opus", ModelRole: "coordinator",
		Child: ChildState{PID: pid},
	}
}

func handoffOf(slots ...HandoffSlot) *Handoff {
	return &Handoff{V: HandoffVersion, Slots: slots}
}

func loopWithHandoff(cfg Config, r *scriptedRunner, h *Handoff) (Halt, []Event) {
	var buf bytes.Buffer
	cfg.Handoff = h
	halt := Loop(context.Background(), cfg, r, newTestEmitter(&buf), &testClock{})
	return halt, decodeEventsBuf(&buf)
}

func decodeEventsBuf(buf *bytes.Buffer) []Event {
	var evs []Event
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var ev Event
		if err := ev.UnmarshalJSON([]byte(line)); err != nil {
			panic(err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func TestInheritFirstStatusShowsInheritedSlots(t *testing.T) {
	cfg := triKindConfig(3, 0)
	dir := t.TempDir()
	cfg.Status = NewStatusWriter(dir, time.Now)
	cfg.Handoff = handoffOf(inheritedWork(0, 100), HandoffSlot{
		Slot: 2, Kind: butlerKind, Revision: "rev2",
		ChildStart: "2026-10-11T10:01:00Z", ChildStartN: 3,
		Key: dispatchkey.Chore("sweep"), Pass: "scan",
		Child: ChildState{PID: 101},
	})
	r := &scriptedRunner{}
	var buf bytes.Buffer
	p, _ := newPool(context.Background(), cfg, r, newTestEmitter(&buf), &testClock{})
	defer p.cancel()

	p.occupyInherited()

	rep, err := ReadStatus(dir)
	if err != nil || rep.Status == nil {
		t.Fatalf("ReadStatus = %+v, %v", rep, err)
	}
	slots := rep.Status.Slots
	if len(slots) != 3 {
		t.Fatalf("slots = %+v, want 3", slots)
	}
	want0 := SlotStatus{
		Slot: 0, Busy: true, Kind: workKind, Revision: "rev1", Issues: []string{"42"},
		Pass: "implement", Model: "opus", ModelRole: "coordinator",
		ChildStart: "2026-10-11T10:00:00Z", ChildStartN: 1,
	}
	got0 := slots[0]
	if !statusSlotEqual(got0, want0) {
		t.Errorf("slot 0 = %+v, want %+v", slots[0], want0)
	}
	if slots[1].Busy {
		t.Errorf("slot 1 = %+v, want idle", slots[1])
	}
	s2 := slots[2]
	if !s2.Busy || s2.Kind != butlerKind || s2.Revision != "rev2" || s2.Chore != "sweep" || s2.Pass != "scan" ||
		s2.ChildStart != "2026-10-11T10:01:00Z" || s2.ChildStartN != 3 || len(s2.Issues) != 0 {
		t.Errorf("slot 2 = %+v, want busy butler on chore sweep", s2)
	}
	if r.runCount() != 0 {
		t.Errorf("StartChild calls = %d, want 0 before any slot runs", r.runCount())
	}
}

// statusSlotEqual compares the handoff-carried fields of two SlotStatus.
func statusSlotEqual(a, b SlotStatus) bool {
	return a.Slot == b.Slot && a.Busy == b.Busy && a.Kind == b.Kind && a.Revision == b.Revision &&
		slices.Equal(a.Issues, b.Issues) && a.Chore == b.Chore && a.Pass == b.Pass &&
		a.Model == b.Model && a.ModelRole == b.ModelRole &&
		a.ChildStart == b.ChildStart && a.ChildStartN == b.ChildStartN
}

func TestInheritEmitsChildInheritedBeforeAnyChildStart(t *testing.T) {
	cfg := testConfig(2)
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(2)
	cfg.Handoff = handoffOf(inheritedWork(0, 100), inheritedWork(1, 101))
	cfg.Handoff.Slots[1].Key = dispatchkey.Issue("43")
	var buf bytes.Buffer
	done := make(chan Halt, 1)
	go func() { done <- Loop(context.Background(), cfg, r, newTestEmitter(&buf), &testClock{}) }()
	r.awaitStart(t)
	r.awaitStart(t)
	r.releaseSlot(t, 0, ChildResult{Exit: 5})
	r.releaseSlot(t, 1, ChildResult{Exit: 5})
	<-done

	evs := decodeEventsBuf(&buf)
	var inherited []Event
	for _, ev := range evs {
		if ev.Event == "child_start" {
			t.Fatalf("child_start emitted for a pool whose slots are all inherited: %v", eventNames(evs))
		}
		if ev.Event == "child_inherited" {
			inherited = append(inherited, ev)
		}
	}
	if len(inherited) != 2 {
		t.Fatalf("child_inherited events = %d, want 2 (%v)", len(inherited), eventNames(evs))
	}
	for i, ev := range inherited {
		wantKey := dispatchkey.Issue([]string{"42", "43"}[i])
		if ev.Kind != workKind || ev.Revision != "rev1" || ev.Slot == nil || *ev.Slot != i ||
			ev.Key != wantKey || ev.ChildLog != "logs/work.log" {
			t.Errorf("child_inherited[%d] = %+v", i, ev)
		}
	}
	if calls := r.inheritCalls(); len(calls) != 2 {
		t.Errorf("InheritChild calls = %+v, want 2", calls)
	}
}

func TestInheritedRecordsFlowLikeAnOwnChilds(t *testing.T) {
	cfg := testConfig(1)
	cfg.Handoff = handoffOf(inheritedWork(0, 100))
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(1)
	var buf bytes.Buffer
	done := make(chan Halt, 1)
	go func() { done <- Loop(context.Background(), cfg, r, newTestEmitter(&buf), &testClock{}) }()
	r.awaitStart(t)

	r.fireOnRecord(t, 0, Record{Event: report.EventBox, Key: dispatchkey.Issue("44"), Phase: "review"})
	r.fireOnRecord(t, 0, Record{Event: report.EventModel, Model: "sonnet"})
	r.fireOnRecord(t, 0, Record{Event: report.EventSettled, Key: dispatchkey.Issue("44")})
	r.releaseSlot(t, 0, ChildResult{Exit: 5})
	<-done

	names := eventNames(decodeEventsBuf(&buf))
	for _, want := range []string{"child_inherited", "box", "model", "settled", "child_finish"} {
		if !slices.Contains(names, want) {
			t.Errorf("events %v lack %q", names, want)
		}
	}
	for _, ev := range decodeEventsBuf(&buf) {
		if ev.Event == "child_finish" && ev.Key != dispatchkey.Issue("44") {
			t.Errorf("child_finish key = %v, want the latest boxed claim 44", ev.Key)
		}
	}
}

// TestInheritedNotDueParksTheKindLikeAnOwnChilds: an inherited butler child's
// not_due report parks the daemon until the reported instant, as
// TestLoopButlerParksUntilReportedNextDue pins for an own child.
func TestInheritedNotDueParksTheKindLikeAnOwnChilds(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	due := base.Add(2 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{}
	clk.setNow(base)
	cfg := butlerOnlyConfig()
	cfg.Handoff = handoffOf(HandoffSlot{Slot: 0, Kind: butlerKind, Revision: "rev1", Key: dispatchkey.Chore("bugs"), Child: ChildState{PID: 100}})
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}, {Exit: 2}}}
	r.holdSlots(1)
	var secondStart time.Time
	r.onStart = func(context.Context, ChildRequest) error {
		secondStart = clk.Now()
		cancel()
		return errors.New("test stop")
	}
	var buf bytes.Buffer
	done := make(chan Halt, 1)
	go func() { done <- Loop(ctx, cfg, r, newTestEmitter(&buf), clk) }()
	r.awaitStart(t)
	r.fireOnRecord(t, 0, Record{Event: report.EventNotDue, Key: dispatchkey.Chore("bugs"), NextDue: report.NextDue{At: due}})
	r.releaseSlot(t, 0, ChildResult{Exit: 2})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return")
	}
	if !secondStart.Equal(due) {
		t.Fatalf("next child started at %v, want the inherited child's reported next_due %v", secondStart, due)
	}
}

func finishOf(t *testing.T, evs []Event) Event {
	t.Helper()
	for _, ev := range evs {
		if ev.Event == "child_finish" {
			return ev
		}
	}
	t.Fatalf("no child_finish in %v", eventNames(evs))
	return Event{}
}

func TestInheritedExitZeroFinishesWithTheFlightsKey(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
	halt, evs := loopWithHandoff(testConfig(1), r, handoffOf(inheritedWork(0, 100)))
	fin := finishOf(t, evs)
	if fin.Exit == nil || *fin.Exit != 0 || fin.Outcome != "dispatched" || fin.Key != dispatchkey.Issue("42") || fin.Kind != workKind || fin.Slot == nil || *fin.Slot != 0 {
		t.Fatalf("child_finish = %+v, want exit 0 dispatched for issue 42 on slot 0", fin)
	}
	if halt.Class != HaltChildHostTainted {
		t.Fatalf("halt = %v, want the second child's host-tainted halt", halt)
	}
}

// TestInheritedExitTwoBacksOffLikeAnOwnChilds runs the same exit-2-then-halt
// script cold and resumed: apart from the opening child_start/child_inherited
// the streams must agree event for event.
func TestInheritedExitTwoBacksOffLikeAnOwnChilds(t *testing.T) {
	cold := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}, {Exit: 5}}}
	_, coldEvs := loopWithHandoff(testConfig(1), cold, nil)
	resumed := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}, exited: map[int]ChildResult{100: {Exit: 2}}}
	_, resumedEvs := loopWithHandoff(testConfig(1), resumed, handoffOf(inheritedWork(0, 100)))

	coldNames, resumedNames := eventNames(coldEvs), eventNames(resumedEvs)
	i := slices.Index(resumedNames, "child_inherited")
	if i < 0 {
		t.Fatalf("no child_inherited in %v", resumedNames)
	}
	resumedNames[i] = "child_start"
	if !slices.Equal(coldNames, resumedNames) {
		t.Fatalf("resumed stream %v differs from the cold stream %v", resumedNames, coldNames)
	}
	if !slices.Contains(resumedNames, "idle") {
		t.Fatalf("stream %v shows no idle backoff after exit 2", resumedNames)
	}
	if fin := finishOf(t, resumedEvs); fin.Outcome != "queue-empty" {
		t.Fatalf("outcome = %q, want queue-empty", fin.Outcome)
	}
}

func TestInheritedFailureFeedsTheBreaker(t *testing.T) {
	cfg := testConfig(1)
	cfg.BreakerThreshold = 1
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 1}}}
	halt, evs := loopWithHandoff(cfg, r, handoffOf(inheritedWork(0, 100)))
	if halt.Class != HaltBreaker {
		t.Fatalf("halt = %v (%v), want HaltBreaker", halt, eventNames(evs))
	}
	if fin := finishOf(t, evs); fin.Outcome != "error" || fin.Exit == nil || *fin.Exit != 1 {
		t.Fatalf("child_finish = %+v, want outcome error exit 1", fin)
	}
	if r.runCount() != 0 {
		t.Fatalf("StartChild calls = %d, want none after the breaker tripped", r.runCount())
	}
}

func TestInheritedHaltingExitsHaltThePool(t *testing.T) {
	for _, tc := range []struct {
		exit int
		want HaltClass
	}{
		{5, HaltChildHostTainted},
		{6, HaltChildConfigInvalid},
	} {
		r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: tc.exit}}}
		halt, evs := loopWithHandoff(testConfig(1), r, handoffOf(inheritedWork(0, 100)))
		if halt.Class != tc.want {
			t.Errorf("exit %d: halt = %v, want %v", tc.exit, halt, tc.want)
		}
		if r.runCount() != 0 {
			t.Errorf("exit %d: StartChild calls = %d, want 0", tc.exit, r.runCount())
		}
		if fin := finishOf(t, evs); fin.Exit == nil || *fin.Exit != tc.exit {
			t.Errorf("exit %d: child_finish = %+v", tc.exit, fin)
		}
	}
}

func TestInheritedChildThatExitedDuringTheHandoffReportsItsExit(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		results:   []ChildResult{{Exit: 0}},
		exited:    map[int]ChildResult{100: {Exit: 6}},
	}
	halt, evs := loopWithHandoff(testConfig(1), r, handoffOf(inheritedWork(0, 100)))
	if fin := finishOf(t, evs); fin.Exit == nil || *fin.Exit != 6 || fin.Outcome != "config-invalid" {
		t.Fatalf("child_finish = %+v, want the real exit 6", fin)
	}
	if halt.Class != HaltChildConfigInvalid {
		t.Fatalf("halt = %v, want HaltChildConfigInvalid", halt)
	}
}

func TestInheritedSeamErrorFinishesWithOutcomeError(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}, inheritErr: errors.New("pidfd gone"), results: []ChildResult{{Exit: 5}}}
	_, evs := loopWithHandoff(testConfig(1), r, handoffOf(inheritedWork(0, 100)))
	names := eventNames(evs)
	if fin := finishOf(t, evs); fin.Outcome != "error" || fin.Exit != nil {
		t.Fatalf("child_finish = %+v, want outcome error with no exit", fin)
	}
	if i, j := slices.Index(names, "child_inherited"), slices.Index(names, "child_finish"); i < 0 || j < i {
		t.Fatalf("events %v, want child_inherited before child_finish", names)
	}
}

func TestInvalidHandoffHaltsInvalidConfigWithoutInheriting(t *testing.T) {
	dup := inheritedWork(0, 101)
	badKind := inheritedWork(0, 100)
	badKind.Kind = KindOf(dispatchkind.Research)
	for name, h := range map[string]*Handoff{
		"slot out of range": handoffOf(inheritedWork(2, 100)),
		"negative slot":     handoffOf(inheritedWork(-1, 100)),
		"duplicate slot":    handoffOf(inheritedWork(0, 100), dup),
		"kind not in kinds": handoffOf(badKind),
	} {
		r := &scriptedRunner{revisions: []string{"rev1"}}
		halt, evs := loopWithHandoff(testConfig(2), r, h)
		if halt.Class != HaltInvalidConfig {
			t.Errorf("%s: halt = %v, want HaltInvalidConfig", name, halt)
		}
		if n := len(r.inheritCalls()) + r.runCount(); n != 0 {
			t.Errorf("%s: seam calls = %d, want 0", name, n)
		}
		if slices.Contains(eventNames(evs), "child_inherited") {
			t.Errorf("%s: child_inherited emitted for an invalid handoff", name)
		}
	}
}

// TestInheritedLeadSlotDoesNotStrandSiblings: slot 0 (the pre-assigned baton
// leader) is inherited and never finishes, yet slot 1 still starts a child.
func TestInheritedLeadSlotDoesNotStrandSiblings(t *testing.T) {
	cfg := testConfig(2)
	cfg.Handoff = handoffOf(inheritedWork(leadSlot, 100))
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(2)
	var buf bytes.Buffer
	done := make(chan Halt, 1)
	go func() { done <- Loop(context.Background(), cfg, r, newTestEmitter(&buf), &testClock{}) }()
	// Both announce on started, in either order.
	got := map[int]bool{r.awaitStart(t): true, r.awaitStart(t): true}
	if !got[0] || !got[1] {
		t.Fatalf("started slots = %v, want the inherited slot 0 and a new child on slot 1", got)
	}
	if calls := r.calls(); len(calls) != 1 || calls[0].Slot != 1 {
		t.Fatalf("StartChild calls = %+v, want exactly slot 1", calls)
	}
	r.releaseSlot(t, 0, ChildResult{Exit: 5})
	r.releaseSlot(t, 1, ChildResult{Exit: 5})
	<-done
}
