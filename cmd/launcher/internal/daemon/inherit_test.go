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
	if slots[0].Since != "2026-10-11T10:00:00Z" || slots[2].Since != "2026-10-11T10:01:00Z" {
		t.Errorf("since = %q, %q, want the children's real start times", slots[0].Since, slots[2].Since)
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
	var reason string
	for _, ev := range evs {
		if ev.Event == "backoff" {
			reason = ev.Reason
			break
		}
	}
	if !strings.HasPrefix(reason, "inherit-child: ") || !strings.Contains(reason, "pidfd gone") {
		t.Errorf("backoff reason = %q, want it to name the inherit-child seam and its error", reason)
	}
}

func TestInvalidHandoffHaltsInvalidConfigWithoutInheriting(t *testing.T) {
	dup := inheritedWork(0, 101)
	badKind := inheritedWork(0, 100)
	badKind.Kind = KindOf(dispatchkind.Research)
	pastKind := inheritedWork(0, 100)
	pastKind.GateGen = 2
	pastCarried := handoffOf(pastKind)
	pastCarried.Kinds = []HandoffKind{{Kind: workKind, Gen: 1}}
	noKindEntry := inheritedWork(0, 100)
	noKindEntry.GateGen = 1
	for name, h := range map[string]*Handoff{
		"gate generation past the kind's":    pastCarried,
		"gate generation with no kind entry": handoffOf(noKindEntry),
		"slot out of range":                  handoffOf(inheritedWork(2, 100)),
		"negative slot":                      handoffOf(inheritedWork(-1, 100)),
		"duplicate slot":                     handoffOf(inheritedWork(0, 100), dup),
		"kind not in kinds":                  handoffOf(badKind),
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

var handoffBase = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// resumeAt runs Loop on cfg resumed from h, with the clock at handoffBase, and
// returns the halt, the events and the clock reading at each StartChild.
func resumeAt(cfg Config, r *scriptedRunner, h *Handoff) (Halt, []Event, []time.Time) {
	clk := &testClock{}
	clk.setNow(handoffBase)
	var starts []time.Time
	r.onStart = func(context.Context, ChildRequest) error {
		starts = append(starts, clk.Now())
		return nil
	}
	var buf bytes.Buffer
	cfg.Handoff = h
	halt := Loop(context.Background(), cfg, r, newTestEmitter(&buf), clk)
	return halt, decodeEventsBuf(&buf), starts
}

func TestHandoffCarriedGateHoldsTheKindUntilItsDeadline(t *testing.T) {
	until := handoffBase.Add(10 * time.Minute)
	for _, tc := range []struct {
		name string
		h    *Handoff
		want time.Time
	}{
		{"cold", nil, handoffBase},
		{"carried", &Handoff{Kinds: []HandoffKind{{Kind: workKind, Until: until}}}, until},
	} {
		r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}}
		_, _, starts := resumeAt(testConfig(1), r, tc.h)
		if len(starts) != 1 || !starts[0].Equal(tc.want) {
			t.Errorf("%s: child starts = %v, want one at %v", tc.name, starts, tc.want)
		}
	}
}

func TestHandoffCarriedJamGateIsLiftedByAMovedTip(t *testing.T) {
	until := handoffBase.Add(time.Hour)
	h := &Handoff{Kinds: []HandoffKind{{Kind: workKind, Until: until, Jammed: true}}}
	r := &scriptedRunner{revisions: []string{"rev1"}, moved: []bool{true}, results: []ChildResult{{Exit: 5}}}
	_, evs, starts := resumeAt(testConfig(1), r, h)
	if len(starts) != 1 || !starts[0].Before(until) {
		t.Fatalf("child starts = %v, want one before the carried jam's %v", starts, until)
	}
	for _, ev := range evs {
		if ev.Event == "tip_moved" {
			if !slices.Equal(ev.Kinds, []Kind{workKind}) {
				t.Fatalf("tip_moved kinds = %v, want [work]", ev.Kinds)
			}
			return
		}
	}
	t.Fatalf("no tip_moved in %v", eventNames(evs))
}

func TestHandoffCarriedBackoffStepContinuesFromTheCarriedWait(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    *Handoff
		want string
	}{
		{"cold", nil, testIdleFloor.String()},
		{"carried", &Handoff{Kinds: []HandoffKind{{Kind: workKind, BackoffCur: 8 * testIdleFloor}}}, (8 * testIdleFloor).String()},
		{"clamped to the new cap", &Handoff{Kinds: []HandoffKind{{Kind: workKind, BackoffCur: 2 * testIdleCap}}}, testIdleCap.String()},
	} {
		r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}, {Exit: 5}}}
		_, evs, _ := resumeAt(testConfig(1), r, tc.h)
		var wait string
		for _, ev := range evs {
			if ev.Event == "idle" {
				wait = ev.Wait
				break
			}
		}
		if wait != tc.want {
			t.Errorf("%s: first idle wait = %q, want %q", tc.name, wait, tc.want)
		}
	}
}

