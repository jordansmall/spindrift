package daemon

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/report"
)

// dualKindConfig is testConfig's issue #3541 sibling: both kinds, plus the
// reservation knob these tests exist to exercise. Slot count and the
// reservation are the two things each test below actually varies.
func dualKindConfig(slots, reservation int) Config {
	cfg := testConfig(slots)
	cfg.Kinds = []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)}
	cfg.Reservation = reservation
	return cfg
}

// slotsSeen returns the distinct slots among calls whose Kind is kind.
func slotsSeen(calls []runCall, kind Kind) map[int]bool {
	seen := map[int]bool{}
	for _, c := range calls {
		if c.Kind == kind {
			seen[c.Slot] = true
		}
	}
	return seen
}

// kindGate stops a dual-kind Loop run through scriptedRunner's onStart hook.
// limit is the total StartChild count at which onStart fires cancelFn.
// With cancelWhen set, limit is only a safety cap far above the goal, so a
// regression fails in bounded time, and requireGoalMet checks that the goal,
// not the cap, stopped the run; with cancelWhen nil, limit is the intended
// stop. cancelWhen, evaluated after every recorded call through r's
// accessors (never r.mu directly), is the state each test asserts on (e.g.
// "every slot has run") rather than a shared call count: with concurrent
// slots, one fast slot can burn a count before a sibling is ever scheduled.
type kindGate struct {
	r          *scriptedRunner
	limit      int
	cancelWhen func(r *scriptedRunner) bool
	cancelFn   context.CancelFunc

	mu           sync.Mutex
	capExhausted bool // limit fired before cancelWhen's goal was ever met
}

func (g *kindGate) onStart(ctx context.Context, req ChildRequest) error {
	goalMet := g.cancelWhen != nil && g.cancelWhen(g.r)
	shouldCancel := goalMet
	if !goalMet && g.limit > 0 && g.r.runCount() >= g.limit {
		// capExhausted only means something when there was a goal to miss.
		if g.cancelWhen != nil {
			g.mu.Lock()
			g.capExhausted = true
			g.mu.Unlock()
		}
		shouldCancel = true
	}
	if shouldCancel && g.cancelFn != nil {
		g.cancelFn()
	}
	return nil
}

// fataler is the sliver of *testing.T requireGoalMet needs, so a self-test
// can pass a recording double.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

// requireGoalMet fails the test if the safety cap fired before cancelWhen's
// condition was satisfied; otherwise a capped-out run can look identical to
// a successful one. A no-op with cancelWhen nil.
func (g *kindGate) requireGoalMet(t fataler) {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.capExhausted {
		t.Fatalf("kindGate: safety cap (%d calls) fired before cancelWhen's goal was ever met — likely a regression, not scheduler flake", g.limit)
	}
}

