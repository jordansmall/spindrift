package daemon

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkind"
)

// quadKindConfig is triKindConfig's four-kind sibling (issue #4656): every
// kind the daemon can draw, in dispatchkind.All's declaration order.
func quadKindConfig(slots int) Config {
	cfg := testConfig(slots)
	cfg.Kinds = []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research), KindOf(dispatchkind.Butler), KindOf(dispatchkind.Recover)}
	return cfg
}

// runQuadKind runs a one-slot pool over every kind until stopWhen holds and
// returns the kinds the runner was asked to start, in order.
func runQuadKind(t *testing.T, byKind map[Kind][]ChildResult, stopWhen func(r *scriptedRunner) bool) []Kind {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedRunner{revisions: []string{"rev1"}, byKind: byKind}
	gate := &kindGate{r: r, limit: 20, cancelFn: cancel, cancelWhen: stopWhen}
	r.onStart = gate.onStart
	var buf bytes.Buffer

	reason := Loop(ctx, quadKindConfig(1), r, newTestEmitter(&buf), &testClock{}).String()
	if !strings.Contains(reason, "context-cancelled") {
		t.Fatalf("halt reason = %q, want context-cancelled (the test's own stop)", reason)
	}
	gate.requireGoalMet(t)
	var kinds []Kind
	for _, c := range r.calls() {
		kinds = append(kinds, c.Kind)
	}
	return kinds
}

// TestPoolRecoverIsTriedBeforeEveryOtherKind pins issue #4656's first tier:
// with every kind holding work, a free slot picks recover ahead of work, so
// stranded finished work lands before a new Box starts.
func TestPoolRecoverIsTriedBeforeEveryOtherKind(t *testing.T) {
	recoverKind := KindOf(dispatchkind.Recover)
	got := runQuadKind(t, map[Kind][]ChildResult{
		KindOf(dispatchkind.Work):     {{Exit: 0}},
		KindOf(dispatchkind.Research): {{Exit: 0}},
		KindOf(dispatchkind.Butler):   {{Exit: 0}},
		recoverKind:                   {{Exit: 0}},
	}, func(r *scriptedRunner) bool { return r.runCount() >= 1 })
	if len(got) == 0 || got[0] != recoverKind {
		t.Fatalf("first started kind = %v, want recover ahead of dispatch, research and butler", got)
	}
}

// TestPoolRecoverExitTwoParksItThenDispatchRuns pins that recover is an
// ordinary exit-driven kind: exit 2 (nothing eligible) backs it off like any
// other kind's "no work", and the slot then moves on to dispatch.
func TestPoolRecoverExitTwoParksItThenDispatchRuns(t *testing.T) {
	recoverKind, workKind := KindOf(dispatchkind.Recover), KindOf(dispatchkind.Work)
	got := runQuadKind(t, map[Kind][]ChildResult{
		recoverKind: {{Exit: 2}},
		workKind:    {{Exit: 0}},
	}, func(r *scriptedRunner) bool { return r.kindCount(workKind) >= 1 })
	want := []Kind{recoverKind, workKind}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("started kinds = %v, want %v: recover once, parked, then dispatch", got, want)
	}
}

// TestSlotOrderFirstTierPrecedesEveryOther pins the first tier ahead of
// reserved, normal and idle, whichever way the reservation leans and
// wherever recover sits in the configured kinds.
func TestSlotOrderFirstTierPrecedesEveryOther(t *testing.T) {
	w, r, b, rc := KindOf(dispatchkind.Work), KindOf(dispatchkind.Research), KindOf(dispatchkind.Butler), KindOf(dispatchkind.Recover)
	for _, tc := range []struct {
		name           string
		kinds          []Kind
		preferReserved bool
		want           []Kind
	}{
		{"below reservation", []Kind{w, r, b, rc}, true, []Kind{rc, r, w, b}},
		{"at reservation", []Kind{w, r, b, rc}, false, []Kind{rc, w, r, b}},
		{"recover given first", []Kind{rc, b, r, w}, false, []Kind{rc, w, r, b}},
		{"recover with work only", []Kind{w, rc}, false, []Kind{rc, w}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := slotOrder(tc.kinds, tc.preferReserved)
			if strings.Join(kindStrings(got), ",") != strings.Join(kindStrings(tc.want), ",") {
				t.Fatalf("slotOrder(%v, %v) = %v, want %v", tc.kinds, tc.preferReserved, got, tc.want)
			}
		})
	}
}

