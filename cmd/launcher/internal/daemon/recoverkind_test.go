package daemon

import (
	"bytes"
	"context"
	"strings"
	"testing"

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
