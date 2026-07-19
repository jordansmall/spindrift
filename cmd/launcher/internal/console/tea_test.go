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
	"github.com/charmbracelet/lipgloss"
	teatest "github.com/charmbracelet/x/exp/teatest"

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

// rowNumberCell returns the exact padded number cell a Section table row
// renders for issue/pick number n (view.go's numberColWidth) — used to build
// assertions that disambiguate "#4" from "#40" against the real column
// width instead of a hand-counted space literal.
func rowNumberCell(n string) string {
	return clip("#"+n, numberColWidth, true)
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

// TestTea_CursorKeys_MoveHighlightedRow verifies j/down and k/up move the
// cursor marker across the visible backlog — vim's standard pair, restored
// now that Terminate moved off "k" to "X" (issue #784, #838, #1500).
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

	sendKey(tm, "k")
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_GKey_JumpsToLastRow verifies "G" moves the cursor marker straight
// to the backlog's last row, scrolling it into view (issue #1628 AC1).
func TestTea_GKey_JumpsToLastRow(t *testing.T) {
	f := forge.NewFake()
	for i := 0; i < 50; i++ {
		f.SetIssue(forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i), State: forge.IssueOpen})
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 10))
	waitForOutput(t, tm, "> #0")

	sendKey(tm, "G")
	waitForOutput(t, tm, "> "+rowNumberCell("49"))

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_ggChord_JumpsToFirstRow verifies a lone "g" followed by a second
// "g" moves the cursor marker back to the first row and resets the scroll
// offset, from a cursor already scrolled well down the list (issue #1628
// AC2).
func TestTea_ggChord_JumpsToFirstRow(t *testing.T) {
	f := forge.NewFake()
	for i := 0; i < 50; i++ {
		f.SetIssue(forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i), State: forge.IssueOpen})
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 10))
	waitForOutput(t, tm, "> #0")

	sendKey(tm, "G")
	waitForOutput(t, tm, "> "+rowNumberCell("49"))

	sendKey(tm, "g")
	sendKey(tm, "g")
	waitForOutput(t, tm, "> "+rowNumberCell("0"))

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_gLeader_NonGKey_CancelsAndStillActsNormally verifies a lone "g"
// followed by any other key cancels the pending leader without consuming
// that key — unlike the "pa" chord, which resolves to a pick, the g-leader's
// AC requires the second key's own binding to still apply (issue #1628 AC).
func TestTea_gLeader_NonGKey_CancelsAndStillActsNormally(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	f.SetIssue(forge.Issue{Number: "2", Title: "second", State: forge.IssueOpen})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "g")
	sendKey(tm, "j") // not "g" — cancels the leader, then still moves the cursor down
	waitForOutput(t, tm, "> #2")

	sendKey(tm, "q")
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.PendingG {
		t.Errorf("PendingG = true after a non-g key, want false")
	}
}

// TestTea_gLeader_Timeout_CancelsPendingG verifies a lone "g" left
// unanswered cancels the pending leader once the 200ms window times out —
// mirroring TestTea_PickKey_Timeout_ClearsPendingIndicator for the "gg"
// chord (issue #1628 AC).
func TestTea_gLeader_Timeout_CancelsPendingG(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "g")
	time.Sleep(gChordTimeout + 50*time.Millisecond)

	sendKey(tm, "q")
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.PendingG {
		t.Errorf("PendingG = true after the leader window timed out, want false")
	}
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
	waitForOutput(t, tm, rowNumberCell("4")+" issue 4")

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
	picks := make([]Pick, 3)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})
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
	waitForOutput(t, tm, rowNumberCell("25")+" issue 25")

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
	waitForOutput(t, tm, rowNumberCell("4")+" issue 4")

	sendKey(tm, "pgdown")
	waitForOutput(t, tm, rowNumberCell("8")+" issue 8")

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

// TestTea_ScrollKeys_PageThroughRunningSection verifies pgdown pages the
// Running Section's own viewport once "2" has switched there — paging works
// for whichever Section is active (issue #1037 AC4, generalized from Tab
// focus to ActiveSection by issue #1500).
func TestTea_ScrollKeys_PageThroughRunningSection(t *testing.T) {
	f := forge.NewFake()
	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	for i := 0; i < 50; i++ {
		launch.Queue.Add(Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued})
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 10))
	sendKey(tm, "2")
	waitForOutput(t, tm, "pick 0")

	sendKey(tm, "pgdown")
	waitForOutput(t, tm, rowNumberCell("4")+" pick 4")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_SectionKeys_HLAndDigitsSwitchSections verifies "H"/"L" step
// between Sections and a digit jumps straight to one, and that cursor keys
// act on whichever Section is now active — the section-switched list's
// navigation (ADR 0030), replacing the retired Tab focus-toggle (issue
// #845, issue #1500).
func TestTea_SectionKeys_HLAndDigitsSwitchSections(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)
	launch.Queue.Add(Pick{Number: "10", Title: "pick one", State: PickQueued})
	launch.Queue.Add(Pick{Number: "11", Title: "pick two", State: PickQueued})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "first")

	sendKey(tm, "2")
	waitForOutput(t, tm, "pick one")

	sendKey(tm, "j")
	waitForOutput(t, tm, "> #11")

	sendKey(tm, "H")
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_EnterKey_OnRunningSection_DrillsRunningPick verifies Enter, on the
// Running Section, opens the highlighted pick's sidebar when its state is
// PickRunning — the context-sensitive Enter's work-Section drill (issue
// #845, generalized to ActiveSection by issue #1500, then to the sidebar by
// #1501).
func TestTea_EnterKey_OnRunningSection_DrillsRunningPick(t *testing.T) {
	tm := sidebarOpen(t)

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

// TestTea_EnterKey_OnRunningSection_NoOpOnQueuedRow verifies Enter, with focus
// on the work queue, is a no-op on a row that hasn't reached a
// Transcript-bearing state yet (PickQueued) — never opens a pane with
// nothing to show (issue #845).
func TestTea_EnterKey_OnRunningSection_NoOpOnQueuedRow(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "2")
	waitForOutput(t, tm, "queued")

	sendKey(tm, "enter")
	sendKey(tm, "q")
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil — Enter on a queued row must not open the sidebar", fm.m.Sidebar)
	}
}

// TestTea_EnterKey_OnRunningSection_ShowsNoticeOnQueuedRow verifies Enter, with
// focus on the work queue, renders a visible notice on a row that hasn't
// reached a Transcript-bearing state yet (PickQueued) — previously a silent
// no-op (issue #998).
func TestTea_EnterKey_OnRunningSection_ShowsNoticeOnQueuedRow(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "2")
	waitForOutput(t, tm, "queued")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "no transcript to show")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_EnterKey_OnRunningSection_NoticeClearsOnNextKey verifies the
