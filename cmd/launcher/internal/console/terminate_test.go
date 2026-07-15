package console

import (
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

// newTermTestLauncher builds a Launcher wired to fakes plus a real Factory/Settle
// over a temp log dir, and returns the tracker Terminate should act on plus
// the log dir itself, for tests that need to read the Box log back.
func newTermTestLauncher(t *testing.T) (launch *Launcher, fc *forge.Fake, fr *runner.Fake, dir string) {
	t.Helper()
	labels := forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"}
	fc = forge.NewFake(labels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	fr = runner.NewFake()
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	s := settle.New(settle.Config{MergeMode: "manual", CompleteLabel: "agent-complete"}, fc, fc)
	launch = &Launcher{CodeForge: fc, Factory: factory, Settle: s, Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})
	return launch, fc, fr, dir
}

// TestLauncher_Terminate_ReapsTransitionsAndComments verifies the full ADR
// 0024 action: the running Box is reaped by its deterministic name, the
// issue transitions InProgress -> Dispatchable (never Failed, never a new
// tracker state), a comment names the terminate and links the open PR, and
// the queue pick lands PickTerminated.
func TestLauncher_Terminate_ReapsTransitionsAndComments(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)
	fc.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/owner/repo/pull/7"})

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if len(fr.KillCalls) != 1 || fr.KillCalls[0] != "agent-issue-42" {
		t.Errorf("KillCalls: want [agent-issue-42], got %v", fr.KillCalls)
	}

	// Terminate clears both possible "from" labels (InProgress for a
	// running Box/CI watch, Complete if it landed during the merge gate —
	// see TestLauncher_Terminate_DuringMergeGate_ClearsCompleteLabel) since
	// it cannot know which one is actually present without adapter-specific
	// label inspection.
	if len(fc.TransitionStateCalls) != 2 {
		t.Fatalf("TransitionStateCalls: want 2, got %+v", fc.TransitionStateCalls)
	}
	for _, call := range fc.TransitionStateCalls {
		if call.To != forge.Dispatchable {
			t.Errorf("transition = %+v, want To=Dispatchable", call)
		}
	}

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("CommentCalls: want 1, got %+v", fc.CommentCalls)
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, "Terminated") {
		t.Errorf("comment must name the terminate; body=%q", body)
	}
	if !strings.Contains(body, "https://github.com/owner/repo/pull/7") {
		t.Errorf("comment must link the open PR; body=%q", body)
	}

	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickTerminated {
		t.Errorf("queue pick = %+v, want PickTerminated", snap)
	}
}

// TestLauncher_Terminate_DuringMergeGate_ClearsCompleteLabel verifies
// Terminate cleanly returns the issue to Dispatchable even when it lands
// during the merge gate: gateToGreen swaps InProgress -> Complete as soon as
// CI confirms green, before selfHeal ever attempts the merge, so an issue
// terminated while merge-gate retries were still in flight already carries
// Complete, not InProgress. Terminate must not leave it holding both
// Complete and Dispatchable at once.
func TestLauncher_Terminate_DuringMergeGate_ClearsCompleteLabel(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Complete: "agent-complete"}
	fc := forge.NewFake(labels)
	fc.BranchPrefix = "agent/issue-"
	// Simulates gateToGreen's own transition, already applied before
	// Terminate is ever called.
	fc.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-complete"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, runner.NewFake(), drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)
	s := settle.New(settle.Config{MergeMode: "manual", CompleteLabel: "agent-complete"}, fc, fc)
	launch := &Launcher{CodeForge: fc, Factory: factory, Settle: s, Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	iss, err := fc.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if containsString(iss.Labels, "agent-complete") {
		t.Errorf("labels = %v, want agent-complete cleared", iss.Labels)
	}
	if !containsString(iss.Labels, "ready-for-agent") {
		t.Errorf("labels = %v, want ready-for-agent present", iss.Labels)
	}
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// TestLauncher_Terminate_NoOpenPR_CommentNotesNone verifies the comment still
// posts, naming the absence of a dangling PR, when the issue never got one.
func TestLauncher_Terminate_NoOpenPR_CommentNotesNone(t *testing.T) {
	launch, fc, _, _ := newTermTestLauncher(t)

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("CommentCalls: want 1, got %+v", fc.CommentCalls)
	}
	if strings.Contains(fc.CommentCalls[0].Body, "https://") {
		t.Errorf("no PR was open; comment must not fabricate a link: %q", fc.CommentCalls[0].Body)
	}
}

// TestLauncher_Terminate_AppendsBoxLogTerminalLine verifies the Box log gets
// a terminal line recording the operator's action.
func TestLauncher_Terminate_AppendsBoxLogTerminalLine(t *testing.T) {
	launch, fc, _, dir := newTermTestLauncher(t)
	logPath := filepath.Join(dir, "logs", "issue-42.log")
	if err := os.WriteFile(logPath, []byte("initial run output\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "terminate") {
		t.Errorf("Box log = %q, want it to carry a terminal line", got)
	}
}

// TestLauncher_TerminateThenRepick_AdoptsAbandonedPR verifies the
// terminate-then-repick reclaim loop end to end (ADR 0024, issue #649): once
// Terminate leaves an issue Dispatchable with an open PR still up, re-picking
// it dispatches a fresh Box that — reporting no outcome line, as a box that
// finds the PR already open would — routes through the existing settle
// adoption path (SettleAdopted) rather than being abandoned by a stale
// terminate mark left over from the prior run.
func TestLauncher_TerminateThenRepick_AdoptsAbandonedPR(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Complete: "agent-complete"}
	fc := forge.NewFake(labels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	fr := runner.NewFake()
	fr.RunFunc = func(runner.Box) error { return nil } // exits zero, writes no outcome line
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)
	s := settle.New(settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     "agent-complete",
		MergePollInterval: 0,
		MergePollTimeout:  100,
	}, fc, fc)

	launch := &Launcher{CodeForge: fc, Factory: factory, Settle: s, Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	// Terminate: issue -> Dispatchable, registry marks #42, PR left dangling.
	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	fc.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/owner/repo/pull/7"})
	fc.SetCheckStates("https://github.com/owner/repo/pull/7", []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	// Re-pick: a fresh claim must not inherit the stale terminate mark.
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(fc, dir)

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := launch.Queue.Snapshot()
		if len(snap) == 2 && snap[1].State == PickSettled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("re-pick never settled: %+v", snap)
		}
		time.Sleep(time.Millisecond)
	}

	if fc.Merged != "https://github.com/owner/repo/pull/7" {
		t.Errorf("Merged = %q, want the adopted PR merged (adoption path ran to completion, not abandoned)", fc.Merged)
	}
}

// TestLauncher_Terminate_MarksRegistry verifies the shared termination
// registry records the issue, so an in-flight settle loop checking it (via
// Settle.SetTerminated) notices on its next checkpoint.
func TestLauncher_Terminate_MarksRegistry(t *testing.T) {
	launch, fc, _, _ := newTermTestLauncher(t)

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if !launch.registry().Marked("42") {
		t.Error("registry: want #42 marked terminated")
	}
}
