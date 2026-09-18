package console

import (
	"strings"
	"testing"
)

// With live Dispatches, "q" must arm a pending quit confirm instead of
// quitting immediately (issue #651).
func TestUpdate_QuitRequestedMsg_SetsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, QuitRequestedMsg{})

	if m.Mode != ModeQuitConfirm {
		t.Errorf("Mode = %v, want ModeQuitConfirm after QuitRequestedMsg", m.Mode)
	}
}

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

func TestView_QuitConfirm_ShowsConfirmPrompt(t *testing.T) {
	m := NewModel()
	m.Mode = ModeQuitConfirm

	got := View(m)
	if !strings.Contains(got, "drain") || !strings.Contains(got, "terminate-all") || !strings.Contains(got, "stay") {
		t.Errorf("View = %q, want a drain/terminate-all/stay confirm prompt", got)
	}
}

// The quit confirm hint must render dim (RoleDim, "\x1b[90m") through the
// shared footer renderer, like the other migrated footers (issue #1793).
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
