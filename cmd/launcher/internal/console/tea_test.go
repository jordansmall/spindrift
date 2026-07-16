package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
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
	case "tab":
		tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	case "ctrl+c":
		tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
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

// TestTeaUpdate_WindowSizeMsg_SetsModelDimensions verifies the tea layer
// translates Bubble Tea's WindowSizeMsg into the pure Model's Width/Height
// (issue #842) — exercised by calling teaModel.Update directly, since AC4
// leaves View unchanged for this slice and there is nothing rendered to
// assert on yet.
func TestTeaUpdate_WindowSizeMsg_SetsModelDimensions(t *testing.T) {
	f := forge.NewFake()
	tm := newTeaModel(f, t.TempDir(), nil)

	updated, _ := tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	got := updated.(teaModel).m

	if got.Width != 100 {
		t.Errorf("Width = %d, want 100", got.Width)
	}
	if got.Height != 40 {
		t.Errorf("Height = %d, want 40", got.Height)
	}
}

// TestTea_InitialTermSize_SetsModelDimensions verifies the program's initial
// size event (teatest.WithInitialTermSize, sent through the real Bubble Tea
// program before Init) lands on the pure Model the same as a later resize
// (issue #842 AC3).
func TestTea_InitialTermSize_SetsModelDimensions(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(100, 40))
	waitForOutput(t, tm, "first")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	final := tm.FinalModel(t).(teaModel).m
	if final.Width != 100 {
		t.Errorf("Width = %d, want 100", final.Width)
	}
	if final.Height != 40 {
		t.Errorf("Height = %d, want 40", final.Height)
	}
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

// TestTea_ScrollKeys_PageThroughBacklogWithoutMovingCursor verifies pgdown/
// pgup move the focused backlog column's viewport directly, independent of
// the cursor, revealing and restoring rows past the fold, by exactly one
// screenful of rendered rows — derived from the live viewport rather than a
// fixed constant (issue #1036 AC2, issue #1037 AC1/AC2). At Width 80/Height
// 10 the backlog column's item budget is 5, but only 4 of those rows render
// as content at offset 0 (the 5th is held back for the "N more below" line),
// so one pgdown must land the viewport on row 4 — landing on row 5 would
// silently skip row 4, the exact row right past the fold.
func TestTea_ScrollKeys_PageThroughBacklogWithoutMovingCursor(t *testing.T) {
	f := forge.NewFake()
	for i := 0; i < 50; i++ {
		f.SetIssue(forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i), State: forge.IssueOpen})
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 10))
	waitForOutput(t, tm, "> #0")

	sendKey(tm, "pgdown")
	waitForOutput(t, tm, "#4  issue 4")

	sendKey(tm, "pgup")
	waitForOutput(t, tm, "> #0")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_ScrollKeys_PageSizeTracksViewportHeight verifies the page jump's
// size tracks the current viewport height rather than a value fixed at
// startup: the same pgdown that lands on row 5 at Height 10 lands on a later
// row once the terminal is taller and the backlog column can fit more rows
// per screen, so paging stays a full page after a resize (issue #1037 AC2).
func TestTea_ScrollKeys_PageSizeTracksViewportHeight(t *testing.T) {
	f := forge.NewFake()
	for i := 0; i < 50; i++ {
		f.SetIssue(forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i), State: forge.IssueOpen})
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 10))
	waitForOutput(t, tm, "> #0")

	tm.Send(tea.WindowSizeMsg{Width: 80, Height: 20})
	waitForOutput(t, tm, "> #0")

	// A page size still stuck on the Height-10 window (landing around row 4)
	// or the pre-#1037 fixed constant (10, landing its window at rows 10-23)
	// would never surface row 25. It's visible only once the page jump uses
	// the taller terminal's own item budget, landing the viewport around
	// offset 14 (rows 14-27).
	sendKey(tm, "pgdown")
	waitForOutput(t, tm, "#25  issue 25")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_ScrollKeys_PageDown_SkipsNoRow verifies two consecutive pgdown
