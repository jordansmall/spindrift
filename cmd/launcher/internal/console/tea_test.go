package console

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	case "pgup":
		tm.Send(tea.KeyMsg{Type: tea.KeyPgUp})
	case "pgdown":
		tm.Send(tea.KeyMsg{Type: tea.KeyPgDown})
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

// TestTea_CursorKeys_MoveHighlightedRow verifies j/down and the up arrow
// move the cursor marker across the visible backlog (issue #784). "k" is
// bound to Terminate (ADR 0024, issue #785), not cursor-up — only the arrow
// key drives upward movement.
func TestTea_CursorKeys_MoveHighlightedRow(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	f.SetIssue(forge.Issue{Number: "2", Title: "second", State: forge.IssueOpen})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "j")
	waitForOutput(t, tm, "> #2")

	sendKey(tm, "up")
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "down")
	waitForOutput(t, tm, "> #2")

	sendKey(tm, "up")
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_DrillInKey_OpensTranscriptPane verifies "d" opens a full-screen
// transcript pane for the highlighted Dispatch, replacing the backlog
// (issue #786).
func TestTea_DrillInKey_OpensTranscriptPane(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "d")
	waitForOutput(t, tm, "transcript #42", "[implementor] hi")

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_DrillInToggleKey_SwapsRenderedAndRaw verifies "t" swaps the open
// drill-in pane between the rendered transcript and the byte-exact raw JSONL
// log (issue #786).
func TestTea_DrillInToggleKey_SwapsRenderedAndRaw(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "d")
	waitForOutput(t, tm, "[implementor] hi")

	sendKey(tm, "t")
	waitForOutput(t, tm, `"type":"assistant"`)

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_DrillInScrollKeys_PageThroughTranscript verifies pgdown/pgup move
// the drill-in pane's scroll offset, hiding and restoring the leading lines
// (issue #786).
func TestTea_DrillInScrollKeys_PageThroughTranscript(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"line-%02d"}]}}`+"\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(lines.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "d")
	waitForOutput(t, tm, "line-00")

	sendKey(tm, "pgdown")
	waitForOutput(t, tm, "line-19")

	sendKey(tm, "pgup")
	waitForOutput(t, tm, "line-00")

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_DrillInKey_NoDriver_ShowsGracefulMessage verifies drilling in
// during a launch-less session (no Driver configured) surfaces a readable
// error in the pane instead of panicking on a nil Driver (issue #786 AC4).
func TestTea_DrillInKey_NoDriver_ShowsGracefulMessage(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "d")
	waitForOutput(t, tm, "transcript #42", "drill-in failed")

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

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

// TestTea_WithLauncher_RendersHeldPickWithBlockedByBadge verifies a held
// pick's "held by #N" badge reaches the rendered output through the tea
// layer's per-render Queue sync, not just the pure View tests (issue #785:
// "queue rows show ... blocked (with a held by #N badge)").
func TestTea_WithLauncher_RendersHeldPickWithBlockedByBadge(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	// A held pick's badge is set entirely by Queue.Discover's blocker check
	// (queue.go setHeld); landing the state directly isolates the render-sync
	// behavior under test from blocker-edge mechanics already covered at
	// queue_test.go.
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickHeld, BlockedBy: "#41 (native)"})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "held", "held by #41 (native)")

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

// TestTea_PickKey_PromotesAndQueuesHighlighted verifies "p" promotes the
// highlighted issue through the Untriaged->Dispatchable transition and lands
// it on the queue — the launch button acting on the cursor row (issue #785).
func TestTea_PickKey_PromotesAndQueuesHighlighted(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "p")
	waitForOutput(t, tm, "picks:", "#42", "fix the thing")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_PickKey_FollowedByQuit_PicksThenQuits verifies "p" followed by
// "q" still exits the program — the pending "pa" chord resolves the pick
// (same as any non-"a" key) but must not swallow the universal quit
// keystroke along with it (issue #785 review).
func TestTea_PickKey_FollowedByQuit_PicksThenQuits(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "p")
	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	fm := tm.FinalModel(t).(teaModel)
	if len(fm.m.Picks) != 1 {
		t.Errorf("Picks = %+v, want the pick to have landed before quitting", fm.m.Picks)
	}
}

// TestTea_PickKey_FollowedByNonA_ResolvesToSinglePick verifies "p" followed
// by any key other than "a" resolves the leader chord to a single-issue
// pick immediately, rather than making the operator wait out the timeout
// (issue #785 AC1).
func TestTea_PickKey_FollowedByNonA_ResolvesToSinglePick(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "p")
	sendKey(tm, "z") // not "a" — resolves to a single pick right away
	waitForOutput(t, tm, "picks:", "#42")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_PickKey_AlreadyPicked_NoDuplicateRow verifies picking an issue
// that already has an active (non-terminal) row never appends a second one
// — Queue's row-scan helpers (setState, tryMarkClaiming) assume at most one
// non-terminal row per issue number; a duplicate leaves one row stuck at
// PickQueued forever and can hang the drain loop (issue #785 review).
func TestTea_PickKey_AlreadyPicked_NoDuplicateRow(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "p")
	sendKey(tm, "z") // resolve the "pa" chord to a single pick right away
	waitForOutput(t, tm, "picks:", "#42")

	sendKey(tm, "p")
	sendKey(tm, "z")
	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	fm := tm.FinalModel(t).(teaModel)
	count := 0
	for _, p := range fm.m.Picks {
		if p.Number == "42" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Picks = %+v, want exactly one row for #42, got %d", fm.m.Picks, count)
	}
}

// TestTea_PickKey_FailedPromotion_SurvivesQueueResync verifies a raced/
// closed/relabeled promotion's dissolved row stays on screen — the launcher's
// own per-render Queue resync (syncQueue) must not silently wipe it just
// because the failed pick never landed on the live Queue (issue #785 review).
func TestTea_PickKey_FailedPromotion_SurvivesQueueResync(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	f.TransitionStateErr = errBoom
	launch := &Launcher{CodeForge: f, Queue: NewQueue()}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "p")
	waitForOutput(t, tm, "dissolved")

	// Force a second render (a plain refresh, not a pick) to prove the
	// dissolved row survives more than the one render right after the
	// keypress — the launcher's own per-render Queue resync must not wipe
	// it just because it never landed on the live Queue.
	sendKey(tm, "r")
	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	fm := tm.FinalModel(t).(teaModel)
	if len(fm.m.Picks) != 1 || fm.m.Picks[0].State != PickDissolved {
		t.Errorf("Picks = %+v, want one dissolved pick surviving the resync", fm.m.Picks)
	}
}

// TestTea_TerminateKey_NotLive_NeverArmsConfirm verifies "k" only arms a
// confirm for a highlighted issue with an actual live Dispatch (ADR 0024,
// AC2: "the highlighted live Dispatch") — a plain backlog row that was never
// picked, or a pick that hasn't reached PickRunning yet, must not trigger
// Terminate's full side effects (relabel, comment) on nothing (issue #785
// review).
func TestTea_TerminateKey_NotLive_NeverArmsConfirm(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	launch := &Launcher{CodeForge: f, Queue: NewQueue()}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "k")
	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.PendingTerminate != "" {
		t.Errorf("PendingTerminate = %q, want empty for a non-live issue", fm.m.PendingTerminate)
	}
}

// TestTea_TerminateKey_NilLauncher_NeverArmsConfirm verifies "k" is a no-op
// in a launch-less session — there is no live Dispatch to reclaim, so no
// confirm prompt should ever arm (issue #785 review).
func TestTea_TerminateKey_NilLauncher_NeverArmsConfirm(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "k")
	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.PendingTerminate != "" {
		t.Errorf("PendingTerminate = %q, want empty in a launch-less session", fm.m.PendingTerminate)
	}
}

// TestTea_PickKey_TriggersAutoRefresh_NoExplicitRefreshKey verifies a pick's
// promotion — the session's own tracker write — fires the same
// signalRefresh auto-refresh every other write triggers (#647 AC4), so a
// late-arriving issue surfaces without the operator pressing "r" (issue
// #785 review: "a claim attempt is always a tracker write, win or lose").
func TestTea_PickKey_TriggersAutoRefresh_NoExplicitRefreshKey(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)
	launch.pollInterval = time.Hour // isolate: only the pick's own signal should trigger this refresh

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	f.SetIssue(forge.Issue{Number: "99", Title: "late arrival", State: forge.IssueOpen})
	sendKey(tm, "p")
	waitForOutput(t, tm, "late arrival")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_PickKey_FailedPromotion_StillTriggersAutoRefresh verifies a raced/
// closed/relabeled pick still fires signalRefresh — "a claim attempt is
// always a tracker write, win or lose" (issue #785 review) — even though,
// unlike a successful pick, it never reaches tryLaunch's own drain-side
// signal.
func TestTea_PickKey_FailedPromotion_StillTriggersAutoRefresh(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	f.TransitionStateErr = errBoom
	launch := &Launcher{CodeForge: f, Queue: NewQueue(), pollInterval: time.Hour}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	f.SetIssue(forge.Issue{Number: "99", Title: "late arrival", State: forge.IssueOpen})
	sendKey(tm, "p")
	waitForOutput(t, tm, "late arrival")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_UnpickKey_RemovesQueuedHighlighted verifies "u" drops the
// highlighted issue's queued-but-unlaunched pick, touching nothing on the
// tracker (issue #785).
func TestTea_UnpickKey_RemovesQueuedHighlighted(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "picks:", "queued")

	sendKey(tm, "u")
	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	fm := tm.FinalModel(t).(teaModel)
	if len(fm.m.Picks) != 0 {
		t.Errorf("Picks = %+v, want empty after unpick", fm.m.Picks)
	}
}

// TestTea_PickAllReadyKey_QueuesEveryDispatchableIssue verifies "pa" (the
// literal two-key leader sequence named in issue #785's AC1) picks every
// currently-Dispatchable issue in one bulk gesture (#647 AC3) rather than
// requiring one "p" per row.
func TestTea_PickAllReadyKey_QueuesEveryDispatchableIssue(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Title: "also ready", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing", "also ready")

	sendKey(tm, "p")
	sendKey(tm, "a")
	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	fm := tm.FinalModel(t).(teaModel)
	if len(fm.m.Picks) != 2 {
		t.Fatalf("Picks = %+v, want 2 picks (one per Dispatchable issue)", fm.m.Picks)
	}
	nums := map[string]bool{fm.m.Picks[0].Number: true, fm.m.Picks[1].Number: true}
	if !nums["42"] || !nums["43"] {
		t.Errorf("Picks = %+v, want #42 and #43 both picked", fm.m.Picks)
	}
}

// TestTea_ResizeKey_Raise_LaunchesQueuedPickWithNoActiveDrain verifies "+"
// launches a held/queued pick immediately even when no drain is currently
// active to catch the Limiter's Grown signal (ADR 0023: "raising launches a
// held pick immediately") — a session that never called tryLaunch (no prior
// pick, no poll tick yet) must not leave a queued pick stranded until one
// finally does (issue #785 review).
func TestTea_ResizeKey_Raise_LaunchesQueuedPickWithNoActiveDrain(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})
	launch := newTestLauncher(t, f)
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	// No tryLaunch call yet — no drain is active to observe Resize's Grown
	// signal.

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "+")
	waitForOutput(t, tm, "settled")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_ResizeKeys_RaiseAndLowerLiveCap verifies "+"/"-" adjust the live
// parallelism cap immediately (ADR 0023, issue #785).
func TestTea_ResizeKeys_RaiseAndLowerLiveCap(t *testing.T) {
	f := forge.NewFake()
	launch := &Launcher{CodeForge: f, Queue: NewQueue(), MaxParallel: 3}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "cap: 0/3")

	sendKey(tm, "+")
	waitForOutput(t, tm, "cap: 0/4")

	sendKey(tm, "-")
	sendKey(tm, "-")
	waitForOutput(t, tm, "cap: 0/2")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_RebuildKey_NotStale_NeverRunsRebuildFn verifies "b" is a no-op
// while the image is fresh — the trigger is a stale image (both the AC and
// the help text say "rebuild the stale image"), not a bare keypress (issue
// #785 review).
func TestTea_RebuildKey_NotStale_NeverRunsRebuildFn(t *testing.T) {
	f := forge.NewFake()
	rebuilt := make(chan struct{}, 1)
	launch := &Launcher{
		CodeForge: f,
		Queue:     NewQueue(),
		RebuildFn: func() error { rebuilt <- struct{}{}; return nil },
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "cap:")

	sendKey(tm, "b")
	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	select {
	case <-rebuilt:
		t.Error("RebuildFn ran while the image was fresh")
	default:
	}
}

// TestTea_RebuildKey_RunsRebuildFnAndClearsStale verifies "b" triggers the
// in-session rebuild (issue #652 AC3, issue #785): RebuildFn runs and, on
// success, the stale gate clears.
func TestTea_RebuildKey_RunsRebuildFnAndClearsStale(t *testing.T) {
	f := forge.NewFake()
	rebuilt := make(chan struct{})
	var stale atomic.Bool
	stale.Store(true)
	launch := &Launcher{
		CodeForge: f,
		Queue:     NewQueue(),
		Fresh: func() (bool, bool, string) {
			if stale.Load() {
				return true, false, "rebuild needed"
			}
			return true, true, ""
		},
		RebuildFn: func() error {
			stale.Store(false)
			close(rebuilt)
			return nil
		},
	}
	launch.tryLaunch(f, t.TempDir())
	launch.Wait()

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "stale", "rebuild needed")

	sendKey(tm, "b")
	select {
	case <-rebuilt:
	case <-time.After(2 * time.Second):
		t.Fatal(`RebuildFn never ran after "b"`)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if stale, _, _, _ := launch.StaleStatus(); !stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stale gate never cleared after a successful rebuild")
		}
		time.Sleep(time.Millisecond)
	}

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_TerminateKey_ConfirmThenYes_ReclaimsHighlightedDispatch verifies
// "k" arms a confirm prompt naming the highlighted issue, and "y" then reaps
// the Box and returns the issue to Dispatchable (ADR 0024, issue #785).
func TestTea_TerminateKey_ConfirmThenYes_ReclaimsHighlightedDispatch(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "k")
	waitForOutput(t, tm, "terminate #42?", "y/N")

	sendKey(tm, "y")
	waitForOutput(t, tm, "terminated")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	if len(fr.KillCalls) != 1 || fr.KillCalls[0] != "agent-issue-42" {
		t.Errorf("KillCalls = %v, want exactly one kill of agent-issue-42", fr.KillCalls)
	}
}

// TestTea_TerminateKey_ConfirmThenCapitalY_Confirms verifies the confirm
// prompt accepts "Y" as well as "y" — the "[y/N]" prompt reads as
// case-insensitive to an operator (issue #785 review).
func TestTea_TerminateKey_ConfirmThenCapitalY_Confirms(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "k")
	waitForOutput(t, tm, "terminate #42?")

	tm.Type("Y")
	waitForOutput(t, tm, "terminated")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	if len(fr.KillCalls) != 1 {
		t.Errorf("KillCalls = %v, want exactly one kill", fr.KillCalls)
	}
}

// TestTea_TerminateKey_ConfirmThenOther_Declines verifies any key other than
// "y" at the confirm prompt declines the terminate — the running Dispatch is
// left untouched (ADR 0024, issue #785).
func TestTea_TerminateKey_ConfirmThenOther_Declines(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "k")
	waitForOutput(t, tm, "terminate #42?")

	sendKey(tm, "n")
	waitForOutput(t, tm, "fix the thing") // confirm prompt gone, backlog/queue view back

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	if len(fr.KillCalls) != 0 {
		t.Errorf("KillCalls = %v, want none after declining", fr.KillCalls)
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickRunning {
		t.Errorf("queue pick = %+v, want still PickRunning after declining", snap)
	}
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
