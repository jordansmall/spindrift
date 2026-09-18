package console

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// blockingCommentTracker holds every Comment call until unblock closes, so
// TestLauncher_TerminateAsync_ReturnsBeforeTrackerCallCompletes can prove
// TerminateAsync's goroutine, not its caller, waits on the network I/O.
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

func newTermTestLauncher(t *testing.T) (launch *Launcher, fc *forge.Fake, fr *runner.Fake, dir string) {
	t.Helper()
	labels := forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"}
	fc = forge.NewFake(labels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
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

	s := settle.New(settle.Config{
		MergeMode:     "manual",
		CompleteLabel: "agent-complete",
		Capabilities:  forge.ResolveCapabilities(fc, fc, backend.Descriptor{}, backend.Descriptor{}),
	}, fc, fc)
	launch = &Launcher{CodeForge: fc, Factory: factory, Settle: s, queue: NewQueue()}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})
	return launch, fc, fr, dir
}

// TestLauncher_Terminate_ReapsTransitionsAndComments pins the full ADR 0024
// action: the Box is reaped by its deterministic name, the issue transitions
// from InProgress to Dispatchable (never Failed, never a new tracker state),
// a comment names the terminate and links the open PR, and the queue pick
// lands PickTerminated.
func TestLauncher_Terminate_ReapsTransitionsAndComments(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)
	fc.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/owner/repo/pull/7"})

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if len(fr.KillCalls) != 1 || fr.KillCalls[0] != "agent-issue-42" {
		t.Errorf("KillCalls: want [agent-issue-42], got %v", fr.KillCalls)
	}

	// Terminate clears both possible "from" labels, InProgress for a running
	// Box or CI watch and Complete if it landed during the merge gate (see
	// TestLauncher_Terminate_DuringMergeGate_ClearsCompleteLabel), since it
	// cannot know which one is present without adapter-specific label
	// inspection.
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

// TestLauncher_Terminate_DuringMergeGate_ClearsCompleteLabel pins that
// Terminate returns the issue to Dispatchable even when it already carries
// Complete. selfHeal holds that swap until the landing path settles (issue
// #757), but Terminate can still race a settle that finished just before it
// ran, and must not leave both labels on the issue at once.
func TestLauncher_Terminate_DuringMergeGate_ClearsCompleteLabel(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Complete: "agent-complete"}
	fc := forge.NewFake(labels)
	fc.BranchPrefix = "agent/issue-"
	// The agent-complete label simulates selfHeal's swap landing just before
	// Terminate runs.
	fc.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-complete"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
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
	s := settle.New(settle.Config{
		MergeMode:     "manual",
		CompleteLabel: "agent-complete",
		Capabilities:  forge.ResolveCapabilities(fc, fc, backend.Descriptor{}, backend.Descriptor{}),
	}, fc, fc)
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

// TestLauncher_Terminate_PropagatesKillError pins that a non-nil Factory.Kill
// error returns from Terminate unmasked while every other best-effort step
// (transition, comment, AppendTerminalLine, PickTerminated) still runs
// (issue #749).
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

	logPath := filepath.Join(dir, ".spindrift", "logs", "issue-42.log")
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

// TestLauncher_Terminate_NoOpenPR_CommentNotesNone pins that the comment
// still posts, and invents no link, when the issue never got a PR.
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

func TestLauncher_Terminate_AppendsBoxLogTerminalLine(t *testing.T) {
	launch, fc, _, dir := newTermTestLauncher(t)
	logPath := filepath.Join(dir, ".spindrift", "logs", "issue-42.log")
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

// TestLauncher_TerminateThenRepick_NoOutcomeReportsBlocked pins the
// terminate-then-repick reclaim loop end to end (ADR 0024, issue #649):
// re-picking a terminated issue dispatches a fresh Box that writes no outcome
// line, the prior run's stale terminate mark does not abandon it, and the
// no-outcome path never adopts a PR off draft-ness (issue #1654).
func TestLauncher_TerminateThenRepick_NoOutcomeReportsBlocked(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Complete: "agent-complete"}
	fc := forge.NewFake(labels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
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
		Capabilities:      forge.ResolveCapabilities(fc, fc, backend.Descriptor{}, backend.Descriptor{}),
	}, fc, fc)

	launch := &Launcher{CodeForge: fc, Factory: factory, Settle: s, queue: NewQueue()}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	fc.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/owner/repo/pull/7"})

	// The fresh claim must not inherit the stale terminate mark.
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

	// A no-outcome run is never adopted off draft-ness (issue #1654): the
	// re-picked run finds #7 open and non-draft but reports blocked instead
	// of merging it.
	if fc.Merged != "" {
		t.Errorf("Merged = %q, want no merge (no-outcome runs are never adopted off draft-ness)", fc.Merged)
	}
}

// TestLauncher_TerminateAsync_ReturnsBeforeTrackerCallCompletes pins that
// TerminateAsync backgrounds Terminate's blocking tracker I/O (issue #745):
// it returns while the tracker's Comment call is still blocked, and the queue
// pick only reaches PickTerminated once that call finishes.
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

// TestLauncher_TerminateAsync_DuplicateWhileInFlight_IsNoOp pins that a
// second TerminateAsync call for the same issue, fired while the first is
// still blocked on tracker I/O, fires no second Kill or Comment (issue #745).
// A second "y" confirm on the same row hits this race while isLive still
// reports the pick PickRunning.
func TestLauncher_TerminateAsync_DuplicateWhileInFlight_IsNoOp(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)
	bt := &blockingCommentTracker{IssueTracker: fc, unblock: make(chan struct{})}

	launch.TerminateAsync(bt, "42")
	launch.TerminateAsync(bt, "42")

	close(bt.unblock)
	launch.Wait()

	if got := atomic.LoadInt32(&bt.commentHit); got != 1 {
		t.Errorf("Comment calls = %d, want exactly 1", got)
	}
	if len(fr.KillCalls) != 1 {
		t.Errorf("KillCalls = %v, want exactly one kill", fr.KillCalls)
	}
}

// TestLauncher_Terminate_MarksRegistry pins the mark in the shared
// termination registry, which is how an in-flight settle loop (via
// Settle.SetTerminated) notices the terminate on its next checkpoint.
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

// TestLauncher_terminating_LazilyConstructsMap pins that terminating() builds
// a bare struct literal's nil map on first call, as registry(), limiter() and
// refreshChan() do, so no call site needs a constructor.
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
