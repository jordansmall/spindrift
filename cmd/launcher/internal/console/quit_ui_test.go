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

	if m.Mode != ModeQuitConfirm {
		t.Errorf("Mode = %v, want ModeQuitConfirm after QuitRequestedMsg", m.Mode)
	}
}

// TestUpdate_QuitCancelledMsg_ClearsPending verifies choosing "stay" clears
// the pending quit confirm without quitting.
func TestUpdate_QuitCancelledMsg_ClearsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, QuitRequestedMsg{})
	m = Update(m, QuitCancelledMsg{})

	if m.Mode == ModeQuitConfirm {
		t.Errorf("Mode = %v, want ModeList after cancel", m.Mode)
	}
	if m.Quitting {
		t.Errorf("Quitting = %v, want false after cancel (stay)", m.Quitting)
	}
}

// TestView_QuitConfirm_ShowsConfirmPrompt verifies the operator sees the
// drain/terminate-all/stay choice before quitting with live Dispatches.
func TestView_QuitConfirm_ShowsConfirmPrompt(t *testing.T) {
	m := NewModel()
	m.Mode = ModeQuitConfirm

	got := View(m)
	if !strings.Contains(got, "drain") || !strings.Contains(got, "terminate-all") || !strings.Contains(got, "stay") {
		t.Errorf("View = %q, want a drain/terminate-all/stay confirm prompt", got)
	}
}

// TestView_QuitConfirm_FooterStyledDim verifies the quit confirm prompt's
// drain/terminate-all/stay hint renders dim (RoleDim, "\x1b[90m") via the
// shared footer renderer, the same treatment the other migrated footers
// already got (issue #1793).
func TestView_QuitConfirm_FooterStyledDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := NewModel()
	m.Mode = ModeQuitConfirm

	got := View(m)
	if !strings.Contains(got, "\x1b[90mquit with live Dispatches: drain (d, default) / terminate-all (t) / stay (s)?\x1b[0m") {
		t.Errorf("View() = %q, want the quit-confirm hint dim-styled with its text intact", got)
	}
}
