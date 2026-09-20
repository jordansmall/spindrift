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

// fakeKindRunner is fakeRunner's dual-kind sibling: fakeRunner scripts one
// shared RunChild sequence, which cannot model two independently-emptying
// queues. resultsByKind gives each Kind its own sequence, consumed in call
// order with the last value repeating once exhausted (fakeRunner's own
// exhaustion rule, per kind instead of pool-wide). Mutex-guarded: these
// tests deliberately run several slots concurrently against one fake.
type fakeKindRunner struct {
	revision      string
	resultsByKind map[Kind][]ChildResult

	// limit, if non-zero, is a safety cap on the total RunChild count (every
	// kind combined): far above whatever cancelWhen (or, absent that, limit
	// itself) needs to reach its goal, so a genuine regression fails in
	// bounded time as a clear "safety cap exhausted" fatal rather than
	// hanging the test forever.
	limit int
	// cancelWhen, if set, is evaluated under the fake's own lock right after
	// every call is recorded; the first time it returns true, RunChild fires
	// cancelFn. This is deliberately the condition each test actually
	// asserts on (e.g. "every slot has run"), not a call count: with several
	// slot goroutines calling RunChild concurrently, a shared count races
	// the Go scheduler (one fast slot can burn the whole budget before a
	// sibling is ever scheduled), so the stop has to be the asserted state
	// itself, which the loop is guaranteed to have reached by construction.
	cancelWhen func(f *fakeKindRunner) bool
	cancelFn   context.CancelFunc

	mu           sync.Mutex
	callsByKind  map[Kind]int
	runCalls     []runCall
	capExhausted bool // limit fired before cancelWhen's goal was ever met
}

func (f *fakeKindRunner) ResolveRevision(ctx context.Context) (string, error) {
	return f.revision, nil
}

// SelfPath is never exercised here: these tests leave Config.SelfProgram
// empty, so the self-build check short-circuits before reaching the seam.
func (f *fakeKindRunner) SelfPath(ctx context.Context, revision string) (string, error) {
	return "", nil
}

func (f *fakeKindRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	f.mu.Lock()
	if f.callsByKind == nil {
		f.callsByKind = make(map[Kind]int)
	}
	idx := f.callsByKind[req.Kind]
	list := f.resultsByKind[req.Kind]
	if idx >= len(list) {
		idx = len(list) - 1
	}
	result := list[idx]
	f.callsByKind[req.Kind]++
	f.runCalls = append(f.runCalls, runCall{Kind: req.Kind, Revision: req.Revision, Slot: req.Slot})
	total := len(f.runCalls)

	goalMet := f.cancelWhen != nil && f.cancelWhen(f)
	shouldCancel := goalMet
	if !goalMet && f.limit > 0 && total >= f.limit {
		f.capExhausted = true
		shouldCancel = true
	}
	f.mu.Unlock()

	if shouldCancel && f.cancelFn != nil {
		f.cancelFn()
	}
	return result, nil
}

// requireGoalMet fails the test if the safety cap fired before cancelWhen's
// condition was ever satisfied. That distinguishes "the loop reached the
// state the test wanted, and stopped there" from "a regression means it
// never did, and only the cap kept the test from hanging" — without this, a
// capped-out run can look identical to a successful one to whatever the test
// asserts afterward.
func (f *fakeKindRunner) requireGoalMet(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.capExhausted {
		t.Fatalf("fakeKindRunner: safety cap (%d calls) fired before cancelWhen's goal was ever met — likely a regression, not scheduler flake", f.limit)
	}
}

func (f *fakeKindRunner) callCount(kind Kind) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callsByKind[kind]
}

func (f *fakeKindRunner) calls() []runCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]runCall, len(f.runCalls))
	copy(out, f.runCalls)
	return out
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

// cancelOnSleepClock wraps fakeClock and fires cancelFn the first time
// Sleep is called. A slot only ever calls Sleep from idleSleep once every
// configured kind has backed off (pickKind found nothing runnable) — that
// is exactly the "pool has genuinely idled" moment test 8 needs to stop on.
type cancelOnSleepClock struct {
	*fakeClock
	cancelFn context.CancelFunc
	once     sync.Once
}

func (c *cancelOnSleepClock) Sleep(ctx context.Context, d time.Duration) {
	c.fakeClock.Sleep(ctx, d)
	c.once.Do(c.cancelFn)
}