// no-transcript notice armed by Enter clears once the operator's next
// keypress arrives — a one-shot hint, not a sticky one (issue #998).
func TestTea_EnterKey_OnRunningSection_NoticeClearsOnNextKey(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	launch := &Launcher{CodeForge: f, Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "2")
	waitForOutput(t, tm, "queued")

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

// sidebarOpen prepares a fake forge issue #42 with a running pick and a
// one-line transcript, then drives the launcher through the Running
// Section/enter to land on an open sidebar (fullscreen, since the 80-column
// test terminal is narrower than sidebarFits requires), ready for a test to
// assert further keystrokes against.
func sidebarOpen(t *testing.T) *teatest.TestModel {
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

	sendKey(tm, "2")
	waitForOutput(t, tm, "running")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42", "hi")

	return tm
}

// TestTea_PollTick_AdvancesOpenSidebarActivityFeed verifies the selected
// running Dispatch's Activity feed advances on its own, with no operator
// keypress, as its pass log grows across successive poll ticks — the payoff
// of the whole live-tail sidebar (issue #1502, ADR 0030).
func TestTea_PollTick_AdvancesOpenSidebarActivityFeed(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "logs", "issue-42.log")
	first := `{"type":"assistant","message":{"content":[{"type":"text","text":"first update"}]}}` + "\n"
	if err := os.WriteFile(logPath, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := newTestLauncher(t, f)
	launch.pollInterval = 5 * time.Millisecond
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "2")
	waitForOutput(t, tm, "running")
	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42", "first update")

	second := first + `{"type":"assistant","message":{"content":[{"type":"text","text":"second update"}]}}` + "\n"
	if err := os.WriteFile(logPath, []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, tm, "second update")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_SidebarKey_ScrollUpDetachesFollow_GReattaches verifies the
// operator-facing follow/detach/re-attach gesture end to end: the freshly
// opened sidebar shows "[follow]", scrolling up shows "[paused]", and "G"
// shows "[follow]" again (issue #1502, ADR 0030).
func TestTea_SidebarKey_ScrollUpDetachesFollow_GReattaches(t *testing.T) {
	// sidebarOpen's own waitForOutput already drained the frame that first
	// showed "[follow]" (tm.Output() drains as it's read) — asserting it
	// again here would block forever waiting for a repeat of bytes already
	// consumed, so the sequence starts from the first state-changing key
	// instead (issue #1502).
	tm := sidebarOpen(t)

	sendKey(tm, "k")
	waitForOutput(t, tm, "[paused]")

	sendKey(tm, "G")
	waitForOutput(t, tm, "[follow]")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_Sidebar_RetainsPositionAcrossDispatchSwitch verifies switching the
// docked sidebar from one running Dispatch to another and back restores the
// first Dispatch's scroll offset and detached Follow state exactly where the
// operator left it — hopping between running Dispatches never loses their
// place (issue #1502, ADR 0030).
func TestTea_Sidebar_RetainsPositionAcrossDispatchSwitch(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	f.SetIssue(forge.Issue{Number: "43", Title: "second thing", State: forge.IssueOpen})

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
	other := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-43.log"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := newTestLauncher(t, f)
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})
	launch.Queue.Add(Pick{Number: "43", Title: "second thing", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(sidebarMinListWidth+sidebarWidth+1, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "2")
	waitForOutput(t, tm, "running")
	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42")

	sendKey(tm, "pgdown") // moves Offset without detaching Follow
	sendKey(tm, "k")      // detaches Follow, Offset stays off 0
	waitForOutput(t, tm, "[paused]")

	sendKey(tm, "h")
	sendKey(tm, "j")
	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #43")

	sendKey(tm, "h")
	sendKey(tm, "k")
	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42")

	sendKey(tm, "q")
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.Sidebar == nil || fm.m.Sidebar.Number != "42" {
		t.Fatalf("Sidebar = %+v, want it reopened on #42", fm.m.Sidebar)
	}
	if fm.m.Sidebar.Follow {
		t.Error("Follow = true, want false — retained from before the switch to #43")
	}
	if fm.m.Sidebar.Offset == 0 {
		t.Error("Offset = 0, want the retained non-zero position from before the switch to #43")
	}
}

// TestTea_PollTick_DoesNotRefreshSettledSidebar verifies a Settled
// Dispatch's open sidebar never re-derives its Activity feed on a poll tick,
// even though its pass log grows on disk afterward — a Settled Dispatch has
// nothing left to tail (#1501 AC5), and refreshing it anyway would widen the
// bounded-I/O scope past "the selected Dispatch, while it's actually running"
// (review finding on issue #1502).
func TestTea_PollTick_DoesNotRefreshSettledSidebar(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "logs", "issue-42.log")
	first := `{"type":"assistant","message":{"content":[{"type":"text","text":"first update"}]}}` + "\n"
	if err := os.WriteFile(logPath, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := newTestLauncher(t, f)
	launch.pollInterval = 5 * time.Millisecond
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickSettled})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "4")
	waitForOutput(t, tm, "settled")
	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42", "first update")

	second := first + `{"type":"assistant","message":{"content":[{"type":"text","text":"second update"}]}}` + "\n"
	if err := os.WriteFile(logPath, []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}
	// No waitForOutput for "second update": there is nothing to wait for —
	// the point is it must never arrive. Sleep past several poll ticks
	// instead, then assert directly against the Model.
	time.Sleep(50 * time.Millisecond)

	sendKey(tm, "q")
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.Sidebar == nil {
		t.Fatal("Sidebar = nil, want it still open on #42")
	}
	if strings.Contains(fmt.Sprint(fm.m.Sidebar.Activity), "second update") {
		t.Errorf("Activity = %v, want the growth never to have arrived — a Settled Dispatch's sidebar must never refresh", fm.m.Sidebar.Activity)
	}
}

// TestTea_SidebarKey_OpensActivityPane verifies Enter, on the Running
// Section, opens a full-screen sidebar showing the highlighted running
// pick's Activity feed by default (issue #786; retargeted to a work-Section
// Enter by issue #845, generalized from FocusedColumn to ActiveSection by
// issue #1500, and from a fullscreen-only Transcript drill-in to the
// Activity-feed-default sidebar by #1501).
func TestTea_SidebarKey_OpensActivityPane(t *testing.T) {
	tm := sidebarOpen(t)

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	waitFinished(t, tm)
}

