package console

import (
	"strings"
	"testing"
)

// Terminate (ADR 0024, issue #649) requires an explicit confirm before it
// acts, so "k <num>" only arms a pending confirm on the model.
func TestUpdate_TerminateRequestedMsg_SetsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, TerminateRequestedMsg{Number: "42"})

	if m.Mode != ModeTerminateConfirm {
		t.Errorf("Mode = %v, want ModeTerminateConfirm", m.Mode)
	}
	if m.TerminateConfirm.Number != "42" {
		t.Errorf("TerminateConfirm.Number = %q, want %q", m.TerminateConfirm.Number, "42")
	}
}

func TestUpdate_TerminateConfirmedMsg_ClearsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, TerminateRequestedMsg{Number: "42"})
	m = Update(m, TerminateConfirmedMsg{Number: "42"})

	if m.Mode == ModeTerminateConfirm {
		t.Errorf("Mode = %v, want ModeList after confirm", m.Mode)
	}
}

func TestUpdate_TerminateCancelledMsg_ClearsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, TerminateRequestedMsg{Number: "42"})
	m = Update(m, TerminateCancelledMsg{})

	if m.Mode == ModeTerminateConfirm {
		t.Errorf("Mode = %v, want ModeList after cancel", m.Mode)
	}
}

func TestView_TerminateConfirm_ShowsConfirmPrompt(t *testing.T) {
	m := NewModel()
	m.Mode = ModeTerminateConfirm
	m.TerminateConfirm = TerminateConfirmState{Number: "42"}

	got := View(m)
	if !strings.Contains(got, "#42") || !strings.Contains(got, "y/N") {
		t.Errorf("View = %q, want a confirm prompt naming #42", got)
	}
}

// Issue #1793 moved this footer to the shared renderer, so the y/N/q/ctrl+c
// hint must come out dim (RoleDim, "\x1b[90m") like the other migrated
// footers.
func TestView_TerminateConfirm_FooterStyledDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := NewModel()
	m.Mode = ModeTerminateConfirm
	m.TerminateConfirm = TerminateConfirmState{Number: "42"}

	got := View(m)
	if !strings.Contains(got, "\x1b[90m[y/N/q/ctrl+c]\x1b[0m") {
		t.Errorf("View() = %q, want the terminate-confirm hint dim-styled with its text intact", got)
	}
}