// TestPoolBothKindsShareOneSlotCapAcrossThePool pins issue #3541's central
// pool-sizing property: with both kinds configured, Slots still bounds the
// total number of children in flight, not each kind separately (which would
// silently double an operator's MEMORY_LIMIT x MAX_PARALLEL sizing).
func TestPoolBothKindsShareOneSlotCapAcrossThePool(t *testing.T) {
	const slots = 3
	r := newBlockingRunner("rev1", slots)
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	cfg := dualKindConfig(slots, 1)

	done := make(chan string, 1)
	go func() {
		done <- Loop(context.Background(), cfg, r, em, clk)
	}()

	seen := map[int]bool{}
	for i := 0; i < slots; i++ {
		seen[<-r.started] = true
	}
	if len(seen) != slots {
		t.Fatalf("distinct slots started = %v, want %d distinct slots", seen, slots)
	}
	if peak := r.peakConcurrency(); peak != slots {
		t.Fatalf("peak concurrency = %d, want %d: both kinds sharing one pool must still cap at Slots", peak, slots)
	}

	for s := 0; s < slots; s++ {
		r.release[s] <- ChildResult{Exit: 7}
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
	r := &fakeKindRunner{
		revision: "rev1",
		resultsByKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 0}},
			KindResearch: {{Exit: 0}},
		},
		limit: 5000, // safety cap: neither kind ever gates, so only the goal below stops the loop
	}
	r.cancelFn = cancel
	// Goal: every slot has run at least once. A shared call-count budget
	// would race the scheduler across the 3 slot goroutines; this instead
	// stops exactly when the state under test has been reached.
	r.cancelWhen = func(f *fakeKindRunner) bool {
		seen := map[int]bool{}
		for _, c := range f.runCalls {
			seen[c.Slot] = true
		}
		return len(seen) >= slots
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, dualKindConfig(slots, 1), r, em, clk)
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want context-cancelled (the test's own stop)", reason)
	}
	r.requireGoalMet(t)

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
	r := &fakeKindRunner{
		revision: "rev1",
		resultsByKind: map[Kind][]ChildResult{
			KindResearch: {{Exit: 2}},
			KindDispatch: {{Exit: 0}},
		},
		limit: 3000, // safety cap: dispatch never gates, so only the goal below stops the loop
	}
	r.cancelFn = cancel
	// Goal: dispatch has run in both slots (the burst this test pins).
	// Research gates itself for good on its first call regardless of when
	// this fires, so stopping here rather than on a shared call count still
	// leaves the "research called exactly once" assertion below meaningful.
	r.cancelWhen = func(f *fakeKindRunner) bool {
		seen := map[int]bool{}
		for _, c := range f.runCalls {
			if c.Kind == KindDispatch {
				seen[c.Slot] = true
			}
		}
		return seen[0] && seen[1]
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(2, 1), r, em, clk)
	r.requireGoalMet(t)

	// Only slot 0 (the reserved one) ever prefers research, and its single
	// empty result gates it for good (the fake clock never advances here),
	// so research must be tried exactly once.
	if got := r.callCount(KindResearch); got != 1 {
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
	r := &fakeKindRunner{
		revision: "rev1",
		resultsByKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 2}},
			KindResearch: {{Exit: 0}},
		},
		limit: 3000, // safety cap: research never gates, so only the goal below stops the loop
	}
	r.cancelFn = cancel
	// Goal: research has run in both slots (the mirrored burst this test pins).
	r.cancelWhen = func(f *fakeKindRunner) bool {
		seen := map[int]bool{}
		for _, c := range f.runCalls {
			if c.Kind == KindResearch {
				seen[c.Slot] = true
			}
		}
		return seen[0] && seen[1]
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(2, 1), r, em, clk)
	r.requireGoalMet(t)

	if got := r.callCount(KindDispatch); got != 1 {
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
	r := &fakeKindRunner{
		revision: "rev1",
		resultsByKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 2}},
			KindResearch: {{Exit: 0}},
		},
		limit: 100, // safety cap: single slot makes this deterministic; a real regression would still hang without one
	}
	r.cancelFn = cancel
	// Goal: research (which never gates) has run 5 times, i.e. exactly
	// filling out the 10-call trace this test asserts on.
	r.cancelWhen = func(f *fakeKindRunner) bool {
		return f.callsByKind[KindResearch] >= 5
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(1, 0), r, em, clk)
	r.requireGoalMet(t)

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
	r := &fakeKindRunner{
		revision: "rev1",
		resultsByKind: map[Kind][]ChildResult{
			KindResearch: {{Exit: 0}, {Exit: 0}, {Exit: 0}, {Exit: 2}},
			KindDispatch: {{Exit: 0}},
		},
		limit: 3000, // safety cap: dispatch never gates, so only the goal below stops the loop
	}
	r.cancelFn = cancel
	// Goal: dispatch has run in both slots — reachable only once research's
	// queue has drained and gated the whole pool, so by construction research
	// will already have run at least 3 times (its own queued results) by then.
	r.cancelWhen = func(f *fakeKindRunner) bool {
		seen := map[int]bool{}
		for _, c := range f.runCalls {
			if c.Kind == KindDispatch {
				seen[c.Slot] = true
			}
		}
		return seen[0] && seen[1]
	}
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(slots, slots), r, em, clk)
	r.requireGoalMet(t)

	if got := r.callCount(KindResearch); got < 3 {
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
// backoff — clk.waits must stay empty the whole run.
func TestLoopEmptyWorkQueueDoesNotSlowResearchDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &fakeKindRunner{
		revision: "rev1",
		resultsByKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 2}},
			KindResearch: {{Exit: 0}},
		},
		limit: 30,
	}
	r.cancelFn = cancel
	clk := &fakeClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	Loop(ctx, dualKindConfig(1, 0), r, em, clk)

	if got := r.callCount(KindDispatch); got != 1 {
		t.Fatalf("dispatch calls = %d, want exactly 1: it should gate itself once and never be retried against a clock that never advances", got)
	}
	if got := r.callCount(KindResearch); got != 29 {
		t.Fatalf("research calls = %d, want 29: it must keep being dispatched every remaining iteration", got)
	}
	if len(clk.waits) != 0 {
		t.Fatalf("waits = %v, want none: an empty work queue must never make the slot sleep while research keeps dispatching", clk.waits)
	}
}

