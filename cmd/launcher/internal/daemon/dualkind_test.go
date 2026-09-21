package daemon

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// dualKindConfig is testConfig's issue #3541 sibling: both kinds, plus the
// reservation knob these tests exist to exercise. Slot count and the
// reservation are the two things each test below actually varies.
func dualKindConfig(slots, reservation int) Config {
	cfg := testConfig(slots)
	cfg.Kinds = []Kind{KindDispatch, KindResearch}
	cfg.ResearchReservation = reservation
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

// kindGate stops a dual-kind Loop run through scriptedRunner's onStart
// hook rather than being grown onto the seam double itself — this is
// dualkind_test.go's own scheduling-stop concern, not
// something every scriptedRunner user needs. limit is the total RunChild
// count across every kind at which onStart fires cancelFn. With cancelWhen
// set, limit is a safety cap set far above whatever the goal below needs,
// so a genuine regression fails in bounded time as a clear "safety cap
// exhausted" fatal rather than hanging the test forever, and
// requireGoalMet is the check that the goal — not the cap — is what
// actually stopped the run. With cancelWhen nil there is no goal to check:
// limit IS the run's intended stop, and requireGoalMet is a no-op.
// cancelWhen, when set, is evaluated right after every call is recorded
// (via r's own calls()/kindCount() accessors, never r's internal lock); the
// first time it returns true, onStart fires cancelFn. This is deliberately
// the condition each test actually asserts on (e.g. "every slot has run"),
// not a call count: with several slot goroutines calling RunChild
// concurrently, a shared count races the Go scheduler (one fast slot can
// burn the whole budget before a sibling is ever scheduled), so the stop
// has to be the asserted state itself, which the loop is guaranteed to
// have reached by construction.
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
		// capExhausted only means something when there was a goal to miss;
		// with cancelWhen nil, limit is the intended stop, not a cap that
		// fired early.
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

// fataler is the sliver of *testing.T requireGoalMet needs — small enough
// that a self-test can pass a recording double instead, to observe a
// requireGoalMet failure without actually failing the suite.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

// requireGoalMet fails the test if the safety cap fired before cancelWhen's
// condition was ever satisfied. That distinguishes "the loop reached the
// state the test wanted, and stopped there" from "a regression means it
// never did, and only the cap kept the test from hanging" — without this, a
// capped-out run can look identical to a successful one to whatever the test
// asserts afterward. With cancelWhen nil, capExhausted is never set (see
// onStart), so this is a no-op: limit was the run's intended stop, not a
// cap that fired early.
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
	// onStart restores this test's pre-gate concurrent first wave (issue
	// #3634).
	r.announceEachSlot()
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := dualKindConfig(slots, 1)

	done := make(chan string, 1)
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
		r.releaseSlot(t, s, ChildResult{Exit: 7})
	}
	reason := <-done
	if !strings.Contains(reason, "signalled-stop") {
		t.Fatalf("halt reason = %q, want it to name signalled-stop", reason)
	}
}

// TestPoolReservedSlotPrefersResearchWhileResearchHasWork pins
// ResearchReservation as a per-slot floor: with Slots: 3, ResearchReservation:
// 1, slot 0 always fills itself with research and slots 1/2 always fill
// themselves with work, so long as both queues keep dispatching.
func TestPoolReservedSlotPrefersResearchWhileResearchHasWork(t *testing.T) {
	const slots = 3
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 0}},
			KindResearch: {{Exit: 0}},
		},
	}
	gate := &kindGate{
		r:        r,
		limit:    5000, // safety cap: neither kind ever gates, so only the goal below stops the loop
		cancelFn: cancel,
		// Goal: every slot has run at least once. A shared call-count budget
		// would race the scheduler across the 3 slot goroutines; this instead
		// stops exactly when the state under test has been reached.
		cancelWhen: func(r *scriptedRunner) bool {
			seen := map[int]bool{}
			for _, c := range r.calls() {
				seen[c.Slot] = true
			}
			return len(seen) >= slots
		},
	}
	r.onStart = gate.onStart
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, dualKindConfig(slots, 1), r, em, clk)
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want context-cancelled (the test's own stop)", reason)
	}
	gate.requireGoalMet(t)

	calls := r.calls()
	if len(calls) == 0 {
		t.Fatalf("no run calls recorded")
	}
	perSlot := map[int]int{}
	for _, c := range calls {
		perSlot[c.Slot]++
		switch c.Slot {
		case 0:
			if c.Kind != KindResearch {
				t.Errorf("slot 0 ran kind %q, want every slot-0 child to be research", c.Kind)
			}
		case 1, 2:
			if c.Kind != KindDispatch {
				t.Errorf("slot %d ran kind %q, want every slot-1/2 child to be dispatch", c.Slot, c.Kind)
			}
		default:
			t.Errorf("unexpected slot %d", c.Slot)
		}
	}
	for s := 0; s < 3; s++ {
		if perSlot[s] == 0 {
			t.Errorf("slot %d never ran a child", s)
		}
	}
}

