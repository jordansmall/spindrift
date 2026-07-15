package console

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// TestRun_QuitCommand_ExitsCleanly verifies "q" ends the loop and returns
// without error, after rendering the initial backlog once.
func TestRun_QuitCommand_ExitsCleanly(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("q\n"), &out, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "first") {
		t.Errorf("output = %q, want the initial backlog rendered", out.String())
	}
}

// TestRun_EOF_ExitsCleanlyWithoutQuitCommand verifies Run returns once input
// runs out, even without an explicit "q" — a scripted test reader (or a
// closed pipe) must never hang the loop.
func TestRun_EOF_ExitsCleanlyWithoutQuitCommand(t *testing.T) {
	f := forge.NewFake()
	if err := Run(f, t.TempDir(), strings.NewReader(""), &strings.Builder{}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRun_RefreshCommand_ReQueriesTracker verifies "r" re-queries the
// tracker and re-renders — an issue added to the tracker after Run starts
// appears only once "r" is sent.
func TestRun_RefreshCommand_ReQueriesTracker(t *testing.T) {
	f := forge.NewFake()

	var out strings.Builder
	in := strings.NewReader("r\nq\n")
	if err := Run(f, t.TempDir(), in, &out, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	f.SetIssue(forge.Issue{Number: "5", Title: "late arrival", State: forge.IssueOpen})

	out.Reset()
	in = strings.NewReader("r\nq\n")
	if err := Run(f, t.TempDir(), in, &out, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "late arrival") {
		t.Errorf("output after refresh = %q, want the newly added issue", out.String())
	}
}

// TestRun_FilterCommand_Narrows verifies "f <label>" renders only issues
// carrying a matching label. Run always renders the full unfiltered backlog
// first (before any command is read), so "alpha" legitimately appears once
// from that initial render — the assertion is that the *second* render,
// triggered by "f b", omits it, which a raw single-occurrence count of each
// title across the whole transcript captures directly: alpha appears only
// in the initial render (once), beta appears in both (twice).
func TestRun_FilterCommand_Narrows(t *testing.T) {
	f := newAlphaBetaFake()

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("f b\nq\n"), &out, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Count(out.String(), "alpha"); got != 1 {
		t.Errorf("alpha occurrences = %d, want 1 (initial render only)", got)
	}
	if got := strings.Count(out.String(), "beta"); got != 2 {
		t.Errorf("beta occurrences = %d, want 2 (initial + filtered render)", got)
	}
}

// TestRun_BareFilterCommand_RestoresFullList verifies a bare "f" clears an
// active filter, restoring issues the prior filter had narrowed out: alpha
// reappears in the third render (initial, filtered-out, restored).
func TestRun_BareFilterCommand_RestoresFullList(t *testing.T) {
	f := newAlphaBetaFake()

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("f b\nf\nq\n"), &out, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Count(out.String(), "alpha"); got != 2 {
		t.Errorf("alpha occurrences = %d, want 2 (initial + restored render)", got)
	}
}

// TestRun_PickCommand_PromotesAndQueues verifies "p <num>" promotes the
// named issue on the tracker and renders it queued — the operator's launch
// button (#646).
func TestRun_PickCommand_PromotesAndQueues(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("p 42\nq\n"), &out, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "queued") {
		t.Errorf("output = %q, want the pick rendered queued", out.String())
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(iss, "ready-for-agent") {
		t.Errorf("issue #42 labels = %v, want ready-for-agent promoted onto it", iss.Labels)
	}
}

// TestRun_PickAllReadyCommand_QueuesExactlyTheDispatchableSet verifies
// "pa" picks exactly the issues currently Dispatchable on the tracker, and
// nothing else — an explicit action, never standing discovery (#647 AC3).
func TestRun_PickAllReadyCommand_QueuesExactlyTheDispatchableSet(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "first", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Title: "second", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "44", Title: "not triaged", State: forge.IssueOpen})

	launch := newTestLauncher(t, f)

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("pa\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := launch.Queue.Snapshot()
		if len(snap) == 2 && snap[0].State == PickSettled && snap[1].State == PickSettled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("picks never settled: %+v", snap)
		}
		time.Sleep(time.Millisecond)
	}

	snap := launch.Queue.Snapshot()
	got := map[string]bool{snap[0].Number: true, snap[1].Number: true}
	if !got["42"] || !got["43"] {
		t.Errorf("picked = %+v, want #42 and #43 only", snap)
	}
}

// TestRun_UnpickCommand_RemovesFromQueue verifies "u <num>" removes a
// queued pick and makes zero further tracker calls.
func TestRun_UnpickCommand_RemovesFromQueue(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("p 42\nu 42\nq\n"), &out, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	callsAfterPick := len(f.TransitionStateCalls)
	if callsAfterPick != 1 {
		t.Fatalf("TransitionStateCalls after pick+unpick = %d, want 1 (unpick makes none)", callsAfterPick)
	}
	if strings.Count(out.String(), "queued") != 1 {
		t.Errorf("output = %q, want \"queued\" to appear only in the pick's own render, not after unpick", out.String())
	}
}