// presses expose every row in between — a page size computed from the raw
// item budget instead of the rendered content-row count would overshoot the
// "N more below" line held back at each truncated screen and silently skip
// the row right past the fold on every page boundary (issue #1037 AC1).
func TestTea_ScrollKeys_PageDown_SkipsNoRow(t *testing.T) {
	f := forge.NewFake()
	for i := 0; i < 50; i++ {
		f.SetIssue(forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i), State: forge.IssueOpen})
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 10))
	waitForOutput(t, tm, "> #0")

	sendKey(tm, "pgdown")
	waitForOutput(t, tm, "#4  issue 4")

	sendKey(tm, "pgdown")
	waitForOutput(t, tm, "#8  issue 8")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_ScrollKeys_PageDownClampsAtBacklogEnd verifies repeated pgdown
// presses stop advancing once the viewport reaches the end of the backlog
// instead of scrolling past it, surfacing the last row (issue #1037 AC1).
func TestTea_ScrollKeys_PageDownClampsAtBacklogEnd(t *testing.T) {
	f := forge.NewFake()
	for i := 0; i < 12; i++ {
		f.SetIssue(forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i), State: forge.IssueOpen})
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 10))
	waitForOutput(t, tm, "> #0")

	for i := 0; i < 10; i++ {
		sendKey(tm, "pgdown")
	}
	waitForOutput(t, tm, "issue 11")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_ScrollKeys_PageThroughQueueWhenFocused verifies pgdown pages the
// work-queue column's viewport instead of the backlog's once Tab has moved
// focus there — paging works for whichever body column is focused (issue
// #1037 AC4).
func TestTea_ScrollKeys_PageThroughQueueWhenFocused(t *testing.T) {
	f := forge.NewFake()
	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	for i := 0; i < 50; i++ {
		launch.Queue.Add(Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued})
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 10))
	waitForOutput(t, tm, "pick 0")
	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "pgdown")
	waitForOutput(t, tm, "#4  [queued]  pick 4")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_TabKey_SwitchesFocusBetweenColumns verifies Tab moves the focus
// marker from the backlog column to the work-queue column and back, and that
// cursor keys move the newly focused column's cursor — the two-column focus
// split (issue #845).
func TestTea_TabKey_SwitchesFocusBetweenColumns(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)
	launch.Queue.Add(Pick{Number: "10", Title: "pick one", State: PickQueued})
	launch.Queue.Add(Pick{Number: "11", Title: "pick two", State: PickQueued})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "backlog [focus]")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "j")
	waitForOutput(t, tm, "> #11")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "backlog [focus]")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_EnterKey_OnFocusedQueue_DrillsRunningPick verifies Enter, with
// focus on the work queue, opens the highlighted pick's Transcript when its
// state is PickRunning — the context-sensitive Enter's queue-side drill
// (issue #845).
func TestTea_EnterKey_OnFocusedQueue_DrillsRunningPick(t *testing.T) {
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
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "transcript #42", "[implementor] hi")

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_EnterKey_OnFocusedQueue_NoOpOnQueuedRow verifies Enter, with focus
// on the work queue, is a no-op on a row that hasn't reached a
// Transcript-bearing state yet (PickQueued) — never opens a pane with
// nothing to show (issue #845).
func TestTea_EnterKey_OnFocusedQueue_NoOpOnQueuedRow(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "enter")
	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.DrillIn != nil {
		t.Errorf("DrillIn = %+v, want nil — Enter on a queued row must not open a transcript pane", fm.m.DrillIn)
	}
}