// TestPoolWorkBurstsIntoWholePoolWhenResearchQueueEmpty pins the burst
// contract: a reservation is a floor, not a ceiling. Slots: 2,
// ResearchReservation: 1; research empties on its first check (and, with a
// frozen fake clock, never becomes runnable again), so both slots settle
// into running work.
func TestPoolWorkBurstsIntoWholePoolWhenResearchQueueEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindResearch: {{Exit: 2}},
			KindDispatch: {{Exit: 0}},
		},
	}
	gate := &kindGate{
		r:        r,
		limit:    3000, // safety cap: dispatch never gates, so only the goal below stops the loop
		cancelFn: cancel,
		// Goal: dispatch has run in both slots (the burst this test pins).
		// Research gates itself for good on its first call regardless of when
		// this fires, so stopping here rather than on a shared call count still
		// leaves the "research called exactly once" assertion below meaningful.
		cancelWhen: func(r *scriptedRunner) bool {
			seen := map[int]bool{}
			for _, c := range r.calls() {
				if c.Kind == KindDispatch {
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

	// Only slot 0 (the reserved one) ever prefers research, and its single
	// empty result gates it for good (the fake clock never advances here),
	// so research must be tried exactly once.
	if got := r.kindCount(KindResearch); got != 1 {
		t.Fatalf("research calls = %d, want exactly 1: its first empty result should gate it for the rest of the run", got)
	}
	if seen := slotsSeen(r.calls(), KindDispatch); !seen[0] || !seen[1] {
		t.Fatalf("dispatch slots seen = %v, want both slot 0 and slot 1 (research emptying should burst work into the whole pool)", seen)
	}
}

// TestPoolResearchBurstsIntoWholePoolWhenWorkQueueEmpty mirrors
// TestPoolWorkBurstsIntoWholePoolWhenResearchQueueEmpty: work empties on its
// first (and only) check, so research ends up running in every slot.
func TestPoolResearchBurstsIntoWholePoolWhenWorkQueueEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 2}},
			KindResearch: {{Exit: 0}},
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
				if c.Kind == KindResearch {
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

	if got := r.kindCount(KindDispatch); got != 1 {
		t.Fatalf("dispatch calls = %d, want exactly 1: its first empty result should gate it for the rest of the run", got)
	}
	if seen := slotsSeen(r.calls(), KindResearch); !seen[0] || !seen[1] {
		t.Fatalf("research slots seen = %v, want both slot 0 and slot 1 (work emptying should burst research into the whole pool)", seen)
	}
}

// TestPoolZeroReservationIsWorkFirstWithResearchOnLeftovers pins
// ResearchReservation: 0's contract at a single slot (deterministic: no
// sibling to race against): every dispatch while work has queued work, and
// research only once work has gated itself empty.
func TestPoolZeroReservationIsWorkFirstWithResearchOnLeftovers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 2}},
			KindResearch: {{Exit: 0}},
		},
	}
	gate := &kindGate{
		r:        r,
		limit:    100, // safety cap: single slot makes this deterministic; a real regression would still hang without one
		cancelFn: cancel,
		// Goal: research (which never gates) has run 5 times, i.e. exactly
		// filling out the 10-call trace this test asserts on.
		cancelWhen: func(r *scriptedRunner) bool {
			return r.kindCount(KindResearch) >= 5
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
		if calls[i].Kind != KindDispatch {
			t.Fatalf("calls[%d] = %q, want dispatch (work has queued work through its 5th check)", i, calls[i].Kind)
		}
	}
	for i := 5; i < 10; i++ {
		if calls[i].Kind != KindResearch {
			t.Fatalf("calls[%d] = %q, want research (only reached once work gated itself empty)", i, calls[i].Kind)
		}
	}
}

