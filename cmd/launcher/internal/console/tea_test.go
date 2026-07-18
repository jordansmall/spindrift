package console

import (
	"bytes"
	"errors"
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
	"github.com/mattn/go-runewidth"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// teatestTimeout bounds every teatest wait in this package — both for output
// to appear (waitForOutput/WaitFor) and for the program to finish shutting
// down (waitFinished/WaitFinished). It is a hang detector, not a latency
// assertion: the tests measure eventual behavior, never speed, so the bound
// only has to exceed worst-case timing under a fully loaded CI runner — the
// full `nix flake check` on a 4-core box, where the go-test derivation races
// heavy image builds and the Bubble Tea event loop is CPU-starved. One
// generous budget in a single place replaces the per-site literals that were
// bumped piecemeal (2s -> 5s -> 15s -> 30s across several commits); a tight
// bound here only ever flakes, it never catches a real defect — a hung
// program still fails, just later.
//
// That escalation to 30s was chasing the wrong cause: every one of those
// CI flakes was `WaitFinished` hanging outright on the "quit with live
// Dispatches" confirm prompt (issue #1277), a real deadlock no timeout could
// fix, not a slow render under load — a bigger number just delayed the same
// failure. #1277 fixed it at the source with deterministic "settled" guards
// on the launch-backed pick tests, and 30s held for the rest of this file
// afterward. Only one test kept flaking past the fix —
// TestTea_ResizeKey_Raise_LaunchesQueuedPickWithNoActiveDrain, a genuinely
// heavier CPU-starvation case that pushed the bound to 60s (issue #1327's
// history) — and #1327 dropped it out of the teatest mechanism entirely for
// a direct handleKey call plus launch.Wait(), so it no longer answers to
// this constant at all. With the deadlock fixed and the one CPU-starved
// outlier gone, nothing left in this file has ever demonstrated a need past
// 30s; issue #1278 restores that tighter, evidenced bound rather than
// carrying the 60s headroom the departed test alone required. When a
// specific test hangs regardless of this bound, the fix is a deterministic
// wait on real state, not a bigger number — see the "settled" guards on
// the launch-backed pick tests further down this file.
const teatestTimeout = 30 * time.Second

// waitForOutput blocks until tm's output contains every one of want, failing
// the test if it never does within teatestTimeout. tm.Output() drains as
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
	}, teatest.WithDuration(teatestTimeout), teatest.WithCheckInterval(5*time.Millisecond))
}

// waitFinished blocks until tm's program has fully shut down, failing the test
// if teardown exceeds teatestTimeout. See teatestTimeout for why the bound is
// generous rather than tight.
func waitFinished(t *testing.T, tm *teatest.TestModel) {
	t.Helper()
	tm.WaitFinished(t, teatest.WithFinalTimeout(teatestTimeout))
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)

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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
}

// TestTea_CursorKeys_MoveHighlightedRow verifies j/down and the up arrow
// move the cursor marker across the visible backlog (issue #784). "k" is
// bound to Terminate (ADR 0024, issue #785), not cursor-up, so "i" is the
// single-letter cursor-up partner for "j" instead (issue #838).
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

	sendKey(tm, "j")
	waitForOutput(t, tm, "> #2")

	sendKey(tm, "i")
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "q")
	waitFinished(t, tm)
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
	waitFinished(t, tm)
}

// TestTea_ScrollKeys_PgdownScrollsBacklogOffScreenWhenContentFits verifies
// the rendered effect of pgdown when the whole backlog already fits within
// one screen (issue #1060): the top, already-fully-visible row disappears
// from the rendered column instead of the press no-op'ing — the model
// package's own offset-only scroll tests don't by themselves prove a row
// silently drops off the rendered window.
func TestTea_ScrollKeys_PgdownScrollsBacklogOffScreenWhenContentFits(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	issues := make([]forge.Issue, 3)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})
	tm := teaModel{m: m}

	before := tm.View()
	if !strings.Contains(before, "> #0") || !strings.Contains(before, "#1") || !strings.Contains(before, "#2") {
		t.Fatalf("test setup: before pgdown, all 3 issues must be visible, got %q", before)
	}
	if strings.Contains(before, "more below") {
		t.Fatalf("test setup: before pgdown, backlog must already fully fit on screen, got %q", before)
	}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})

	after := tm.View()
	if strings.Contains(after, "#0") || strings.Contains(after, "#1") {
		t.Errorf("after pgdown, backlog still shows issue #0 or #1, want both scrolled off screen: %q", after)
	}
	if !strings.Contains(after, "(3-3 of 3)") || !strings.Contains(after, "#2") {
		t.Errorf("after pgdown, backlog want offset landed on the last row (3-3 of 3) showing only issue #2: %q", after)
	}
}