// TestPoolBothKindsShareOneSlotCapAcrossThePool pins issue #3541's central
// pool-sizing property: with both kinds configured, Slots still bounds the
// total number of children in flight, not each kind separately (which would
// silently double an operator's MEMORY_LIMIT x MAX_PARALLEL sizing).
func TestPoolBothKindsShareOneSlotCapAcrossThePool(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(slots)
	// onStart restores this test's concurrent first wave: every slot
	// announces at once, so none ever parks waiting for the discovery
	// baton.
	r.announceEachSlot()
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := dualKindConfig(slots, 1)

	done := make(chan Halt, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	seen := map[int]bool{}
	for i := 0; i < slots; i++ {
		seen[r.awaitStart(t)] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}
	if peak := r.peakConcurrency(); peak != slots {
		t.Fatalf("peak concurrency = %d, want %d: both kinds sharing one pool must still cap at Slots", peak, slots)
	}

	for s := 0; s < slots; s++ {
		r.releaseSlot(t, s, ChildResult{Exit: 5})
	}
	reason := (<-done).String()
	if !strings.Contains(reason, "host-tainted") {
		t.Fatalf("halt reason = %q, want it to name host-tainted", reason)
	}
}

// TestPoolReservationKeepsResearchFloorWhileBothKindsHaveWork pins
// Reservation as a live floor (issue #3582): with Slots: 3,
// Reservation: 1 and both queues always dispatching, the pool holds
// exactly one research and two work children at once, and whichever slot
// turns over is refilled with the kind that keeps that mix. No slot identity
// is asserted: the floor follows the running count, not a reserved slot.
func TestPoolReservationKeepsResearchFloorWhileBothKindsHaveWork(t *testing.T) {
	const slots = 3
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	work, research := KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			work:     {{Exit: 0}},
			research: {{Exit: 0}},
		},
	}
	type started struct {
		kind Kind
		slot int
	}
	starts := make(chan started, 64)
	release := make([]chan struct{}, slots)
	for i := range release {
		release[i] = make(chan struct{}, 1)
	}
	r.onStart = func(ctx context.Context, req ChildRequest) error {
		// A claim passes the discovery baton on, so the siblings are not
		// parked behind this held child.
		req.OnRecord(Record{Event: report.EventBox, Key: dispatchkey.Issue(fmt.Sprintf("held-%d", req.Slot))})
		starts <- started{req.Kind, req.Slot}
		select {
		case <-release[req.Slot]:
		case <-ctx.Done():
		}
		return nil
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	done := make(chan Halt, 1)
	go func() { done <- Loop(ctx, dualKindConfig(slots, 1), r, em, clk) }()

	next := func() started {
		t.Helper()
		select {
		case s := <-starts:
			return s
		case <-time.After(5 * time.Second):
			t.Fatalf("no child started within 5s")
			return started{}
		}
	}
	running := map[int]Kind{}
	for i := 0; i < slots; i++ {
		s := next()
		running[s.slot] = s.kind
	}
	mix := func() (nResearch, nWork int) {
		for _, k := range running {
			if k == research {
				nResearch++
			} else {
				nWork++
			}
		}
		return
	}
	if nr, nw := mix(); len(running) != slots || nr != 1 || nw != 2 {
		t.Fatalf("settled pool = %v, want 1 research + 2 work across %d distinct slots", running, slots)
	}

	// Release one child of each kind in turn; the replacement must restore
	// the mix, i.e. match the kind that just left.
	for _, leaving := range []Kind{work, research, work} {
		slot := -1
		for s, k := range running {
			if k == leaving {
				slot = s
				break
			}
		}
		if slot < 0 {
			t.Fatalf("no running %q child to release in %v", leaving, running)
		}
		release[slot] <- struct{}{}
		s := next()
		if s.slot != slot || s.kind != leaving {
			t.Fatalf("after releasing %q on slot %d, next start = %+v, want the same kind refilling the same slot", leaving, slot, s)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Loop did not return after cancel")
	}
}

// TestPoolWorkBurstsIntoWholePoolWhenResearchQueueEmpty pins the burst
// contract: a reservation never holds a slot for research that has nothing
// queued. Slots: 2, Reservation: 1; research empties on its first check and,
// with a frozen clock, never becomes runnable again, so both slots settle
// into work.
func TestPoolWorkBurstsIntoWholePoolWhenResearchQueueEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindOf(dispatchkind.Research): {{Exit: 2}},
			KindOf(dispatchkind.Work):     {{Exit: 0}},
		},
	}
	gate := &kindGate{
		r:        r,
		limit:    3000, // safety cap: dispatch never gates, so only the goal below stops the loop
		cancelFn: cancel,
		// Goal: dispatch has run in both slots. Research gates for good on its
		// first call whenever this fires, so the exactly-once assertion below
		// stays meaningful.
		cancelWhen: func(r *scriptedRunner) bool {
			seen := map[int]bool{}
			for _, c := range r.calls() {
				if c.Kind == KindOf(dispatchkind.Work) {
					seen[c.Slot] = true
				}
			}
			return seen[0] && seen[1]
		},
	}
	r.onStart = gate.onStart
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(2, 1), r, em, clk)
	gate.requireGoalMet(t)

	// Research's single empty result gates it for good (the fake clock never
	// advances here) and lands before the baton passes, so no sibling
	// re-checks the empty queue: research must be tried exactly once.
	if got := r.kindCount(KindOf(dispatchkind.Research)); got != 1 {
		t.Fatalf("research calls = %d, want exactly 1: its first empty result should gate it for the rest of the run", got)
	}
	if seen := slotsSeen(r.calls(), KindOf(dispatchkind.Work)); !seen[0] || !seen[1] {
		t.Fatalf("dispatch slots seen = %v, want both slot 0 and slot 1 (research emptying should burst work into the whole pool)", seen)
	}
}

