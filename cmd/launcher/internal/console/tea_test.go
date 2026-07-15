package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	teatest "github.com/charmbracelet/x/exp/teatest"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// waitForOutput blocks until tm's output contains every one of want, failing
// the test if it never does within a bounded duration. tm.Output() drains as
// it's read, so every substring a caller needs from one render must be
// awaited together in a single call — a second call only ever sees bytes
// written after the first call's read, not the full history.
func waitForOutput(t *testing.T, tm *teatest.TestModel, want ...string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		for _, w := range want {
			if !strings.Contains(string(b), w) {
				return false
			}
		}
		return true
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(5*time.Millisecond))
}

func sendKey(tm *teatest.TestModel, s string) {
	switch s {
	case "enter":
		tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	case "esc":
		tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	case "backspace":
		tm.Send(tea.KeyMsg{Type: tea.KeyBackspace})
	case "up":
		tm.Send(tea.KeyMsg{Type: tea.KeyUp})
	case "down":
		tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	default:
		tm.Type(s)
	}
}

// TestTea_InitialRender_ShowsBacklog verifies the program's first render
// loads and shows the open backlog with no operator input (issue #784).
func TestTea_InitialRender_ShowsBacklog(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "first")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_QuitKey_ExitsCleanly verifies "q" ends the program.
func TestTea_QuitKey_ExitsCleanly(t *testing.T) {
	f := forge.NewFake()
	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_RefreshKey_ReQueriesTracker verifies "r" re-queries the tracker
// and re-renders — an issue added after the program starts appears only
// once "r" is sent.
func TestTea_RefreshKey_ReQueriesTracker(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "first") // wait for at least one render before mutating the fixture

	f.SetIssue(forge.Issue{Number: "5", Title: "late arrival", State: forge.IssueOpen})
	sendKey(tm, "r")
	waitForOutput(t, tm, "late arrival")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_FilterMode_TypingNarrowsEnterApplies verifies "/" enters
// filter-input mode, typing narrows the list live, and Enter keeps the
// narrowed filter (issue #784).
func TestTea_FilterMode_TypingNarrowsEnterApplies(t *testing.T) {
	f := newAlphaBetaFake()
	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "alpha")

	sendKey(tm, "/")
	sendKey(tm, "b")
	waitForOutput(t, tm, "beta")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "beta")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_FilterMode_EscCancelRestoresPriorFilter verifies Esc discards an
// in-progress filter edit and restores whatever filter was active before
// "/" was pressed (issue #784).
func TestTea_FilterMode_EscCancelRestoresPriorFilter(t *testing.T) {
	f := newAlphaBetaFake()
	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "alpha")

	sendKey(tm, "/")
	sendKey(tm, "b")
	waitForOutput(t, tm, "beta")
	sendKey(tm, "enter")

	sendKey(tm, "/")
	sendKey(tm, "x") // "bx" matches nothing
	sendKey(tm, "esc")
	waitForOutput(t, tm, "beta") // reverted to the "b" filter from before this edit

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_CursorKeys_MoveHighlightedRow verifies j/down and k/up move the
// cursor marker across the visible backlog (issue #784).
func TestTea_CursorKeys_MoveHighlightedRow(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	f.SetIssue(forge.Issue{Number: "2", Title: "second", State: forge.IssueOpen})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "j")
	waitForOutput(t, tm, "> #2")

	sendKey(tm, "k")
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "down")
	waitForOutput(t, tm, "> #2")

	sendKey(tm, "up")
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_HelpKey_OpensOverlay verifies "?" opens the help overlay listing
// the bound keys (issue #784).
func TestTea_HelpKey_OpensOverlay(t *testing.T) {
	f := forge.NewFake()
	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))

	sendKey(tm, "?")
	waitForOutput(t, tm, "toggle this help")

	sendKey(tm, "?") // close the overlay — it's modal, so "q" alone can't reach quit while open
	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_BackgroundPoll_RefreshesWithoutOperatorInput verifies a slow fixed
// background poll re-queries the tracker on its own — no operator command —
// so a late-arriving issue still surfaces during an otherwise-idle session
// (#647 AC5, issue #784).
func TestTea_BackgroundPoll_RefreshesWithoutOperatorInput(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	launch := &Launcher{CodeForge: f, Queue: NewQueue(), pollInterval: 5 * time.Millisecond}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "first")

	f.SetIssue(forge.Issue{Number: "2", Title: "late arrival", State: forge.IssueOpen})
	waitForOutput(t, tm, "late arrival")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_LaunchRefreshSignal_RefreshesWithoutOperatorInput verifies a
// background write's refresh signal (Launcher.signalRefresh, the session's
// own tracker-write trigger) re-renders without the operator sending "r"
// (#647 AC4, issue #784).
func TestTea_LaunchRefreshSignal_RefreshesWithoutOperatorInput(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	launch := &Launcher{CodeForge: f, Queue: NewQueue(), pollInterval: time.Hour}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "first")

	f.SetIssue(forge.Issue{Number: "2", Title: "late arrival", State: forge.IssueOpen})
	launch.signalRefresh()
	waitForOutput(t, tm, "late arrival")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_WithLauncher_RendersCapAndLive verifies the session's live
// parallelism cap and current live count (issue #653) reach the rendered
// output through the same per-render sync QueueSnapshotMsg uses.
func TestTea_WithLauncher_RendersCapAndLive(t *testing.T) {
	f := forge.NewFake()
	launch := &Launcher{CodeForge: f, Queue: NewQueue(), MaxParallel: 3}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "cap: 0/3")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_WithLauncher_RendersLiveQueueState verifies every render — not
// just the one right after a pick — reflects the launcher's live Queue
// state, so a transition that happens entirely in the background (claim,
// run, settle, or a raced-claim dissolve) actually reaches the operator's
// screen (#646 AC4, AC6).
func TestTea_WithLauncher_RendersLiveQueueState(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	// Simulate a state transition that happened entirely on the background
	// Queue (a real launch's claim/run/settle) — isolating the render-sync
	// behavior from how a pick enters the queue.
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickDissolved, Reason: "issue is closed"})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "dissolved", "issue is closed")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_StaleStatus_RendersBanner verifies the tea layer's per-render sync
// installs the launcher's live stale verdict onto the view, exactly as
// syncQueue does for the picks queue — the operator sees the banner without
// an explicit refresh (issue #652 AC1).
func TestTea_StaleStatus_RendersBanner(t *testing.T) {
	f := forge.NewFake()
	launch := newTestLauncher(t, f)
	launch.Fresh = func() (bool, bool, string) { return true, false, "rebuild needed" }
	launch.tryLaunch(f, t.TempDir())
	launch.Wait()

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "stale", "rebuild needed")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_SettleTriggersAutoRefresh_NoExplicitRefreshKey verifies a settle —
// the session's own tracker write — fires a backlog refresh on its own, so
// an issue added to the tracker while a Box is running appears on a render
// once that Box settles, without the operator ever pressing "r" (#647 AC4,
// issue #784). The pick itself is landed directly on launch.Queue (the
// retired "p" keystroke has no tea-layer replacement yet) so this isolates
// the async-refresh behavior under test.
func TestTea_SettleTriggersAutoRefresh_NoExplicitRefreshKey(t *testing.T) {
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
	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue(), pollInterval: time.Hour}

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)

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
	waitForOutput(t, tm, "late arrival")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
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
// settle.Fake — enough plumbing to prove a queued issue runs a real (fake)
// Box and settles.
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