// TestTea_ScrollKeys_PgdownScrollsQueueOffScreenWhenContentFits mirrors
// TestTea_ScrollKeys_PgdownScrollsBacklogOffScreenWhenContentFits for the
// picks queue column (issue #1060).
func TestTea_ScrollKeys_PgdownScrollsQueueOffScreenWhenContentFits(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, FocusToggleMsg{})
	picks := make([]Pick, 3)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m.Picks = picks
	tm := teaModel{m: m}

	before := tm.View()
	if !strings.Contains(before, "> #0") || !strings.Contains(before, "#1") || !strings.Contains(before, "#2") {
		t.Fatalf("test setup: before pgdown, all 3 picks must be visible, got %q", before)
	}
	if strings.Contains(before, "more below") {
		t.Fatalf("test setup: before pgdown, queue must already fully fit on screen, got %q", before)
	}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})

	after := tm.View()
	if strings.Contains(after, "#0") || strings.Contains(after, "#1") {
		t.Errorf("after pgdown, queue still shows pick #0 or #1, want both scrolled off screen: %q", after)
	}
	if !strings.Contains(after, "(3-3 of 3)") || !strings.Contains(after, "#2") {
		t.Errorf("after pgdown, queue want offset landed on the last row (3-3 of 3) showing only pick #2: %q", after)
	}
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.QueueEnterNotice != "" {
		t.Errorf("QueueEnterNotice = %q after drilling into a transcript, want \"\"", fm.m.QueueEnterNotice)
	}
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
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.DrillIn != nil {
		t.Errorf("DrillIn = %+v, want nil — Enter on a queued row must not open a transcript pane", fm.m.DrillIn)
	}
}

// TestTea_EnterKey_OnFocusedQueue_ShowsNoticeOnQueuedRow verifies Enter, with
// focus on the work queue, renders a visible notice on a row that hasn't
// reached a Transcript-bearing state yet (PickQueued) — previously a silent
// no-op (issue #998).
func TestTea_EnterKey_OnFocusedQueue_ShowsNoticeOnQueuedRow(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "no transcript to show")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_EnterKey_OnFocusedQueue_NoticeClearsOnNextKey verifies the
// no-transcript notice armed by Enter clears once the operator's next
// keypress arrives — a one-shot hint, not a sticky one (issue #998).
func TestTea_EnterKey_OnFocusedQueue_NoticeClearsOnNextKey(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "tab")
	waitForOutput(t, tm, "picks [focus]")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "no transcript to show")

	sendKey(tm, "j")
	sendKey(tm, "q")
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.QueueEnterNotice != "" {
		t.Errorf("QueueEnterNotice = %q after next keypress, want \"\"", fm.m.QueueEnterNotice)
	}
}

