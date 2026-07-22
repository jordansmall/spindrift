package console

import (
	"strings"
	"testing"
)

// TestUpdate_TerminateRequestedMsg_SetsPending verifies "k <num>" arms a
// pending confirm on the model — Terminate (ADR 0024, issue #649) requires
// an explicit confirm before acting.
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

// TestUpdate_TerminateConfirmedMsg_ClearsPending verifies a confirmed
// terminate clears the pending confirm so the next render returns to the
// normal backlog/queue view.
func TestUpdate_TerminateConfirmedMsg_ClearsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, TerminateRequestedMsg{Number: "42"})
	m = Update(m, TerminateConfirmedMsg{Number: "42"})

	if m.Mode == ModeTerminateConfirm {
		t.Errorf("Mode = %v, want ModeList after confirm", m.Mode)
	}
}

// TestUpdate_TerminateCancelledMsg_ClearsPending verifies declining the
// confirm clears the pending state without acting.
func TestUpdate_TerminateCancelledMsg_ClearsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, TerminateRequestedMsg{Number: "42"})
	m = Update(m, TerminateCancelledMsg{})

	if m.Mode == ModeTerminateConfirm {
		t.Errorf("Mode = %v, want ModeList after cancel", m.Mode)
	}
}

// TestView_TerminateConfirm_ShowsConfirmPrompt verifies the operator sees an
// explicit confirm prompt naming the issue before Terminate acts.
func TestView_TerminateConfirm_ShowsConfirmPrompt(t *testing.T) {
	m := NewModel()
	m.Mode = ModeTerminateConfirm
	m.TerminateConfirm = TerminateConfirmState{Number: "42"}

	got := View(m)
	if !strings.Contains(got, "#42") || !strings.Contains(got, "y/N") {
		t.Errorf("View = %q, want a confirm prompt naming #42", got)
	}
}

// TestView_TerminateConfirm_FooterStyledDim verifies the terminate confirm
// prompt's y/N/q/ctrl+c hint renders dim (RoleDim, "\x1b[90m") via the
// shared footer renderer, the same treatment the other migrated footers
// already got (issue #1793).
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