// TestPoolReservationEqualsSlotsIsResearchFirst mirrors
// TestPoolZeroReservationIsWorkFirstWithResearchOnLeftovers, but exercised
// across every slot (ResearchReservation == Slots means every slot, not
// just one, prefers research) to prove the reservation applies pool-wide,
// not to a single hardcoded slot.
func TestPoolReservationEqualsSlotsIsResearchFirst(t *testing.T) {
	const slots = 2
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindResearch: {{Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 2}},
			KindDispatch: {{Exit: 0}},
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
				if c.Kind == KindDispatch {
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

	if got := r.kindCount(KindResearch); got < 3 {
		t.Fatalf("research calls = %d, want at least 3: research's queued work must run before any burst to work", got)
	}
	if seen := slotsSeen(r.calls(), KindDispatch); !seen[0] || !seen[1] {
		t.Fatalf("dispatch slots seen = %v, want both slot 0 and slot 1 once research's queue empties", seen)
	}
}

// TestLoopEmptyWorkQueueDoesNotSlowResearchDown pins the independence of
// the two kinds' gates: work is permanently empty (and, once gated, never
// rechecked against a fake clock that never advances) while research keeps
// dispatching. The slot must never fall back to sleeping out work's
// backoff — clk.waits() must stay empty the whole run.
func TestLoopEmptyWorkQueueDoesNotSlowResearchDown(t *testing.T) {
	// gateLimit counts total calls across both kinds (see kindGate). Dispatch
	// gates itself after exactly one call, leaving research the remaining
	// gateLimit-1 calls before the shared cap cancels the run.
	const gateLimit = 30
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 2}},
			KindResearch: {{Exit: 0}},
		},
	}
	gate := &kindGate{r: r, limit: gateLimit, cancelFn: cancel}
	r.onStart = gate.onStart
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(1, 0), r, em, clk)

	if got := r.kindCount(KindDispatch); got != 1 {
		t.Fatalf("dispatch calls = %d, want exactly 1: it should gate itself once and never be retried against a clock that never advances", got)
	}
	if got := r.kindCount(KindResearch); got != gateLimit-1 {
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
	// onSleep fires cancelFn the first time Sleep is called. A slot only
	// ever calls Sleep from idleSleep once every configured kind has backed
	// off (pickKind found nothing runnable) — that is exactly the "pool has
	// genuinely idled" moment this test needs to stop on.
	var once sync.Once
	clk.onSleep = func() { once.Do(cancel) }
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 2}},
			KindResearch: {{Exit: 2}},
		},
	}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, dualKindConfig(1, 0), r, em, clk)
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want context-cancelled (the test's own stop, fired by the first Sleep)", reason)
	}

	calls := r.calls()
	if len(calls) != 2 {
		t.Fatalf("run calls = %v, want exactly 2: one check per kind before the pool idles", calls)
	}
	if calls[0].Kind != KindDispatch || calls[1].Kind != KindResearch {
		t.Fatalf("run calls = %v, want [dispatch research] (work-first order at ResearchReservation 0)", calls)
	}
	if clk.waitCount() != 1 || clk.waits()[0] != testIdleFloor {
		t.Fatalf("waits = %v, want exactly one wait of %v, taken only once both kinds had gated", clk.waits(), testIdleFloor)
	}
}