// TestHasTranscript_PerState verifies hasTranscript against every PickState:
// true for running/settled/terminated/failed (each left logs on disk from a
// Box that ran or is running), false for queued/claiming/held/dissolved
// (never launched) — issue #845, PickFailed's inclusion per issue #992.
func TestHasTranscript_PerState(t *testing.T) {
	tests := []struct {
		state PickState
		want  bool
	}{
		{PickQueued, false},
		{PickClaiming, false},
		{PickRunning, true},
		{PickHeld, false},
		{PickSettled, true},
		{PickDissolved, false},
		{PickTerminated, true},
		{PickFailed, true},
	}
	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
			if got := hasTranscript(tt.state); got != tt.want {
				t.Errorf("hasTranscript(%s) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

// drillInOpen prepares a fake forge issue #42 with a running pick and a
// one-line transcript, then drives the launcher through tab/enter to land on
// an open drill-in transcript pane, ready for a test to assert further
// keystrokes against.
func drillInOpen(t *testing.T) *teatest.TestModel {
	t.Helper()

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

	return tm
}

// TestTea_DrillInKey_OpensTranscriptPane verifies Enter, focused on the work
// queue, opens a full-screen transcript pane for the highlighted running
// pick, replacing the backlog (issue #786; retargeted to focused-queue Enter
// by issue #845, which retires the old "d"/backlog-Enter drill-in binding).
func TestTea_DrillInKey_OpensTranscriptPane(t *testing.T) {
	tm := drillInOpen(t)

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	waitFinished(t, tm)
}

// TestTea_DrillInKey_QuitsWithoutClosing verifies "q" hard-quits straight
// out of an open drill-in pane, without requiring "x" first — the drill-in
// guard in handleKey must not swallow the universal quit keystroke (issue
// #826).
func TestTea_DrillInKey_QuitsWithoutClosing(t *testing.T) {
	tm := drillInOpen(t)

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_DrillInKey_QuitsOnCtrlCWithoutClosing verifies "ctrl+c" hard-quits
// straight out of an open drill-in pane, without requiring "x" first — same
// universal-quit carve-out as "q" (issue #826).
func TestTea_DrillInKey_QuitsOnCtrlCWithoutClosing(t *testing.T) {
	tm := drillInOpen(t)

	sendKey(tm, "ctrl+c")
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)

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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
}

// TestTea_HandleKey_RebuildOutputKey_OpensPaneWhenOutputPresent verifies "o"
// opens the rebuild-output pane once a rebuild has captured output (issue
// #1128).
func TestTea_HandleKey_RebuildOutputKey_OpensPaneWhenOutputPresent(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{RebuildOutput: "l0\nl1"})
	tm := teaModel{m: m}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if !tm.m.ShowRebuildOutput {
		t.Error("ShowRebuildOutput = false after \"o\" with output present, want true")
	}
}

// TestTea_HandleKey_RebuildOutputKey_NoOpWhenOutputEmpty verifies "o" is a
// no-op with nothing captured yet.
func TestTea_HandleKey_RebuildOutputKey_NoOpWhenOutputEmpty(t *testing.T) {
	tm := teaModel{m: NewModel()}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if tm.m.ShowRebuildOutput {
		t.Error("ShowRebuildOutput = true with no output captured, want false")
	}
}

// TestTea_HandleRebuildOutputKey_ClosesOnXOrEsc verifies "x" and "esc" both
// close the rebuild-output pane, mirroring the drill-in pane's close keys.
func TestTea_HandleRebuildOutputKey_ClosesOnXOrEsc(t *testing.T) {
	for _, key := range []string{"x", "esc"} {
		m := Update(NewModel(), StaleStatusMsg{RebuildOutput: "l0\nl1"})
		m = Update(m, RebuildOutputOpenMsg{})
		tm := teaModel{m: m}

		tm.m = tm.handleRebuildOutputKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if tm.m.ShowRebuildOutput {
			t.Errorf("key %q: ShowRebuildOutput = true, want false (closed)", key)
		}
	}
}

// TestTea_HandleRebuildOutputKey_ScrollsOnJK verifies "j"/"k" move
// RebuildOutputOffset while the pane is open.
func TestTea_HandleRebuildOutputKey_ScrollsOnJK(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{RebuildOutput: "l0\nl1\nl2\nl3\nl4"})
	m = Update(m, RebuildOutputOpenMsg{})
	tm := teaModel{m: m}

	tm.m = tm.handleRebuildOutputKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tm.m.RebuildOutputOffset != 1 {
		t.Errorf("RebuildOutputOffset = %d after \"j\", want 1", tm.m.RebuildOutputOffset)
	}

	tm.m = tm.handleRebuildOutputKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tm.m.RebuildOutputOffset != 0 {
		t.Errorf("RebuildOutputOffset = %d after \"k\", want 0", tm.m.RebuildOutputOffset)
	}
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
}

// waitGoroutine polls the goroutine dump for name, failing the test if its
// presence doesn't match want within a second. Matches by name rather than a
// raw runtime.NumGoroutine() diff, since unrelated Cmd goroutines (e.g.
// pollTick) would conflate with the one under test.
func waitGoroutine(t *testing.T, name string, want bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		var buf bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
		if strings.Contains(buf.String(), name) == want {
			return
		}
		if time.Now().After(deadline) {
			if want {
				t.Fatalf("%s goroutine never started:\n%s", name, buf.String())
			}
			t.Fatalf("%s goroutine leaked after quit:\n%s", name, buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTea_QuitKey_CancelsWaitRefreshSignalGoroutine verifies quitting a
// session with a live launch that never signals a refresh doesn't leak the
// waitRefreshSignal goroutine blocked on Launcher.Refreshes() (issue #823) —
// bubbletea can't cancel a Cmd goroutine itself (it's parked on <-ch forever
// by design), so teaModel must cancel it on the way out. Two-phase check:
// confirms the goroutine exists before quit (otherwise a regression that
// skips arming it would pass vacuously — there'd be nothing to cancel), then
// confirms it's gone after.
func TestTea_QuitKey_CancelsWaitRefreshSignalGoroutine(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	launch := &Launcher{CodeForge: f, Queue: NewQueue(), pollInterval: time.Hour}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "first")
	waitGoroutine(t, "waitRefreshSignal.func1", true)

	sendKey(tm, "q")
	waitFinished(t, tm)
	waitGoroutine(t, "waitRefreshSignal.func1", false)
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
	waitFinished(t, tm)
}

// TestTea_WithLauncher_RendersLiveQueueState verifies every render — not
// just the one right after a pick — reflects the launcher's live Queue
// state, so a transition that happens entirely in the background (claim,
// run, settle, or a raced-claim dissolve) actually reaches the operator's
// screen (#646 AC4, AC6).
func TestTea_WithLauncher_RendersLiveQueueState(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent", "priority-p1", "bug"}})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	// Simulate a state transition that happened entirely on the background
	// Queue (a real launch's claim/run/settle) — isolating the render-sync
	// behavior from how a pick enters the queue.
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickDissolved, Reason: "issue is closed"})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	// A realistic 3-label backlog issue widens the backlog column to its
	// leftColumnFraction cap on an 80-col terminal, which narrows the queue
	// column — but the Reason badge now sits ahead of Title in the row
	// (issue #858), so it renders in full and Title clips instead.
	waitForOutput(t, tm, "dissolved", "issue is closed")

	sendKey(tm, "q")
	waitFinished(t, tm)
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
	// Same leftColumnFraction/row-order rationale as
	// TestTea_WithLauncher_RendersLiveQueueState, but with the 2-label
	// backlog issue above — the "held by" badge sits ahead of Title, so
	// it renders in full and Title clips instead.
	waitForOutput(t, tm, "held", "held by #41 (native)")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_WithLauncher_RendersBlockerDespiteLongTitle verifies a held pick's
// "held by #N" badge reaches the rendered output through the tea layer even
// paired with a realistically long title on an 80-column terminal — the
// queue row previously put Title before BlockedBy, so clip()'s tail
// truncation dropped the blocker badge first (issue #858).
func TestTea_WithLauncher_RendersBlockerDespiteLongTitle(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the launcher retry backoff for the dispatch workflow", State: forge.IssueOpen})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the launcher retry backoff for the dispatch workflow", State: PickHeld, BlockedBy: "#41 (native)"})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "held", "held by #41 (native)")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_StaleStatus_RendersBanner verifies the tea layer's per-render sync
// installs the launcher's live stale verdict onto the view, exactly as
// syncQueue does for the picks queue — the operator sees the banner without
// an explicit refresh (issue #652 AC1).
func TestTea_StaleStatus_RendersBanner(t *testing.T) {
	f := forge.NewFake()
	launch := newTestLauncher(t, f)
	markStale(launch, "rebuild needed")

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "stale", "rebuild needed")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_StaleDetectedWhileIdle_SignalsRefreshWithoutPoll verifies stale
// detection itself wakes an already-idling Program — not the next poll tick
// (90s) or a coincidental Msg. TestTea_StaleStatus_RendersBanner above drives
// staleness synchronously via freshnessChecker() before the tea model even
// exists, masking this exact gap; this test detects staleness only after the
// Program is running idle, with pollInterval set far longer than the test's
// wait window so a poll tick could never be the cause (issue #762).
func TestTea_StaleDetectedWhileIdle_SignalsRefreshWithoutPoll(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)
	launch.pollInterval = time.Hour

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "first")

	markStale(launch, "rebuild needed")
	waitForOutput(t, tm, "stale", "rebuild needed")

	sendKey(tm, "q")
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	// "settled 1" proves the fake Dispatch this promotion launched has
	// finished — otherwise "q" can race the still-live pick onto the quit
	// confirm (issue #822) instead of exiting, hanging until teatest's
	// timeout, the same guard the "p"-key pick tests already carry.
	waitForOutput(t, tm, "#42", "fix the thing", "settled 1")

	sendKey(tm, "q")
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	// "settled 1" guards the same live-dispatch quit-confirm race
	// TestTea_PickKey_PromotesAndQueuesHighlighted hits (issue #822):
	// without it, "q" can race the still-live pick and hang until
	// teatest's timeout instead of exiting.
	waitForOutput(t, tm, "  #42", "settled 1")

	sendKey(tm, "q")
	waitFinished(t, tm)

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
	// timeout fires unassisted, resolving to a single pick; "settled 1"
	// guards the same live-dispatch quit-confirm race (issue #822).
	waitForOutput(t, tm, "  #42", "settled 1")

	sendKey(tm, "q")
	waitFinished(t, tm)

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
	waitFinished(t, tm)

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
	// "settled 1" guards the same live-dispatch quit-confirm race the other
	// launch-backed pick tests guard against (issue #822): without it, "q"
	// can race the still-live pick and hang until teatest's timeout instead
	// of exiting.
	waitForOutput(t, tm, "  #42", "settled 1")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_PickKey_FollowedByNonA_ClearsPendingIndicator verifies the pending