// A flight inherited from under an older gate generation than the carried one
// is the same observation as the sibling that closed the gate (#4618): its
// no-work exit must neither double the backoff nor move the deadline. One
// that started under the carried generation is a fresh observation.
func TestHandoffInheritedNoWorkExitIsJudgedAgainstItsOwnGateGeneration(t *testing.T) {
	until := handoffBase.Add(10 * time.Minute)
	cur := 4 * testIdleFloor
	for _, tc := range []struct {
		name    string
		gateGen int
		want    []string
	}{
		{"older generation is absorbed", 0, []string{(10 * time.Minute).String(), cur.String()}},
		{"carried generation escalates", 1, []string{cur.String(), (2 * cur).String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slot := inheritedWork(0, 100)
			slot.GateGen = tc.gateGen
			h := handoffOf(slot)
			h.Kinds = []HandoffKind{{Kind: workKind, Gen: 1, Until: until, BackoffCur: cur}}
			r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}, {Exit: 5}}, exited: map[int]ChildResult{100: {Exit: 2}}}
			_, evs, _ := resumeAt(testConfig(1), r, h)
			var waits []string
			for _, ev := range evs {
				if ev.Event == "idle" {
					waits = append(waits, ev.Wait)
				}
			}
			if !slices.Equal(waits, tc.want) {
				t.Errorf("idle waits = %v, want %v", waits, tc.want)
			}
		})
	}
}

func TestHandoffCarriedDueKeepsTheKindParkedUntilItsInstant(t *testing.T) {
	due := handoffBase.Add(2 * time.Hour)
	h := &Handoff{Kinds: []HandoffKind{{Kind: butlerKind, DueAt: due}}}
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}}
	_, _, starts := resumeAt(butlerOnlyConfig(), r, h)
	if len(starts) != 1 || !starts[0].Equal(due) {
		t.Fatalf("child starts = %v, want one at the carried next_due %v", starts, due)
	}
}

func TestHandoffCarriedRateLimitPausesTheKind(t *testing.T) {
	until := handoffBase.Add(30 * time.Minute)
	h := &Handoff{Kinds: []HandoffKind{{Kind: workKind, LimitedUntil: until}}}
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}}
	_, _, starts := resumeAt(testConfig(1), r, h)
	if len(starts) != 1 || starts[0].Before(until) {
		t.Fatalf("child starts = %v, want one no earlier than the carried pause %v", starts, until)
	}
}

func TestHandoffCarriedBreakerWindowCountsTowardsTheTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		breaker []time.Time
		want    HaltClass
	}{
		{"no carried failures", nil, HaltChildHostTainted},
		{"two recent", []time.Time{handoffBase.Add(-time.Minute), handoffBase.Add(-2 * time.Minute)}, HaltBreaker},
		{"one recent, one aged out", []time.Time{handoffBase.Add(-time.Minute), handoffBase.Add(-2 * testBreakerWindow)}, HaltChildHostTainted},
	} {
		cfg := testConfig(1)
		cfg.BreakerThreshold = 3
		h := handoffOf(inheritedWork(0, 100))
		h.Breaker = tc.breaker
		r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}, exited: map[int]ChildResult{100: {Exit: 1}}}
		halt, evs, _ := resumeAt(cfg, r, h)
		if halt.Class != tc.want {
			t.Errorf("%s: halt = %v (%v), want class %v", tc.name, halt, eventNames(evs), tc.want)
		}
	}
}

func TestHandoffCarriedStopHaltsAfterTheInheritedChildFinishes(t *testing.T) {
	h := handoffOf(inheritedWork(0, 100))
	h.Stop = true
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}}}
	halt, evs, _ := resumeAt(testConfig(1), r, h)
	if halt.Class != HaltOperatorStop {
		t.Fatalf("halt = %v, want HaltOperatorStop", halt)
	}
	if r.runCount() != 0 {
		t.Errorf("StartChild calls = %d, want none under a carried stop", r.runCount())
	}
	finishOf(t, evs)
	calls := r.inheritCalls()
	if len(calls) != 1 {
		t.Fatalf("InheritChild calls = %+v, want 1", calls)
	}
	if calls[0].CtxErr != nil {
		t.Errorf("InheritChild ctx err = %v, want a live ctx: a halt must not abort the re-attach", calls[0].CtxErr)
	}
	select {
	case <-calls[0].Stop:
	default:
		t.Error("inherited child's Stop is not closed")
	}
	select {
	case <-calls[0].Abort:
		t.Error("inherited child's Abort is closed, but only Stop was carried")
	default:
	}
}