// TestPoolResearchGateLandsBeforeBatonPasses pins the ordering behind the
// burst test above: a Wait child's kind-state fold (the "idle" event from
// noteWaitResult) must reach the stream before the baton_pass that wakes a
// parked sibling. Passing first lets the sibling's Schedule.Decide see research
// still ungated and below its reservation, start a second research child, and
// flake that test (issue #4380).
func TestPoolResearchGateLandsBeforeBatonPasses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindOf(dispatchkind.Research): {{Exit: 2}},
			KindOf(dispatchkind.Work):     {{Exit: 0}},
		},
	}
	gate := &kindGate{
		r:        r,
		limit:    3000,
		cancelFn: cancel,
		// Slot 0 is the lead baton holder and prefers research while no
		// research child runs, so research's one child runs there; work
		// reaching slot 0 means that child has finished.
		cancelWhen: func(r *scriptedRunner) bool {
			return slotsSeen(r.calls(), KindOf(dispatchkind.Work))[0]
		},
	}
	r.onStart = gate.onStart
	var buf bytes.Buffer
	Loop(ctx, dualKindConfig(2, 1), r, newTestEmitter(&buf), &testClock{})
	gate.requireGoalMet(t)

	researchKind := KindOf(dispatchkind.Research)
	finished := false
	for _, ev := range decodeEvents(t, &buf) {
		if !finished {
			finished = ev.Event == "child_finish" && ev.Kind == researchKind
			continue
		}
		switch {
		case ev.Event == "idle" && ev.Kind == researchKind:
			return
		case ev.Event == "baton_pass":
			t.Fatalf("baton_pass reached the stream before research's idle event")
		}
	}
	t.Fatalf("no research idle event followed research's child_finish")
}

// TestPoolResearchBurstsIntoWholePoolWhenWorkQueueEmpty mirrors
// TestPoolWorkBurstsIntoWholePoolWhenResearchQueueEmpty: work empties on its
// only check, so research runs in every slot. The first research child is
// held open until work has been checked: the reservation is a live running
// count, so with research's floor filled the sibling prefers work and gives
// it the check that gates it. Without the hold, instant research children
// keep that count at zero and work is never tried.
func TestPoolResearchBurstsIntoWholePoolWhenWorkQueueEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindOf(dispatchkind.Work):     {{Exit: 2}},
			KindOf(dispatchkind.Research): {{Exit: 0}},
		},
	}
	gate := &kindGate{
		r:        r,
		limit:    3000, // safety cap: research never gates, so only the goal below stops the loop
		cancelFn: cancel,
		// Goal: research has run in both slots (the mirrored burst this test pins).
		cancelWhen: func(r *scriptedRunner) bool {
			seen := map[int]bool{}
			for _, c := range r.calls() {
				if c.Kind == KindOf(dispatchkind.Research) {
					seen[c.Slot] = true
				}
			}
			return seen[0] && seen[1]
		},
	}
	workChecked := make(chan struct{})
	var workOnce, researchOnce sync.Once
	r.onStart = func(ctx context.Context, req ChildRequest) error {
		switch req.Kind {
		case KindOf(dispatchkind.Work):
			workOnce.Do(func() { close(workChecked) })
		case KindOf(dispatchkind.Research):
			researchOnce.Do(func() {
				// A claim passes the discovery baton on, so the sibling is not
				// parked behind this held child.
				req.OnRecord(Record{Event: report.EventBox, Key: dispatchkey.Issue("held")})
				select {
				case <-workChecked:
				case <-ctx.Done():
				}
			})
		}
		return gate.onStart(ctx, req)
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(2, 1), r, em, clk)
	gate.requireGoalMet(t)

	if got := r.kindCount(KindOf(dispatchkind.Work)); got != 1 {
		t.Fatalf("dispatch calls = %d, want exactly 1: its first empty result should gate it for the rest of the run", got)
	}
	if seen := slotsSeen(r.calls(), KindOf(dispatchkind.Research)); !seen[0] || !seen[1] {
		t.Fatalf("research slots seen = %v, want both slot 0 and slot 1 (work emptying should burst research into the whole pool)", seen)
	}
}

