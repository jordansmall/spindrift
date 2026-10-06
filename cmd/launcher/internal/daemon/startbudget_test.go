package daemon

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startBudgetLoop runs a probed work Loop over slots slots with every child
// held, on a parked clock whose sleeps are signalled. The caller cancels and
// drains done.
func startBudgetLoop(t *testing.T, slots int, r *scriptedRunner) (clk *testClock, cancel context.CancelFunc, done <-chan Halt) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clk = &testClock{
		now:         time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		sleepSignal: make(chan struct{}, slots),
	}
	clk.park()
	r.revisions = []string{"rev1"}
	r.holdSlots(slots)
	var buf bytes.Buffer
	ch := make(chan Halt, 1)
	go func() { ch <- Loop(ctx, probedConfig(slots, time.Hour, workKind), r, newTestEmitter(&buf), clk) }()
	return clk, cancel, ch
}

// stagedBudgetPool is a probed work pool whose slots are launched by hand, so a
// test can have slot 0 already running a child before any sibling first
// decides. Under Loop every slot decides at once and a sibling can pick Start
// before slot 0 marks itself running, which blocks it on the baton for reasons
// unrelated to the start budget.
type stagedBudgetPool struct {
	p      *pool
	pctx   context.Context
	cancel context.CancelFunc
	cfg    Config
	clk    *testClock
	out    *notifyWriter
	wg     sync.WaitGroup
}

// newStagedBudgetPool parks the clock and holds every child; each sleep is
// signalled on clk, buffered to the slot count.
func newStagedBudgetPool(slots int, r *scriptedRunner) *stagedBudgetPool {
	ctx, cancel := context.WithCancel(context.Background())
	s := &stagedBudgetPool{
		cancel: cancel,
		cfg:    probedConfig(slots, time.Hour, workKind),
		clk: &testClock{
			now:         time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			sleepSignal: make(chan struct{}, slots),
		},
		out: newNotifyWriter(),
	}
	s.clk.park()
	r.revisions = []string{"rev1"}
	r.holdSlots(slots)
	s.p, s.pctx = newPool(ctx, s.cfg, r, newTestEmitter(s.out), s.clk)
	return s
}

func (s *stagedBudgetPool) startSlot(slot int) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		runSlot(s.pctx, slot, s.cfg, s.p)
	}()
}

// stop cancels the pool, lets the held children return and waits for every
// slot.
func (s *stagedBudgetPool) stop(t *testing.T, r *scriptedRunner, held ...int) {
	t.Helper()
	s.cancel()
	for _, slot := range held {
		r.releaseSlot(t, slot, ChildResult{Exit: 0})
	}
	s.wg.Wait()
}

// The start budget alone holds siblings back: demand stays at one and slot 0's
// child is still discovering (no claim yet), so a sibling that read the count
// without the budget would start a second child onto the same item. Siblings
// are launched after slot 0 is running, so each decides against it.
func TestLoopStartBudgetParksSiblingsWhileChildDiscovers(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{}
	r.setDemand(workKind, 1)
	s := newStagedBudgetPool(slots, r)
	defer s.cancel()

	s.startSlot(0)
	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("first slot to start = %d, want 0", got)
	}
	for sib := 1; sib < slots; sib++ {
		s.startSlot(sib)
	}
	// A sibling that decided Start would block on the baton, never sleep.
	s.clk.awaitSleep(t, slots-1)

	if got := r.runCount(); got != 1 {
		t.Fatalf("children started = %d, want 1 for one queued issue", got)
	}
	if out := s.out.String(); strings.Contains(out, `"event":"baton_hold"`) {
		t.Fatalf("a sibling decided Start and blocked on the baton, want it parked by the start budget:\n%s", out)
	}

	s.stop(t, r, 0)
}

