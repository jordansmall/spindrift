package daemon

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// triKindConfig is dualKindConfig's three-kind sibling (issue #3878): all
// three kinds share one pool, in the daemon's default order (dispatch,
// research, butler — dispatchkind.All's declaration order).
func triKindConfig(slots, reservation int) Config {
	cfg := testConfig(slots)
	cfg.Kinds = []Kind{KindDispatch, KindResearch, KindButler}
	cfg.ResearchReservation = reservation
	return cfg
}

// TestPoolButlerStartsOnlyOnceDispatchAndResearchBothBackOff pins ADR
// 0056's idle-tier gate: a slot picks the butler only once every other
// configured kind has reported no work. Dispatch backing off alone must not
// start it — research still has work to do first — and it starts only once
// research backs off too.
func TestPoolButlerStartsOnlyOnceDispatchAndResearchBothBackOff(t *testing.T) {
	const gateLimit = 20 // safety cap; the real run needs exactly 4 calls
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		byKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 2}},            // permanently no work
			KindResearch: {{Exit: 0}, {Exit: 2}}, // one dispatchable check, then it backs off too
			KindButler:   {{Exit: 0}},
		},
	}
	gate := &kindGate{
		r:        r,
		limit:    gateLimit,
		cancelFn: cancel,
		cancelWhen: func(r *scriptedRunner) bool {
			return r.kindCount(KindButler) >= 1
		},
	}
	r.onStart = gate.onStart
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	reason := Loop(ctx, triKindConfig(1, 0), r, em, clk).String()
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want context-cancelled (the test's own stop, fired once butler ran)", reason)
	}
	gate.requireGoalMet(t)

	// Exactly dispatch, then research twice (one dispatchable, one empty),
	// then butler — the precise single-slot interleaving the idle tier
	// promises: butler is tried dead last, and only once, every check.
	wantKinds := []Kind{KindDispatch, KindResearch, KindResearch, KindButler}
	calls := r.calls()
	if len(calls) != len(wantKinds) {
		t.Fatalf("run calls = %v, want exactly %v", calls, wantKinds)
	}
	for i, want := range wantKinds {
		if calls[i].Kind != want {
			t.Fatalf("run calls = %v, want %v", calls, wantKinds)
		}
	}
}

// TestPoolRunningButlerChildIsNeverPreempted pins the awake-window rule
// (ADR 0056: "gate starting, never stopping") at the pool level: once a
// slot has started a butler child, dispatch or research regaining work
// while that child is still in flight must never cancel it. The pool's own
// derived context (pctx) is the thing that would have to be cancelled to
// interrupt a running child, so this asserts directly on it rather than on
// a timing-sensitive absence of some hypothetical preemption event.
func TestPoolRunningButlerChildIsNeverPreempted(t *testing.T) {
	cfg := triKindConfig(1, 0)
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.holdSlots(1)
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)

	p, pctx := newPool(context.Background(), cfg, r, em, clk)
	defer p.cancel()
	// Seed dispatch and research backed off before the slot ever picks, so
	// its very first pick is forced into the idle tier — otherwise there is
	// nothing running for a "work appears" moment to try to preempt.
	p.markNoWork(KindDispatch, clk.Now(), false)
	p.markNoWork(KindResearch, clk.Now(), false)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runSlot(pctx, 0, cfg, p)
	}()

	if slot := r.awaitStart(t); slot != 0 {
		t.Fatalf("started slot = %d, want 0", slot)
	}
	if calls := r.calls(); len(calls) != 1 || calls[0].Kind != KindButler {
		t.Fatalf("first call = %v, want exactly one butler call", calls)
	}

	// "Work appears" while the butler child is still in flight.
	p.resetKind(KindDispatch)
	if pctx.Err() != nil {
		t.Fatalf("pool ctx = %v, want nil: a running butler child must never be cancelled by another kind regaining work", pctx.Err())
	}

	// The held child releases normally — it was never cancelled out from
	// under the runner.
	r.releaseSlot(t, 0, ChildResult{Exit: 0})

	// The next pick, now that dispatch is runnable again, goes to dispatch,
	// not butler again.
	if slot := r.awaitStart(t); slot != 0 {
		t.Fatalf("started slot = %d, want 0", slot)
	}
	if calls := r.calls(); len(calls) != 2 || calls[1].Kind != KindDispatch {
		t.Fatalf("second call = %v, want dispatch", calls)
	}

	r.releaseSlot(t, 0, ChildResult{Exit: 5}) // host-tainted: halts the pool cleanly
	awaitWG(t, r, &wg)
}

// TestPoolButlerBackoffGrowsIndependently pins the idle tier onto the same
// per-kind backoff every other kind gets (kindBackoff): butler's own
// no-work streak doubles across its own consecutive empty checks exactly
// like dispatch/research's would, and a dispatch reset in between never
// perturbs it — the counterpart of TestPoolPerKindBackoffGrowsIndependently
// for the idle-priority kind added by issue #3878.
func TestPoolButlerBackoffGrowsIndependently(t *testing.T) {
	clk := &testClock{}
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	cfg := triKindConfig(1, 0)
	p, _ := newPool(context.Background(), cfg, nil, em, clk)

	wantButler := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}
	for i, want := range wantButler {
		got := p.markNoWork(KindButler, clk.Now(), false)
		if got != want {
			t.Fatalf("butler markNoWork()[%d] = %v, want %v", i, got, want)
		}
		// Dispatch resetting in between must never touch butler's own streak.
		p.resetKind(KindDispatch)
		if until, gated := p.st.kinds[KindDispatch].readyAt(clk.Now()); gated || !until.IsZero() {
			t.Fatalf("dispatch readyAt() = (%v, %v) after reset, want (zero, false)", until, gated)
		}
	}
}