// TestPoolZeroReservationIsWorkFirstWithResearchOnLeftovers pins
// Reservation: 0's contract at a single slot (deterministic: no
// sibling to race against): every dispatch while work has queued work, and
// research only once work has gated itself empty.
func TestPoolZeroReservationIsWorkFirstWithResearchOnLeftovers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindOf(dispatchkind.Work):     {{Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 2}},
			KindOf(dispatchkind.Research): {{Exit: 0}},
		},
	}
	gate := &kindGate{
		r:        r,
		limit:    100, // safety cap: single slot makes this deterministic; a real regression would still hang without one
		cancelFn: cancel,
		// Goal: research (which never gates) has run 5 times, i.e. exactly
		// filling out the 10-call trace this test asserts on.
		cancelWhen: func(r *scriptedRunner) bool {
			return r.kindCount(KindOf(dispatchkind.Research)) >= 5
		},
	}
	r.onStart = gate.onStart
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(1, 0), r, em, clk)
	gate.requireGoalMet(t)

	calls := r.calls()
	if len(calls) != 10 {
		t.Fatalf("run calls = %d, want 10 (the test's own limit)", len(calls))
	}
	for i := 0; i < 5; i++ {
		if calls[i].Kind != KindOf(dispatchkind.Work) {
			t.Fatalf("calls[%d] = %q, want dispatch (work has queued work through its 5th check)", i, calls[i].Kind)
		}
	}
	for i := 5; i < 10; i++ {
		if calls[i].Kind != KindOf(dispatchkind.Research) {
			t.Fatalf("calls[%d] = %q, want research (only reached once work gated itself empty)", i, calls[i].Kind)
		}
	}
}

// TestPoolReservationEqualsSlotsIsResearchFirst mirrors
// TestPoolZeroReservationIsWorkFirstWithResearchOnLeftovers across every
// slot, proving the reservation applies pool-wide.
func TestPoolReservationEqualsSlotsIsResearchFirst(t *testing.T) {
	const slots = 2
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindOf(dispatchkind.Research): {{Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 2}},
			KindOf(dispatchkind.Work):     {{Exit: 0}},
		},
	}
	gate := &kindGate{
		r:        r,
		limit:    3000, // safety cap: dispatch never gates, so only the goal below stops the loop
		cancelFn: cancel,
		// Goal: dispatch has run in both slots — reachable only once research's
		// queue has drained and gated the whole pool, so by construction research
		// will already have run at least 3 times (its own queued results) by then.
		cancelWhen: func(r *scriptedRunner) bool {
			seen := map[int]bool{}
			for _, c := range r.calls() {
				if c.Kind == KindOf(dispatchkind.Work) {
					seen[c.Slot] = true
				}
			}
			return seen[0] && seen[1]
		},
	}
	r.onStart = gate.onStart
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(slots, slots), r, em, clk)
	gate.requireGoalMet(t)

	if got := r.kindCount(KindOf(dispatchkind.Research)); got < 3 {
		t.Fatalf("research calls = %d, want at least 3: research's queued work must run before any burst to work", got)
	}
	if seen := slotsSeen(r.calls(), KindOf(dispatchkind.Work)); !seen[0] || !seen[1] {
		t.Fatalf("dispatch slots seen = %v, want both slot 0 and slot 1 once research's queue empties", seen)
	}
}