func TestHandoffCarriedAbortReachesTheInheritedChild(t *testing.T) {
	h := handoffOf(inheritedWork(0, 100))
	h.Abort = true
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 5}}}
	resumeAt(testConfig(1), r, h)
	calls := r.inheritCalls()
	if len(calls) != 1 {
		t.Fatalf("InheritChild calls = %+v, want 1", calls)
	}
	select {
	case <-calls[0].Abort:
	default:
		t.Error("inherited child's Abort is not closed")
	}
}

func TestHandoffInvalidKindEntriesAreInvalidConfig(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kinds []HandoffKind
	}{
		{"unconfigured kind", []HandoffKind{{Kind: butlerKind}}},
		{"duplicate kind", []HandoffKind{{Kind: workKind}, {Kind: workKind}}},
	} {
		r := &scriptedRunner{revisions: []string{"rev1"}}
		halt, _, _ := resumeAt(testConfig(1), r, &Handoff{Kinds: tc.kinds})
		if halt.Class != HaltInvalidConfig || r.runCount() != 0 {
			t.Errorf("%s: halt = %v, runs = %d, want HaltInvalidConfig and no child", tc.name, halt, r.runCount())
		}
	}
}

func TestInheritedSlotSinceFallsBackToNowWhenChildStartDoesNotParse(t *testing.T) {
	cfg := testConfig(1)
	dir := t.TempDir()
	cfg.Status = NewStatusWriter(dir, time.Now)
	hs := inheritedWork(0, 100)
	hs.ChildStart = "not a time"
	cfg.Handoff = handoffOf(hs)
	clk := &testClock{}
	clk.setNow(handoffBase)
	var buf bytes.Buffer
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), clk)
	defer p.cancel()

	p.occupyInherited()

	rep, err := ReadStatus(dir)
	if err != nil || rep.Status == nil {
		t.Fatalf("ReadStatus = %+v, %v", rep, err)
	}
	if got, want := rep.Status.Slots[0].Since, handoffBase.UTC().Format(time.RFC3339); got != want {
		t.Errorf("since = %q, want the resume time %q", got, want)
	}
}

// TestLoopFirstStatusShowsInheritedSlotsBeforeAnyStartChild pins the same
// criterion as the occupyInherited test above, but through Loop: the status
// file as read at the first StartChild already carries the inherited slots.
func TestLoopFirstStatusShowsInheritedSlotsBeforeAnyStartChild(t *testing.T) {
	cfg := triKindConfig(3, 0)
	dir := t.TempDir()
	cfg.Status = NewStatusWriter(dir, time.Now)
	cfg.Handoff = handoffOf(inheritedWork(0, 100), HandoffSlot{
		Slot: 2, Kind: butlerKind, Revision: "rev2",
		ChildStart: "2026-10-11T10:01:00Z", ChildStartN: 3,
		Key: dispatchkey.Chore("sweep"), Pass: "scan",
		Child: ChildState{PID: 101},
	})
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(3)
	var atStart *Status
	r.onStart = func(context.Context, ChildRequest) error {
		if rep, err := ReadStatus(dir); err == nil && atStart == nil {
			atStart = rep.Status
		}
		return nil
	}
	var buf bytes.Buffer
	done := make(chan Halt, 1)
	go func() { done <- Loop(context.Background(), cfg, r, newTestEmitter(&buf), &testClock{}) }()
	for range 3 {
		r.awaitStart(t)
	}
	for slot := range 3 {
		r.releaseSlot(t, slot, ChildResult{Exit: 5})
	}
	<-done

	if atStart == nil || len(atStart.Slots) != 3 {
		t.Fatalf("status at the first StartChild = %+v, want 3 slots", atStart)
	}
	want0 := SlotStatus{
		Slot: 0, Busy: true, Kind: workKind, Revision: "rev1", Issues: []string{"42"},
		Pass: "implement", Model: "opus", ModelRole: "coordinator",
		ChildStart: "2026-10-11T10:00:00Z", ChildStartN: 1,
	}
	if !statusSlotEqual(atStart.Slots[0], want0) {
		t.Errorf("slot 0 = %+v, want %+v", atStart.Slots[0], want0)
	}
	s2 := atStart.Slots[2]
	if !s2.Busy || s2.Kind != butlerKind || s2.Revision != "rev2" || s2.Chore != "sweep" || s2.Pass != "scan" ||
		s2.ChildStart != "2026-10-11T10:01:00Z" || s2.ChildStartN != 3 {
		t.Errorf("slot 2 = %+v, want busy butler on chore sweep", s2)
	}
}
