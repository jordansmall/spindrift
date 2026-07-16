package console

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
)

// TestQueueSettler_Settle_MarksPickSettledAndDelegates verifies the queue
// settler marks the numbered pick settled and still drives the wrapped
// Settler's real Settle call — the "running -> settled" half of a launched
// pick's row (#646 AC4).
func TestQueueSettler_Settle_MarksPickSettledAndDelegates(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})
	inner := settle.NewFake()
	qs := queueSettler{Settler: inner, q: q}

	result := dispatch.Result{Success: true}
	qs.Settle(nil, "42", 0, result)

	if len(inner.SettleCalls) != 1 || inner.SettleCalls[0].Num != "42" {
		t.Errorf("inner.SettleCalls = %+v, want one call for #42", inner.SettleCalls)
	}
	if got := q.Snapshot()[0].State; got != PickSettled {
		t.Errorf("pick state = %v, want settled", got)
	}
}

// TestQueueSettler_Settle_SkipsPickUpdateWhenTerminated verifies that when
// the settling issue was marked on the shared termination registry (ADR
// 0024, issue #649) — Terminate landed mid-settle, in the window between
// Run() succeeding and this Settle call completing — the queue settler
// leaves the pick's row alone instead of overwriting Terminate's own
// PickTerminated back to PickSettled.
func TestQueueSettler_Settle_SkipsPickUpdateWhenTerminated(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickTerminated, Reason: "terminated by operator"})
	reg := terminate.NewRegistry()
	gen := reg.Begin("42")
	reg.Mark("42")
	inner := settle.NewFake()
	qs := queueSettler{Settler: inner, q: q, terminated: reg}

	result := dispatch.Result{Success: true}
	qs.Settle(nil, "42", gen, result)

	if got := q.Snapshot()[0].State; got != PickTerminated {
		t.Errorf("pick state = %v, want it left at PickTerminated", got)
	}
}

// TestQueueSettler_Settle_StaleGenerationAfterRepickDoesNotCorruptNewRow
// reproduces the issue #743 race directly: an old settle goroutine's Settle
// call — for the generation Terminate marked — completes only after a
// re-pick has already begun a fresh generation and appended a new,
// currently-running row for the same issue number. Before #743, discover's
// blind Unmark would have cleared Terminate's mark out from under the old
// goroutine, so this late Settle call would have wrongly overwritten the new
// row via Queue.setState's back-to-front "newest row wins" scan. With
// generation-scoped marks, the old call's own (stale) generation still reads
// terminated, so it must leave the new row untouched.
func TestQueueSettler_Settle_StaleGenerationAfterRepickDoesNotCorruptNewRow(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickTerminated, Reason: "terminated by operator"})
	reg := terminate.NewRegistry()
	oldGen := reg.Begin("42") // the original dispatch's own claim
	reg.Mark("42")            // Terminate marks that generation dead
	reg.Begin("42")           // a re-pick's discover claims a fresh incarnation
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})
	inner := settle.NewFake()
	qs := queueSettler{Settler: inner, q: q, terminated: reg}

	result := dispatch.Result{Success: true}
	qs.Settle(nil, "42", oldGen, result) // the stale, old-generation settle finally completes

	picks := q.Snapshot()
	if picks[0].State != PickTerminated {
		t.Errorf("old row state = %v, want it left at PickTerminated", picks[0].State)
	}
	if picks[1].State != PickRunning {
		t.Errorf("new row state = %v, want it left at PickRunning (untouched by the stale settle)", picks[1].State)
	}
}

// TestQueueSettler_Fail_MarksPickFailedAndDelegates verifies the queue
// settler marks the numbered pick failed and still drives the wrapped
// Settler's own Fail hook, giving a naturally-failed Box a terminal queue
// state instead of stranding it at PickRunning (issue #705).
func TestQueueSettler_Fail_MarksPickFailedAndDelegates(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})
	inner := settle.NewFake()
	qs := queueSettler{Settler: inner, q: q}

	result := dispatch.Result{Success: false}
	qs.Fail("42", 0, result)

	if len(inner.FailCalls) != 1 || inner.FailCalls[0].Num != "42" {
		t.Errorf("inner.FailCalls = %+v, want one call for #42", inner.FailCalls)
	}
	if got := q.Snapshot()[0].State; got != PickFailed {
		t.Errorf("pick state = %v, want failed", got)
	}
}