// TestLoopEmptyWorkQueueDoesNotSlowResearchDown pins the independence of
// the two kinds' gates: with work permanently empty and research
// dispatching, the slot never sleeps out work's backoff.
func TestLoopEmptyWorkQueueDoesNotSlowResearchDown(t *testing.T) {
	// gateLimit counts total calls across both kinds (see kindGate). Dispatch
	// gates itself after exactly one call, leaving research the remaining
	// gateLimit-1 calls before the shared cap cancels the run.
	const gateLimit = 30
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindOf(dispatchkind.Work):     {{Exit: 2}},
			KindOf(dispatchkind.Research): {{Exit: 0}},
		},
	}
	gate := &kindGate{r: r, limit: gateLimit, cancelFn: cancel}
	r.onStart = gate.onStart
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(1, 0), r, em, clk)

	if got := r.kindCount(KindOf(dispatchkind.Work)); got != 1 {
		t.Fatalf("dispatch calls = %d, want exactly 1: it should gate itself once and never be retried against a clock that never advances", got)
	}
	if got := r.kindCount(KindOf(dispatchkind.Research)); got != gateLimit-1 {
		t.Fatalf("research calls = %d, want %d: it must keep being dispatched every remaining iteration", got, gateLimit-1)
	}
	if clk.waitCount() != 0 {
		t.Fatalf("waits = %v, want none: an empty work queue must never make the slot sleep while research keeps dispatching", clk.waits())
	}
}

// TestLoopIdlesOnlyOnceBothKindsHaveBackedOff pins the other half of the
// independence contract: with a single slot and both kinds permanently
// empty, the slot must try each kind once before it ever sleeps — it does
// not idle on the first kind's empty result alone.
func TestLoopIdlesOnlyOnceBothKindsHaveBackedOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clk := &testClock{}
	// The first Sleep is the "every kind has backed off" moment to stop on.
	var once sync.Once
	clk.onSleep = func() { once.Do(cancel) }
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindOf(dispatchkind.Work):     {{Exit: 2}},
			KindOf(dispatchkind.Research): {{Exit: 2}},
		},
	}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, dualKindConfig(1, 0), r, em, clk).String()
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want context-cancelled (the test's own stop, fired by the first Sleep)", reason)
	}

	calls := r.calls()
	if len(calls) != 2 {
		t.Fatalf("run calls = %v, want exactly 2: one check per kind before the pool idles", calls)
	}
	if calls[0].Kind != KindOf(dispatchkind.Work) || calls[1].Kind != KindOf(dispatchkind.Research) {
		t.Fatalf("run calls = %v, want [dispatch research] (work-first order at Reservation 0)", calls)
	}
	if clk.waitCount() != 1 || clk.waits()[0] != testIdleFloor {
		t.Fatalf("waits = %v, want exactly one wait of %v, taken only once both kinds had gated", clk.waits(), testIdleFloor)
	}
}

// TestPoolPerKindBackoffGrowsIndependently drives markNoWork and resetKind
// directly per kind, to isolate one kind's streak from end-to-end
// interleaving: dispatch's wait doubles across its empty results while
// research resetting in between never perturbs it. Assertions are on the
// published nextCheck.
func TestPoolPerKindBackoffGrowsIndependently(t *testing.T) {
	clk := &testClock{}
	clk.setNow(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	cfg := dualKindConfig(1, 0)
	// Second-scale waits: the published nextCheck is RFC3339, so a
	// millisecond floor would be invisible there.
	cfg.IdleFloor, cfg.IdleCap = time.Second, time.Hour
	dir, sw := statusDir(t)
	cfg.Status = sw
	p, _ := newPool(context.Background(), cfg, nil, em, clk)
	work, research := KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)
	rfc := func(d time.Duration) string { return clk.Now().Add(d).UTC().Format(time.RFC3339) }

	wantWork := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	for i, want := range wantWork {
		p.markNoWork(work, clk.Now(), false)
		if got := kindCheck(t, readStatus(t, dir), work).NextCheck; got != rfc(want) {
			t.Fatalf("dispatch nextCheck after check %d = %q, want %q", i, got, rfc(want))
		}
		// Research dispatching (Continue -> reset) between each of dispatch's
		// empty checks must never touch dispatch's own streak.
		p.resetKind(research)
		st := readStatus(t, dir)
		if got := kindCheck(t, st, research).NextCheck; got != "" {
			t.Fatalf("research nextCheck = %q after reset, want none", got)
		}
		if got := kindCheck(t, st, work).NextCheck; got != rfc(want) {
			t.Fatalf("dispatch nextCheck after research reset %d = %q, want %q kept", i, got, rfc(want))
		}
	}
}

