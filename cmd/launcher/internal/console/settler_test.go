package console

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/settle"
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
	qs.Settle(nil, "42", result)

	if len(inner.SettleCalls) != 1 || inner.SettleCalls[0].Num != "42" {
		t.Errorf("inner.SettleCalls = %+v, want one call for #42", inner.SettleCalls)
	}
	if got := q.Snapshot()[0].State; got != PickSettled {
		t.Errorf("pick state = %v, want settled", got)
	}
}
