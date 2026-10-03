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

	factory, fr, dir := newTermFactory(t)

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
// the settle.Registrar seam, reading the same registry) notices the terminate
// on its next checkpoint.
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

// TestLauncher_registry_UsesSettlersOwnRegistry pins that the Console's handle
// and its settler's are one registry. main.go's recover path acquires the
// settler's registry independently of the session; when that acquisition
// installed a registry instead of reading one, a later operator Terminate
// marked a registry no in-flight settle goroutine ever checked, and the settle
// drove the reclaimed issue to agent-complete out from under it (#3522).
func TestLauncher_registry_UsesSettlersOwnRegistry(t *testing.T) {
	launch, _, _, _ := newTermTestLauncher(t)
	st, ok := launch.Settle.(*settle.Settle)
	if !ok {
		t.Fatalf("Settle = %T, want *settle.Settle", launch.Settle)
	}

	gen := launch.registry().Begin("42")
	launch.registry().Mark("42")
	if !st.Registry().Marked("42", gen) {
		t.Error("settler registry: want #42 marked through the Console's handle")
	}

	gen43 := st.Registry().Begin("43")
	st.Registry().Mark("43")
	if !launch.registry().Marked("43", gen43) {
		t.Error("Console registry: want #43 marked through the settler's handle")
	}
}

// newTermFactory builds a Factory on its own runner.Fake, so a test can tell
// which of two Factories a Terminate reaped through.
func newTermFactory(t *testing.T) (factory *dispatch.Factory, fr *runner.Fake, dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	fr = runner.NewFake()
	factory, err = dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)
	return factory, fr, dir
}

func newResearchFake(nums ...string) *forge.Fake {
	rf := forge.NewFake(forge.ResearchDispatchLabels())
	for _, n := range nums {
		rf.SetIssue(forge.Issue{Number: n, Title: "research it", Labels: []string{"agent-research-in-progress"}})
	}
	return rf
}

// TestLauncher_Terminate_ResearchPick_RoutesToResearchStack pins that a
// running research pick is reclaimed through the research tracker and reaped
// by the research Factory, whichever tracker the caller passes (issue #4148),
// and that its comment never invents a branch note: research is advise-only.
func TestLauncher_Terminate_ResearchPick_RoutesToResearchStack(t *testing.T) {
	launch, fc, workRunner, _ := newTermTestLauncher(t)
	rf := newResearchFake("42")
	rFactory, researchRunner, _ := newTermFactory(t)
	launch.ResearchTracker = rf
	launch.ResearchFactory = rFactory
	launch.queue = NewQueue()
	launch.queue.Add(Pick{Number: "42", Title: "research it", Kind: KindResearch, State: PickRunning})

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if len(researchRunner.KillCalls) != 1 || researchRunner.KillCalls[0] != "agent-issue-42" {
		t.Errorf("research KillCalls: want [agent-issue-42], got %v", researchRunner.KillCalls)
	}
	if len(workRunner.KillCalls) != 0 {
		t.Errorf("work KillCalls: want none, got %v", workRunner.KillCalls)
	}
	if len(rf.TransitionStateCalls) != 2 {
		t.Errorf("research TransitionStateCalls: want 2, got %+v", rf.TransitionStateCalls)
	}
	if len(fc.TransitionStateCalls) != 0 || len(fc.CommentCalls) != 0 {
		t.Errorf("work tracker touched: transitions=%+v comments=%+v", fc.TransitionStateCalls, fc.CommentCalls)
	}
	if len(rf.CommentCalls) != 1 {
		t.Fatalf("research CommentCalls: want 1, got %+v", rf.CommentCalls)
	}
	if body := rf.CommentCalls[0].Body; strings.Contains(body, "branch=") || strings.Contains(body, "https://") {
		t.Errorf("advise-only comment must resolve no branch or PR: %q", body)
	}
	iss, err := rf.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(iss.Labels, "agent-research") || containsString(iss.Labels, "agent-research-in-progress") {
		t.Errorf("research labels = %v, want agent-research back, in-progress cleared", iss.Labels)
	}
	if snap := launch.queue.Snapshot(); len(snap) != 1 || snap[0].State != PickTerminated {
		t.Errorf("queue pick = %+v, want PickTerminated", snap)
	}
}