// A probe already in flight when a claim lands read the pre-claim count, so it
// must not mark the kind fresh: the claimed item is still in that count, and
// trusting it would start a second child onto it.
func TestLoopInFlightProbeAcrossClaimIsReprobed(t *testing.T) {
	r := &scriptedRunner{}
	r.setDemand(workKind, 1)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	// Call 1 is slot 0's cold probe; call 2 is the sibling's, held across the
	// claim and answering the stale 1 once released; call 3 is the forced
	// re-probe, which finds the queue drained.
	r.onDemand = func(ctx context.Context, _ Kind) {
		switch calls.Add(1) {
		case 2:
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		case 3:
			r.setDemand(workKind, 0)
		}
	}
	s := newStagedBudgetPool(2, r)
	defer s.cancel()

	s.startSlot(0)
	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("first slot to start = %d, want 0", got)
	}
	// Past the probe interval, so the sibling's first decide is a probe.
	s.clk.advanceBy(2 * time.Hour)
	s.startSlot(1)
	<-entered

	r.fireOnIssue(t, 0, "42")
	close(release)
	s.clk.awaitSleep(t, 1)

	if got := r.demandCount(workKind); got != 3 {
		t.Fatalf("Demand calls = %d, want 3 (the claim must force a re-probe after the in-flight one)", got)
	}
	if got := r.runCount(); got != 1 {
		t.Fatalf("children started = %d, want 1 for one queued issue", got)
	}

	s.stop(t, r, 0)
}

// One queued issue starts one child however many slots are free: once the child
// claims, the re-probe reads the drained queue and holds the siblings.
func TestLoopOneQueuedIssueStartsOneChild(t *testing.T) {
	const slots = 3
	r := &scriptedRunner{}
	r.setDemand(workKind, 1)
	clk, cancel, done := startBudgetLoop(t, slots, r)
	defer cancel()

	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("first slot to start = %d, want 0", got)
	}

	// The claim empties the queue; it passes the baton to the siblings, who
	// must read the re-probe's zero rather than the stale count of one.
	r.setDemand(workKind, 0)
	r.fireOnIssue(t, 0, "42")
	clk.awaitSleep(t, slots-1)

	if got := r.runCount(); got != 1 {
		t.Fatalf("children started = %d, want 1 for one queued issue", got)
	}

	cancel()
	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	<-done
}

// A start budget of ready-starting still fills every free slot when the queue
// is deeper than the pool: three issues, two slots, two concurrent children.
func TestLoopThreeQueuedIssuesStartTwoChildrenOnTwoSlots(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{}
	r.setDemand(workKind, 3)
	_, cancel, done := startBudgetLoop(t, slots, r)
	defer cancel()

	if got := r.awaitStart(t); got != 0 {
		t.Fatalf("first slot to start = %d, want 0", got)
	}
	r.setDemand(workKind, 2)
	r.fireOnIssue(t, 0, "42")
	if got := r.awaitStart(t); got != 1 {
		t.Fatalf("second slot to start = %d, want 1", got)
	}

	if got := r.peakConcurrency(); got != slots {
		t.Fatalf("peak concurrent children = %d, want %d", got, slots)
	}

	cancel()
	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	r.releaseSlot(t, 1, ChildResult{Exit: 0})
	<-done
}

// A claim moves the count, so the kind is re-probed before its next child
// starts: the Demand call lands between the claim and the next child.
func TestLoopClaimForcesReprobeBeforeNextStart(t *testing.T) {
	const slots = 2
	r := &scriptedRunner{}
	r.setDemand(workKind, 3)
	var mu sync.Mutex
	var log []string
	note := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		log = append(log, s)
	}
	r.onDemand = func(context.Context, Kind) { note("demand") }
	r.onStart = func(context.Context, ChildRequest) error { note("start"); return nil }
	_, cancel, done := startBudgetLoop(t, slots, r)
	defer cancel()

	r.awaitStart(t)
	note("claim")
	r.fireOnIssue(t, 0, "42")
	r.awaitStart(t)

	mu.Lock()
	got := slices.Clone(log)
	mu.Unlock()
	// Only the property is pinned: a sibling's probe already in flight across
	// the claim may add entries, but a fresh probe must precede the next start.
	at := slices.Index(got, "claim")
	if at < 0 || got[len(got)-1] != "start" || !slices.Contains(got[at:], "demand") {
		t.Fatalf("probe/claim/start order = %v, want a demand after the claim, then the final start", got)
	}

	cancel()
	r.releaseSlot(t, 0, ChildResult{Exit: 0})
	r.releaseSlot(t, 1, ChildResult{Exit: 0})
	<-done
}