// pick indicator armed by "p" clears once a trailing non-a/q key resolves
// the chord to a single-issue pick (issue #1238).
func TestTea_PickKey_FollowedByNonA_ClearsPendingIndicator(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "p")
	waitForOutput(t, tm, "p_")

	sendKey(tm, "z") // not "a" — resolves to a single pick right away
	// "settled 1" guards the same live-dispatch quit-confirm race
	// TestTea_PickKey_PromotesAndQueuesHighlighted hits (issue #822):
	// without it, "q" can race the still-live pick and hang until
	// teatest's timeout instead of exiting.
	waitForOutput(t, tm, "  #42", "settled 1")

	sendKey(tm, "q")
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.PendingPick {
		t.Errorf("PendingPick = true after non-a/q key resolved the chord, want false")
	}
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
	waitFinished(t, tm)

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

	// Positive path: the live Queue shows an active row while Model.Picks
	// is empty/stale — alreadyActive must read the live Queue.Snapshot(),
	// not just fall through to Model.Picks, for each non-terminal state.
	for _, state := range []PickState{PickQueued, PickHeld, PickClaiming, PickRunning} {
		t.Run(state.String(), func(t *testing.T) {
			f := forge.NewFake()
			launch := &Launcher{CodeForge: f, Queue: NewQueue()}
			tm := newTeaModel(f, t.TempDir(), launch)

			launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: state})

			if !tm.alreadyActive("42") {
				t.Errorf("alreadyActive(%q) = false, want true — live Queue shows %v", "42", state)
			}
		})
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
	waitFinished(t, tm)

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
	waitFinished(t, tm)

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
	waitFinished(t, tm)

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
	// "settled 1" alongside "late arrival" proves the pick's fake Dispatch
	// has finished before "q", guarding the live-dispatch quit-confirm race
	// (issue #822) — without it "q" can race the still-live pick onto the
	// confirm and hang until teatest's timeout.
	waitForOutput(t, tm, "late arrival", "settled 1")

	sendKey(tm, "q")
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)

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
	waitFinished(t, tm)

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

	tm := newTeaModel(f, t.TempDir(), launch)
	// tryLaunch runs synchronously inside the "+" case, not via the
	// returned Cmd, so discarding both return values here still exercises
	// it — a future move of that call into the Cmd would leave the pick
	// stranded at PickQueued below and this test would go red, not pass
	// silently.
	tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})

	// launch.Wait() joins the drain goroutine the fallback tryLaunch call
	// spawns, so this blocks on the real launch instead of a rendered frame
	// under a stopwatch (issue #1327) — no teatest, no wall-clock timeout.
	launch.Wait()

	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickSettled {
		t.Errorf("Queue.Snapshot() = %+v, want #42 at PickSettled (fallback tryLaunch never ran)", snap)
	}
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
	waitFinished(t, tm)
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
		RebuildFn: func() (string, string, error) { rebuilt <- struct{}{}; return "", "", nil },
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "running ")

	sendKey(tm, "b")
	sendKey(tm, "q")
	waitFinished(t, tm)

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
		RebuildFn: func() (string, string, error) {
			stale.Store(false)
			close(rebuilt)
			return "", "", nil
		},
	}
	// It carries an open blocker so the post-rebuild re-drain (Rebuild
	// calls tryLaunch again on success) holds it instead of actually
	// launching a Box — this Launcher has no Factory to run one.
	queueStalePick(t, launch, f)

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
		if stale, _, _, _, _, _ := launch.StaleStatus(); !stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stale gate never cleared after a successful rebuild")
		}
		time.Sleep(time.Millisecond)
	}

	sendKey(tm, "q")
	waitFinished(t, tm)
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
	waitFinished(t, tm)

	if len(fr.KillCalls) != 1 || fr.KillCalls[0] != "agent-issue-42" {
		t.Errorf("KillCalls = %v, want exactly one kill of agent-issue-42", fr.KillCalls)
	}
}

