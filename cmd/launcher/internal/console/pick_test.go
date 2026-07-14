package console

import "testing"

// TestUpdate_PickQueuedMsg_AppendsPick verifies Update adds a new queued Pick
// to Model.Picks, defaulted to KindWork — the operator's launch decision
// landing on the session queue (#646).
func TestUpdate_PickQueuedMsg_AppendsPick(t *testing.T) {
	m := NewModel()
	m = Update(m, PickQueuedMsg{Number: "42", Title: "fix the thing", Kind: KindWork})

	if len(m.Picks) != 1 {
		t.Fatalf("Picks = %+v, want one queued pick", m.Picks)
	}
	got := m.Picks[0]
	if got.Number != "42" || got.Title != "fix the thing" || got.Kind != KindWork || got.State != PickQueued {
		t.Errorf("Picks[0] = %+v, want {42 fix the thing work queued}", got)
	}
}

// TestUpdate_UnpickMsg_RemovesQueuedPick verifies UnpickMsg drops a queued
// pick from Model.Picks — the operator changing their mind before launch,
// with no tracker interaction of any kind (#646).
func TestUpdate_UnpickMsg_RemovesQueuedPick(t *testing.T) {
	m := NewModel()
	m = Update(m, PickQueuedMsg{Number: "42", Title: "fix the thing", Kind: KindWork})

	m = Update(m, UnpickMsg{Number: "42"})

	if len(m.Picks) != 0 {
		t.Errorf("Picks = %+v, want empty after unpick", m.Picks)
	}
}

// TestUpdate_UnpickMsg_LeavesNonQueuedPickAlone verifies Unpick only ever
// removes a pick still holding at PickQueued — a pick already claiming,
// running, or settled cannot be unpicked out from under its Dispatch.
func TestUpdate_UnpickMsg_LeavesNonQueuedPickAlone(t *testing.T) {
	m := NewModel()
	m.Picks = []Pick{{Number: "42", State: PickRunning}}

	m = Update(m, UnpickMsg{Number: "42"})

	if len(m.Picks) != 1 {
		t.Errorf("Picks = %+v, want the running pick left in place", m.Picks)
	}
}