// TestPoolPerKindBackoffGrowsIndependently drives pool.kinds directly
// (sanctioned here as the only honest way to isolate one kind's streak from
// the daemon's normal end-to-end interleaving): dispatch's wait must double
// across its own consecutive empty results exactly like a lone idleBackoff
// would, while research resetting in between must never perturb it, and
// vice versa. The assertions are on markNoWork's return — the same value
// runSlot stamps onto the idle event's Wait field — not on any private
// idleBackoff field.
func TestPoolPerKindBackoffGrowsIndependently(t *testing.T) {
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	cfg := dualKindConfig(1, 0)
	p, _ := newPool(context.Background(), cfg, nil, em, clk)

	wantWork := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond}
	for i, want := range wantWork {
		got := p.kinds[KindDispatch].markNoWork(clk.Now(), false)
		if got != want {
			t.Fatalf("dispatch markNoWork()[%d] = %v, want %v", i, got, want)
		}
		// Research dispatching (Continue -> reset) between each of dispatch's
		// empty checks must never touch dispatch's own streak.
		p.kinds[KindResearch].reset()
		if until, gated := p.kinds[KindResearch].readyAt(clk.Now()); gated || !until.IsZero() {
			t.Fatalf("research readyAt() = (%v, %v) after reset, want (zero, false)", until, gated)
		}
	}

	if got := p.kinds[KindDispatch].markNoWork(clk.Now(), false); got != 16*time.Millisecond {
		t.Fatalf("dispatch markNoWork() after 4 prior checks and interleaved research resets = %v, want 16ms (research's timer never advanced dispatch's)", got)
	}
}

// TestLoopSingleKindConfigurationsNeverRunTheOtherKind pins issue #3541's
// backward-compatible edge: Kinds: {KindDispatch} alone must never produce a
// research child, whatever ResearchReservation is set to (it only has
// meaning once a second kind exists), and Kinds: {KindResearch} alone must
// never produce a dispatch child.
func TestLoopSingleKindConfigurationsNeverRunTheOtherKind(t *testing.T) {
	t.Run("dispatch-only", func(t *testing.T) {
		cfg := testConfig(1)
		cfg.ResearchReservation = 1 // nonsensical if it mattered; must be inert with one kind
		r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
		clk := &testClock{}
		var buf bytes.Buffer
		em := newTestEmitter(&buf)

		Loop(context.Background(), cfg, r, em, clk)

		for _, c := range r.calls() {
			if c.Kind != KindDispatch {
				t.Fatalf("run calls = %v, want every call to be dispatch", r.calls())
			}
		}
	})

	t.Run("research-only", func(t *testing.T) {
		cfg := testConfig(1)
		cfg.Kinds = []Kind{KindResearch}
		cfg.ResearchReservation = 0
		r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
		clk := &testClock{}
		var buf bytes.Buffer
		em := newTestEmitter(&buf)

		Loop(context.Background(), cfg, r, em, clk)

		for _, c := range r.calls() {
			if c.Kind != KindResearch {
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
			KindDispatch: {{Exit: 0}, {Exit: 2}},
			KindResearch: {{Exit: 0}},
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
			if ev.Event == "child_start" && ev.Kind == KindResearch {
				sawResearchStart = true
			}
			if ev.Event == "child_start" && ev.Kind == KindDispatch {
				sawDispatchStart = true
			}
		case "idle":
			if ev.Kind == "" {
				t.Fatalf("idle event missing kind: %+v", ev)
			}
			if ev.Kind == KindDispatch {
				sawDispatchIdle = true
			}
		}
	}
	if !sawDispatchStart || !sawResearchStart {
		t.Fatalf("kinds seen on child_start/child_finish = %v, want both dispatch and research", kindsInOrder)
	}
	if !sawDispatchIdle {
		t.Fatalf("no idle event carried kind %q (its own backoff transition)", KindDispatch)
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

// runNTimes drives r through n RunChild calls on slot 0, the gate's onStart
// already installed as r.onStart.
func runNTimes(t *testing.T, r *scriptedRunner, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := r.RunChild(context.Background(), ChildRequest{Kind: KindResearch, Slot: 0}); err != nil {
			t.Fatalf("RunChild: %v", err)
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
		r := &scriptedRunner{byKind: map[Kind][]ChildResult{KindResearch: {{Exit: 0}}}}
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
		r := &scriptedRunner{byKind: map[Kind][]ChildResult{KindResearch: {{Exit: 0}}}}
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