// TestTea_TerminateKey_ConfirmPrompt_HintsQuitKeys verifies the live confirm
// prompt itself hints that q/ctrl+c decline and quit, not just the "?" help
// overlay — discoverability gap flagged by #748 review (issue #1095).
func TestTea_TerminateKey_ConfirmPrompt_HintsQuitKeys(t *testing.T) {
	launch, fc, _, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "k")
	waitForOutput(t, tm, "terminate #42?", "y/N/q/ctrl+c")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")

	sendKey(tm, "d")
	waitFinished(t, tm)
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
	waitFinished(t, tm)

	if len(fr.KillCalls) != 1 {
		t.Errorf("KillCalls = %v, want exactly one kill", fr.KillCalls)
	}
}

// TestTea_TerminateKey_QueueFocused_TargetsQueueHighlighted verifies "k"
// resolves the queue's own highlighted row (QueueCursor) while the queue has
// focus, not the backlog's stale Cursor — the two only move independently
// (model.go's CursorMoveMsg branch), so a queue-focused "down" leaves the
// backlog Cursor sitting on #42 while the operator is looking at #43
// highlighted in the queue column (view.go only draws ">" for the focused
// pane). Confirming must target what's on screen, not the invisible backlog
// row (issue #997).
func TestTea_TerminateKey_QueueFocused_TargetsQueueHighlighted(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)
	fc.SetIssue(forge.Issue{Number: "43", Title: "also running", Labels: []string{"agent-in-progress"}})
	launch.Queue.Add(Pick{Number: "43", Title: "also running", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "also running")

	sendKey(tm, "tab")  // focus queue; QueueCursor stays at 0 (#42)
	sendKey(tm, "down") // QueueCursor -> 1 (#43); backlog Cursor untouched at 0 (#42)
	sendKey(tm, "k")
	waitForOutput(t, tm, "terminate #43?", "y/N")

	sendKey(tm, "y")
	waitForOutput(t, tm, "terminated")

	// #42 is still PickRunning (only #43 was targeted), so "q" arms the
	// live-Dispatch quit confirm (issue #822) rather than exiting outright.
	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	waitFinished(t, tm)

	if len(fr.KillCalls) != 1 || fr.KillCalls[0] != "agent-issue-43" {
		t.Errorf("KillCalls = %v, want exactly one kill of agent-issue-43", fr.KillCalls)
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
	waitFinished(t, tm)

	if len(fr.KillCalls) != 0 {
		t.Errorf("KillCalls = %v, want none after declining", fr.KillCalls)
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickRunning {
		t.Errorf("queue pick = %+v, want still PickRunning after declining", snap)
	}
}

// TestTea_TerminateKey_ConfirmThenQuit_ArmsPendingQuitConfirm verifies the
// universal quit keystroke at the terminate-confirm prompt is not swallowed
// by the pending terminate, but also does not quit outright: "q" declines
// the terminate and arms the same drain/terminate-all/stay confirm the main
// quit key uses, since a terminate-confirm prompt guarantees a live
// Dispatch (issue #1215, ADR 0023). Driving "d" (drain) finishes the
// scenario without killing anything.
func TestTea_TerminateKey_ConfirmThenQuit_ArmsPendingQuitConfirm(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "k")
	waitForOutput(t, tm, "terminate #42?")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches", "drain", "terminate-all", "stay")

	sendKey(tm, "d")
	waitFinished(t, tm)

	if len(fr.KillCalls) != 0 {
		t.Errorf("KillCalls = %v, want none after draining at the quit confirm", fr.KillCalls)
	}
}

// TestTea_TerminateKey_ConfirmThenQuit_TerminateAll_ReapsLiveDispatch
// verifies "t" (terminate-all) works when the quit confirm is reached via
// the terminate-confirm "q" escape, not just the main quit key (issue
// #1215).
func TestTea_TerminateKey_ConfirmThenQuit_TerminateAll_ReapsLiveDispatch(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "k")
	waitForOutput(t, tm, "terminate #42?")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")

	sendKey(tm, "t")
	waitFinished(t, tm)
	launch.Wait()

	if len(fr.KillCalls) != 1 || fr.KillCalls[0] != "agent-issue-42" {
		t.Errorf("KillCalls = %v, want exactly one kill of agent-issue-42", fr.KillCalls)
	}
}