// TestRun_PickCommand_WithLauncher_LaunchesRealDispatch verifies that when
// Run is given a Launcher, "p <num>" doesn't just queue the pick — it
// drives the pick through the continuous engine to settle, in the
// background, so the read loop stays responsive (#646).
func TestRun_PickCommand_WithLauncher_LaunchesRealDispatch(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})

	launch := newTestLauncher(t, f)

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("p 42\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := launch.Queue.Snapshot()
		if len(snap) == 1 && snap[0].State == PickSettled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pick never settled: %+v", snap)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRun_WithLauncher_RendersLiveQueueState verifies every render — not
// just the one right after a pick — reflects the launcher's live Queue
// state, so a transition that happens entirely in the background (claim,
// run, settle, or a raced-claim dissolve) actually reaches the operator's
// screen instead of freezing at "queued" (#646 AC4, AC6).
func TestRun_WithLauncher_RendersLiveQueueState(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	// Simulate a state transition that happened entirely on the background
	// Queue (a real launch's claim/run/settle), bypassing Run's own "p"
	// handling so this test isolates the render-sync behavior.
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickDissolved, Reason: "issue is closed"})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("r\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "dissolved") || !strings.Contains(out.String(), "issue is closed") {
		t.Errorf("output = %q, want the background dissolve reflected on a later render", out.String())
	}
}

// TestRun_BarePickCommand_IsNoop verifies a bare "p" (no issue number) makes
// no tracker call and queues nothing, instead of promoting a phantom pick
// numbered "".
func TestRun_BarePickCommand_IsNoop(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("p\nq\n"), &out, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none for a bare pick", f.TransitionStateCalls)
	}
}

// TestRun_PickCommand_WithLauncher_PromotionFails_DissolvedRowSurvivesRender
// verifies a failed promotion's dissolved row is landed on launch.Queue too
// — not just Model.Picks via PickFailedMsg — so it survives Run's
// per-render resync from the live Queue instead of vanishing on the very
// next render (#646 AC6).
func TestRun_PickCommand_WithLauncher_PromotionFails_DissolvedRowSurvivesRender(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	f.TransitionStateErr = errBoom

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("p 42\nr\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Count(out.String(), "dissolved") != 2 {
		t.Errorf("output = %q, want the dissolved row on both post-pick renders (after \"p 42\" and after \"r\"), not just the first", out.String())
	}
}