func kindStrings(ks []Kind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

// TestLoopRecoverOutboxDemandStartsNoChildUntilBundleAppears pins issue
// #4657: a probed recover kind is drawn by the host's outbox count, so an
// empty outbox starts no recover child (other kinds still run), and the child
// starts within one probe interval of an eligible bundle appearing.
func TestLoopRecoverOutboxDemandStartsNoChildUntilBundleAppears(t *testing.T) {
	const interval = 10 * time.Second
	recoverKind := KindOf(dispatchkind.Recover)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &testClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	starts := &startTimes{clk: clk}
	var appeared time.Time
	r := &scriptedRunner{revisions: []string{"rev1"}}
	r.setDemand(recoverKind, 0)
	r.setDemand(workKind, 1)
	r.onDemand = func(_ context.Context, k Kind) {
		if k == recoverKind && r.demandCount(k) == 3 {
			appeared = clk.Now()
			r.setDemand(k, 1)
		}
	}
	r.onStart = func(_ context.Context, req ChildRequest) error {
		if req.Kind == recoverKind {
			starts.record()
			cancel()
			return nil
		}
		// Work runs while recover is empty; its starts move the fake clock on.
		clk.advanceBy(interval / 2)
		if r.kindCount(workKind) > 20 {
			cancel()
		}
		return nil
	}
	cfg := probedConfig(1, interval, recoverKind, workKind)
	cfg.IdleFloor, cfg.IdleCap = time.Hour, 2*time.Hour
	var buf bytes.Buffer

	Loop(ctx, cfg, r, newTestEmitter(&buf), clk)

	if r.kindCount(recoverKind) != 1 || starts.len() != 1 {
		t.Fatalf("recover children started = %d, want exactly 1 once the bundle appeared", r.kindCount(recoverKind))
	}
	if r.demandCount(recoverKind) != 3 {
		t.Fatalf("recover Demand calls = %d, want 3 (two empty probes, then the one that saw a bundle)", r.demandCount(recoverKind))
	}
	if got := starts.times[0].Sub(appeared); got > interval {
		t.Fatalf("recover started %s after the bundle appeared, want within one probe interval %s", got, interval)
	}
}

// recoverUpperBoundLoop runs a one-slot recover pool whose every child exits 2
// against a nonzero outbox count, stopping at the nth start, and returns the
// fake-clock start offsets from t0.
func recoverUpperBoundLoop(t *testing.T, stopAt int, onDemand func(r *scriptedRunner, recoverKind Kind)) []time.Duration {
	t.Helper()
	recoverKind := KindOf(dispatchkind.Recover)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &testClock{now: t0}
	starts := &startTimes{clk: clk}
	r := &scriptedRunner{revisions: []string{"rev1"}, results: []ChildResult{{Exit: 2}}}
	r.setDemand(recoverKind, 1)
	if onDemand != nil {
		r.onDemand = func(_ context.Context, k Kind) {
			if k == recoverKind {
				onDemand(r, recoverKind)
			}
		}
	}
	r.onStart = func(context.Context, ChildRequest) error {
		starts.record()
		if starts.len() == stopAt {
			cancel()
		}
		return nil
	}
	cfg := probedConfig(1, time.Second, recoverKind)
	cfg.IdleFloor, cfg.IdleCap = 4*time.Second, 16*time.Second
	var buf bytes.Buffer

	Loop(ctx, cfg, r, newTestEmitter(&buf), clk)

	return starts.offsets(t0)
}

func requireOffsets(t *testing.T, got, want []time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("start offsets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("start offsets = %v, want %v", got, want)
		}
	}
}

// The outbox count is an upper bound: a bundle awaiting a human merge stays
// counted while the child, finding it ineligible, exits 2. The kind must back
// off toward IdleCap rather than restart a child every probe interval.
func TestLoopRecoverExitTwoAgainstSteadyDemandBacksOff(t *testing.T) {
	got := recoverUpperBoundLoop(t, 5, nil)

	s := time.Second
	requireOffsets(t, got, []time.Duration{0, 4 * s, 12 * s, 28 * s, 44 * s})
}

// A rise in the count is a new bundle: it lifts the backoff and starts a child
// within a probe interval, not after the grown wait.
func TestLoopRecoverDemandRiseLiftsExitTwoBackoff(t *testing.T) {
	got := recoverUpperBoundLoop(t, 2, func(r *scriptedRunner, k Kind) {
		if r.demandCount(k) == 4 {
			r.setDemand(k, 2)
		}
	})

	s := time.Second
	// Probes land at 1s, 2s and 3s; the rise seen at the third starts the
	// second child at 3s, before the 4s first backoff would have lapsed.
	requireOffsets(t, got, []time.Duration{0, 3 * s})
}