// TestLoopSingleKindConfigurationsNeverRunTheOtherKind pins issue #3541: a
// work-only pool never produces a research child, whatever Reservation is,
// and a research-only pool never produces a dispatch child.
func TestLoopSingleKindConfigurationsNeverRunTheOtherKind(t *testing.T) {
	t.Run("dispatch-only", func(t *testing.T) {
		cfg := testConfig(1)
		cfg.Reservation = 1 // nonsensical if it mattered; must be inert with one kind
		r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
		clk := &testClock{}
		var buf bytes.Buffer
		em := newTestEmitter(&buf)

		Loop(context.Background(), cfg, r, em, clk)

		for _, c := range r.calls() {
			if c.Kind != KindOf(dispatchkind.Work) {
				t.Fatalf("run calls = %v, want every call to be dispatch", r.calls())
			}
		}
	})

	t.Run("research-only", func(t *testing.T) {
		cfg := testConfig(1)
		cfg.Kinds = []Kind{KindOf(dispatchkind.Research)}
		cfg.Reservation = 0
		r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
		clk := &testClock{}
		var buf bytes.Buffer
		em := newTestEmitter(&buf)

		Loop(context.Background(), cfg, r, em, clk)

		for _, c := range r.calls() {
			if c.Kind != KindOf(dispatchkind.Research) {
				t.Fatalf("run calls = %v, want every call to be research", r.calls())
			}
		}
	})
}

// TestLoopEventsCarryKindOnEveryTransition pins the event-stream contract:
// child_start/child_finish/idle every carry the Kind of the child that
// produced them, and the two kinds' events genuinely interleave in one
// stream rather than one kind's events all preceding the other's.
func TestLoopEventsCarryKindOnEveryTransition(t *testing.T) {
	const gateLimit = 6 // safety cap; no assertion below derives a count from it
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindOf(dispatchkind.Work):     {{Exit: 0}, {Exit: 2}},
			KindOf(dispatchkind.Research): {{Exit: 0}},
		},
	}
	gate := &kindGate{r: r, limit: gateLimit, cancelFn: cancel}
	r.onStart = gate.onStart
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(1, 0), r, em, clk)

	events := decodeEvents(t, &buf)
	var sawDispatchIdle, sawResearchStart, sawDispatchStart bool
	var kindsInOrder []Kind
	for _, ev := range events {
		switch ev.Event {
		case "child_start", "child_finish":
			if ev.Kind == "" {
				t.Fatalf("%s event missing kind: %+v", ev.Event, ev)
			}
			kindsInOrder = append(kindsInOrder, ev.Kind)
			if ev.Event == "child_start" && ev.Kind == KindOf(dispatchkind.Research) {
				sawResearchStart = true
			}
			if ev.Event == "child_start" && ev.Kind == KindOf(dispatchkind.Work) {
				sawDispatchStart = true
			}
		case "idle":
			if ev.Kind == "" {
				t.Fatalf("idle event missing kind: %+v", ev)
			}
			if ev.Kind == KindOf(dispatchkind.Work) {
				sawDispatchIdle = true
			}
		}
	}
	if !sawDispatchStart || !sawResearchStart {
		t.Fatalf("kinds seen on child_start/child_finish = %v, want both dispatch and research", kindsInOrder)
	}
	if !sawDispatchIdle {
		t.Fatalf("no idle event carried kind %q (its own backoff transition)", KindOf(dispatchkind.Work))
	}

	// Genuine interleaving, not "every dispatch event then every research
	// event": somewhere in the stream a research kind directly follows a
	// dispatch kind or vice versa.
	interleaved := false
	for i := 1; i < len(kindsInOrder); i++ {
		if kindsInOrder[i] != kindsInOrder[i-1] {
			interleaved = true
			break
		}
	}
	if !interleaved {
		t.Fatalf("kinds in order = %v, want the two kinds interleaved rather than one wholly preceding the other", kindsInOrder)
	}
}

// fatalRecorder is a fataler double that records a Fatalf call instead of
// aborting the goroutine, so TestKindGateRequireGoalMet can observe both of
// requireGoalMet's outcomes without one of them actually failing the suite.
type fatalRecorder struct {
	called bool
}