// TestTea_SidebarKey_QuitsWithoutClosing verifies "q" hard-quits straight
// out of an open sidebar, without requiring "x" first — the sidebar guard in
// handleKey must not swallow the universal quit keystroke (issue #826).
func TestTea_SidebarKey_QuitsWithoutClosing(t *testing.T) {
	tm := sidebarOpen(t)

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_SidebarKey_QuitsOnCtrlCWithoutClosing verifies "ctrl+c" hard-quits
// straight out of an open sidebar, without requiring "x" first — same
// universal-quit carve-out as "q" (issue #826).
func TestTea_SidebarKey_QuitsOnCtrlCWithoutClosing(t *testing.T) {
	tm := sidebarOpen(t)

	sendKey(tm, "ctrl+c")
	waitFinished(t, tm)
}

// TestTea_FocusKeys_MoveBetweenListAndDockedSidebar verifies "h"/"l" move
// keyboard focus between the list and a docked sidebar on a terminal wide
// enough to show both (sidebarFits): "h" moves it to the list, where "j"
// still moves the row cursor (proving "t" sent right after is a no-op on the
// list, not a sidebar toggle); "l" then returns focus to the sidebar, where
// "t" still cycles its content (#1501, ADR 0030). Asserted against the final
// Model rather than screen-scraped mid-sequence text — teatest's output
// reader only ever shows a frame that actually changed visible bytes, which
// an ANSI-stripped (NO_COLOR-profile) focus-only style change need not do.
func TestTea_FocusKeys_MoveBetweenListAndDockedSidebar(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	f.SetIssue(forge.Issue{Number: "43", Title: "second thing", State: forge.IssueOpen})

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
	launch.Queue.Add(Pick{Number: "43", Title: "second thing", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(sidebarMinListWidth+sidebarWidth+1, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "2")
	waitForOutput(t, tm, "running")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42", "second thing")

	sendKey(tm, "h")
	sendKey(tm, "j")
	waitForOutput(t, tm, "> #43")

	sendKey(tm, "t") // no-op: focus is on the list, not the sidebar
	sendKey(tm, "l")
	sendKey(tm, "t") // reaches the sidebar now that "l" refocused it

	// "q" hard-quits directly here, with no drain/terminate-all/stay confirm
	// — Focus ended on the sidebar above, and the sidebar's own "q" always
	// force-quits regardless of live Dispatches (issue #826's precedent,
	// inherited from the old drill-in pane).
	sendKey(tm, "q")
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.Focus != FocusSidebar {
		t.Errorf("Focus = %v, want FocusSidebar after \"l\" refocused it", fm.m.Focus)
	}
	if fm.m.Cursor != 1 {
		t.Errorf("Cursor = %d, want 1 — \"j\" must have moved the list cursor while \"h\" had focus there", fm.m.Cursor)
	}
	if fm.m.Sidebar == nil || !fm.m.Sidebar.ShowTranscript {
		t.Error("Sidebar.ShowTranscript = false, want true — the second \"t\" (sent after \"l\" refocused the sidebar) must have cycled it, proving the first \"t\" (sent while list-focused) was a no-op")
	}
}

// TestTea_SidebarKey_ClosesFromDockedListFocus verifies both "esc" and "x"
// close a docked sidebar even when keyboard focus is on the list — the
// key-routing guard in handleKey only ever reached handleSidebarKey's
// close case when Focus was on the sidebar, so a docked sidebar with focus
// moved back to the list ("h") could not be dismissed without first
// pressing "l" to refocus it (issue #1582).
func TestTea_SidebarKey_ClosesFromDockedListFocus(t *testing.T) {
	for _, key := range []string{"esc", "x"} {
		t.Run(key, func(t *testing.T) {
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

			tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(sidebarMinListWidth+sidebarWidth+1, 24))
			waitForOutput(t, tm, "fix the thing")

			sendKey(tm, "2")
			waitForOutput(t, tm, "running")
			sendKey(tm, "enter")
			waitForOutput(t, tm, "activity #42")

			sendKey(tm, "h") // move focus back to the list, sidebar stays docked open
			sendKey(tm, key)

			sendKey(tm, "q")
			waitForOutput(t, tm, "quit with live Dispatches")
			sendKey(tm, "d")
			waitFinished(t, tm)

			fm := tm.FinalModel(t).(teaModel)
			if fm.m.Sidebar != nil {
				t.Errorf("Sidebar = %+v, want nil — %q from docked list focus must close it", fm.m.Sidebar, key)
			}
			if fm.m.Focus != FocusList {
				t.Errorf("Focus = %v, want FocusList after close", fm.m.Focus)
			}
		})
	}
}

// TestTea_SidebarKey_NoSidebarListKeysStillNoOp verifies "x" and "esc" on the
// list remain a no-op when no sidebar is open — the new close case in the
// list handler only fires when Model.Sidebar is non-nil (issue #1582 AC2).
func TestTea_SidebarKey_NoSidebarListKeysStillNoOp(t *testing.T) {
	for _, key := range []string{"esc", "x"} {
		t.Run(key, func(t *testing.T) {
			f := forge.NewFake()
			f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

			tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
			waitForOutput(t, tm, "fix the thing")

			sendKey(tm, key)

			sendKey(tm, "q")
			waitFinished(t, tm)

			fm := tm.FinalModel(t).(teaModel)
			if fm.m.Sidebar != nil {
				t.Errorf("Sidebar = %+v, want nil — %q must stay a no-op with no sidebar open", fm.m.Sidebar, key)
			}
			if fm.m.Focus != FocusList {
				t.Errorf("Focus = %v, want FocusList unchanged", fm.m.Focus)
			}
		})
	}
}

// TestTea_EnterKey_OnStaticRow_OpensSidebar verifies Enter opens the sidebar
// for a Settled, Terminated, or Failed pick — the static case with nothing
// left to tail, still shown from its final on-disk logs (#1501 AC5).
func TestTea_EnterKey_OnStaticRow_OpensSidebar(t *testing.T) {
	tests := []struct {
		state      PickState
		sectionKey string
		stateWord  string
	}{
		{PickSettled, "4", "settled"},
		{PickTerminated, "5", "terminated"},
		{PickFailed, "5", "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
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
			launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: tt.state})

			tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
			waitForOutput(t, tm, "fix the thing")

			sendKey(tm, tt.sectionKey)
			waitForOutput(t, tm, tt.stateWord)

			sendKey(tm, "enter")
			waitForOutput(t, tm, "activity #42", "hi")

			sendKey(tm, "q")
			waitFinished(t, tm)
		})
	}
}

// TestTea_HandleKey_ArrowKeys_MirrorHAndL verifies the left/right arrow keys
// move sidebar focus exactly like "h"/"l" — the same case in handleKey's
// switch, exercised directly here since the two full teatest sequences above
// already cover "h"/"l" letter-by-letter (#1501, ADR 0030).
func TestTea_HandleKey_ArrowKeys_MirrorHAndL(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + 1, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42"})
	m = Update(m, FocusListMsg{}) // opening already focused the sidebar; start from the list
	tm := teaModel{m: m}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRight})
	if tm.m.Focus != FocusSidebar {
		t.Errorf("Focus = %v after right arrow, want FocusSidebar", tm.m.Focus)
	}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
	if tm.m.Focus != FocusList {
		t.Errorf("Focus = %v after left arrow, want FocusList", tm.m.Focus)
	}
}

// TestTea_HandleKey_ZKey_TogglesSidebarZoom verifies "z" forces the sidebar
// into its fullscreen zoom, and a second "z" releases it back to docked —
// exercised directly on a terminal wide enough to dock, so the toggle's
// effect isn't masked by sidebarFits' own narrow-terminal fallback (issue
// #1502, ADR 0030).
func TestTea_HandleKey_ZKey_TogglesSidebarZoom(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + 1, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42"})
	tm := teaModel{m: m}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	if !tm.m.SidebarZoom {
		t.Error("SidebarZoom = false, want true after \"z\"")
	}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	if tm.m.SidebarZoom {
		t.Error("SidebarZoom = true, want false after a second \"z\"")
	}
}

