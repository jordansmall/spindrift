package console

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestUpdate_TerminateRequestedMsg_SetsPending verifies "k <num>" arms a
// pending confirm on the model — Terminate (ADR 0024, issue #649) requires
// an explicit confirm before acting.
func TestUpdate_TerminateRequestedMsg_SetsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, TerminateRequestedMsg{Number: "42"})

	if m.PendingTerminate != "42" {
		t.Errorf("PendingTerminate = %q, want %q", m.PendingTerminate, "42")
	}
}

// TestUpdate_TerminateConfirmedMsg_ClearsPending verifies a confirmed
// terminate clears the pending confirm so the next render returns to the
// normal backlog/queue view.
func TestUpdate_TerminateConfirmedMsg_ClearsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, TerminateRequestedMsg{Number: "42"})
	m = Update(m, TerminateConfirmedMsg{Number: "42"})

	if m.PendingTerminate != "" {
		t.Errorf("PendingTerminate = %q, want empty after confirm", m.PendingTerminate)
	}
}

// TestUpdate_TerminateCancelledMsg_ClearsPending verifies declining the
// confirm clears the pending state without acting.
func TestUpdate_TerminateCancelledMsg_ClearsPending(t *testing.T) {
	m := NewModel()
	m = Update(m, TerminateRequestedMsg{Number: "42"})
	m = Update(m, TerminateCancelledMsg{})

	if m.PendingTerminate != "" {
		t.Errorf("PendingTerminate = %q, want empty after cancel", m.PendingTerminate)
	}
}

// TestView_PendingTerminate_ShowsConfirmPrompt verifies the operator sees an
// explicit confirm prompt naming the issue before Terminate acts.
func TestView_PendingTerminate_ShowsConfirmPrompt(t *testing.T) {
	m := NewModel()
	m.PendingTerminate = "42"

	got := View(m)
	if !strings.Contains(got, "#42") || !strings.Contains(got, "y/N") {
		t.Errorf("View = %q, want a confirm prompt naming #42", got)
	}
}

// TestRun_TerminateCommand_RequiresConfirm verifies "k <num>" alone never
// acts — no tracker transition, no comment — until a "y" confirm follows.
func TestRun_TerminateCommand_RequiresConfirm(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	launch := newTestLauncher(t, f)
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("k 42\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none without a confirm", f.TransitionStateCalls)
	}
	if !strings.Contains(out.String(), "#42") || !strings.Contains(out.String(), "y/N") {
		t.Errorf("output = %q, want a confirm prompt naming #42", out.String())
	}
}

// TestRun_TerminateCommand_ConfirmedYes_ActsAndDissolvesPick verifies "k
// <num>" then "y" reaps the Box, transitions the issue to Dispatchable, and
// lands the queue pick PickTerminated.
func TestRun_TerminateCommand_ConfirmedYes_ActsAndDissolvesPick(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	launch := newTestLauncher(t, f)
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("k 42\ny\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(f.TransitionStateCalls) != 1 {
		t.Fatalf("TransitionStateCalls: want 1, got %+v", f.TransitionStateCalls)
	}
	call := f.TransitionStateCalls[0]
	if call.From != forge.InProgress || call.To != forge.Dispatchable {
		t.Errorf("transition = %+v, want InProgress -> Dispatchable", call)
	}
	if len(f.CommentCalls) != 1 {
		t.Errorf("CommentCalls = %+v, want exactly one terminate comment", f.CommentCalls)
	}

	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickTerminated {
		t.Errorf("queue pick = %+v, want PickTerminated", snap)
	}
}

// TestRun_TerminateCommand_DeclinedNo_TakesNoAction verifies anything other
// than "y"/"yes" cancels the pending terminate without acting.
func TestRun_TerminateCommand_DeclinedNo_TakesNoAction(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	launch := newTestLauncher(t, f)
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("k 42\nn\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none after declining", f.TransitionStateCalls)
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickRunning {
		t.Errorf("queue pick = %+v, want unchanged PickRunning", snap)
	}
}