// TestTea_TerminateKey_ConfirmThenQuit_Stay_KeepsRunning verifies "s" (stay)
// works when the quit confirm is reached via the terminate-confirm "q"
// escape, declining the quit and leaving the session running (issue #1215).
func TestTea_TerminateKey_ConfirmThenQuit_Stay_KeepsRunning(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "k")
	waitForOutput(t, tm, "terminate #42?")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")

	sendKey(tm, "s")
	waitForOutput(t, tm, "fix the thing") // confirm prompt gone, backlog/queue view back

	// Still live after staying — drain to finish the test cleanly.
	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	waitFinished(t, tm)

	if len(fr.KillCalls) != 0 {
		t.Errorf("KillCalls = %v, want none after staying", fr.KillCalls)
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickRunning {
		t.Errorf("queue pick = %+v, want still PickRunning after staying", snap)
	}
}

// TestTea_TerminateKey_ConfirmThenYes_RespondsWhileTrackerCommentBlocks
// verifies handleTerminateConfirmKey's "y" branch — the Update-path call
// site itself, driven by a real keypress through teatest, not a bare method
// call — returns before Launcher.TerminateAsync's backgrounded Terminate
// call finishes its tracker.Comment I/O (issue #745). A blockingCommentTracker
// wired under newTeaModel holds Comment open on an unblock channel; while it
// is still blocked, the confirm prompt must already be gone and the backlog
// view back (proving Update returned) and the queue pick must still read
// PickRunning (proving Terminate itself, which only sets PickTerminated
// after Comment returns, has not reached that line yet) — asserting the
// Update path never blocked, not just that the terminate eventually
// completes, which TestTea_TerminateKey_ConfirmThenYes_ReclaimsHighlightedDispatch
// already covers with a non-blocking tracker (issue #1084).
func TestTea_TerminateKey_ConfirmThenYes_RespondsWhileTrackerCommentBlocks(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)
	bt := &blockingCommentTracker{IssueTracker: fc, unblock: make(chan struct{})}

	tm := teatest.NewTestModel(t, newTeaModel(bt, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "k")
	waitForOutput(t, tm, "terminate #42?", "y/N")

	sendKey(tm, "y")
	// If handleTerminateConfirmKey ever called Terminate synchronously
	// instead of TerminateAsync, Update itself would block here on
	// bt.unblock (still closed below) and this render would never arrive —
	// waitForOutput would hang until teatestTimeout and fail the test.
	waitForOutput(t, tm, "fix the thing") // confirm prompt gone, backlog/queue view back

	// The background goroutine's own scheduling isn't ordered against this
	// render, so poll rather than read commentHit once — a bounded wait
	// still fails fast if Comment was never reached at all.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&bt.commentHit) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Comment was never called on the tracker")
		}
		time.Sleep(time.Millisecond)
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickRunning {
		t.Fatalf("queue pick = %+v, want still PickRunning while Comment is blocked", snap)
	}

	close(bt.unblock)
	waitForOutput(t, tm, "terminated")

	sendKey(tm, "q")
	waitFinished(t, tm)

	if len(fr.KillCalls) != 1 || fr.KillCalls[0] != "agent-issue-42" {
		t.Errorf("KillCalls = %v, want exactly one kill of agent-issue-42", fr.KillCalls)
	}
}