// TestTea_HandleKey_ZoomedSidebar_RoutesKeysToSidebarRegardlessOfFocus
// verifies a zoomed sidebar routes every keypress to the sidebar even while
// Model.Focus is still FocusList — the same "no list on screen to route
// list-only keys to" rule handleKey already applies to the narrow-terminal
// fullscreen fallback (issue #1502, ADR 0030).
func TestTea_HandleKey_ZoomedSidebar_RoutesKeysToSidebarRegardlessOfFocus(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + 1, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: strings.Join(lines, "\n")})
	m = Update(m, SidebarToggleMsg{})
	m = Update(m, FocusListMsg{})
	m = Update(m, SidebarZoomToggleMsg{})
	tm := teaModel{m: m}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})

	if tm.m.Sidebar.Offset != 1 {
		t.Errorf("Sidebar.Offset = %d, want 1 — \"j\" must have scrolled the zoomed sidebar despite Focus == FocusList", tm.m.Sidebar.Offset)
	}
}

// TestTea_HandleKey_HKey_NoOpWhileZoomed verifies "h"/left is a no-op while
// the sidebar is zoomed, even on a terminal wide enough to dock — zoomed
// fullscreen has no list on screen to focus, the same rule already applied
// to the narrow-terminal fallback; moving Focus to FocusList here anyway
// would desync it from the still-fullscreen render until "z" un-zooms
// (review finding on issue #1502).
func TestTea_HandleKey_HKey_NoOpWhileZoomed(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + 1, Height: 24})
	m = Update(m, SidebarLoadedMsg{Number: "42"})
	m = Update(m, SidebarZoomToggleMsg{})
	tm := teaModel{m: m}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})

	if tm.m.Focus != FocusSidebar {
		t.Errorf("Focus = %v, want FocusSidebar unchanged — \"h\" must be a no-op while zoomed", tm.m.Focus)
	}
}

// TestTea_SidebarToggleKey_CyclesActivityTranscriptRaw verifies "t" advances
// the open sidebar around its Activity feed -> Transcript (rendered) ->
// Transcript (raw) -> Activity feed cycle, so the byte-exact raw form stays
// reachable without a second key (issue #786, extended by #1501).
func TestTea_SidebarToggleKey_CyclesActivityTranscriptRaw(t *testing.T) {
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

	sendKey(tm, "2")
	waitForOutput(t, tm, "running")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42")

	sendKey(tm, "t")
	waitForOutput(t, tm, "transcript #42", "[implementor] hi")

	sendKey(tm, "t")
	waitForOutput(t, tm, "(raw)", `"type":"assistant"`)

	sendKey(tm, "t")
	waitForOutput(t, tm, "activity #42")

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	waitFinished(t, tm)
}

// TestTea_SidebarScrollKeys_PageThroughContent verifies pgdown/pgup move the
// sidebar's scroll offset, hiding and restoring the leading lines (issue
// #786). The content has to outrun the 24-row test terminal's fullscreen
// budget, or clampSidebarOffset's viewport cap (issue #829) pins Offset at 0
// as a real no-op and pgdown never produces a fresh frame. A fresh open
// starts at the bottom while following (ADR 0030, issue #1502) rather than
// at line-00, so the sequence pages all the way to the top first — three
// pgups guarantee reaching Offset 0 from a max Offset of 28 (50 lines, a
// 22-line budget) — before exercising the original pgdown/pgup mechanics
// from that known point.
func TestTea_SidebarScrollKeys_PageThroughContent(t *testing.T) {
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

	sendKey(tm, "2")
	waitForOutput(t, tm, "running")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "line-49") // fresh open while following starts at the bottom

	sendKey(tm, "pgup")
	sendKey(tm, "pgup")
	sendKey(tm, "pgup") // detaches Follow along the way
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

// TestTea_SidebarKey_NoDriver_ShowsGracefulMessage verifies opening the
// sidebar during a launch-less session (no Driver configured) surfaces a
// readable error instead of panicking on a nil Driver (issue #786 AC4).
// Picks is seeded directly (rather than via the "p"/Enter pick path) since a
// nil Launcher can never promote a pick to PickRunning through the normal
// flow — isolating the no-Driver render path from how the row got there.
func TestTea_SidebarKey_NoDriver_ShowsGracefulMessage(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	tm0 := newTeaModel(f, t.TempDir(), nil)
	tm0.m.Picks = append(tm0.m.Picks, Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := teatest.NewTestModel(t, tm0, teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "2")
	waitForOutput(t, tm, "running")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42", "sidebar failed")

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_SidebarKey_ClaimedNotYetLaunched_ShowsEmptyActivityNotError
// verifies a pick that reads PickRunning a moment before its Box's first log
// write lands on disk opens the sidebar showing an empty Activity feed, not
// an error — hasTranscript's PickRunning gate can admit this window, and
// ActivityFeed's own graceful-empty contract must win over DrillIn's "no
// logs found" for the combined SidebarLoadedMsg (#1501 review finding).
func TestTea_SidebarKey_ClaimedNotYetLaunched_ShowsEmptyActivityNotError(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	launch := newTestLauncher(t, f)
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	// No log file written for #42 -- the race this test targets.
	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "2")
	waitForOutput(t, tm, "running")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42")

	sendKey(tm, "q")
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.Sidebar == nil {
		t.Fatal("Sidebar = nil, want a loaded (empty) sidebar")
	}
	if fm.m.Sidebar.Err != nil {
		t.Errorf("Sidebar.Err = %v, want nil (graceful empty, not a failure)", fm.m.Sidebar.Err)
	}
	if len(fm.m.Sidebar.Activity) != 0 {
		t.Errorf("Sidebar.Activity = %v, want empty", fm.m.Sidebar.Activity)
	}
}

// TestTea_HandleKey_RebuildOutputKey_OpensPaneWhenOutputPresent verifies "o"
// opens the rebuild-output pane once a rebuild has captured output (issue
// #1128).
func TestTea_HandleKey_RebuildOutputKey_OpensPaneWhenOutputPresent(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1"}})
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
		m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1"}})
		m = Update(m, RebuildOutputOpenMsg{})
		tm := teaModel{m: m}

		tm.m, _ = tm.handleRebuildOutputKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if tm.m.ShowRebuildOutput {
			t.Errorf("key %q: ShowRebuildOutput = true, want false (closed)", key)
		}
	}
}

// TestTea_HandleRebuildOutputKey_ScrollsOnJK verifies "j"/"k" move
// RebuildOutputOffset while the pane is open.
func TestTea_HandleRebuildOutputKey_ScrollsOnJK(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2\nl3\nl4"}})
	m = Update(m, RebuildOutputOpenMsg{})
	tm := teaModel{m: m}

	tm.m, _ = tm.handleRebuildOutputKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tm.m.RebuildOutputOffset != 1 {
		t.Errorf("RebuildOutputOffset = %d after \"j\", want 1", tm.m.RebuildOutputOffset)
	}

	tm.m, _ = tm.handleRebuildOutputKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tm.m.RebuildOutputOffset != 0 {
		t.Errorf("RebuildOutputOffset = %d after \"k\", want 0", tm.m.RebuildOutputOffset)
	}
}

