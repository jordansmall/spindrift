package console

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// blockingCommentTracker wraps a forge.IssueTracker and blocks every Comment
// call on unblock — TestLauncher_TerminateAsync_ReturnsBeforeTrackerCallCompletes'
// way of proving TerminateAsync's goroutine, not its caller, waits on the
// network I/O.
type blockingCommentTracker struct {
	forge.IssueTracker
	unblock    chan struct{}
	commentHit int32
}

func (b *blockingCommentTracker) Comment(num, body string) error {
	atomic.AddInt32(&b.commentHit, 1)
	<-b.unblock
	return b.IssueTracker.Comment(num, body)
}

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
	launch = &Launcher{CodeForge: fc, Factory: factory, Settle: s, queue: NewQueue()}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})
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

	snap := launch.queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickTerminated {
		t.Errorf("queue pick = %+v, want PickTerminated", snap)
	}
}

// TestLauncher_Terminate_DuringMergeGate_ClearsCompleteLabel verifies
// Terminate cleanly returns the issue to Dispatchable even when it already
// carries Complete -- selfHeal now holds that swap until the landing path
// settles (issue #757), but Terminate can still race a settle that completed
// just before it ran. Terminate must not leave the issue holding both
// Complete and Dispatchable at once.
func TestLauncher_Terminate_DuringMergeGate_ClearsCompleteLabel(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Complete: "agent-complete"}
	fc := forge.NewFake(labels)
	fc.BranchPrefix = "agent/issue-"
	// Simulates the landing path having just settled (selfHeal's swap to
	// Complete) right before Terminate is called.
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
	launch := &Launcher{CodeForge: fc, Factory: factory, Settle: s, queue: NewQueue()}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

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

// TestLauncher_Terminate_PropagatesKillError verifies a non-nil
// Factory.Kill error surfaces from Terminate's return unmasked, while every
// other best-effort step (transition, comment, AppendTerminalLine,
// PickTerminated) still runs regardless (issue #749).
func TestLauncher_Terminate_PropagatesKillError(t *testing.T) {
	launch, fc, fr, dir := newTermTestLauncher(t)
	fr.KillErr = errors.New("boom: kill failed")

	err := launch.Terminate(fc, "42")
	if err != fr.KillErr {
		t.Fatalf("Terminate err = %v, want %v", err, fr.KillErr)
	}

	if len(fc.TransitionStateCalls) != 2 {
		t.Errorf("TransitionStateCalls: want 2 despite kill error, got %+v", fc.TransitionStateCalls)
	}
	if len(fc.CommentCalls) != 1 {
		t.Errorf("CommentCalls: want 1 despite kill error, got %+v", fc.CommentCalls)
	}

	logPath := filepath.Join(dir, "logs", "issue-42.log")
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "terminate") {
		t.Errorf("Box log = %q, want it to carry a terminal line despite kill error", got)
	}

	snap := launch.queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickTerminated {
		t.Errorf("queue pick = %+v, want PickTerminated despite kill error", snap)
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

	launch := &Launcher{CodeForge: fc, Factory: factory, Settle: s, queue: NewQueue()}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	// Terminate: issue -> Dispatchable, registry marks #42, PR left dangling.
	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	fc.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/owner/repo/pull/7"})
	// A leading PENDING proves this run's own checks registered — issue
	// #1652's adopted-path gate does not trust an immediate SUCCESS alone.
	fc.SetCheckStates("https://github.com/owner/repo/pull/7", []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})

	// Re-pick: a fresh claim must not inherit the stale terminate mark.
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(fc, dir)

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := launch.queue.Snapshot()
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

// TestLauncher_TerminateAsync_ReturnsBeforeTrackerCallCompletes verifies
// TerminateAsync backgrounds Terminate's blocking tracker I/O (issue #745):
// the call returns immediately even while the tracker's Comment call is
// still blocked, and the queue pick only reaches PickTerminated once that
// blocked call is allowed to finish.
func TestLauncher_TerminateAsync_ReturnsBeforeTrackerCallCompletes(t *testing.T) {
	launch, fc, _, _ := newTermTestLauncher(t)
	bt := &blockingCommentTracker{IssueTracker: fc, unblock: make(chan struct{})}

	returned := make(chan struct{})
	go func() {
		launch.TerminateAsync(bt, "42")
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("TerminateAsync never returned while Comment was blocked")
	}

	snap := launch.queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickRunning {
		t.Fatalf("queue pick = %+v, want still PickRunning before Comment unblocks", snap)
	}

	close(bt.unblock)

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := launch.queue.Snapshot()
		if len(snap) == 1 && snap[0].State == PickTerminated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue pick never reached PickTerminated: %+v", snap)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestLauncher_TerminateAsync_DuplicateWhileInFlight_IsNoOp verifies a
// second TerminateAsync call for the same issue, fired while the first is
// still blocked on tracker I/O, does not fire a second Kill/Comment for it
// (issue #745) — the race a second "y" confirm on the same row hits while
// isLive still reports the pick PickRunning.
func TestLauncher_TerminateAsync_DuplicateWhileInFlight_IsNoOp(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)
	bt := &blockingCommentTracker{IssueTracker: fc, unblock: make(chan struct{})}

	launch.TerminateAsync(bt, "42")
	launch.TerminateAsync(bt, "42") // duplicate while the first is still blocked

	close(bt.unblock)
	launch.Wait()

	if got := atomic.LoadInt32(&bt.commentHit); got != 1 {
		t.Errorf("Comment calls = %d, want exactly 1", got)
	}
	if len(fr.KillCalls) != 1 {
		t.Errorf("KillCalls = %v, want exactly one kill", fr.KillCalls)
	}
}

// TestLauncher_Terminate_MarksRegistry verifies the shared termination
// registry records the issue, so an in-flight settle loop checking it (via
// Settle.SetTerminated) notices on its next checkpoint.
func TestLauncher_Terminate_MarksRegistry(t *testing.T) {
	launch, fc, _, _ := newTermTestLauncher(t)
	gen := launch.registry().Begin("42")

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if !launch.registry().Marked("42", gen) {
		t.Error("registry: want #42 marked terminated")
	}
}

// TestLauncher_terminating_LazilyConstructsMap verifies terminating() mirrors
// registry()/limiter()/refreshChan(): a bare struct literal's nil map is
// lazily constructed on first call, so no constructor is needed at any
// production or test call site.
func TestLauncher_terminating_LazilyConstructsMap(t *testing.T) {
	launch := &Launcher{}

	got := launch.terminating()
	if got == nil {
		t.Fatal("terminating() = nil, want non-nil map")
	}
	if len(got) != 0 {
		t.Fatalf("terminating() = %v, want empty map", got)
	}
}