// TestTea_DrillInKey_OpensTranscriptPane verifies Enter, focused on the work
// queue, opens a full-screen transcript pane for the highlighted running
// pick, replacing the backlog (issue #786; retargeted to focused-queue Enter
// by issue #845, which retires the old "d"/backlog-Enter drill-in binding).
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
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "transcript #42", "[implementor] hi")

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_DrillInKey_QuitsWithoutClosing verifies "q" hard-quits straight
// out of an open drill-in pane, without requiring "x" first — the drill-in
// guard in handleKey must not swallow the universal quit keystroke (issue
// #826).
func TestTea_DrillInKey_QuitsWithoutClosing(t *testing.T) {
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
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "transcript #42", "[implementor] hi")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_DrillInKey_QuitsOnCtrlCWithoutClosing verifies "ctrl+c" hard-quits
// straight out of an open drill-in pane, without requiring "x" first — same
// universal-quit carve-out as "q" (issue #826).
func TestTea_DrillInKey_QuitsOnCtrlCWithoutClosing(t *testing.T) {
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
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "transcript #42", "[implementor] hi")

	sendKey(tm, "ctrl+c")
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
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "[implementor] hi")

	sendKey(tm, "t")
	waitForOutput(t, tm, `"type":"assistant"`)

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_PaneModeKey_CyclesDockedFloatingFullscreen verifies "m", pressed
// while a drill-in is open, cycles Model.PaneMode through docked -> floating
// -> fullscreen (issue #846, ADR 0025).
func TestTea_PaneModeKey_CyclesDockedFloatingFullscreen(t *testing.T) {
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
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(120, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "[implementor] hi")

	sendKey(tm, "m")
	sendKey(tm, "m")
	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.PaneMode != PaneFullscreen {
		t.Errorf("PaneMode = %v, want PaneFullscreen after two presses of \"m\"", fm.m.PaneMode)
	}
}