// TestTea_HandleRebuildOutputKey_GJumpsToLastPage verifies "G" jumps
// RebuildOutputOffset to the pane's last page, the rebuild-output pane's own
// analogue of the list body's "G" (issue #1630 AC1).
func TestTea_HandleRebuildOutputKey_GJumpsToLastPage(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2\nl3\nl4"}})
	m = Update(m, RebuildOutputOpenMsg{})
	tm := teaModel{m: m}

	tm.m, _ = tm.handleRebuildOutputKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if tm.m.RebuildOutputOffset != 4 {
		t.Errorf("RebuildOutputOffset = %d after \"G\", want 4 (last line, unbounded height)", tm.m.RebuildOutputOffset)
	}
}

// TestTea_HandleKey_RebuildOutput_ggJumpsToFirstPage verifies a lone "g"
// followed by a second "g" resets RebuildOutputOffset to 0 while the
// rebuild-output pane is open — reusing the same PendingG/gChordTick
// machinery issue #1628 introduced for the list body rather than duplicating
// it (issue #1630 AC2/AC3).
func TestTea_HandleKey_RebuildOutput_ggJumpsToFirstPage(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2\nl3\nl4"}})
	m = Update(m, RebuildOutputOpenMsg{})
	m = Update(m, RebuildOutputScrollMsg{Delta: 3})
	tm := teaModel{m: m}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if !tm.m.PendingG {
		t.Fatal("PendingG = false after a lone \"g\", want true")
	}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if tm.m.PendingG {
		t.Error("PendingG = true after the second \"g\", want false (chord resolved)")
	}
	if tm.m.RebuildOutputOffset != 0 {
		t.Errorf("RebuildOutputOffset = %d after \"gg\", want 0", tm.m.RebuildOutputOffset)
	}
}

// TestTea_HandleKey_RebuildOutput_gLeader_NonGKey_CancelsAndStillScrolls
// verifies a lone "g" followed by a non-"g" key in the rebuild-output pane
// cancels the pending leader without consuming that key — its own scroll
// binding still applies, mirroring the list body's own g-leader fallthrough
// (issue #1628 AC) rather than swallowing the second key (issue #1630 AC3).
func TestTea_HandleKey_RebuildOutput_gLeader_NonGKey_CancelsAndStillScrolls(t *testing.T) {
	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2\nl3\nl4"}})
	m = Update(m, RebuildOutputOpenMsg{})
	tm := teaModel{m: m}

	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tm.m.PendingG {
		t.Error("PendingG = true after a non-g key, want false")
	}
	if tm.m.RebuildOutputOffset != 1 {
		t.Errorf("RebuildOutputOffset = %d, want 1 (\"j\" still scrolled after the chord cancelled)", tm.m.RebuildOutputOffset)
	}
}

// TestTea_RebuildOutputPane_ggAndGJumpTopAndBottom drives the rebuild-output
// pane end to end: opening it, jumping to the bottom with "G", then back to
// the top with "gg" — the pane's own analogue of TestTea_GKey_JumpsToLastRow/
// TestTea_ggChord_JumpsToFirstRow for the list body (issue #1630 AC1/AC2).
func TestTea_RebuildOutputPane_ggAndGJumpTopAndBottom(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	launch.rebuildOutput = strings.Join(lines, "\n")

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 10))
	waitForOutput(t, tm, "> #1")

	sendKey(tm, "o")
	waitForOutput(t, tm, "l0")

	sendKey(tm, "G")
	waitForOutput(t, tm, "l19")

	sendKey(tm, "g")
	sendKey(tm, "g")
	waitForOutput(t, tm, "l0")

	sendKey(tm, "q")
	waitFinished(t, tm)
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
	waitForOutput(t, tm, "fix the thing")
	sendKey(tm, "5") // PickDissolved folds into SectionFailed (ADR 0030)
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
	waitForOutput(t, tm, "fix the thing")
	sendKey(tm, "3") // SectionHeld
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
	waitForOutput(t, tm, "fix the launcher") // prefix survives the Backlog title column's clip
	sendKey(tm, "3")                         // SectionHeld
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
	// "settled 1" (the header's status line, always visible regardless of
	// ActiveSection) proves the pick landed and its fake Dispatch finished —
	// otherwise "q" can race the still-live pick and land on the quit
	// confirm (issue #822) instead of exiting, hanging until teatest's
	// timeout (same race TestTea_PickAllReadyKey_QueuesEveryDispatchableIssue
	// already guards against).
	waitForOutput(t, tm, "fix the thing", "settled 1")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_EnterKey_OnBacklogSection_PicksHighlighted verifies Enter, on the
// Backlog Section (the default), Picks the highlighted issue exactly like
// "p" does — the context-sensitive Enter routing (issue #845).
func TestTea_EnterKey_OnBacklogSection_PicksHighlighted(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	launch := newTestLauncher(t, f)

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "enter")
	// "settled 1" (the header's status line, always visible regardless of
	// ActiveSection) proves the fake Dispatch this promotion launched has
	// finished — otherwise "q" can race the still-live pick onto the quit
	// confirm (issue #822) instead of exiting, hanging until teatest's
	// timeout, the same guard the "p"-key pick tests already carry. Once
	// settled, the issue may drop out of the Backlog listing (relabeled
	// past Dispatchable), so only the Settled Section — not switched to
	// here — would still show its title; the header count is what this
	// assertion actually needs (issue #1500).
	waitForOutput(t, tm, "settled 1")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_EnterKey_OnBacklogSection_FailedPromotion_ShowsDissolvedRow
// verifies a raced/closed/relabeled promotion via Enter still surfaces as a
// dissolved row with its reason, same as the "p" key path (issue #845).
func TestTea_EnterKey_OnBacklogSection_FailedPromotion_ShowsDissolvedRow(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})
	f.TransitionStateErr = errBoom
	launch := &Launcher{CodeForge: f, Queue: NewQueue()}

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "enter")
	sendKey(tm, "5") // PickDissolved folds into SectionFailed (ADR 0030)
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
	waitForOutput(t, tm, "settled 1")

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
	waitForOutput(t, tm, "settled 1")

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
	waitForOutput(t, tm, "settled 1")

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
	waitForOutput(t, tm, "settled 1")

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
	// "waiting 1" (the header's status line, always visible regardless of
	// ActiveSection) proves the first pick landed before the second attempt
	// below — checking the header rather than switching to the Running
	// Section keeps the operator on Backlog, where Pick actually acts
	// (issue #1500).
	waitForOutput(t, tm, "waiting 1")

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
	// Wait for the pending-pick chord to resolve (no trailing "a" arrives)
	// before sending "5" — a key sent while PendingPick is still armed
	// resolves the chord instead of switching Sections. The Failed tab's
	// count is the signal: PickDissolved folds into SectionFailed (ADR
	// 0030), and the header itself never counts Dissolved.
	waitForOutput(t, tm, "Failed(1)")
	sendKey(tm, "5")
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

// TestTea_TerminateKey_NotLive_NeverArmsConfirm verifies "X" only arms a
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

	sendKey(tm, "X")
	sendKey(tm, "q")
	waitFinished(t, tm)

	fm := tm.FinalModel(t).(teaModel)
	if fm.m.PendingTerminate != "" {
		t.Errorf("PendingTerminate = %q, want empty for a non-live issue", fm.m.PendingTerminate)
	}
}