// TestLoopIdlesOnlyOnceBothKindsHaveBackedOff pins the other half of the
// independence contract: with a single slot and both kinds permanently
// empty, the slot must try each kind once before it ever sleeps — it does
// not idle on the first kind's empty result alone.
func TestLoopIdlesOnlyOnceBothKindsHaveBackedOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	baseClk := &fakeClock{}
	clk := &cancelOnSleepClock{fakeClock: baseClk, cancelFn: cancel}
	r := &fakeKindRunner{
		revision: "rev1",
		resultsByKind: map[Kind][]ChildResult{
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
	if len(baseClk.waits) != 1 || baseClk.waits[0] != testIdleFloor {
		t.Fatalf("waits = %v, want exactly one wait of %v, taken only once both kinds had gated", baseClk.waits, testIdleFloor)
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
	clk := &fakeClock{}
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
		if until, gated := p.kinds[KindResearch].readyAt(); gated || !until.IsZero() {
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
		r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
		clk := &fakeClock{}
		var buf bytes.Buffer
		em := newTestEmitter(&buf)

		Loop(context.Background(), cfg, r, em, clk)

		for _, c := range r.runCalls {
			if c.Kind != KindDispatch {
				t.Fatalf("run calls = %v, want every call to be dispatch", r.runCalls)
			}
		}
	})

	t.Run("research-only", func(t *testing.T) {
		cfg := testConfig(1)
		cfg.Kinds = []Kind{KindResearch}
		cfg.ResearchReservation = 0
		r := &fakeRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 0}, {Exit: 5}}}
		clk := &fakeClock{}
		var buf bytes.Buffer
		em := newTestEmitter(&buf)

		Loop(context.Background(), cfg, r, em, clk)

		for _, c := range r.runCalls {
			if c.Kind != KindResearch {
				t.Fatalf("run calls = %v, want every call to be research", r.runCalls)
			}
		}
	})
}

// TestLoopEventsCarryKindOnEveryTransition pins the event-stream contract:
// child_start/child_finish/idle every carry the Kind of the child that
// produced them, and the two kinds' events genuinely interleave in one
// stream rather than one kind's events all preceding the other's.
func TestLoopEventsCarryKindOnEveryTransition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &fakeKindRunner{
		revision: "rev1",
		resultsByKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 0}, {Exit: 2}},
			KindResearch: {{Exit: 0}},
		},
		limit: 6,
	}
	r.cancelFn = cancel
	clk := &fakeClock{}
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