// TestTea_TerminateConfirmKey_Quit_ClearsPendingTerminateArmsPendingQuit
// verifies "q"/"ctrl+c" at the terminate confirm prompt declines the
// terminate (clearing PendingTerminate, so a later keypress cannot loop back
// into this same handler) and arms the quit confirm instead of quitting
// directly (issue #1215).
func TestTea_TerminateConfirmKey_Quit_ClearsPendingTerminateArmsPendingQuit(t *testing.T) {
	keys := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyCtrlC},
	}
	for _, key := range keys {
		t.Run(key.String(), func(t *testing.T) {
			m := Update(NewModel(), TerminateRequestedMsg{Number: "42"})
			tm := teaModel{m: m}

			tm = tm.handleTerminateConfirmKey(key)

			if tm.m.PendingTerminate != "" {
				t.Errorf("PendingTerminate = %q, want cleared after declining the terminate to quit", tm.m.PendingTerminate)
			}
			if !tm.m.PendingQuit {
				t.Error("PendingQuit = false, want true after quitting at the terminate confirm prompt")
			}
			if tm.m.Quitting {
				t.Error("Quitting = true, want false — quit confirm armed, not yet decided")
			}
		})
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

// queueStalePick queues a pick and drains it through tryLaunch — a queued
// pick is what actually hits the stale gate in production (Rebuild's own doc
// comment: "any pick held ... through the stale window") — tryLaunch is a
// real no-op on an empty queue post-#754, so an empty-queue call here would
// never reach freshnessChecker at all.
func queueStalePick(t *testing.T, launch *Launcher, f forge.IssueTracker) {
	t.Helper()
	launch.Queue.Add(Pick{Number: "1", Title: "placeholder", State: PickQueued})
	launch.tryLaunch(f, t.TempDir())
	launch.Wait()
}

// markStale sets launch.Fresh to report staleness with msg and runs
// freshnessChecker() once to apply it synchronously — no queue, no
// goroutine, no Wait().
func markStale(launch *Launcher, msg string) {
	launch.Fresh = func() (bool, bool, string) { return true, false, msg }
	launch.freshnessChecker()()
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)
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
	waitFinished(t, tm)

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
	waitFinished(t, tm)
}

// TestTea_PickKeyThenQuit_WithLiveDispatch_ArmsPendingQuitConfirm verifies
// the pending-pick chord's "q" resolves the pick and then still checks for
// live Dispatches before quitting, arming the confirm dialog instead of
// exiting immediately (issue #1216).
func TestTea_PickKeyThenQuit_WithLiveDispatch_ArmsPendingQuitConfirm(t *testing.T) {
	launch, fc, _, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "p")
	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches", "drain", "terminate-all", "stay")

	sendKey(tm, "d")
	waitFinished(t, tm)
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
	waitFinished(t, tm)
}

// TestOrphanRecoveryCmd_OrphanedIssuesErr_ReturnsMsg verifies a failed
// OrphanedIssues() lookup surfaces through the returned tea.Msg instead of
// being swallowed silently (issue #1218).
func TestOrphanRecoveryCmd_OrphanedIssuesErr_ReturnsMsg(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	fr := runner.NewFake()
	fr.ListRunningErr = errors.New("boom")
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	launch := &Launcher{
		Factory:   factory,
		RecoverFn: func(string) error { return nil },
	}

	msg := orphanRecoveryCmd(launch)()

	rec, ok := msg.(OrphanRecoveryMsg)
	if !ok {
		t.Fatalf("orphanRecoveryCmd()() = %#v (%T), want OrphanRecoveryMsg", msg, msg)
	}
	if !strings.Contains(rec.Err, "boom") {
		t.Errorf("OrphanRecoveryMsg.Err = %q, want it to mention the OrphanedIssues() failure %q", rec.Err, "boom")
	}
}

// TestOrphanRecoveryCmd_RecoverFnErr_ReturnsMsg verifies a failed adopt
// surfaces through the returned tea.Msg, naming the issue number and the
// underlying error, instead of being swallowed (issue #1218).
func TestOrphanRecoveryCmd_RecoverFnErr_ReturnsMsg(t *testing.T) {
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

	launch := &Launcher{
		Factory:   factory,
		RecoverFn: func(string) error { return errors.New("adopt boom") },
	}

	msg := orphanRecoveryCmd(launch)()

	rec, ok := msg.(OrphanRecoveryMsg)
	if !ok {
		t.Fatalf("orphanRecoveryCmd()() = %#v (%T), want OrphanRecoveryMsg", msg, msg)
	}
	if !strings.Contains(rec.Err, "42") || !strings.Contains(rec.Err, "adopt boom") {
		t.Errorf("OrphanRecoveryMsg.Err = %q, want it to name issue 42 and mention %q", rec.Err, "adopt boom")
	}
}