// TestTea_TerminateKey_NilLauncher_NeverArmsConfirm verifies "X" is a no-op
// in a launch-less session — there is no live Dispatch to reclaim, so no
// confirm prompt should ever arm (issue #785 review).
func TestTea_TerminateKey_NilLauncher_NeverArmsConfirm(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	tm := teatest.NewTestModel(t, newTeaModel(f, t.TempDir(), nil), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "X")
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
	waitForOutput(t, tm, "fix the thing")

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
		if status := launch.StaleStatus(); !status.Stale {
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
// "X" arms a confirm prompt naming the highlighted issue, and "y" then reaps
// the Box and returns the issue to Dispatchable (ADR 0024, issue #785).
func TestTea_TerminateKey_ConfirmThenYes_ReclaimsHighlightedDispatch(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "X")
	waitForOutput(t, tm, "terminate #42?", "y/N")

	sendKey(tm, "y")
	sendKey(tm, "5") // PickTerminated folds into SectionFailed (ADR 0030)
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

	sendKey(tm, "X")
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

	sendKey(tm, "X")
	waitForOutput(t, tm, "terminate #42?")

	tm.Type("Y")
	sendKey(tm, "5") // PickTerminated folds into SectionFailed (ADR 0030)
	waitForOutput(t, tm, "terminated")

	sendKey(tm, "q")
	waitFinished(t, tm)

	if len(fr.KillCalls) != 1 {
		t.Errorf("KillCalls = %v, want exactly one kill", fr.KillCalls)
	}
}

// TestTea_TerminateKey_InRunningSection_TargetsHighlighted verifies "X"
// resolves the Running Section's own highlighted row — switching Sections
// resets Cursor to 0 (issue #1500), so a cursor move within the Running
// Section after the switch must target the row it actually highlights
// there, not carry over any position from the Backlog Section (issue #997,
// generalized from FocusedColumn to ActiveSection by issue #1500).
func TestTea_TerminateKey_InRunningSection_TargetsHighlighted(t *testing.T) {
	launch, fc, fr, _ := newTermTestLauncher(t)
	fc.SetIssue(forge.Issue{Number: "43", Title: "also running", Labels: []string{"agent-in-progress"}})
	launch.Queue.Add(Pick{Number: "43", Title: "also running", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(fc, t.TempDir(), launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "also running")

	sendKey(tm, "2")    // switch to the Running Section; Cursor resets to 0 (#42)
	sendKey(tm, "down") // Cursor -> 1 (#43)
	sendKey(tm, "X")
	waitForOutput(t, tm, "terminate #43?", "y/N")

	sendKey(tm, "y")
	sendKey(tm, "5") // #43 (PickTerminated) folds into SectionFailed (ADR 0030)
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

	sendKey(tm, "X")
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

	sendKey(tm, "X")
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

	sendKey(tm, "X")
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

	sendKey(tm, "X")
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

	sendKey(tm, "X")
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
	sendKey(tm, "5") // PickTerminated folds into SectionFailed (ADR 0030)
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

// TestTea_Init_DetectsOrphanedIssuesWithoutAdopting verifies a sandbox still
// running from a prior crashed session is flagged an orphan at startup but
// never adopted through RecoverFn on its own — adoption is the operator's
// explicit gesture now, not a startup sweep (issue #1619, demoted from
// #651/#822's auto-adopt).
func TestTea_Init_DetectsOrphanedIssuesWithoutAdopting(t *testing.T) {
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
		t.Errorf("RecoverFn called with %q at startup, want it never called without the operator's explicit adopt gesture", num)
	case <-time.After(500 * time.Millisecond):
	}

	sendKey(tm, "q")
	waitFinished(t, tm)

	final := tm.FinalModel(t).(teaModel)
	if !final.m.IsOrphan("42") {
		t.Error("IsOrphan(42) = false, want true — startup detection must still flag the running orphan")
	}
}

// TestTea_EnterOnOrphanRow_OpensSidebarReadOnly verifies pressing Enter on a
// Backlog row flagged as an orphan (issue #1619) opens the same live-tail
// sidebar a session-launched Dispatch gets, loaded from that issue's local
// pass logs, instead of picking the issue onto the operator's queue — and
// that opening it never calls RecoverFn, since drill-in on an orphan row is
// read-only end to end (issue #1621).
func TestTea_EnterOnOrphanRow_OpensSidebarReadOnly(t *testing.T) {
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

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	// RunningNames flags #42 as a locally-running orphan sandbox at startup
	// detection (issue #1619) — a manually-seeded OrphanNums would just be
	// overwritten by the real orphanDetectCmd Init() fires, same as any
	// other startup detection result.
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
		Settle:    settle.NewFake(),
		Queue:     NewQueue(),
		RecoverFn: func(num string) error {
			recovered <- num
			return nil
		},
	}
	t.Cleanup(launch.Wait)

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42", "hi")

	// The toggle and close gestures are as much a part of the read-only
	// drill-in as opening it — AC4 names all three explicitly.
	sendKey(tm, "t")
	waitForOutput(t, tm, "transcript #42")
	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	select {
	case num := <-recovered:
		t.Errorf("RecoverFn called with %q, want drill-in to never adopt", num)
	case <-time.After(200 * time.Millisecond):
	}

	sendKey(tm, "q")
	waitFinished(t, tm)

	final := tm.FinalModel(t).(teaModel)
	if len(final.m.Picks) != 0 {
		t.Errorf("Picks = %v, want Enter on an orphan row to never queue a pick", final.m.Picks)
	}
}

// TestTea_OrphanRow_ShowsLiveHeartbeat verifies a Backlog row flagged an
// orphan (issue #1619) shows its box's live heartbeat, derived from its
// on-disk pass log the same way a running Pick's queue row already does
// (#647 AC2) — the operator can tell an orphan is still making progress
// without drilling in (issue #1621).
func TestTea_OrphanRow_ShowsLiveHeartbeat(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"result","num_turns":17,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(line), 0o644); err != nil {
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

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
	launch.pollInterval = 5 * time.Millisecond
	t.Cleanup(launch.Wait)

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(120, 24))
	waitForOutput(t, tm, "orphan", "17 turn")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_OrphanAdopted_ClearsOrphanHeartbeats verifies OrphanHeartbeats
// drops a number the instant it stops being an orphan — an adopt succeeding
// (or any other path that empties OrphanNums) must not leave a stale
// heartbeat sitting in the map forever, even though view.go's IsOrphan gate
// already keeps it from ever rendering (issue #1621 review finding).
func TestTea_OrphanAdopted_ClearsOrphanHeartbeats(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"result","num_turns":17,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(line), 0o644); err != nil {
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

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
	t.Cleanup(launch.Wait)

	tm := newTeaModel(f, dir, launch)
	model, _ := tm.Update(OrphanDetectedMsg{Numbers: []string{"42"}})
	tm = model.(teaModel)
	if tm.m.OrphanHeartbeats["42"] == "" {
		t.Fatal("test setup: OrphanHeartbeats[42] = \"\", want a parsed heartbeat before the adopt")
	}

	model, _ = tm.Update(OrphanAdoptedMsg{Number: "42"})
	tm = model.(teaModel)
	if len(tm.m.OrphanHeartbeats) != 0 {
		t.Errorf("OrphanHeartbeats = %v, want it cleared once #42 is no longer an orphan", tm.m.OrphanHeartbeats)
	}
}

// TestTea_EnterOnOrphanRow_NoLocalLogs_ShowsGracefulNotice verifies an
// orphan row with no local pass log yet — e.g. a box the orphan-detected
// sandbox hasn't written its first log line for, or one CI dispatched on a
// remote runner and this host only ever sees as a running container —
// opens the sidebar with a graceful explanatory message rather than a blank
// pane or an error (issue #1621).
func TestTea_EnterOnOrphanRow_NoLocalLogs_ShowsGracefulNotice(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Deliberately no logs/issue-42.log -- the race/remote-dispatch case
	// this test targets.

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

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
	t.Cleanup(launch.Wait)

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "no local logs for this dispatch")

	sendKey(tm, "q")
	waitFinished(t, tm)

	final := tm.FinalModel(t).(teaModel)
	if final.m.Sidebar == nil {
		t.Fatal("Sidebar = nil, want a loaded sidebar with a graceful notice")
	}
	if final.m.Sidebar.Err != nil {
		t.Errorf("Sidebar.Err = %v, want nil (graceful notice, not a failure)", final.m.Sidebar.Err)
	}
}

// TestTea_OrphanSidebar_NoticeClearsOnceRealActivityArrivesLive verifies a
// "no local logs for this dispatch" Notice, shown while an orphan row's
// sidebar is open on an issue with nothing on disk yet, clears the instant
// the box's first log line lands and syncQueue's live tail picks it up —
// the operator's stale-race window resolving itself must not leave the
// notice covering up real content that has since arrived (issue #1621).
func TestTea_OrphanSidebar_NoticeClearsOnceRealActivityArrivesLive(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Deliberately no logs/issue-42.log yet -- the sidebar opens on the
	// graceful-notice path this test then races against a real log write.

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

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
	launch.pollInterval = 5 * time.Millisecond
	t.Cleanup(launch.Wait)

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "no local logs for this dispatch")

	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"box is alive"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, tm, "box is alive")

	sendKey(tm, "q")
	waitFinished(t, tm)

	final := tm.FinalModel(t).(teaModel)
	if final.m.Sidebar == nil {
		t.Fatal("Sidebar = nil, want it still open")
	}
	if final.m.Sidebar.Notice != "" {
		t.Errorf("Sidebar.Notice = %q, want cleared once real Activity arrived", final.m.Sidebar.Notice)
	}
}