// TestLauncher_Terminate_ResearchPick_UnwiredFallsBackToWork pins that a
// research pick with no research stack wired reclaims through the caller's
// tracker and the work Factory rather than panicking.
func TestLauncher_Terminate_ResearchPick_UnwiredFallsBackToWork(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)
	launch.queue = NewQueue()
	launch.queue.Add(Pick{Number: "42", Title: "research it", Kind: KindResearch, State: PickRunning})

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if len(fr.KillCalls) != 1 {
		t.Errorf("work KillCalls: want 1, got %v", fr.KillCalls)
	}
	if len(fc.TransitionStateCalls) != 2 || len(fc.CommentCalls) != 1 {
		t.Errorf("work tracker: transitions=%+v comments=%+v", fc.TransitionStateCalls, fc.CommentCalls)
	}
}

// TestLauncher_Terminate_ResearchPick_OnlyTrackerWired_ReclaimsThroughResearchTracker
// pins that a research pick whose Factory is unwired still reclaims through
// the research tracker Pick claimed it on, reaping via the work Factory.
func TestLauncher_Terminate_ResearchPick_OnlyTrackerWired_ReclaimsThroughResearchTracker(t *testing.T) {
	launch, fc, workRunner, _ := newTermTestLauncher(t)
	rf := newResearchFake("42")
	launch.ResearchTracker = rf
	launch.queue = NewQueue()
	launch.queue.Add(Pick{Number: "42", Title: "research it", Kind: KindResearch, State: PickRunning})

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if len(workRunner.KillCalls) != 1 {
		t.Errorf("work KillCalls: want 1, got %v", workRunner.KillCalls)
	}
	if len(rf.TransitionStateCalls) != 2 || len(rf.CommentCalls) != 1 {
		t.Errorf("research tracker: transitions=%+v comments=%+v", rf.TransitionStateCalls, rf.CommentCalls)
	}
	if len(fc.TransitionStateCalls) != 0 || len(fc.CommentCalls) != 0 {
		t.Errorf("work tracker touched: transitions=%+v comments=%+v", fc.TransitionStateCalls, fc.CommentCalls)
	}
}

// TestLauncher_Terminate_MixedQueue_EachPickRoutesToItsOwnStack pins the
// terminate-all shape: both picks are handed the work tracker, each lands on
// its own kind's tracker and Factory.
func TestLauncher_Terminate_MixedQueue_EachPickRoutesToItsOwnStack(t *testing.T) {
	launch, fc, workRunner, _ := newTermTestLauncher(t)
	fc.SetIssue(forge.Issue{Number: "7", Title: "work", Labels: []string{"agent-in-progress"}})
	rf := newResearchFake("43")
	rFactory, researchRunner, _ := newTermFactory(t)
	launch.ResearchTracker = rf
	launch.ResearchFactory = rFactory
	launch.queue.Add(Pick{Number: "43", Title: "research it", Kind: KindResearch, State: PickRunning})

	live := launch.LiveIssues()
	if len(live) != 2 || live[0] != "42" || live[1] != "43" {
		t.Fatalf("LiveIssues = %v, want [42 43]", live)
	}
	for _, num := range live {
		launch.TerminateAsync(fc, num)
	}
	launch.Wait()

	if len(workRunner.KillCalls) != 1 || workRunner.KillCalls[0] != "agent-issue-42" {
		t.Errorf("work KillCalls: want [agent-issue-42], got %v", workRunner.KillCalls)
	}
	if len(researchRunner.KillCalls) != 1 || researchRunner.KillCalls[0] != "agent-issue-43" {
		t.Errorf("research KillCalls: want [agent-issue-43], got %v", researchRunner.KillCalls)
	}
	if len(fc.CommentCalls) != 1 || fc.CommentCalls[0].Num != "42" {
		t.Errorf("work comments: want one for #42, got %+v", fc.CommentCalls)
	}
	if len(rf.CommentCalls) != 1 || rf.CommentCalls[0].Num != "43" {
		t.Errorf("research comments: want one for #43, got %+v", rf.CommentCalls)
	}
}

