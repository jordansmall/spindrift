package console

import (
	"strings"
	"testing"
)

// TestUpdate_QuitRequestedMsg_SetsPending verifies "q" with live Dispatches
// arms a pending quit confirm on the model instead of quitting immediately
// (issue #651).
func TestUpdate_QuitRequestedMsg_SetsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, QuitRequestedMsg{})

	if !m.PendingQuit {
		t.Errorf("PendingQuit = %v, want true after QuitRequestedMsg", m.PendingQuit)
	}
}

// TestUpdate_QuitCancelledMsg_ClearsPending verifies choosing "stay" clears
// the pending quit confirm without quitting.
func TestUpdate_QuitCancelledMsg_ClearsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, QuitRequestedMsg{})
	m = Update(m, QuitCancelledMsg{})

	if m.PendingQuit {
		t.Errorf("PendingQuit = %v, want false after cancel", m.PendingQuit)
	}
	if m.Quitting {
		t.Errorf("Quitting = %v, want false after cancel (stay)", m.Quitting)
	}
}

// TestView_PendingQuit_ShowsConfirmPrompt verifies the operator sees the
// drain/terminate-all/stay choice before quitting with live Dispatches.
func TestView_PendingQuit_ShowsConfirmPrompt(t *testing.T) {
	m := NewModel()
	m.PendingQuit = true

	got := View(m)
	if !strings.Contains(got, "drain") || !strings.Contains(got, "terminate-all") || !strings.Contains(got, "stay") {
		t.Errorf("View = %q, want a drain/terminate-all/stay confirm prompt", got)
	}
}