// TestTea_Init_OrphanRecoveryErr_SurfacedInHeader verifies a failed adopt at
// startup reaches the rendered header through the real Bubble Tea event loop
// — not just the pure Update function in isolation (issue #1218).
func TestTea_Init_OrphanRecoveryErr_SurfacedInHeader(t *testing.T) {
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

	launch := &Launcher{
		CodeForge: f,
		Factory:   factory,
		Queue:     NewQueue(),
		RecoverFn: func(string) error { return errors.New("adopt boom") },
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "orphan recovery failed")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_WideCharacterTitle_NeverOverflowsTerminalWidth verifies backlog and
// queue titles full of wide CJK characters render within the terminal's
// actual display width through the full Bubble Tea render path, not just
// rune count — a rune-count-only clip truncates by counting runes instead of
// columns, so a mostly-wide-character row can survive truncation still
// twice as wide as the column budget it was meant to enforce (issue #859
// AC4/AC6). Both columns carry a wide title so a fix that only tightens one
// side can't hide behind the other's slack. Table cases extend coverage
// beyond CJK to emoji, zero-width combining marks, and ANSI-escaped content
// (issue #1261) — each character type has a different display-width
// calculation, so a fix that's correct for one can still be wrong for
// another.
func TestTea_WideCharacterTitle_NeverOverflowsTerminalWidth(t *testing.T) {
	tests := []struct {
		name         string
		backlogTitle string
		queueTitle   string
	}{
		{
			name:         "CJK wide characters",
			backlogTitle: strings.Repeat("中", 40), // 40 runes / 80 display columns
			queueTitle:   strings.Repeat("文", 40),
		},
		{
			// U+FE0F is an emoji variation selector: it's zero-width
			// itself and go-runewidth's current width table already
			// keeps rocket/sparkles at a constant width with or without
			// it, so this fixture doesn't prove VS16-conditional width
			// -- it pins that a title carrying the codepoint still
			// renders and clips like plain emoji, per AC's "including
			// emoji variation selectors" requirement.
			name:         "emoji with variation selector",
			backlogTitle: strings.Repeat("\U0001F680\ufe0f", 40), // rocket + U+FE0F emoji variation selector
			queueTitle:   strings.Repeat("\u2728\ufe0f", 40),     // sparkles + U+FE0F emoji variation selector
		},
		{
			name:         "zero-width combining marks",
			backlogTitle: strings.Repeat("e\u0301", 80), // decomposed e + U+0301 combining acute accent, 80 base+mark pairs / 80 display columns
			queueTitle:   strings.Repeat("n\u0303", 80), // decomposed n + U+0303 combining tilde
		},
		{
			name:         "ANSI-escaped content",
			backlogTitle: "\x1b[31m" + strings.Repeat("critical ", 9) + "\x1b[0m", // 9 words x 9 cols = 81 visible columns once SanitizeControlSequences strips the SGR escape
			queueTitle:   "\x1b[33m" + strings.Repeat("blocked ", 10) + "\x1b[0m", // 10 words x 8 cols = 80 visible columns once SanitizeControlSequences strips the SGR escape
		},
		{
			// realistic issue title: natural English phrasing mixing an
			// emoji and an accented word, not a synthetic repeat of one
			// rune, and long enough to force real clipping like the other
			// cases rather than just smoke-testing render.
			name:         "realistic issue title with emoji and accent",
			backlogTitle: "🚀 fix the launcher retry backoff for the café dispatch workflow so it survives transient outages",
			queueTitle:   "✨ add naïve caching layer for the GitHub API v2 client to cut redundant round trips",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := forge.NewFake()
			f.SetIssue(forge.Issue{Number: "1", Title: tt.backlogTitle, State: forge.IssueOpen})
			launch := &Launcher{CodeForge: f, Queue: NewQueue()}
			launch.Queue.Add(Pick{Number: "2", Title: tt.queueTitle, State: PickQueued})

			tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
			waitForOutput(t, tm, "#1")

			sendKey(tm, "q")
			final := tm.FinalModel(t, teatest.WithFinalTimeout(teatestTimeout)).(teaModel)

			// #859's AC called for "golden output reflects correct clipping
			// at visual column boundaries" — read literally that implies a
			// golden-file snapshot, but this package has no golden-file
			// tooling at all, and none of its other clip tests use one
			// either (view_test.go's
			// TestView_TwoColumn_Body_LinesNeverExceedTerminalWidth and
			// TestClip_WideCharacters_MeasuresDisplayWidthNotRuneCount both
			// use this same width idiom, just against their own budgets).
			// The per-line display-width check below, run against real
			// Bubble Tea render output, IS this package's established
			// verification for "correct clipping at visual column
			// boundaries": it fails exactly when a line spills past the
			// column budget, which is what a golden diff would also catch
			// here, without requiring fixture upkeep for a rendering
			// surface this volatile (issue #1260).
			out := View(final.m)
			for _, l := range strings.Split(out, "\n") {
				if w := runewidth.StringWidth(l); w > 80 {
					t.Errorf("View() line %q has display width %d, want it clamped to Width (80)", l, w)
				}
			}
			if !strings.Contains(out, "#1") {
				t.Errorf("View() = %q, want the backlog issue number #1 to survive clipping", out)
			}
			if !strings.Contains(out, "#2") {
				t.Errorf("View() = %q, want the queue pick number #2 to survive clipping", out)
			}
		})
	}
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