// TestLauncher_Terminate_RunningResearchRowUnderNewerQueuedWork_RoutesToResearch
// pins that Terminate targets the running Box, not the newest row: LiveIssues
// reports the running research row, so the kill, reclaim, and PickTerminated
// mark must all land on it, leaving the newer queued work row untouched.
func TestLauncher_Terminate_RunningResearchRowUnderNewerQueuedWork_RoutesToResearch(t *testing.T) {
	launch, fc, workRunner, _ := newTermTestLauncher(t)
	rf := newResearchFake("42")
	rFactory, researchRunner, _ := newTermFactory(t)
	launch.ResearchTracker = rf
	launch.ResearchFactory = rFactory
	launch.queue = NewQueue()
	launch.queue.Add(Pick{Number: "42", Title: "research it", Kind: KindResearch, State: PickRunning})
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	if live := launch.LiveIssues(); len(live) != 1 || live[0] != "42" {
		t.Fatalf("LiveIssues = %v, want [42]", live)
	}
	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if len(researchRunner.KillCalls) != 1 || len(workRunner.KillCalls) != 0 {
		t.Errorf("KillCalls: research=%v work=%v, want research only", researchRunner.KillCalls, workRunner.KillCalls)
	}
	if len(rf.TransitionStateCalls) != 2 || len(rf.CommentCalls) != 1 {
		t.Errorf("research tracker: transitions=%+v comments=%+v", rf.TransitionStateCalls, rf.CommentCalls)
	}
	if len(fc.TransitionStateCalls) != 0 || len(fc.CommentCalls) != 0 {
		t.Errorf("work tracker touched: transitions=%+v comments=%+v", fc.TransitionStateCalls, fc.CommentCalls)
	}
	snap := launch.queue.Snapshot()
	if len(snap) != 2 || snap[0].State != PickTerminated || snap[1].State != PickQueued {
		t.Errorf("queue = %+v, want [research terminated, work queued]", snap)
	}
}

// TestLauncher_Terminate_RunningWorkRowUnderNewerQueuedResearch_RoutesToWork
// is the mirror: a running work row routes to the work stack even when a newer
// research row for the same number is queued.
func TestLauncher_Terminate_RunningWorkRowUnderNewerQueuedResearch_RoutesToWork(t *testing.T) {
	launch, fc, workRunner, _ := newTermTestLauncher(t)
	rf := newResearchFake("42")
	rFactory, researchRunner, _ := newTermFactory(t)
	launch.ResearchTracker = rf
	launch.ResearchFactory = rFactory
	launch.queue.Add(Pick{Number: "42", Title: "research it", Kind: KindResearch, State: PickQueued})

	if err := launch.Terminate(fc, "42"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if len(workRunner.KillCalls) != 1 || len(researchRunner.KillCalls) != 0 {
		t.Errorf("KillCalls: work=%v research=%v, want work only", workRunner.KillCalls, researchRunner.KillCalls)
	}
	if len(fc.TransitionStateCalls) != 2 || len(fc.CommentCalls) != 1 {
		t.Errorf("work tracker: transitions=%+v comments=%+v", fc.TransitionStateCalls, fc.CommentCalls)
	}
	if len(rf.TransitionStateCalls) != 0 || len(rf.CommentCalls) != 0 {
		t.Errorf("research tracker touched: transitions=%+v comments=%+v", rf.TransitionStateCalls, rf.CommentCalls)
	}
	snap := launch.queue.Snapshot()
	if len(snap) != 2 || snap[0].State != PickTerminated || snap[1].State != PickQueued {
		t.Errorf("queue = %+v, want [work terminated, research queued]", snap)
	}
}