// TestTea_ReopenOrphanSidebar_PicksUpTranscriptGrowth verifies closing and
// reopening an orphan row's sidebar re-runs the whole load, picking up
// Transcript growth the live Activity feed alone wouldn't — the same
// reopen-to-refresh contract a session-launched Dispatch's sidebar already
// has (issue #719, inherited), extended to orphan rows (issue #1621).
func TestTea_ReopenOrphanSidebar_PicksUpTranscriptGrowth(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "logs", "issue-42.log")
	first := `{"type":"assistant","message":{"content":[{"type":"text","text":"first pass"}]}}` + "\n"
	if err := os.WriteFile(logPath, []byte(first), 0o644); err != nil {
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

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
	t.Cleanup(launch.Wait)

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42", "first pass")

	sendKey(tm, "x")
	waitForOutput(t, tm, "fix the thing")

	// Grown while the sidebar was closed -- a Transcript-only load's own
	// #719 case, not the live Activity feed (which only advances while the
	// sidebar stays open).
	grown := first + `{"type":"assistant","message":{"content":[{"type":"text","text":"second pass"}]}}` + "\n"
	if err := os.WriteFile(logPath, []byte(grown), 0o644); err != nil {
		t.Fatal(err)
	}

	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42", "second pass")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_PollTick_AdvancesOpenOrphanSidebarActivityFeed verifies an orphan
// row's open sidebar advances on its own as its box's on-disk pass log
// grows, with no operator keypress — the same live-tail payoff a
// session-launched running Dispatch's sidebar already gets (issue #1502),
// now extended to a box this session never launched (issue #1621).
func TestTea_PollTick_AdvancesOpenOrphanSidebarActivityFeed(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "logs", "issue-42.log")
	first := `{"type":"assistant","message":{"content":[{"type":"text","text":"first update"}]}}` + "\n"
	if err := os.WriteFile(logPath, []byte(first), 0o644); err != nil {
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

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
	launch.pollInterval = 5 * time.Millisecond
	t.Cleanup(launch.Wait)

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "enter")
	waitForOutput(t, tm, "activity #42", "first update")

	second := first + `{"type":"assistant","message":{"content":[{"type":"text","text":"second update"}]}}` + "\n"
	if err := os.WriteFile(logPath, []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, tm, "second update")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestOrphanDetectCmd_ReturnsDetectedNumbers verifies orphanDetectCmd reports
// every issue OrphanedIssues found running through OrphanDetectedMsg, with
// no RecoverFn call of its own (issue #1619).
func TestOrphanDetectCmd_ReturnsDetectedNumbers(t *testing.T) {
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

	launch := &Launcher{Factory: factory}

	msg := orphanDetectCmd(launch)()

	rec, ok := msg.(OrphanDetectedMsg)
	if !ok {
		t.Fatalf("orphanDetectCmd()() = %#v (%T), want OrphanDetectedMsg", msg, msg)
	}
	if len(rec.Numbers) != 1 || rec.Numbers[0] != "42" {
		t.Errorf("OrphanDetectedMsg.Numbers = %v, want [42]", rec.Numbers)
	}
}

// TestOrphanDetectCmd_OrphanedIssuesErr_ReportsNoOrphans verifies a failed
// OrphanedIssues() lookup at startup degrades to "no orphans detected"
// rather than surfacing a failure banner — startup detection is best-effort
// and silent on its own failure, mirroring DogfoodNotice's read-error
// fallback, since #1619 retired the only startup warning ("orphan recovery
// failed") this lookup used to feed.
func TestOrphanDetectCmd_OrphanedIssuesErr_ReportsNoOrphans(t *testing.T) {
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

	launch := &Launcher{Factory: factory}

	if msg := orphanDetectCmd(launch)(); msg != nil {
		t.Errorf("orphanDetectCmd()() = %#v, want nil on a failed lookup", msg)
	}
}

// TestTea_AdoptOrphanKey_NoOpenPR_SurfacesReasonWithNoAdoption verifies the
// explicit adopt gesture ("A") on an orphan-flagged Backlog row with no open
// PR reports the reason through the same banner startup recovery used to
// show, and never queues or launches anything (issue #1619).
func TestTea_AdoptOrphanKey_NoOpenPR_SurfacesReasonWithNoAdoption(t *testing.T) {
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
		RecoverFn: func(string) error { return errors.New("issue 42: no open PR") },
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "A")
	waitForOutput(t, tm, "orphan adopt failed", "no open PR")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_AdoptOrphanKey_DraftPR_SurfacesReasonWithNoAdoption verifies the
// gesture's other "changes nothing" case — a draft PR, distinct from no PR
// at all — reports that specific reason too, matching the acceptance
// criterion naming both (issue #1619 AC).
func TestTea_AdoptOrphanKey_DraftPR_SurfacesReasonWithNoAdoption(t *testing.T) {
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
		RecoverFn: func(string) error { return errors.New("issue 42: draft PR") },
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "A")
	waitForOutput(t, tm, "orphan adopt failed", "draft PR")

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_AdoptOrphanKey_Success_ClearsFlagPreventingRepeatAdopt verifies a
// successful adopt clears the row's orphan flag, so a second "A" press on
// the same, now-adopted row never fires RecoverFn again — a repeat press
// would otherwise race a second same-process settle over the PR the first
// adopt already claimed (issue #1619 review finding).
func TestTea_AdoptOrphanKey_Success_ClearsFlagPreventingRepeatAdopt(t *testing.T) {
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

	var calls int32
	launch := &Launcher{
		CodeForge: f,
		Factory:   factory,
		Queue:     NewQueue(),
		RecoverFn: func(string) error {
			atomic.AddInt32(&calls, 1)
			return nil
		},
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "A")
	// Poll t's own final orphan flag via a short settle window instead of a
	// rendered signal — a successful adopt renders no banner of its own
	// (the whole point being "changes nothing" beyond clearing the flag).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&calls) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("RecoverFn calls = %d after first \"A\", want 1", atomic.LoadInt32(&calls))
	}
	// Give the OrphanAdoptedMsg a moment to land on the Model before the
	// second press, mirroring the same settle window above.
	time.Sleep(50 * time.Millisecond)

	sendKey(tm, "A")
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("RecoverFn calls = %d after a second \"A\" on the same row, want still 1 — the orphan flag must clear on adopt", got)
	}

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_AdoptOrphanKey_SecondPressWhileInFlight_NeverFiresTwice verifies a
// second "A" press on the same orphan-flagged row, sent while the first
// adopt's RecoverFn call is still in flight (before OrphanAdoptedMsg has had
// a chance to clear the orphan flag), never fires a second RecoverFn call —
// two concurrent RecoverFn calls for the same issue would race two
// SettleAdopted goroutines over the same PR, the exact same-process
// merge-authority race #1619 exists to prevent (review finding: the flag
// only clears once RecoverFn returns, leaving the in-flight window itself
// unguarded).
func TestTea_AdoptOrphanKey_SecondPressWhileInFlight_NeverFiresTwice(t *testing.T) {
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

	var calls int32
	entered := make(chan struct{}, 1)
	unblock := make(chan struct{})
	launch := &Launcher{
		CodeForge: f,
		Factory:   factory,
		Queue:     NewQueue(),
		RecoverFn: func(string) error {
			atomic.AddInt32(&calls, 1)
			entered <- struct{}{}
			<-unblock
			return nil
		},
	}

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "A")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("RecoverFn never entered after first \"A\"")
	}

	// The first adopt is now blocked inside RecoverFn, well before
	// OrphanAdoptedMsg could have landed to clear the orphan flag — exactly
	// the in-flight window a second press must not slip through.
	sendKey(tm, "A")
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("RecoverFn calls = %d while the first adopt was still in flight, want 1", got)
	}

	close(unblock)
	sendKey(tm, "q")
	waitFinished(t, tm)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("RecoverFn calls = %d total, want 1", got)
	}
}