// TestTea_DrillInScrollKeys_PageThroughTranscript verifies pgdown/pgup move
// the drill-in pane's scroll offset, hiding and restoring the leading lines
// (issue #786). The transcript has to outrun the 24-row test terminal's
// fullscreen budget, or clampDrillInOffset's viewport cap (issue #829) pins
// Offset at 0 as a real no-op and pgdown never produces a fresh frame.
func TestTea_DrillInScrollKeys_PageThroughTranscript(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"line-%02d"}]}}`+"\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(lines.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := newTestLauncher(t, f)
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "line-00")

	sendKey(tm, "pgdown")
	waitForOutput(t, tm, "line-19")

	sendKey(tm, "pgup")
	waitForOutput(t, tm, "line-00")

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_DrillInKey_NoDriver_ShowsGracefulMessage verifies drilling in
// during a launch-less session (no Driver configured) surfaces a readable
// error in the pane instead of panicking on a nil Driver (issue #786 AC4).
// Picks is seeded directly (rather than via the "p"/Enter pick path) since a
// nil Launcher can never promote a pick to PickRunning through the normal
// flow — isolating the no-Driver render path from how the row got there.
func TestTea_DrillInKey_NoDriver_ShowsGracefulMessage(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	tm0 := newTeaModel(f, t.TempDir(), nil)
	tm0.m.Picks = append(tm0.m.Picks, Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := teatest.NewTestModel(t, tm0, teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "enter")
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

// TestTea_QuitKey_CancelsWaitRefreshSignalGoroutine verifies quitting a
// session with a live launch that never signals a refresh doesn't leak the
// waitRefreshSignal goroutine blocked on Launcher.Refreshes() (issue #823) —
// bubbletea can't cancel a Cmd goroutine itself (it's parked on <-ch forever
// by design), so teaModel must cancel it on the way out. Checks the
// goroutine dump for waitRefreshSignal by name rather than a raw
// runtime.NumGoroutine() diff, since the unrelated pollTick Cmd (armed with
// pollInterval: time.Hour so it can't fire mid-test) leaks a goroutine of
// its own that a raw count would conflate with this one.
func TestTea_QuitKey_CancelsWaitRefreshSignalGoroutine(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	launch := &Launcher{CodeForge: f, Queue: NewQueue(), pollInterval: time.Hour}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "first")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	deadline := time.Now().Add(time.Second)
	for {
		var buf bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
		if !strings.Contains(buf.String(), "waitRefreshSignal") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitRefreshSignal goroutine leaked after quit:\n%s", buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTea_WithLauncher_RendersCapAndLive verifies the session's live
// parallelism cap and current live count (issue #653) reach the rendered
// output through the same per-render sync QueueSnapshotMsg uses.
func TestTea_WithLauncher_RendersCapAndLive(t *testing.T) {
	f := forge.NewFake()
	launch := &Launcher{CodeForge: f, Queue: NewQueue(), MaxParallel: 3}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "running 0/3")

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
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent", "agent-in-progress", "priority-p1"}})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	// Simulate a state transition that happened entirely on the background
	// Queue (a real launch's claim/run/settle) — isolating the render-sync
	// behavior from how a pick enters the queue.
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickDissolved, Reason: "issue is closed"})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	// A realistic 3-label backlog issue widens the backlog column to its
	// leftColumnFraction cap on an 80-col terminal, which in turn narrows the
	// queue column enough that the Reason badge clips mid-word ("issue is
	// closed" -> "issue is cl…", view.go's clip) — accepted and asserted
	// here rather than masked by a label-free fixture (issue #857).
	waitForOutput(t, tm, "dissolved", "issue is cl…")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_WithLauncher_RendersHeldPickWithBlockedByBadge verifies a held
// pick's "held by #N" badge reaches the rendered output through the tea
// layer's per-render Queue sync, not just the pure View tests (issue #785:
// "queue rows show ... blocked (with a held by #N badge)").
func TestTea_WithLauncher_RendersHeldPickWithBlockedByBadge(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent", "priority-p1"}})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	// A held pick's badge is set entirely by Queue.Discover's blocker check
	// (queue.go setHeld); landing the state directly isolates the render-sync
	// behavior under test from blocker-edge mechanics already covered at
	// queue_test.go.
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickHeld, BlockedBy: "#41 (native)"})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	// As in TestTea_WithLauncher_RendersLiveQueueState, a realistic 2-label
	// backlog issue narrows the queue column enough that the "held by" badge
	// clips mid-word ("held by #41 (native)" -> "held by #41 (nat…") —
	// accepted and asserted rather than masked by a label-free fixture
	// (issue #857).
	waitForOutput(t, tm, "held", "held by #41 (nat…")

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
	// A queued pick is what actually hits the stale gate in production
	// (Rebuild's own doc comment: "any pick held ... through the stale
	// window") — tryLaunch is a real no-op on an empty queue post-#754, so
	// an empty-queue call here would never reach freshnessChecker at all.
	launch.Queue.Add(Pick{Number: "1", Title: "placeholder", State: PickQueued})
	launch.tryLaunch(f, t.TempDir())
	launch.Wait()

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "stale", "rebuild needed")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_StaleDetectedWhileIdle_SignalsRefreshWithoutPoll verifies stale
// detection itself wakes an already-idling Program — not the next poll tick
// (90s) or a coincidental Msg. TestTea_StaleStatus_RendersBanner above drives
// staleness through Wait() before the tea model even exists, masking this
// exact gap; this test detects staleness only after the Program is running
// idle, with pollInterval set far longer than the test's wait window so a
// poll tick could never be the cause (issue #762).
func TestTea_StaleDetectedWhileIdle_SignalsRefreshWithoutPoll(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)
	launch.pollInterval = time.Hour

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "first")

	launch.Fresh = func() (bool, bool, string) { return true, false, "rebuild needed" }
	launch.freshnessChecker()()
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
	// "  #42" (queue row's two-space indent) proves the pick landed on the
	// work-queue column — asserting on the row itself rather than the
	// "picks:" label, whose position indicator (issue #1037) now varies with
	// the queue's row count instead of staying fixed text. "settled 1" in
	// the same wait proves the fake Dispatch has also finished — otherwise
	// "q" can race the still-live pick and land on the quit confirm (issue
	// #822) instead of exiting, hanging until teatest's timeout (same race
	// TestTea_PickAllReadyKey_QueuesEveryDispatchableIssue already guards
	// against).
	waitForOutput(t, tm, "  #42", "fix the thing", "settled 1")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_EnterKey_OnFocusedBacklog_PicksHighlighted verifies Enter, with
// focus on the backlog (the default), Picks the highlighted issue exactly
// like "p" does — the context-sensitive Enter routing (issue #845).
func TestTea_EnterKey_OnFocusedBacklog_PicksHighlighted(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "#42", "fix the thing")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_EnterKey_OnFocusedBacklog_FailedPromotion_ShowsDissolvedRow
// verifies a raced/closed/relabeled promotion via Enter still surfaces as a
// dissolved row with its reason, same as the "p" key path (issue #845).
func TestTea_EnterKey_OnFocusedBacklog_FailedPromotion_ShowsDissolvedRow(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	f.TransitionStateErr = errBoom
	launch := &Launcher{CodeForge: f, Queue: NewQueue()}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "dissolved")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_PickKey_ShowsPendingIndicator verifies "p" renders a visible hint
// immediately, before the trailing "a" arrives or the 200ms leader window
// times out — the pending pick chord was previously silent (issue #835).
func TestTea_PickKey_ShowsPendingIndicator(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "p")
	waitForOutput(t, tm, "p_")

	sendKey(tm, "a")
	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_PickKey_FollowedByA_ClearsPendingIndicator verifies the pending
// pick indicator armed by "p" clears once the trailing "a" resolves the
// chord (issue #835).
func TestTea_PickKey_FollowedByA_ClearsPendingIndicator(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "p")
	waitForOutput(t, tm, "p_")

	sendKey(tm, "a")
	waitForOutput(t, tm, "  #42")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.PendingPick {
		t.Errorf("PendingPick = true after \"a\" resolved the chord, want false")
	}
}

// TestTea_PickKey_Timeout_ClearsPendingIndicator verifies the pending pick
// indicator armed by a lone "p" clears once the 200ms leader window times
// out and resolves to a single-issue pick (issue #835).
func TestTea_PickKey_Timeout_ClearsPendingIndicator(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "p")
	waitForOutput(t, tm, "p_")
	waitForOutput(t, tm, "  #42") // timeout fires unassisted, resolving to a single pick

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.PendingPick {
		t.Errorf("PendingPick = true after timeout resolved the chord, want false")
	}
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
	waitForOutput(t, tm, "  #42")

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
	waitForOutput(t, tm, "  #42")

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

// TestTea_AlreadyActive_ReadsLiveQueueSnapshot verifies alreadyActive
// consults the launcher's live Queue, not the last-synced Model.Picks, when
// a launch is present — a background drain can settle a row on Queue
// between two Update calls, and until the next syncQueue catches up,
// Model.Picks still shows the old non-terminal state (issue #837).
func TestTea_AlreadyActive_ReadsLiveQueueSnapshot(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	tm := newTeaModel(f, t.TempDir(), launch)

	// Stale Model.Picks still shows #42 as running (as of the last sync)...
	tm.m.Picks = append(tm.m.Picks, Pick{Number: "42", Title: "fix the thing", State: PickRunning})
	// ...but the live Queue has since settled it in the background, with no
	// intervening syncQueue to refresh Model.Picks.
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickSettled})

	if tm.alreadyActive("42") {
		t.Errorf("alreadyActive(%q) = true, want false — live Queue shows settled, only the stale Model.Picks snapshot shows running", "42")
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
	waitForOutput(t, tm, "picks (1-1 of 1):", "queued")

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
	// Both picks race the fake Dispatch to completion behind the default
	// cap of 1; wait for both to settle so "q" always lands with nothing
	// live, rather than racing the quit confirm (issue #822).
	waitForOutput(t, tm, "settled 2")
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
	waitForOutput(t, tm, "running 0/3")

	sendKey(tm, "+")
	waitForOutput(t, tm, "running 0/4")

	sendKey(tm, "-")
	sendKey(tm, "-")
	waitForOutput(t, tm, "running 0/2")

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
		RebuildFn: func() (string, error) { rebuilt <- struct{}{}; return "", nil },
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "running ")

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
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "1", Title: "placeholder", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "2", State: forge.IssueOpen})
	f.NativeDeps = map[string][]string{"1": {"2"}}
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
		RebuildFn: func() (string, error) {
			stale.Store(false)
			close(rebuilt)
			return "", nil
		},
	}
	// A queued pick is what actually hits the stale gate in production
	// (Rebuild's own doc comment: "any pick held ... through the stale
	// window") — tryLaunch is a real no-op on an empty queue post-#754, so
	// an empty-queue call here would never reach freshnessChecker at all.
	// It carries an open blocker so the post-rebuild re-drain (Rebuild
	// calls tryLaunch again on success) holds it instead of actually
	// launching a Box — this Launcher has no Factory to run one.
	launch.Queue.Add(Pick{Number: "1", Title: "placeholder", State: PickQueued})
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
		if stale, _, _, _, _ := launch.StaleStatus(); !stale {
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

	// The Dispatch is still live after declining, so "q" now arms the
	// quit confirm (issue #822) instead of exiting immediately — drain to
	// finish the test.
	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	if len(fr.KillCalls) != 0 {
		t.Errorf("KillCalls = %v, want none after declining", fr.KillCalls)
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickRunning {
		t.Errorf("queue pick = %+v, want still PickRunning after declining", snap)
	}
}

// TestTea_TerminateKey_ConfirmThenQuit_DeclinesAndQuits verifies the
// universal quit keystroke at the confirm prompt is not swallowed by the
// pending terminate: "q" declines the terminate (same as any other
// non-"y" key) and still exits the program in one keystroke (issue #748).
func TestTea_TerminateKey_ConfirmThenQuit_DeclinesAndQuits(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "k")
	waitForOutput(t, tm, "terminate #42?")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	if len(fr.KillCalls) != 0 {
		t.Errorf("KillCalls = %v, want none after quitting at the confirm prompt", fr.KillCalls)
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickRunning {
		t.Errorf("queue pick = %+v, want still PickRunning after quitting at the confirm prompt", snap)
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
	launch := &Launcher{CodeForge: cf, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
	// Cleanup runs LIFO, so this drains any in-flight background dispatch
	// before factory.Cleanup releases its resources — an un-joined drain
	// goroutine otherwise keeps running (and printing) after the test that
	// spawned it returns, stealing scheduler time from whatever teatest-based
	// test runs next and risking its own tight WithDuration deadline.
	t.Cleanup(launch.Wait)
	return launch
}

// TestTea_Update_ReusesHeartbeatCacheAcrossCalls verifies the tea layer's
// heartbeat cache survives across repeated Update calls on the same session
// (issue #731) — not just within one syncQueue call — by proving a second
// Update, given an on-disk log rewritten to different content but pinned
// back to the same size/mtime, still reports the first call's line rather
// than a reparse of the new content. teaModel.Update takes a value receiver,
// so this also proves the cache lives behind a pointer field rather than
// being silently reallocated (and thus reset) on every copy.
func TestTea_Update_ReusesHeartbeatCacheAcrossCalls(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "logs", "issue-42.log")
	first := `{"type":"result","num_turns":17,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	launch := newTestLauncher(t, f)
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := newTeaModel(f, dir, launch)
	model, _ := tm.Update(struct{}{})
	tm = model.(teaModel)
	first1 := heartbeatFor(t, tm.m, "42")
	if want := "17 turn"; !strings.Contains(first1, want) {
		t.Fatalf("first Update heartbeat = %q, want it to contain %q", first1, want)
	}

	second := `{"type":"result","num_turns":99,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if len(second) != len(first) {
		t.Fatalf("test setup: second log must be same length as first, got %d want %d", len(second), len(first))
	}
	if err := os.WriteFile(path, []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	model, _ = tm.Update(struct{}{})
	tm = model.(teaModel)
	got := heartbeatFor(t, tm.m, "42")
	if got != first1 {
		t.Errorf("second Update heartbeat = %q, want cached %q (unchanged stat must skip reparse)", got, first1)
	}
}

// TestTea_QuitKey_WithLiveDispatch_ArmsPendingQuitConfirm verifies "q" with a
// live Dispatch running arms the drain/terminate-all/stay confirm instead of
// exiting immediately (issue #651, ADR 0023, issue #822).
func TestTea_QuitKey_WithLiveDispatch_ArmsPendingQuitConfirm(t *testing.T) {
	launch, fc, _, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches", "drain", "terminate-all", "stay")

	sendKey(tm, "s")
	waitForOutput(t, tm, "fix the thing") // confirm prompt gone, backlog/queue view back

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_QuitKey_TerminateAll_ReapsEveryLiveDispatch verifies "t" at the
// quit confirm terminates every live Dispatch before exiting (issue #651,
// ADR 0023, issue #822).
func TestTea_QuitKey_TerminateAll_ReapsEveryLiveDispatch(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")

	sendKey(tm, "t")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
	// TerminateAsync's Kill runs in a background goroutine tracked by
	// launch.wg — production's own Run waits on it the same way
	// (tea.go's Run) after the program exits, before this can safely read
	// fr.KillCalls.
	launch.Wait()

	if len(fr.KillCalls) != 1 || fr.KillCalls[0] != "agent-issue-42" {
		t.Errorf("KillCalls = %v, want exactly one kill of agent-issue-42", fr.KillCalls)
	}
}

// TestTea_QuitKey_Stay_DeclinesAndKeepsRunning verifies "s" at the quit
// confirm cancels the quit, touching no live Dispatch and leaving the
// session running (issue #651, ADR 0023, issue #822).
func TestTea_QuitKey_Stay_DeclinesAndKeepsRunning(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")

	sendKey(tm, "s")
	waitForOutput(t, tm, "fix the thing") // confirm prompt gone, backlog/queue view back

	// Still live after staying — drain to finish the test cleanly.
	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	if len(fr.KillCalls) != 0 {
		t.Errorf("KillCalls = %v, want none after staying", fr.KillCalls)
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickRunning {
		t.Errorf("queue pick = %+v, want still PickRunning after staying", snap)
	}
}

// TestTea_QuitKey_NoLiveDispatch_ExitsImmediately verifies "q" with no live
// Dispatch skips the confirm entirely and exits in one keystroke, matching
// the pre-#822 behaviour for a session with nothing running (issue #651).
func TestTea_QuitKey_NoLiveDispatch_ExitsImmediately(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	launch := &Launcher{CodeForge: f, Queue: NewQueue()}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestTea_Init_RecoversOrphanedIssuesOnStartup verifies a sandbox still
// running from a prior crashed session gets adopted through RecoverFn at
// startup, without blocking the initial render (issue #651, issue #822).
func TestTea_Init_RecoversOrphanedIssuesOnStartup(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	fr := runner.NewFake()
	fr.RunningNames = []string{"agent-issue-42"}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	recovered := make(chan string, 1)
	launch := &Launcher{
		CodeForge: f,
		Factory:   factory,
		Queue:     NewQueue(),
		RecoverFn: func(num string) error {
			recovered <- num
			return nil
		},
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	select {
	case num := <-recovered:
		if num != "42" {
			t.Errorf("RecoverFn called with %q, want 42", num)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RecoverFn never called for orphaned issue 42")
	}

	sendKey(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// heartbeatFor returns m's Heartbeat field for the pick numbered number, or
// fails the test if no such pick is present.
func heartbeatFor(t *testing.T, m Model, number string) string {
	t.Helper()
	for _, p := range m.Picks {
		if p.Number == number {
			return p.Heartbeat
		}
	}
	t.Fatalf("no pick %q in Picks %+v", number, m.Picks)
	return ""
}