// TestRun_SettleTriggersAutoRefresh_NoExplicitRefreshCommand verifies a
// settle — the session's own tracker write — fires a backlog refresh on its
// own, so an issue added to the tracker while a Box is running appears on a
// render once that Box settles, without the operator ever sending "r"
// (#647 AC4).
func TestRun_SettleTriggersAutoRefresh_NoExplicitRefreshCommand(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})

	fr := runner.NewFake()
	release := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		<-release
		return nil
	}
	dir := t.TempDir()
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
	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}

	inR, inW := io.Pipe()
	var out syncBuilder
	done := make(chan error, 1)
	go func() { done <- Run(f, dir, inR, &out, launch) }()

	if _, err := inW.Write([]byte("p 42\n")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := launch.Queue.Snapshot()
		if len(snap) == 1 && snap[0].State == PickRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pick never started running: %+v", snap)
		}
		time.Sleep(time.Millisecond)
	}

	f.SetIssue(forge.Issue{Number: "99", Title: "late arrival", State: forge.IssueOpen})
	close(release)

	deadline = time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), "late arrival") {
		if time.Now().After(deadline) {
			t.Fatalf("output = %q, want the late-arriving issue after a settle-triggered auto-refresh, no \"r\" sent", out.String())
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := inW.Write([]byte("q\n")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRun_BackgroundPoll_RefreshesWithoutOperatorInputOrTrackerWrite verifies
// a slow fixed background poll re-queries the tracker on its own — no
// operator command, no settle/promotion/claim — so a late-arriving issue
// still surfaces during an otherwise-idle session (#647 AC5).
func TestRun_BackgroundPoll_RefreshesWithoutOperatorInputOrTrackerWrite(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})

	launch := &Launcher{CodeForge: f, Queue: NewQueue(), pollInterval: 5 * time.Millisecond}

	inR, inW := io.Pipe()
	var out syncBuilder
	done := make(chan error, 1)
	go func() { done <- Run(f, t.TempDir(), inR, &out, launch) }()

	f.SetIssue(forge.Issue{Number: "2", Title: "late arrival", State: forge.IssueOpen})

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), "late arrival") {
		if time.Now().After(deadline) {
			t.Fatalf("output = %q, want the late-arriving issue after a background poll, no command sent", out.String())
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := inW.Write([]byte("q\n")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRun_HeldPick_LaunchesOnBackgroundPollAfterDrainIdles verifies a pick
// held on a blocker that clears out-of-band — no sibling Dispatch in this
// session settles to trigger a refill — still launches with no further
// operator action, once the background poll notices (#650).
func TestRun_HeldPick_LaunchesOnBackgroundPollAfterDrainIdles(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "41", State: forge.IssueOpen}) // #42's blocker, unmet at pick time
	f.NativeDeps = map[string][]string{"42": {"41"}}

	fr := runner.NewFake()
	dir := t.TempDir()
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
	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue(), pollInterval: 5 * time.Millisecond}

	inR, inW := io.Pipe()
	var out syncBuilder
	done := make(chan error, 1)
	go func() { done <- Run(f, dir, inR, &out, launch) }()

	if _, err := inW.Write([]byte("p 42\n")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), "held") {
		if time.Now().After(deadline) {
			t.Fatalf("output = %q, want #42 held on its open blocker", out.String())
		}
		time.Sleep(time.Millisecond)
	}

	f.SetIssue(forge.Issue{Number: "41", State: forge.IssueClosed})

	deadline = time.Now().Add(2 * time.Second)
	for len(fr.RunCalls) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("RunCalls = %v, want #42 dispatched once its blocker cleared, with no further operator action", fr.RunCalls)
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := inW.Write([]byte("q\n")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRun_RunningPick_RendersLiveHeartbeat verifies a running row's render
// picks up the heartbeat line the Driver's own heartbeat parser emits as the
// Box's log grows — scannable without drilling in (#647 AC2).
func TestRun_RunningPick_RendersLiveHeartbeat(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})

	fr := runner.NewFake()
	release := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		fmt.Fprintln(box.Output, `{"type":"result","num_turns":3}`)
		<-release
		return nil
	}
	dir := t.TempDir()
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
	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue(), pollInterval: 5 * time.Millisecond}

	inR, inW := io.Pipe()
	var out syncBuilder
	done := make(chan error, 1)
	go func() { done <- Run(f, dir, inR, &out, launch) }()

	if _, err := inW.Write([]byte("p 42\n")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), "3 turn") {
		if time.Now().After(deadline) {
			t.Fatalf("output = %q, want a rendered heartbeat line containing \"3 turn\"", out.String())
		}
		time.Sleep(time.Millisecond)
	}

	close(release)
	if _, err := inW.Write([]byte("q\n")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// syncBuilder is a strings.Builder safe for concurrent Write/String calls —
// Run's goroutine and the test's polling goroutine both touch out.
type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newAlphaBetaFake returns a Fake tracker with two open issues, "alpha"
// labeled "a" and "beta" labeled "b" — shared fixture for filter tests.
func newAlphaBetaFake() *forge.Fake {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "alpha", State: forge.IssueOpen, Labels: []string{"a"}})
	f.SetIssue(forge.Issue{Number: "2", Title: "beta", State: forge.IssueOpen, Labels: []string{"b"}})
	return f
}

// newTestLauncher builds a Launcher wired to a runner.Fake Box and a
// settle.Fake, matching the waves package's own dispatch.Factory test
// helper — enough plumbing to prove a picked issue runs a real (fake) Box
// and settles.
func newTestLauncher(t *testing.T, cf forge.CodeForge) *Launcher {
	t.Helper()
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
	return &Launcher{CodeForge: cf, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
}

// TestRun_DrillCommand_ShowsRenderedTranscript verifies "d <num>" loads the
// issue's Dispatch logs through the Launcher's Driver and renders the
// transcript view, replacing the backlog on the next render (#648).
func TestRun_DrillCommand_ShowsRenderedTranscript(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	launch := newTestLauncher(t, f)

	var out strings.Builder
	if err := Run(f, dir, strings.NewReader("d 42\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "working on it") {
		t.Errorf("output = %q, want the rendered transcript", out.String())
	}
}

// TestRun_ToggleCommand_SwitchesToRawLog verifies "t" swaps the drill-in
// view from rendered to the raw byte-exact log.
func TestRun_ToggleCommand_SwitchesToRawLog(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	launch := newTestLauncher(t, f)

	var out strings.Builder
	if err := Run(f, dir, strings.NewReader("d 42\nt\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), `"type":"assistant"`) {
		t.Errorf("output = %q, want the raw log after toggling", out.String())
	}
}

// TestRun_CloseCommand_ReturnsToBacklog verifies "x" leaves the drill-in
// view and restores the backlog rendering.
func TestRun_CloseCommand_ReturnsToBacklog(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	launch := newTestLauncher(t, f)

	var out strings.Builder
	if err := Run(f, dir, strings.NewReader("d 42\nx\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "fix the thing") {
		t.Errorf("output = %q, want the backlog restored after close", out.String())
	}
}

// TestRun_DrillCommand_NoLauncher_SurfacesError verifies "d <num>" with no
// Launcher (a launch-less session) surfaces an error instead of panicking
// on a nil Driver.
func TestRun_DrillCommand_NoLauncher_SurfacesError(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("d 42\nq\n"), &out, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "no Driver configured") {
		t.Errorf("output = %q, want a no-Driver error surfaced", out.String())
	}
}