// TestTea_AdoptOrphanKey_NonOrphanRow_NoAdopt verifies "A" on a highlighted
// Backlog row that was never flagged an orphan is a no-op — the gesture is
// scoped to orphan-flagged rows only (issue #1619).
func TestTea_AdoptOrphanKey_NonOrphanRow_NoAdopt(t *testing.T) {
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
	fr := runner.NewFake() // no RunningNames: 42 is never reported an orphan
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

	sendKey(tm, "A")

	select {
	case num := <-recovered:
		t.Errorf("RecoverFn called with %q, want never called for a non-orphan row", num)
	case <-time.After(500 * time.Millisecond):
	}

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_AdoptOrphanKey_OutsideBacklogSection_NoAdopt verifies "A" is
// scoped to the Backlog Section — pressed while a work Section is active,
// it must never adopt, even if the active Section happens to show the same
// issue number as a Pick (issue #1619).
func TestTea_AdoptOrphanKey_OutsideBacklogSection_NoAdopt(t *testing.T) {
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

	sendKey(tm, "2") // SectionRunning
	sendKey(tm, "A")

	select {
	case num := <-recovered:
		t.Errorf("RecoverFn called with %q, want never called outside SectionBacklog", num)
	case <-time.After(500 * time.Millisecond):
	}

	sendKey(tm, "q")
	waitFinished(t, tm)
}

// TestTea_Init_OrphanedIssuesErr_NeverWarnsAtStartup verifies a failed
// OrphanedIssues() lookup at startup degrades silently — no "orphan
// recovery failed" banner, since startup never adopts (and so never fails
// to adopt) on its own anymore (issue #1619).
func TestTea_Init_OrphanedIssuesErr_NeverWarnsAtStartup(t *testing.T) {
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
	fr.ListRunningErr = errors.New("boom")
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
	waitForOutput(t, tm, "fix the thing")
	time.Sleep(500 * time.Millisecond)

	sendKey(tm, "q")
	waitFinished(t, tm)

	final := tm.FinalModel(t).(teaModel)
	if strings.Contains(View(final.m), "orphan adopt failed") {
		t.Error("View() shows \"orphan adopt failed\" after a startup lookup failure, want no banner — startup never adopts")
	}
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
	// Pinned to a color-capable terminal: the header is styled (ADR 0031)
	// and the width check below must hold with those ANSI codes in play,
	// not just when ambient TERM happens to disable them.
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

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
			// either (view_test.go's TestClip_WideCharacters_MeasuresDisplayWidthNotRuneCount
			// uses this same width idiom, just against its own budget). The
			// per-line display-width check below, run against real Bubble
			// Tea render output, IS this package's established verification
			// for "correct clipping at visual column boundaries": it fails
			// exactly when a line spills past the column budget, which is
			// what a golden diff would also catch here, without requiring
			// fixture upkeep for a rendering surface this volatile (issue
			// #1260). Only one Section renders at a time (ADR 0030), so the
			// backlog and Running Section renders are checked separately —
			// each still has to fit the terminal, but neither has to fit
			// alongside the other's row anymore (issue #1500).
			backlogOut := View(Update(final.m, SectionJumpMsg{Section: SectionBacklog}))
			runningOut := View(Update(final.m, SectionJumpMsg{Section: SectionRunning}))
			// lipgloss.Width, not runewidth.StringWidth: a styled header
			// line carries ANSI color codes that runewidth counts as
			// display width and lipgloss's ANSI-aware measurement does
			// not.
			for _, out := range []string{backlogOut, runningOut} {
				for _, l := range strings.Split(out, "\n") {
					if w := lipgloss.Width(l); w > 80 {
						t.Errorf("View() line %q has display width %d, want it clamped to Width (80)", l, w)
					}
				}
			}
			if !strings.Contains(backlogOut, "#1") {
				t.Errorf("backlog View() = %q, want the backlog issue number #1 to survive clipping", backlogOut)
			}
			if !strings.Contains(runningOut, "#2") {
				t.Errorf("Running Section View() = %q, want the queue pick number #2 to survive clipping", runningOut)
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
