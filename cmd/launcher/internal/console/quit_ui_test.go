package console

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// newTestLauncherBlockingRun builds a Launcher wired to a runner.Fake Box
// whose Run blocks on the returned channel until closed — enough to hold a
// pick at PickRunning (and the session's Live() above zero) for tests that
// need a live Dispatch in flight, e.g. the quit confirm dialog (issue #651).
func newTestLauncherBlockingRun(t *testing.T, cf forge.CodeForge) (launch *Launcher, dir string, release chan struct{}) {
	t.Helper()
	release = make(chan struct{})
	fr := runner.NewFake()
	fr.RunFunc = func(runner.Box) error {
		<-release
		return nil
	}
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)
	launch = &Launcher{CodeForge: cf, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
	return launch, dir, release
}

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

// TestRun_QuitCommand_WithLiveDispatch_ShowsConfirmDialog verifies "q" with
// a live Dispatch running arms the drain/terminate-all/stay confirm instead
// of quitting immediately (issue #651).
func TestRun_QuitCommand_WithLiveDispatch_ShowsConfirmDialog(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	launch, dir, release := newTestLauncherBlockingRun(t, f)

	inR, inW := io.Pipe()
	var out syncBuilder
	done := make(chan error, 1)
	go func() { done <- Run(f, dir, inR, &out, launch) }()

	if _, err := inW.Write([]byte("p 42\n")); err != nil {
		t.Fatal(err)
	}
	waitForPickStates(t, launch.Queue, map[string]PickState{"42": PickRunning})

	if _, err := inW.Write([]byte("q\n")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), "drain") {
		if time.Now().After(deadline) {
			t.Fatalf("output = %q, want the drain/terminate-all/stay confirm dialog", out.String())
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := inW.Write([]byte("s\n")); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitForPickStates(t, launch.Queue, map[string]PickState{"42": PickSettled})

	if _, err := inW.Write([]byte("q\n")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRun_QuitDrain_DropsQueuedPicksAndExitsAfterLastSettle verifies issue
// #651: choosing "drain" launches nothing new (a queued-but-unlaunched pick
// is dropped, its issue left untouched at Dispatchable) and Run doesn't
// return until the still-running Dispatch actually settles.
func TestRun_QuitDrain_DropsQueuedPicksAndExitsAfterLastSettle(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "first", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Title: "second", Labels: []string{"ready-for-agent"}})

	launch, dir, release := newTestLauncherBlockingRun(t, f)

	inR, inW := io.Pipe()
	var out syncBuilder
	done := make(chan error, 1)
	go func() { done <- Run(f, dir, inR, &out, launch) }()

	if _, err := inW.Write([]byte("p 42\np 43\n")); err != nil {
		t.Fatal(err)
	}
	waitForPickStates(t, launch.Queue, map[string]PickState{"42": PickRunning, "43": PickQueued})

	if _, err := inW.Write([]byte("q\ndrain\n")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		found43 := false
		for _, p := range launch.Queue.Snapshot() {
			if p.Number == "43" {
				found43 = true
			}
		}
		if !found43 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("#43 still on queue = %+v, want dropped by drain", launch.Queue.Snapshot())
		}
		time.Sleep(time.Millisecond)
	}

	// #42 must still be running (drain never touches an in-flight Dispatch).
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].Number != "42" || snap[0].State != PickRunning {
		t.Fatalf("queue after drain = %+v, want only #42 still running", snap)
	}

	iss, err := f.Issue("43")
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(iss, "ready-for-agent") || hasLabel(iss, "agent-in-progress") {
		t.Errorf("#43 labels = %v, want left Dispatchable, never claimed", iss.Labels)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRun_QuitTerminateAll_TerminatesEveryRunningPickThenExits verifies
// choosing "terminate-all" applies Terminate (ADR 0024) to every live
// Dispatch, then exits (issue #651).
func TestRun_QuitTerminateAll_TerminatesEveryRunningPickThenExits(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	launch := newTestLauncher(t, f)
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("q\nterminate-all\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Terminate clears both possible "from" labels (InProgress and
	// Complete) — see TestLauncher_Terminate_DuringMergeGate_ClearsCompleteLabel.
	if len(f.TransitionStateCalls) != 2 {
		t.Fatalf("TransitionStateCalls: want 2, got %+v", f.TransitionStateCalls)
	}
	for _, call := range f.TransitionStateCalls {
		if call.To != forge.Dispatchable {
			t.Errorf("transition = %+v, want To=Dispatchable", call)
		}
	}
	if len(f.CommentCalls) != 1 {
		t.Errorf("CommentCalls = %+v, want exactly one terminate comment", f.CommentCalls)
	}

	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickTerminated {
		t.Errorf("queue pick = %+v, want PickTerminated", snap)
	}
}