func (f *fatalRecorder) Helper() {}

func (f *fatalRecorder) Fatalf(format string, args ...any) {
	f.called = true
}

// runNTimes drives r through n StartChild calls on slot 0, the gate's onStart
// already installed as r.onStart.
func runNTimes(t *testing.T, r *scriptedRunner, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := r.run(context.Background(), ChildRequest{Kind: KindOf(dispatchkind.Research), Slot: 0}); err != nil {
			t.Fatalf("StartChild: %v", err)
		}
	}
}

// TestKindGateRequireGoalMet pins the two-mode contract the fix for issue
// #3620 restores: with cancelWhen nil, limit is the run's own intended
// stop, so running a gate to its limit must not read as a failed goal; with
// cancelWhen set and never satisfied, hitting limit is exactly the
// regression requireGoalMet exists to catch.
func TestKindGateRequireGoalMet(t *testing.T) {
	t.Run("cancelWhen nil: reaching limit is not a failure", func(t *testing.T) {
		r := &scriptedRunner{byKind: map[Kind][]ChildResult{KindOf(dispatchkind.Research): {{Exit: 0}}}}
		gate := &kindGate{r: r, limit: 3}
		r.onStart = gate.onStart
		runNTimes(t, r, 3)

		rec := &fatalRecorder{}
		gate.requireGoalMet(rec)
		if rec.called {
			t.Fatalf("requireGoalMet fatal'd with cancelWhen nil and limit reached — limit was the run's own intended stop")
		}
	})

	t.Run("cancelWhen set and never satisfied: requireGoalMet fails", func(t *testing.T) {
		r := &scriptedRunner{byKind: map[Kind][]ChildResult{KindOf(dispatchkind.Research): {{Exit: 0}}}}
		gate := &kindGate{r: r, limit: 3, cancelWhen: func(r *scriptedRunner) bool { return false }}
		r.onStart = gate.onStart
		runNTimes(t, r, 3)

		rec := &fatalRecorder{}
		gate.requireGoalMet(rec)
		if !rec.called {
			t.Fatalf("requireGoalMet did not fatal with cancelWhen set and never satisfied — the cap fired before the goal, which is exactly the regression it must catch")
		}
	})
}

// TestPoolResearchFloorRecoversWhileReservedSlotHoldsLongWork pins the
// reservation as a live floor (issue #3582): research gates on its first
// check, slot 0 takes a long-running work child, and once research's gate
// lapses a sibling's turnover must pick research — a per-slot static
// preference would wait for slot 0 to free up.
func TestPoolResearchFloorRecoversWhileReservedSlotHoldsLongWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	work, research := KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			work: {{Exit: 0}},
			// Always empty: an Exit 0 would clear research's gate before the clock
			// advances, and the idle-instant children would keep re-picking it.
			research: {{Exit: 2}},
		},
	}
	slot0Held := make(chan struct{})
	researchSlots := make(chan int, 64)
	var heldOnce sync.Once
	var lapsed atomic.Bool
	r.onStart = func(ctx context.Context, req ChildRequest) error {
		switch {
		case req.Kind == work && req.Slot == 0:
			// A claim passes the discovery baton on, so the siblings are not
			// parked behind this held child.
			req.OnRecord(Record{Event: report.EventBox, Key: dispatchkey.Issue("held")})
			heldOnce.Do(func() { close(slot0Held) })
			<-ctx.Done()
		case req.Kind == research && lapsed.Load():
			select {
			case researchSlots <- req.Slot:
			default:
			}
		}
		return nil
	}
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	done := make(chan Halt, 1)
	go func() { done <- Loop(ctx, dualKindConfig(3, 1), r, em, clk) }()

	select {
	case <-slot0Held:
	case <-time.After(5 * time.Second):
		t.Fatalf("slot 0 never started a work child")
	}

	lapsed.Store(true)
	clk.advanceBy(time.Hour)

	select {
	case slot := <-researchSlots:
		if slot == 0 {
			t.Fatalf("research ran on slot 0, which is still held by a work child")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no sibling slot picked research after its gate lapsed while slot 0 held a work child: the reservation floor was not re-evaluated")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Loop did not return after cancel")
	}
}
