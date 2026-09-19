package console

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

var errBoom = errors.New("boom")

func TestNewModel_Empty(t *testing.T) {
	m := NewModel()
	if len(m.Visible()) != 0 {
		t.Errorf("Visible() = %v, want none", m.Visible())
	}
	if m.Quitting {
		t.Error("Quitting = true, want false")
	}
}

// Update installs the backlog in whatever order the adapter supplied. The
// ordering is the adapter's responsibility, not Update's.
func TestUpdate_IssuesLoadedMsg_ReplacesAll(t *testing.T) {
	m := NewModel()
	issues := []forge.Issue{{Number: "1", Title: "first"}, {Number: "2", Title: "second"}}

	m = Update(m, IssuesLoadedMsg{Issues: issues})

	if len(m.Visible()) != 2 || m.Visible()[0].Number != "1" || m.Visible()[1].Number != "2" {
		t.Errorf("Visible() = %+v, want %+v", m.Visible(), issues)
	}
}

// A failed refresh must never look like an empty backlog, so Update keeps the
// last-good list on screen and records Err for View to surface.
func TestUpdate_IssuesLoadedMsg_ErrKeepsStaleListAndRecordsErr(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1"}}})

	wantErr := errBoom
	m = Update(m, IssuesLoadedMsg{Err: wantErr})

	if len(m.Visible()) != 1 || m.Visible()[0].Number != "1" {
		t.Errorf("Visible() = %+v, want stale [1] kept on error", m.Visible())
	}
	if m.Err != wantErr {
		t.Errorf("Err = %v, want %v", m.Err, wantErr)
	}
}

// Update stays pure: it applies the adapter-computed RecoverableCount and
// never recomputes it from Issues (issue #2255, ADR 0039 slice S4).
func TestUpdate_IssuesLoadedMsg_SetsRecoverableCount(t *testing.T) {
	m := NewModel()

	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1"}}, RecoverableCount: 3})

	if m.RecoverableCount != 3 {
		t.Errorf("RecoverableCount = %d, want 3", m.RecoverableCount)
	}
}

// One round trip covers both acceptance criteria: the filter narrows the list
// interactively, and clearing it restores the full list.
func TestUpdate_FilterChangedMsg_NarrowsAndClearingRestores(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Labels: []string{"ready-for-agent"}},
		{Number: "2", Labels: []string{"agent-in-progress"}},
	}})

	m = Update(m, FilterChangedMsg{Filter: "in-progress"})
	if got := m.Visible(); len(got) != 1 || got[0].Number != "2" {
		t.Errorf("Visible() after filter = %+v, want just #2", got)
	}

	m = Update(m, FilterChangedMsg{Filter: ""})
	if got := m.Visible(); len(got) != 2 {
		t.Errorf("Visible() after clearing filter = %+v, want both issues", got)
	}
}

// The backlog cursor never walks past what is on screen (issue #784).
func TestUpdate_CursorMoveMsg_MovesWithinVisibleBounds(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1"}, {Number: "2"}}})

	m = Update(m, CursorMoveMsg{Delta: 1})
	if m.Cursor != 1 {
		t.Errorf("Cursor = %d, want 1 after one down-move", m.Cursor)
	}

	m = Update(m, CursorMoveMsg{Delta: 1})
	if m.Cursor != 1 {
		t.Errorf("Cursor = %d, want clamped at 1 (last row)", m.Cursor)
	}
}

func TestUpdate_CursorMoveMsg_ClampsAtZero(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1"}, {Number: "2"}}})

	m = Update(m, CursorMoveMsg{Delta: -1})
	if m.Cursor != 0 {
		t.Errorf("Cursor = %d, want clamped at 0", m.Cursor)
	}
}

func TestUpdate_CursorMoveMsg_EmptyVisible_StaysZero(t *testing.T) {
	m := NewModel()
	m = Update(m, CursorMoveMsg{Delta: 1})
	if m.Cursor != 0 {
		t.Errorf("Cursor = %d, want 0 with nothing visible", m.Cursor)
	}
}

func TestUpdate_FilterChangedMsg_NarrowingClampsCursor(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Labels: []string{"a"}},
		{Number: "2", Labels: []string{"b"}},
	}})
	m = Update(m, CursorMoveMsg{Delta: 1})

	m = Update(m, FilterChangedMsg{Filter: "a"})
	if m.Cursor != 0 {
		t.Errorf("Cursor = %d, want clamped to 0 after the filter narrowed to one row", m.Cursor)
	}
}

// ModeList is modePrecedence's last resort (issue #1543).
func TestModel_ActiveMode_DefaultsToList(t *testing.T) {
	m := NewModel()
	if got := m.ActiveMode(); got != ModeList {
		t.Errorf("ActiveMode() = %v, want ModeList", got)
	}
}

// Sidebar ownership derives from Sidebar/Focus/SidebarZoom rather than from
// Mode, so a Model can carry a stale Mode alongside an active Sidebar.
// ActiveMode must still resolve to ModeSidebar (issue #1543).
func TestModel_ActiveMode_SidebarBeatsEveryOtherMode(t *testing.T) {
	m := NewModel()
	m.Sidebar = &SidebarState{Number: "42"}
	m.Focus = FocusSidebar
	m.Mode = ModeHelp

	if got := m.ActiveMode(); got != ModeSidebar {
		t.Errorf("ActiveMode() = %v, want ModeSidebar even with Mode = ModeHelp", got)
	}
}

// Only a focused, fullscreen-fallback, or zoomed sidebar competes for keyboard
// ownership, so a docked one leaves Mode in charge (ADR 0030's
// sidebarFits/Focus contract, unchanged by issue #1543).
func TestModel_ActiveMode_DockedSidebarDoesNotOwnKeyboard(t *testing.T) {
	m := NewModel()
	m.Width, m.Height = 200, 40
	m.Sidebar = &SidebarState{Number: "42"}
	m.Focus = FocusList
	m.Mode = ModeHelp

	if got := m.ActiveMode(); got != ModeHelp {
		t.Errorf("ActiveMode() = %v, want ModeHelp for a docked, list-focused sidebar", got)
	}
}

// SidebarZoom alone, not just a narrow terminal, takes Sidebar out of
// arrangementSidebarDocked (issue #3017's sidebarDocked helper).
func TestModel_ActiveMode_ZoomedSidebarOwnsKeyboard(t *testing.T) {
	m := NewModel()
	m.Width, m.Height = 200, 40
	m.Sidebar = &SidebarState{Number: "42"}
	m.Focus = FocusList
	m.SidebarZoom = true
	m.Mode = ModeHelp

	if got := m.ActiveMode(); got != ModeSidebar {
		t.Errorf("ActiveMode() = %v, want ModeSidebar for a zoomed sidebar even though it fits docked", got)
	}
}

// The same message both opens and closes the help overlay (issue #784).
func TestUpdate_HelpToggleMsg_TogglesModeHelp(t *testing.T) {
	m := NewModel()
	m = Update(m, HelpToggleMsg{})
	if m.Mode != ModeHelp {
		t.Errorf("Mode = %v after one toggle, want ModeHelp", m.Mode)
	}

	m = Update(m, HelpToggleMsg{})
	if m.Mode == ModeHelp {
		t.Error("Mode = ModeHelp after two toggles, want ModeList")
	}
}

// The rebuild-output pane is the captured output field's only consumer (issue
// #1128).
func TestUpdate_RebuildOutputOpenMsg_OpensPaneWhenOutputPresent(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "building...\ndone"}})
	m = Update(m, RebuildOutputOpenMsg{})
	if m.Mode != ModeRebuildOutput {
		t.Errorf("Mode = %v after open with output present, want ModeRebuildOutput", m.Mode)
	}
}

func TestUpdate_RebuildOutputOpenMsg_NoOpWhenOutputEmpty(t *testing.T) {
	m := NewModel()
	m = Update(m, RebuildOutputOpenMsg{})
	if m.Mode == ModeRebuildOutput {
		t.Error("Mode = ModeRebuildOutput with no RebuildOutput captured, want ModeList")
	}
}

func TestUpdate_RebuildOutputScrollMsg_NoOpWhenPaneClosed(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2"}})
	m = Update(m, RebuildOutputScrollMsg{Delta: 1})
	if m.RebuildOutputOffset != 0 {
		t.Errorf("RebuildOutputOffset = %d, want 0 while pane closed", m.RebuildOutputOffset)
	}
	if m.Mode == ModeRebuildOutput {
		t.Error("Mode = ModeRebuildOutput, want ModeList — scroll must not open the pane")
	}
}

// RebuildOutputOffset clamps into the captured output's line bounds the same
// way SidebarScrollMsg clamps Sidebar.Offset.
func TestUpdate_RebuildOutputScrollMsg_MovesOffset(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2\nl3\nl4"}})
	m = Update(m, RebuildOutputOpenMsg{})

	m = Update(m, RebuildOutputScrollMsg{Delta: 2})
	if m.RebuildOutputOffset != 2 {
		t.Errorf("RebuildOutputOffset = %d, want 2", m.RebuildOutputOffset)
	}

	m = Update(m, RebuildOutputScrollMsg{Delta: -100})
	if m.RebuildOutputOffset != 0 {
		t.Errorf("RebuildOutputOffset = %d, want 0 (clamped at the top)", m.RebuildOutputOffset)
	}

	m = Update(m, RebuildOutputScrollMsg{Delta: 100})
	if m.RebuildOutputOffset != 4 {
		t.Errorf("RebuildOutputOffset = %d, want 4 (clamped to the last line)", m.RebuildOutputOffset)
	}
}

// RebuildOutputOpenMsg already refuses to open the pane on empty output, so a
// closed pane is the reachable form of the "no-op on empty output" acceptance
// criterion (issue #1630 AC4).
func TestUpdate_RebuildOutputJumpMsgs_NoOpWhenPaneClosed(t *testing.T) {
	m := NewModel()
	m = Update(m, RebuildOutputJumpToLastMsg{})
	if m.RebuildOutputOffset != 0 {
		t.Errorf("RebuildOutputOffset = %d after jump-to-last while closed, want 0", m.RebuildOutputOffset)
	}

	m = Update(m, RebuildOutputJumpToFirstMsg{})
	if m.RebuildOutputOffset != 0 {
		t.Errorf("RebuildOutputOffset = %d after jump-to-first while closed, want 0", m.RebuildOutputOffset)
	}
	if m.Mode == ModeRebuildOutput {
		t.Error("Mode = ModeRebuildOutput, want ModeList — neither jump must open the pane")
	}
}

// Mirrors CursorJumpToFirstMsg's reset for the list body (issue #1630 AC2).
func TestUpdate_RebuildOutputJumpToFirstMsg_ResetsOffsetToZero(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2\nl3\nl4"}})
	m = Update(m, RebuildOutputOpenMsg{})
	m = Update(m, RebuildOutputScrollMsg{Delta: 3})

	m = Update(m, RebuildOutputJumpToFirstMsg{})
	if m.RebuildOutputOffset != 0 {
		t.Errorf("RebuildOutputOffset = %d, want 0", m.RebuildOutputOffset)
	}
}

// "G" lands on the last page that still fills the viewport, not on the last
// line: the same page-capped clamp the ModeRebuildOutput block applies on
// every Update (issue #1630 AC1).
func TestUpdate_RebuildOutputJumpToLastMsg_JumpsToLastPage(t *testing.T) {
	m := NewModel()
	m = Update(m, SizeChangedMsg{Height: 7}) // headerFooterLines(2) plus the issue #1827 trailing-"\n" reservation(1) leave a 4-row viewport
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9"}})
	m = Update(m, RebuildOutputOpenMsg{})

	m = Update(m, RebuildOutputJumpToLastMsg{})
	if m.RebuildOutputOffset != 6 {
		t.Errorf("RebuildOutputOffset = %d, want 6 (10 lines, 4-row viewport: last page starts at line 6)", m.RebuildOutputOffset)
	}
}

func TestUpdate_RebuildOutputCloseMsg_ClosesPane(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1"}})
	m = Update(m, RebuildOutputOpenMsg{})
	m = Update(m, RebuildOutputCloseMsg{})
	if m.Mode == ModeRebuildOutput {
		t.Error("Mode = ModeRebuildOutput after close, want ModeList")
	}
}

// A later StaleStatusMsg can empty Output out from under an open pane, which
// used to leave Mode at ModeRebuildOutput over blank content (issue #1543).
func TestUpdate_StaleStatusMsg_ClosesOpenPaneWhenOutputEmpties(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2"}})
	m = Update(m, RebuildOutputOpenMsg{})

	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: ""}})
	if m.Mode == ModeRebuildOutput {
		t.Error("Mode = ModeRebuildOutput after Output emptied, want ModeList (pane auto-closes)")
	}
}

// ModeFilterEdit tells the tea layer to route further keystrokes as filter
// text instead of navigation (issue #784).
func TestUpdate_FilterEditStartMsg_EntersEditingMode(t *testing.T) {
	m := NewModel()
	m = Update(m, FilterEditStartMsg{})
	if m.Mode != ModeFilterEdit {
		t.Errorf("Mode = %v after FilterEditStartMsg, want ModeFilterEdit", m.Mode)
	}
}

// The Filter has already narrowed the list live, so Enter only exits editing
// mode and leaves it untouched.
func TestUpdate_FilterEditConfirmMsg_KeepsFilterExitsEditing(t *testing.T) {
	m := NewModel()
	m = Update(m, FilterEditStartMsg{})
	m = Update(m, FilterChangedMsg{Filter: "bug"})

	m = Update(m, FilterEditConfirmMsg{})
	if m.Mode == ModeFilterEdit {
		t.Error("Mode = ModeFilterEdit after confirm, want ModeList")
	}
	if m.Filter != "bug" {
		t.Errorf("Filter = %q after confirm, want %q kept", m.Filter, "bug")
	}
}

// Esc restores whatever Filter was active before "/" was pressed.
func TestUpdate_FilterEditCancelMsg_RevertsFilterExitsEditing(t *testing.T) {
	m := NewModel()
	m = Update(m, FilterChangedMsg{Filter: "bug"})
	m = Update(m, FilterEditStartMsg{})
	m = Update(m, FilterChangedMsg{Filter: "bug-and-more"})

	m = Update(m, FilterEditCancelMsg{})
	if m.Mode == ModeFilterEdit {
		t.Error("Mode = ModeFilterEdit after cancel, want ModeList")
	}
	if m.Filter != "bug" {
		t.Errorf("Filter = %q after cancel, want %q restored", m.Filter, "bug")
	}
}

// QuitMsg is the only thing that flips Quitting, the run loop's signal to exit
// its read loop cleanly.
func TestUpdate_QuitMsg_SetsQuitting(t *testing.T) {
	m := NewModel()
	m = Update(m, QuitMsg{})
	if !m.Quitting {
		t.Error("Quitting = false after QuitMsg, want true")
	}
}

// The message records that a competing headless loop's pid-file is present.
// The startup notice is informational and must never block, so it stays a
// single bit on Model that View can render.
func TestUpdate_DogfoodNoticeMsg_SetsLive(t *testing.T) {
	m := NewModel()
	if m.DogfoodLive {
		t.Error("DogfoodLive = true before any message, want false")
	}

	m = Update(m, DogfoodNoticeMsg{Live: true})
	if !m.DogfoodLive {
		t.Error("DogfoodLive = false after Live:true, want true")
	}

	m = Update(m, DogfoodNoticeMsg{Live: false})
	if m.DogfoodLive {
		t.Error("DogfoodLive = true after Live:false, want false")
	}
}

// ActivityFeed's only per-record clock is the pass log's on-disk mtime, which
// advances to roughly now on every refresh rather than recording when the
// record happened, so a precise-looking HH:MM:SS prefix would mislead (#1584).
func TestFormatActivityLine_RendersTextOnly(t *testing.T) {
	got := formatActivityLine(ActivityLine{Text: "#42 · hi"})
	want := "#42 · hi"
	if got != want {
		t.Errorf("formatActivityLine() = %q, want %q (no timestamp prefix)", got, want)
	}
}

// The queue row's live-tail sidebar gesture opens on the Activity feed, not
// the Transcript (#648, #1501).
func TestUpdate_SidebarLoadedMsg_OpensSidebar_ActivityDefault(t *testing.T) {
	m := NewModel()
	activity := []ActivityLine{{Text: "#42 · hi"}}
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity, Rendered: "[implementor] hi\n", Raw: `{"type":"assistant"}` + "\n"})

	if m.Sidebar == nil {
		t.Fatal("Sidebar = nil, want non-nil after SidebarLoadedMsg")
	}
	if m.Sidebar.Number != "42" {
		t.Errorf("Sidebar.Number = %q, want %q", m.Sidebar.Number, "42")
	}
	if m.Sidebar.ShowTranscript {
		t.Error("Sidebar.ShowTranscript = true, want false (the Activity feed is the default view)")
	}
	if len(m.Sidebar.Lines) != 1 || !strings.Contains(m.Sidebar.Lines[0], "hi") {
		t.Errorf("Sidebar.Lines = %v, want the formatted Activity feed", m.Sidebar.Lines)
	}
	if m.Focus != FocusSidebar {
		t.Errorf("Focus = %v, want FocusSidebar after opening a new sidebar", m.Focus)
	}
}

// The floating log modal's border needs the row identifier, captured at open
// time the way DetailModalOpenMsg carries its own Title (issue #1845).
func TestUpdate_SidebarLoadedMsg_CarriesTitle(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Title: "fix the thing", Activity: []ActivityLine{{Text: "hi"}}})

	if m.Sidebar == nil {
		t.Fatal("Sidebar = nil, want non-nil after SidebarLoadedMsg")
	}
	if m.Sidebar.Title != "fix the thing" {
		t.Errorf("Sidebar.Title = %q, want %q", m.Sidebar.Title, "fix the thing")
	}
}

// Live-tailing is the default the moment a feed opens, not an opt-in the
// operator has to reach for (issue #1502, ADR 0030).
func TestUpdate_SidebarLoadedMsg_FollowDefaultsTrueOnOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	if !m.Sidebar.Follow {
		t.Error("Sidebar.Follow = false, want true on a freshly opened sidebar")
	}
}

// ADR 0030's "follows the newest line by default" describes any opened feed,
// not only a reopen after a close (review finding on issue #1502).
func TestUpdate_SidebarLoadedMsg_FreshOpenWhileFollowing_StartsAtBottom(t *testing.T) {
	activity := make([]ActivityLine, 50)
	for i := range activity {
		activity[i] = ActivityLine{Text: fmt.Sprintf("l%d", i)}
	}

	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity})

	want := len(m.Sidebar.Lines) - sidebarModalScrollBudget(m) // last page fills the floating log modal's own budget (issue #1845)
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (a fresh open while following starts at the bottom)", m.Sidebar.Offset, want)
	}
}

// Pre-splitting into Sidebar.Lines once keeps clampSidebarOffset and the
// render functions from re-splitting the full content on every Update and View
// call (issue #722, inherited from DrillInState.Lines).
func TestUpdate_SidebarLoadedMsg_CachesLineSplit(t *testing.T) {
	m := NewModel()
	activity := []ActivityLine{{Text: "l0"}, {Text: "l1"}, {Text: "l2"}}
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity})

	if len(m.Sidebar.Lines) != len(activity) {
		t.Fatalf("Lines = %v, want %d entries", m.Sidebar.Lines, len(activity))
	}
	for i, a := range activity {
		if !strings.Contains(m.Sidebar.Lines[i], a.Text) {
			t.Errorf("Lines[%d] = %q, want it to contain %q", i, m.Sidebar.Lines[i], a.Text)
		}
	}
}

// "t" cycles Activity, rendered Transcript, raw Transcript, Activity again, so
// the byte-exact raw form stays reachable without a second key (#1501).
func TestUpdate_SidebarToggleMsg_CyclesActivityTranscriptRaw(t *testing.T) {
	m := NewModel()
	activity := []ActivityLine{{Text: "activity line"}}
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity, Rendered: "rendered", Raw: "raw"})

	if m.Sidebar.ShowTranscript {
		t.Fatal("ShowTranscript = true before any toggle, want false")
	}

	m = Update(m, SidebarToggleMsg{})
	if !m.Sidebar.ShowTranscript || m.Sidebar.ShowRaw {
		t.Errorf("after 1 toggle: ShowTranscript=%v ShowRaw=%v, want true/false (rendered Transcript)", m.Sidebar.ShowTranscript, m.Sidebar.ShowRaw)
	}
	if len(m.Sidebar.Lines) != 1 || m.Sidebar.Lines[0] != "rendered" {
		t.Errorf("Lines = %v, want the rendered Transcript", m.Sidebar.Lines)
	}

	m = Update(m, SidebarToggleMsg{})
	if !m.Sidebar.ShowTranscript || !m.Sidebar.ShowRaw {
		t.Errorf("after 2 toggles: ShowTranscript=%v ShowRaw=%v, want true/true (raw Transcript)", m.Sidebar.ShowTranscript, m.Sidebar.ShowRaw)
	}
	if len(m.Sidebar.Lines) != 1 || m.Sidebar.Lines[0] != "raw" {
		t.Errorf("Lines = %v, want the raw Transcript", m.Sidebar.Lines)
	}

	m = Update(m, SidebarToggleMsg{})
	if m.Sidebar.ShowTranscript || m.Sidebar.ShowRaw {
		t.Errorf("after 3 toggles: ShowTranscript=%v ShowRaw=%v, want false/false (back to Activity)", m.Sidebar.ShowTranscript, m.Sidebar.ShowRaw)
	}
	if len(m.Sidebar.Lines) != 1 || !strings.Contains(m.Sidebar.Lines[0], "activity line") {
		t.Errorf("Lines = %v, want the Activity feed again", m.Sidebar.Lines)
	}
}

// The Transcript view's Offset must not leak into the Activity view and read
// as "following" while showing non-bottom content (review finding on issue
// #1502).
func TestUpdate_SidebarToggleMsg_BackToActivityWhileFollowing_SnapsToBottom(t *testing.T) {
	activity := make([]ActivityLine, 50)
	for i := range activity {
		activity[i] = ActivityLine{Text: fmt.Sprintf("l%d", i)}
	}
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity, Rendered: "short transcript"})
	if !m.Sidebar.Follow {
		t.Fatal("test setup: Follow must start true")
	}

	m = Update(m, SidebarToggleMsg{}) // rendered Transcript; Offset clamps to 0 on the short content
	m = Update(m, SidebarToggleMsg{}) // raw Transcript
	m = Update(m, SidebarToggleMsg{}) // back to Activity

	if m.Sidebar.ShowTranscript {
		t.Fatal("test setup: three toggles must land back on the Activity feed")
	}
	want := len(m.Sidebar.Lines) - sidebarModalScrollBudget(m) // last page fills the floating log modal's own budget (issue #1845)
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (following, snapped back to the Activity feed's own bottom)", m.Sidebar.Offset, want)
	}
}

func TestUpdate_SidebarToggleMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarToggleMsg{})
	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil", m.Sidebar)
	}
}

func TestUpdate_SidebarCloseMsg_ReturnsToBacklog(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "rendered"})
	m = Update(m, SidebarCloseMsg{})
	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil after close", m.Sidebar)
	}
	if m.Focus != FocusList {
		t.Errorf("Focus = %v, want FocusList after close", m.Focus)
	}
}

// Paging past either end must leave the pane on its first or last line rather
// than an invalid Offset (issue #786, inherited). No SizeChangedMsg is sent,
// so Height stays 0 and the final pgdown exercises the degenerate-height
// fallback, not the viewport-aware clamp added in #829. The rendered
// Transcript is toggled on so Lines holds the plain l0 through l4 content.
func TestUpdate_SidebarScrollMsg_MovesOffset(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3\nl4"})
	m = Update(m, SidebarToggleMsg{})

	m = Update(m, SidebarScrollMsg{Delta: 2})
	if m.Sidebar.Offset != 2 {
		t.Errorf("Offset = %d, want 2", m.Sidebar.Offset)
	}

	m = Update(m, SidebarScrollMsg{Delta: -1})
	if m.Sidebar.Offset != 1 {
		t.Errorf("Offset = %d, want 1", m.Sidebar.Offset)
	}

	m = Update(m, SidebarScrollMsg{Delta: -100})
	if m.Sidebar.Offset != 0 {
		t.Errorf("Offset = %d, want 0 (clamped at the top)", m.Sidebar.Offset)
	}

	// See the degenerate-height note in the doc comment above.
	m = Update(m, SidebarScrollMsg{Delta: 100})
	if m.Sidebar.Offset != 4 {
		t.Errorf("Offset = %d, want 4 (clamped to the last line)", m.Sidebar.Offset)
	}
}

// A large Delta must never leave the pane showing a single line with the rest
// of the viewport blank, so the clamp lands on the last full page rather than
// len(Lines)-1 (issue #829, inherited).
func TestUpdate_SidebarScrollMsg_ClampsToViewportHeight(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: strings.Join(lines, "\n")})
	m = Update(m, SidebarToggleMsg{})

	m = Update(m, SidebarScrollMsg{Delta: 1000})

	want := 100 - sidebarModalScrollBudget(m) // last page fills the floating log modal's own budget (issue #1845)
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (last page fills viewport)", m.Sidebar.Offset, want)
	}
}

// Docked, the clamp must use bodyBudget(m), the row budget
// renderSidebarDocked actually renders into, not the whole terminal Height
// that the header, banner and tabs eat into (#1501 review finding).
func TestUpdate_SidebarScrollMsg_DockedClampsToBodyBudgetNotFullHeight(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: strings.Join(lines, "\n")})
	m = Update(m, SidebarToggleMsg{})

	m = Update(m, SidebarScrollMsg{Delta: 1000})

	budget := bodyBudget(m)
	if budget >= 20 {
		t.Fatalf("test setup: bodyBudget(m) = %d, want it under Height (20) — header/tabs must actually eat into the docked budget", budget)
	}
	want := 100 - (budget - sidebarDockedFooterLines)
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (last page fills the docked body budget, not the full terminal height)", m.Sidebar.Offset, want)
	}
}

// Asserting through View, not the clamp formula: unless the clamp and the
// docked panel's real bordered row budget agree, the last two lines stay
// permanently unreachable behind the panel border (issue #1755).
func TestUpdate_SidebarScrollMsg_Docked_LastLineReachable(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	lines[99] = "TAIL-MARKER"
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: strings.Join(lines, "\n")})
	m = Update(m, SidebarToggleMsg{})

	m = Update(m, SidebarScrollMsg{Delta: 1000})

	out := View(m)
	if !strings.Contains(out, "TAIL-MARKER") {
		t.Errorf("View() = %q, want the transcript's last line reachable after pgdown to the end", out)
	}
}

// Zoomed on a terminal wide enough to dock, the clamp must use the row budget
// renderSidebarModalContent renders into once SidebarZoom forces the modal
// open (issue #1845), not bodyBudget(m), which only applies to the docked
// render the operator zoomed away from (review finding on issue #1502).
func TestUpdate_SidebarScrollMsg_ZoomedClampsToModalBudgetNotBodyBudget(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: strings.Join(lines, "\n")})
	m = Update(m, SidebarToggleMsg{})
	m = Update(m, SidebarZoomToggleMsg{})

	m = Update(m, SidebarScrollMsg{Delta: 1000})

	budget := bodyBudget(m)
	modalBudget := sidebarModalScrollBudget(m)
	if budget >= 20 || budget == modalBudget {
		t.Fatalf("test setup: bodyBudget(m) = %d, sidebarModalScrollBudget(m) = %d — the docked/zoomed budgets must actually differ to distinguish them", budget, modalBudget)
	}
	want := 100 - modalBudget // last page fills the floating log modal's own budget, not the docked budget
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (last page fills the zoomed modal's own budget, not the docked body budget)", m.Sidebar.Offset, want)
	}
}

// Content that already fits the viewport at Offset 0 must never get pushed to
// a higher Offset showing only its last line over a blank pane (issue #829,
// inherited).
func TestUpdate_SidebarScrollMsg_ShortTranscriptStaysAtTop(t *testing.T) {
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3\nl4"})
	m = Update(m, SidebarToggleMsg{})

	m = Update(m, SidebarScrollMsg{Delta: 1000})

	if m.Sidebar.Offset != 0 {
		t.Errorf("Offset = %d, want 0 (content already fits the viewport)", m.Sidebar.Offset)
	}
}

// Detaching Follow lets the operator review frozen history while the Dispatch
// keeps working, without the feed yanking them back to the bottom (issue
// #1502, ADR 0030).
func TestUpdate_SidebarScrollMsg_ScrollUpDetachesFollow(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3\nl4"})
	m = Update(m, SidebarToggleMsg{})
	if !m.Sidebar.Follow {
		t.Fatal("test setup: Follow must start true")
	}

	m = Update(m, SidebarScrollMsg{Delta: -1})

	if m.Sidebar.Follow {
		t.Error("Follow = true, want false after scrolling up")
	}
}

// Only scrolling up, which means reviewing history, detaches Follow (issue
// #1502, ADR 0030).
func TestUpdate_SidebarScrollMsg_ScrollDownDoesNotDetachFollow(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3\nl4"})
	m = Update(m, SidebarToggleMsg{})

	m = Update(m, SidebarScrollMsg{Delta: 1})

	if !m.Sidebar.Follow {
		t.Error("Follow = false, want true — a downward scroll must not detach it")
	}
}

func TestUpdate_SidebarScrollMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarScrollMsg{Delta: 1})
	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil", m.Sidebar)
	}
}

// G and End are the operator's way back to live-tailing after scrolling up to
// review history (issue #1502, ADR 0030).
func TestUpdate_SidebarJumpToEndMsg_ReattachesFollowAndJumpsToBottom(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3\nl4"})
	m = Update(m, SidebarToggleMsg{})
	m = Update(m, SidebarScrollMsg{Delta: -1}) // detaches Follow
	if m.Sidebar.Follow {
		t.Fatal("test setup: Follow must be detached before jumping to end")
	}

	m = Update(m, SidebarJumpToEndMsg{})

	if !m.Sidebar.Follow {
		t.Error("Follow = false, want true after G/End")
	}
	if m.Sidebar.Offset != 4 {
		t.Errorf("Offset = %d, want 4 (the last line)", m.Sidebar.Offset)
	}
}

func TestUpdate_SidebarJumpToEndMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarJumpToEndMsg{})
	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil", m.Sidebar)
	}
}

// gg parks the operator at the start of the buffer and detaches Follow the
// same way scrolling up with "k" does (issue #1629).
func TestUpdate_SidebarJumpToBeginningMsg_DetachesFollowAndJumpsToTop(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3\nl4"})
	m = Update(m, SidebarToggleMsg{})
	if !m.Sidebar.Follow {
		t.Fatal("test setup: Follow must start attached")
	}

	m = Update(m, SidebarJumpToBeginningMsg{})

	if m.Sidebar.Follow {
		t.Error("Follow = true, want false after gg")
	}
	if m.Sidebar.Offset != 0 {
		t.Errorf("Offset = %d, want 0 (the first line)", m.Sidebar.Offset)
	}
}

func TestUpdate_SidebarJumpToBeginningMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarJumpToBeginningMsg{})
	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil", m.Sidebar)
	}
}

// This is syncQueue's per-message live advance (issue #1502, ADR 0030).
func TestUpdate_SidebarActivityMsg_UpdatesActivityAndLines(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "first"}}})

	m = Update(m, SidebarActivityMsg{Number: "42", Activity: []ActivityLine{{Text: "first"}, {Text: "second"}}})

	if len(m.Sidebar.Activity) != 2 {
		t.Fatalf("Activity = %v, want 2 entries", m.Sidebar.Activity)
	}
	if len(m.Sidebar.Lines) != 2 || !strings.Contains(m.Sidebar.Lines[1], "second") {
		t.Errorf("Lines = %v, want the refreshed Activity feed", m.Sidebar.Lines)
	}
}

// Live-tailing is the default, so growth while Follow is true moves Offset to
// the newest line (issue #1502, ADR 0030).
func TestUpdate_SidebarActivityMsg_FollowSnapsToBottom(t *testing.T) {
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "l0"}}})
	if !m.Sidebar.Follow {
		t.Fatal("test setup: Follow must start true")
	}

	grown := make([]ActivityLine, 100)
	for i := range grown {
		grown[i] = ActivityLine{Text: fmt.Sprintf("l%d", i)}
	}
	m = Update(m, SidebarActivityMsg{Number: "42", Activity: grown})

	want := len(m.Sidebar.Lines) - sidebarModalScrollBudget(m) // last page fills the floating log modal's own budget (issue #1845)
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (the last page, following the growth)", m.Sidebar.Offset, want)
	}
}

// Once an earlier scroll-up has detached Follow, growth must leave Offset
// where the operator left it (issue #1502, ADR 0030).
func TestUpdate_SidebarActivityMsg_PreservesOffsetWhenNotFollowing(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "l0"}, {Text: "l1"}, {Text: "l2"}}})
	m = Update(m, SidebarScrollMsg{Delta: -1}) // detaches Follow
	wantOffset := m.Sidebar.Offset

	m = Update(m, SidebarActivityMsg{Number: "42", Activity: []ActivityLine{{Text: "l0"}, {Text: "l1"}, {Text: "l2"}, {Text: "l3"}}})

	if m.Sidebar.Offset != wantOffset {
		t.Errorf("Offset = %d, want %d (untouched while not following)", m.Sidebar.Offset, wantOffset)
	}
}

// Most syncQueue refreshes re-derive an unchanged on-disk log. A pgdown moves
// Offset without detaching Follow, so a same-content refresh right afterward
// must not yank it back to the bottom (issue #1502).
func TestUpdate_SidebarActivityMsg_UnchangedContentPreservesManualOffset(t *testing.T) {
	activity := make([]ActivityLine, 50)
	for i := range activity {
		activity[i] = ActivityLine{Text: fmt.Sprintf("l%d", i)}
	}
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity})
	m = Update(m, SidebarScrollMsg{Delta: 10}) // positive: moves Offset, Follow stays true
	if !m.Sidebar.Follow {
		t.Fatal("test setup: Follow must stay true after a downward scroll")
	}
	wantOffset := m.Sidebar.Offset
	if wantOffset == 0 {
		t.Fatal("test setup: the scroll must have actually moved Offset off 0")
	}

	m = Update(m, SidebarActivityMsg{Number: "42", Activity: activity}) // same content, no growth

	if m.Sidebar.Offset != wantOffset {
		t.Errorf("Offset = %d, want %d (unchanged content must not re-snap to the bottom)", m.Sidebar.Offset, wantOffset)
	}
}

// A Dispatch rolling from its initial run onto a fix pass gets a fresh,
// shorter pass log, since LogPaths and ActivityFeed key on the latest pass
// alone. Gating the refresh on "grew" would freeze the sidebar on the finished
// pass's stale lines while still labeled following (review finding on #1502).
func TestUpdate_SidebarActivityMsg_PassRolloverUpdatesLinesEvenWhenShorter(t *testing.T) {
	initial := make([]ActivityLine, 20)
	for i := range initial {
		initial[i] = ActivityLine{Text: fmt.Sprintf("pass0-l%d", i)}
	}
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: initial})

	// A fresh fix-pass log starts over: shorter than the finished initial pass,
	// and with entirely different content.
	nextPass := []ActivityLine{{Text: "pass1-l0"}}
	m = Update(m, SidebarActivityMsg{Number: "42", Activity: nextPass})

	if len(m.Sidebar.Lines) != 1 || !strings.Contains(m.Sidebar.Lines[0], "pass1-l0") {
		t.Errorf("Lines = %v, want the new (shorter) pass's content, not the stale finished pass", m.Sidebar.Lines)
	}
	if m.Sidebar.Offset != 0 {
		t.Errorf("Offset = %d, want 0 (following, snapped to the new pass's own bottom)", m.Sidebar.Offset)
	}
}

// A stale in-flight refresh racing a Dispatch switch must never clobber the
// newly selected Dispatch's feed (issue #1502).
func TestUpdate_SidebarActivityMsg_NoOpWhenNumberMismatch(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "l0"}}})

	m = Update(m, SidebarActivityMsg{Number: "43", Activity: []ActivityLine{{Text: "l0"}, {Text: "l1"}}})

	if len(m.Sidebar.Activity) != 1 {
		t.Errorf("Activity = %v, want the original single entry, untouched by a mismatched Number", m.Sidebar.Activity)
	}
}

func TestUpdate_SidebarActivityMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarActivityMsg{Number: "42", Activity: []ActivityLine{{Text: "l0"}}})
	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil", m.Sidebar)
	}
}

// The refresh must never yank the operator's Transcript view back to Activity
// content, yet it still stores the feed so toggling back reflects the growth
// (issue #1502).
func TestUpdate_SidebarActivityMsg_DoesNotOverwriteTranscriptLines(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "l0"}}, Rendered: "transcript line"})
	m = Update(m, SidebarToggleMsg{}) // switches to the rendered Transcript

	m = Update(m, SidebarActivityMsg{Number: "42", Activity: []ActivityLine{{Text: "l0"}, {Text: "l1"}}})

	if len(m.Sidebar.Lines) != 1 || m.Sidebar.Lines[0] != "transcript line" {
		t.Errorf("Lines = %v, want the Transcript untouched", m.Sidebar.Lines)
	}
	if len(m.Sidebar.Activity) != 2 {
		t.Errorf("Activity = %v, want the refreshed feed stored even while not shown", m.Sidebar.Activity)
	}
}

// The Transcript's own live-tail advance, the counterpart to
// SidebarActivityMsg's Activity refresh (issue #1736).
func TestUpdate_SidebarTranscriptMsg_UpdatesTranscriptAndLinesWhileShowing(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "first"})
	m = Update(m, SidebarToggleMsg{}) // switches to the rendered Transcript

	m = Update(m, SidebarTranscriptMsg{Number: "42", Rendered: "first\nsecond", Raw: "raw first\nraw second"})

	if m.Sidebar.TranscriptRendered != "first\nsecond" {
		t.Errorf("TranscriptRendered = %q, want the refreshed render", m.Sidebar.TranscriptRendered)
	}
	if len(m.Sidebar.Lines) != 2 || m.Sidebar.Lines[1] != "second" {
		t.Errorf("Lines = %v, want the refreshed Transcript's own two lines", m.Sidebar.Lines)
	}
}

// The Activity feed's live-tail default (issue #1502), extended to the
// Transcript view (#1736 AC2, "honours follow").
func TestUpdate_SidebarTranscriptMsg_FollowSnapsToBottom(t *testing.T) {
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "l0"})
	m = Update(m, SidebarToggleMsg{}) // switches to the rendered Transcript
	if !m.Sidebar.Follow {
		t.Fatal("test setup: Follow must start true")
	}

	var grown strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&grown, "l%d\n", i)
	}

	m = Update(m, SidebarTranscriptMsg{Number: "42", Rendered: grown.String()})

	want := len(m.Sidebar.Lines) - sidebarModalScrollBudget(m) // last page fills the floating log modal's own budget (issue #1845)
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (the last page, following the growth)", m.Sidebar.Offset, want)
	}
}

// A stale in-flight refresh racing a Dispatch switch must never clobber the
// newly selected Dispatch's Transcript, mirroring SidebarActivityMsg's own
// guard (issue #1736).
func TestUpdate_SidebarTranscriptMsg_NoOpWhenNumberMismatch(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "l0"})
	m = Update(m, SidebarToggleMsg{})

	m = Update(m, SidebarTranscriptMsg{Number: "43", Rendered: "l0\nl1"})

	if m.Sidebar.TranscriptRendered != "l0" {
		t.Errorf("TranscriptRendered = %q, want the original, untouched by a mismatched Number", m.Sidebar.TranscriptRendered)
	}
}

// An open sidebar takes the keyboard only once focus moves to it (#1501, ADR
// 0030).
func TestUpdate_FocusSidebarMsg_MovesFocus(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42"})
	m = Update(m, FocusListMsg{}) // opening already focused the sidebar; force it back to the list first
	m = Update(m, FocusSidebarMsg{})
	if m.Focus != FocusSidebar {
		t.Errorf("Focus = %v, want FocusSidebar", m.Focus)
	}
}

func TestUpdate_FocusSidebarMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, FocusSidebarMsg{})
	if m.Focus != FocusList {
		t.Errorf("Focus = %v, want FocusList (no sidebar to focus)", m.Focus)
	}
}

// Moving focus away must not close the sidebar (#1501, ADR 0030).
func TestUpdate_FocusListMsg_MovesFocus(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42"})
	if m.Focus != FocusSidebar {
		t.Fatal("test setup: opening a sidebar must focus it")
	}

	m = Update(m, FocusListMsg{})
	if m.Focus != FocusList {
		t.Errorf("Focus = %v, want FocusList", m.Focus)
	}
	if m.Sidebar == nil {
		t.Error("Sidebar = nil, want it to stay open — moving focus away must not close it")
	}
}

// Model.Offset clamps into the visible list's bounds the same way
// SidebarScrollMsg clamps Sidebar.Offset (issue #1036, ADR 0030).
func TestUpdate_ScrollMsg_MovesOffset(t *testing.T) {
	m := NewModel()
	issues := make([]forge.Issue, 5)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	m = Update(m, ScrollMsg{Delta: 2})
	if m.Offset != 2 {
		t.Errorf("Offset = %d, want 2", m.Offset)
	}

	m = Update(m, ScrollMsg{Delta: -1})
	if m.Offset != 1 {
		t.Errorf("Offset = %d, want 1", m.Offset)
	}

	m = Update(m, ScrollMsg{Delta: -100})
	if m.Offset != 0 {
		t.Errorf("Offset = %d, want 0 (clamped at the top)", m.Offset)
	}

	m = Update(m, ScrollMsg{Delta: 100})
	if m.Offset != 4 {
		t.Errorf("Offset = %d, want 4 (clamped to the last row)", m.Offset)
	}
}

// "gg" resets the scroll offset too, not just the cursor (issue #1628 AC2).
func TestUpdate_CursorJumpToFirstMsg_ResetsCursorAndOffset(t *testing.T) {
	m := NewModel()
	issues := make([]forge.Issue, 5)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})
	m = Update(m, CursorMoveMsg{Delta: 4})
	m = Update(m, ScrollMsg{Delta: 4})

	m = Update(m, CursorJumpToFirstMsg{})

	if m.Cursor != 0 {
		t.Errorf("Cursor = %d, want 0", m.Cursor)
	}
	if m.Offset != 0 {
		t.Errorf("Offset = %d, want 0", m.Offset)
	}
}

// "G" drags the scroll offset just far enough to keep the last row on screen
// (issue #1628 AC1).
func TestUpdate_CursorJumpToLastMsg_MovesCursorAndDragsOffsetIntoView(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	m = Update(m, CursorJumpToLastMsg{})

	if m.Cursor != 49 {
		t.Errorf("Cursor = %d, want 49 (last row)", m.Cursor)
	}
	if m.Offset == 0 {
		t.Errorf("Offset = 0, want it dragged forward so row 49 is visible")
	}
}

// Asserting through View, not the clamp formula: unless the clamp agrees with
// the bordered panel's real row budget, the last row stays unreachable behind
// the panel border (issue #1755, the list-side counterpart of the sidebar's
// own docked scroll-clamp fix).
func TestUpdate_CursorJumpToLastMsg_Docked_LastRowReachable(t *testing.T) {
	// Height also covers the header's own bordered panel (issue #1756) on top
	// of the docked list/sidebar panel border #1755 budgeted for. Without it no
	// row is left for data, only the column header.
	m := Update(NewModel(), SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 10 + boxBorderRows})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	m = Update(m, CursorJumpToLastMsg{})

	if !strings.Contains(View(m), "issue 49") {
		t.Errorf("View() = %q, want the last row (issue 49) reachable after \"G\" with the sidebar docked", View(m))
	}
}

// ModeList's pinned footer (issue #1792) reserves a row, so listContentBudget
// must agree with what renderBody has room to show or the last row stays
// unreachable behind the footer (the bug class #1755 fixed for the sidebar).
func TestUpdate_CursorJumpToLastMsg_LastRowReachable_WithListFooter(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	m = Update(m, CursorJumpToLastMsg{})

	if !strings.Contains(View(m), "issue 49") {
		t.Errorf("View() = %q, want the last row (issue 49) reachable after \"G\" with the list footer pinned", View(m))
	}
}

// The pending-g leader lives on the Model, so the chord survives between
// Update calls (issue #1628 AC3).
func TestUpdate_GPendingMsg_ArmsPendingG(t *testing.T) {
	m := NewModel()

	m = Update(m, GPendingMsg{})

	if !m.PendingG {
		t.Error("PendingG = false, want true after GPendingMsg")
	}
}

// The chord sends GResolvedMsg when it completes, cancels, or times out
// (issue #1628 AC4).
func TestUpdate_GResolvedMsg_ClearsPendingG(t *testing.T) {
	m := NewModel()
	m = Update(m, GPendingMsg{})

	m = Update(m, GResolvedMsg{})

	if m.PendingG {
		t.Error("PendingG = true, want false after GResolvedMsg")
	}
}

// Neither jump may leave Cursor or Offset pointing past a nonexistent end
// (issue #1628 AC6).
func TestUpdate_CursorJump_EmptySection_NoOp(t *testing.T) {
	m := NewModel()

	m = Update(m, CursorJumpToLastMsg{})
	if m.Cursor != 0 || m.Offset != 0 {
		t.Errorf("after G on empty: Cursor = %d, Offset = %d, want 0, 0", m.Cursor, m.Offset)
	}

	m = Update(m, CursorJumpToFirstMsg{})
	if m.Cursor != 0 || m.Offset != 0 {
		t.Errorf("after gg on empty: Cursor = %d, Offset = %d, want 0, 0", m.Cursor, m.Offset)
	}
}

// Switching Sections resets Offset to 0 (issue #1500), so a scroll sent
// afterward must move that fresh 0, not a stale Backlog offset (ADR 0030).
func TestUpdate_ScrollMsg_MovesOffsetWithinActiveWorkSection(t *testing.T) {
	m := NewModel()
	picks := make([]Pick, 5)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	m = Update(m, SectionJumpMsg{Section: SectionRunning})

	m = Update(m, ScrollMsg{Delta: 3})
	if m.Offset != 3 {
		t.Errorf("Offset = %d, want 3", m.Offset)
	}
}

// When the Section's content already fits one screen, pgdown still advances to
// the last row instead of doing nothing, scrolling the already-visible rows off
// screen (issue #1060).
func TestUpdate_ScrollMsg_OffsetScrollsPastEndWhenContentFitsOnScreen(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	issues := make([]forge.Issue, 3)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	delta := sectionPageSize(m, resolveLayout(m))
	if delta != len(issues) {
		t.Fatalf("sectionPageSize = %d, want %d (test setup: all issues must fit within one screen)", delta, len(issues))
	}

	m = Update(m, ScrollMsg{Delta: delta})
	if m.Offset != len(issues)-1 {
		t.Errorf("Offset = %d, want %d (pgdown scrolls to the last row even though every row already fit on screen)", m.Offset, len(issues)-1)
	}
}

// The viewport advances and rewinds by one as the cursor crosses its bottom
// and top rows, keeping the highlighted row on screen (issue #1036 AC1).
// visibleRows comes from sectionPageSize, the same composition renderBody uses
// (issue #1056), so a geometry change does not need a hand-computed constant
// re-derived here.
func TestUpdate_CursorMoveMsg_OffsetFollowsCursor(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	visibleRows := sectionPageSize(m, resolveLayout(m))

	// Rows 0 through visibleRows-1 are visible at offset 0, so moving down
	// within that window must not scroll.
	for step := 1; step <= visibleRows-1; step++ {
		m = Update(m, CursorMoveMsg{Delta: 1})
		if m.Offset != 0 {
			t.Fatalf("after %d down-moves: Offset = %d, want 0 (cursor %d still on screen)", step, m.Offset, m.Cursor)
		}
	}

	// The cursor sits on the last visible row, so one more down-move pushes it
	// past the bottom and advances the offset by exactly one.
	m = Update(m, CursorMoveMsg{Delta: 1})
	if m.Cursor != visibleRows || m.Offset != 1 {
		t.Fatalf("Cursor = %d, Offset = %d, want Cursor %d, Offset 1 (scrolled to keep the cursor visible)", m.Cursor, m.Offset, visibleRows)
	}

	m = Update(m, CursorMoveMsg{Delta: 1})
	if m.Cursor != visibleRows+1 || m.Offset != 2 {
		t.Fatalf("Cursor = %d, Offset = %d, want Cursor %d, Offset 2 (offset advances one more)", m.Cursor, m.Offset, visibleRows+1)
	}

	// Moving back up, the offset must not rewind while the cursor is still
	// inside the current window (offset 2, visibleRows tall), its top row
	// included.
	for step := 1; step <= visibleRows-1; step++ {
		m = Update(m, CursorMoveMsg{Delta: -1})
		if m.Offset != 2 {
			t.Fatalf("after %d up-moves: Offset = %d, want 2 (cursor %d still on screen)", step, m.Offset, m.Cursor)
		}
	}
	// The 2 here, and the 1s below, count how many times the window crossed a
	// boundary rather than a row position derived from visibleRows, so they
	// hold whatever the geometry.
	if m.Cursor != 2 {
		t.Fatalf("Cursor = %d, want 2", m.Cursor)
	}

	// The cursor sits on row 2, the window's top row, so one more up-move
	// pushes it above the top and rewinds the offset by exactly one.
	m = Update(m, CursorMoveMsg{Delta: -1})
	if m.Cursor != 1 || m.Offset != 1 {
		t.Fatalf("Cursor = %d, Offset = %d, want Cursor 1, Offset 1 (scrolled up to keep the cursor visible)", m.Cursor, m.Offset)
	}

	m = Update(m, CursorMoveMsg{Delta: -100})
	if m.Cursor != 0 || m.Offset != 0 {
		t.Fatalf("Cursor = %d, Offset = %d, want Cursor 0, Offset 0 (clamped to the top)", m.Cursor, m.Offset)
	}
}

// pgup and pgdown deliberately leave Offset non-page-capped (issue #1060,
// tracked separately as #1053). A later cursor move that does not itself need
// to scroll must not pull that inflated Offset back toward
// Viewport.SetHeight's clamp-on-shrink (issue #1540 review finding).
func TestUpdate_CursorMoveMsg_DoesNotRecapOffsetLeftPastFoldByScroll(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 100)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	// Drive the cursor to the end so follow settles Offset at its own natural
	// resting point.
	m = Update(m, CursorMoveMsg{Delta: 1000})
	settled := m.Offset
	if settled <= 0 || settled >= 99 {
		t.Fatalf("test setup: Offset settled at %d, want a real intermediate resting point", settled)
	}

	// pgdown past that resting point, issue #1060's deliberately
	// non-page-capped scroll, lands one row further than follow alone ever
	// would.
	m = Update(m, ScrollMsg{Delta: 1})
	if m.Offset != settled+1 {
		t.Fatalf("test setup: Offset = %d, want %d after nudging one row past the follow rest point", m.Offset, settled+1)
	}

	// The cursor stays at the end, inside the nudged window, so the resulting
	// CursorMoveMsg must leave Offset alone rather than re-capping it back to
	// the follow rest point.
	m = Update(m, CursorMoveMsg{Delta: 0})
	if m.Offset != settled+1 {
		t.Errorf("Offset = %d, want %d (unchanged — pgdown's uncapped overshoot must survive a cursor move that doesn't need to scroll)", m.Offset, settled+1)
	}
}

// A later render must never window past what Visible() holds, so a narrowing
// filter pulls the offset back into range (issue #1036 AC on offset clamping
// when the list shrinks).
func TestUpdate_FilterChangedMsg_ClampsOffsetOnShrink(t *testing.T) {
	m := NewModel()
	issues := make([]forge.Issue, 10)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i), Labels: []string{"keep"}}
	}
	issues[0].Labels = []string{"only-match"}
	m = Update(m, IssuesLoadedMsg{Issues: issues})
	m = Update(m, ScrollMsg{Delta: 8})
	if m.Offset != 8 {
		t.Fatalf("Offset = %d, want 8 before filtering", m.Offset)
	}

	m = Update(m, FilterChangedMsg{Filter: "only-match"})

	if len(m.Visible()) != 1 {
		t.Fatalf("Visible() = %d issues, want 1 after filtering", len(m.Visible()))
	}
	if m.Offset != 0 {
		t.Errorf("Offset = %d, want 0 (clamped after the filtered list shrank to 1 row)", m.Offset)
	}
}

// View's stale banner reads this per-render sync of the launcher's freshness
// and rebuild state (issue #652).
func TestUpdate_StaleStatusMsg_SetsFields(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Message: "rebuild needed", Rebuilding: true, Err: "boom"}})

	if !m.RebuildStatus.Stale {
		t.Error("Stale = false, want true")
	}
	if m.RebuildStatus.Message != "rebuild needed" {
		t.Errorf("Message = %q, want %q", m.RebuildStatus.Message, "rebuild needed")
	}
	if !m.RebuildStatus.Rebuilding {
		t.Error("Rebuilding = false, want true")
	}
	if m.RebuildStatus.Err != "boom" {
		t.Errorf("Err = %q, want %q", m.RebuildStatus.Err, "boom")
	}
}

// View's orphan-recovery banner reads this per-render sync (issue #1218).
func TestUpdate_OrphanRecoveryMsg_SetsErr(t *testing.T) {
	m := NewModel()
	m = Update(m, OrphanRecoveryMsg{Err: "failed to adopt orphan #42: boom"})

	if m.OrphanRecoveryErr != "failed to adopt orphan #42: boom" {
		t.Errorf("OrphanRecoveryErr = %q, want %q", m.OrphanRecoveryErr, "failed to adopt orphan #42: boom")
	}
}

// Update's detect-only half of #1619's demotion: startup only detects now,
// never adopts.
func TestUpdate_OrphanDetectedMsg_FlagsIsOrphan(t *testing.T) {
	m := NewModel()
	m = Update(m, OrphanDetectedMsg{Numbers: []string{"42"}})

	if !m.IsOrphan("42") {
		t.Error("IsOrphan(42) = false, want true after OrphanDetectedMsg{Numbers: [42]}")
	}
	if m.IsOrphan("7") {
		t.Error("IsOrphan(7) = true, want false — never reported as orphaned")
	}
}

// Leaving the flag set would let a second press of the adopt gesture on the
// now-adopted row fire RecoverFn again, racing a second same-process settle
// over the one PR the first adopt claimed (issue #1619 review finding).
func TestUpdate_OrphanAdoptedMsg_ClearsIsOrphan(t *testing.T) {
	m := NewModel()
	m = Update(m, OrphanDetectedMsg{Numbers: []string{"42", "7"}})

	m = Update(m, OrphanAdoptedMsg{Number: "42"})

	if m.IsOrphan("42") {
		t.Error("IsOrphan(42) = true after OrphanAdoptedMsg{Number: 42}, want false")
	}
	if !m.IsOrphan("7") {
		t.Error("IsOrphan(7) = false, want true — only 42 was adopted")
	}
}

// The sibling TestUpdate_StaleStatusMsg_SetsFields only threads the zero
// value, so it never exercised a non-empty Output (issue #1129).
func TestUpdate_StaleStatusMsg_PropagatesCapturedRebuildOutput(t *testing.T) {
	m := NewModel()
	const wantOutput = "nix: building '/nix/store/abc-spindrift-1.2.3.drv'...\n"
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: wantOutput}})

	if m.RebuildStatus.Output != wantOutput {
		t.Errorf("Output = %q, want %q", m.RebuildStatus.Output, wantOutput)
	}
}

// A refresh while live-tailing must keep the operator's view toggle and leave
// focus wherever they moved it.
func TestUpdate_SidebarLoadedMsg_RefreshSameNumber_PreservesToggleState(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "first"})
	m = Update(m, SidebarToggleMsg{})
	m = Update(m, FocusListMsg{})

	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "second (grew)"})
	if !m.Sidebar.ShowTranscript {
		t.Error("ShowTranscript reset to false on refresh, want it preserved as true")
	}
	if m.Sidebar.TranscriptRendered != "second (grew)" {
		t.Errorf("TranscriptRendered = %q, want the refreshed content", m.Sidebar.TranscriptRendered)
	}
	if m.Focus != FocusList {
		t.Errorf("Focus = %v, want FocusList preserved (a same-number refresh must not re-yank focus)", m.Focus)
	}
}

// Hopping between running Dispatches never loses the operator's place (issue
// #1502, ADR 0030).
func TestUpdate_SidebarLoadedMsg_RetainsPositionAcrossDispatchSwitch(t *testing.T) {
	activity := make([]ActivityLine, 50)
	for i := range activity {
		activity[i] = ActivityLine{Text: fmt.Sprintf("l%d", i)}
	}

	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity})
	m = Update(m, SidebarScrollMsg{Delta: 5})
	m = Update(m, SidebarScrollMsg{Delta: -1}) // detaches Follow, a few lines up from wherever Follow had it
	wantOffset, wantFollow := m.Sidebar.Offset, m.Sidebar.Follow
	if wantFollow {
		t.Fatal("test setup: Follow must be detached")
	}

	m = Update(m, SidebarLoadedMsg{Number: "43", Activity: []ActivityLine{{Text: "other dispatch"}}})
	if m.Sidebar.Number != "43" {
		t.Fatal("test setup: sidebar must have switched to #43")
	}

	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity})

	if m.Sidebar.Offset != wantOffset {
		t.Errorf("Offset = %d, want %d (retained from before the switch)", m.Sidebar.Offset, wantOffset)
	}
	if m.Sidebar.Follow != wantFollow {
		t.Errorf("Follow = %v, want %v (retained from before the switch)", m.Sidebar.Follow, wantFollow)
	}
}

// The Dispatch kept working while the sidebar was shut, so reopening while
// still following must land at the fresh content's bottom, not the stale
// Offset saved before close (review finding on issue #1502).
func TestUpdate_SidebarLoadedMsg_ReopenWhileFollowing_SnapsToNewBottom(t *testing.T) {
	initial := make([]ActivityLine, 20)
	for i := range initial {
		initial[i] = ActivityLine{Text: fmt.Sprintf("l%d", i)}
	}

	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: initial})
	m = Update(m, SidebarScrollMsg{Delta: 5}) // moves Offset without detaching Follow
	if !m.Sidebar.Follow {
		t.Fatal("test setup: Follow must stay true after a downward scroll")
	}
	staleOffset := m.Sidebar.Offset

	m = Update(m, SidebarCloseMsg{})

	grown := make([]ActivityLine, 60) // the Dispatch kept working while closed
	for i := range grown {
		grown[i] = ActivityLine{Text: fmt.Sprintf("l%d", i)}
	}
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: grown})

	want := len(m.Sidebar.Lines) - sidebarModalScrollBudget(m) // last page fills the floating log modal's own budget (issue #1845)
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (the new bottom, not the stale %d saved before close)", m.Sidebar.Offset, want, staleOffset)
	}
}

// A reopen restores the saved position rather than resetting to the top with
// Follow re-armed (issue #1502, ADR 0030).
func TestUpdate_SidebarCloseMsg_RetainsPositionForReopen(t *testing.T) {
	activity := make([]ActivityLine, 50)
	for i := range activity {
		activity[i] = ActivityLine{Text: fmt.Sprintf("l%d", i)}
	}

	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity})
	m = Update(m, SidebarScrollMsg{Delta: 5})
	m = Update(m, SidebarScrollMsg{Delta: -1})
	wantOffset, wantFollow := m.Sidebar.Offset, m.Sidebar.Follow

	m = Update(m, SidebarCloseMsg{})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity})

	if m.Sidebar.Offset != wantOffset {
		t.Errorf("Offset = %d, want %d (retained across close/reopen)", m.Sidebar.Offset, wantOffset)
	}
	if m.Sidebar.Follow != wantFollow {
		t.Errorf("Follow = %v, want %v (retained across close/reopen)", m.Sidebar.Follow, wantFollow)
	}
}

// The fullscreen zoom toggle for deep reading is independent of the
// narrow-terminal fallback (issue #1502, ADR 0030).
func TestUpdate_SidebarZoomToggleMsg_TogglesZoom(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	m = Update(m, SidebarZoomToggleMsg{})
	if !m.SidebarZoom {
		t.Error("SidebarZoom = false, want true after one toggle")
	}

	m = Update(m, SidebarZoomToggleMsg{})
	if m.SidebarZoom {
		t.Error("SidebarZoom = true, want false after a second toggle")
	}
}

// Clearing SidebarZoom on close means a later reopen on a wide terminal starts
// docked rather than still forced fullscreen from a prior session (#1502).
func TestUpdate_SidebarCloseMsg_ResetsZoom(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m = Update(m, SidebarZoomToggleMsg{})
	if !m.SidebarZoom {
		t.Fatal("test setup: SidebarZoom must be true")
	}

	m = Update(m, SidebarCloseMsg{})

	if m.SidebarZoom {
		t.Error("SidebarZoom = true, want false after closing the sidebar")
	}
}

// The fixture is the common state after "h": docked sidebar, focus back on the
// list. Switching Sections must dismiss the sidebar instead of leaving it
// pinned to the old Dispatch under the new Section's list (issue #1581).
func TestUpdate_SectionNextMsg_ClosesSidebarWhenOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m = Update(m, FocusListMsg{})

	m = Update(m, SectionNextMsg{})

	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil after switching Sections", m.Sidebar)
	}
	if m.Focus != FocusList {
		t.Errorf("Focus = %v, want FocusList after switching Sections", m.Focus)
	}
}

// Mirrors TestUpdate_SectionNextMsg_ClosesSidebarWhenOpen for the "H"
// direction (issue #1581).
func TestUpdate_SectionPrevMsg_ClosesSidebarWhenOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m = Update(m, FocusListMsg{})

	m = Update(m, SectionPrevMsg{})

	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil after switching Sections", m.Sidebar)
	}
	if m.Focus != FocusList {
		t.Errorf("Focus = %v, want FocusList after switching Sections", m.Focus)
	}
}

// Mirrors TestUpdate_SectionNextMsg_ClosesSidebarWhenOpen for a direct "1" to
// "5" jump, and also pins that SidebarZoom resets and the position is saved so
// a later reopen restores scroll and follow (issue #1581).
func TestUpdate_SectionJumpMsg_ClosesSidebarWhenOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m = Update(m, FocusListMsg{})
	m = Update(m, SidebarZoomToggleMsg{})
	if !m.SidebarZoom {
		t.Fatal("test setup: SidebarZoom must be true")
	}

	m = Update(m, SectionJumpMsg{Section: SectionHeld})

	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil after switching Sections", m.Sidebar)
	}
	if m.Focus != FocusList {
		t.Errorf("Focus = %v, want FocusList after switching Sections", m.Focus)
	}
	if m.SidebarZoom {
		t.Error("SidebarZoom = true, want false after switching Sections")
	}
	if _, ok := m.SidebarPositions["42"]; !ok {
		t.Error("SidebarPositions[42] missing, want position saved before close")
	}
}

// Consistent with switchSection's existing same-section guard on Cursor and
// Offset (issue #1581).
func TestUpdate_SectionJumpMsg_SameSectionLeavesSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m = Update(m, FocusListMsg{})

	m = Update(m, SectionJumpMsg{Section: m.ActiveSection})

	if m.Sidebar == nil {
		t.Error("Sidebar = nil, want unchanged (jumped to the already-active Section)")
	}
}

// The sibling test covers the clamp; this one pins the plain pass-through
// (issue #842).
func TestUpdate_SizeChangedMsg_AppliesWidthHeight(t *testing.T) {
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 100, Height: 40})

	if m.Width != 100 {
		t.Errorf("Width = %d, want 100", m.Width)
	}
	if m.Height != 40 {
		t.Errorf("Height = %d, want 40", m.Height)
	}
}

// A zero or negative dimension clamps to the safe floor instead of landing on
// Model unchanged (issue #842).
func TestUpdate_SizeChangedMsg_ClampsNonPositive(t *testing.T) {
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 0, Height: -5})

	if m.Width != minTerminalDimension {
		t.Errorf("Width = %d, want clamped to %d", m.Width, minTerminalDimension)
	}
	if m.Height != minTerminalDimension {
		t.Errorf("Height = %d, want clamped to %d", m.Height, minTerminalDimension)
	}
}

// DetailModalOpenMsg opens the modal with only the number, title and labels a
// Backlog row has in hand; the async body fetch fills in the rest (#1632).
func TestUpdate_DetailModalLoadedMsg_FillsBodyAndClearsLoading(t *testing.T) {
	m := NewModel()
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: []string{"bug"}})
	if !m.DetailModal.Loading {
		t.Fatal("test setup: DetailModal.Loading = false immediately after DetailModalOpenMsg, want true")
	}

	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: "the full body text"})

	if m.DetailModal.Loading {
		t.Error("DetailModal.Loading = true after DetailModalLoadedMsg, want false")
	}
	if m.DetailModal.Body != "the full body text" {
		t.Errorf("DetailModal.Body = %q, want %q", m.DetailModal.Body, "the full body text")
	}
}

// A load for a ticket the operator has since closed or switched away from must
// never overwrite what the modal shows now, the same same-number guard
// SidebarLoadedMsg applies (issue #1632).
func TestUpdate_DetailModalLoadedMsg_StaleNumberIgnored(t *testing.T) {
	m := NewModel()
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalCloseMsg{})

	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: "arrives after close"})

	if m.DetailModal != nil {
		t.Errorf("DetailModal = %+v after close, want nil — a stale load must not reopen it", m.DetailModal)
	}
}

// Dropping the per-ticket cache makes the next modal open re-fetch rather than
// replay data that may now be stale (issue #1632).
func TestUpdate_DetailCacheInvalidatedMsg_ClearsCache(t *testing.T) {
	m := NewModel()
	m.DetailCache = map[string]DetailModalCache{"42": {Body: "stale"}}

	m = Update(m, DetailCacheInvalidatedMsg{})

	if m.DetailCache != nil {
		t.Errorf("DetailCache = %v after DetailCacheInvalidatedMsg, want nil", m.DetailCache)
	}
}

// The rest of #3018's threading depends on updateLayout's returned layout
// matching resolveLayout on its returned model. That holds because the tail
// resolve runs after every mutation resolveLayout's inputs can undergo; only
// fields resolveLayout never reads (Cursor, Offset, Sidebar.Offset,
// RebuildOutputOffset, DetailModal.Offset) change afterward.
func TestUpdateLayout_ReturnedLayoutMatchesResolveLayoutOnReturnedModel(t *testing.T) {
	base := Update(NewModel(), IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1"}, {Number: "2"}}})
	base = Update(base, SizeChangedMsg{Width: 80, Height: 24})
	base = Update(base, DetailModalOpenMsg{Number: "1", Title: "fix the thing"})
	base = Update(base, DetailModalLoadedMsg{Number: "1", Body: "one two three four five"})

	msgs := []Msg{
		SizeChangedMsg{Width: 100, Height: 30},
		DetailModalScrollMsg{Delta: 1},
		CursorMoveMsg{Delta: 1},
	}
	for _, msg := range msgs {
		got, l := updateLayout(base, msg)
		want := resolveLayout(got)
		if l != want {
			t.Errorf("updateLayout(%T) layout = %+v, want resolveLayout(returned model) = %+v", msg, l, want)
		}
	}
}

// DetailModal.Lines is width-dependent, unlike SidebarState.Lines, which never
// wraps. A stale narrower wrap left in place would carry line breaks sized for
// a width the modal no longer has (issue #1632 review finding).
func TestUpdate_SizeChangedMsg_RewrapsOpenDetailModal(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 10, Height: 24})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: "one two three four five"})
	narrowLines := len(m.DetailModal.Lines)

	m = Update(m, SizeChangedMsg{Width: 200, Height: 24})

	if len(m.DetailModal.Lines) >= narrowLines {
		t.Errorf("Lines = %d after widening, want fewer than the %d-wide wrap's %d lines", len(m.DetailModal.Lines), 10, narrowLines)
	}
	if m.DetailModal.Lines[0] != "one two three four five" {
		t.Errorf("Lines[0] = %q, want the whole body unwrapped at the new 200-column width", m.DetailModal.Lines[0])
	}
}

// Both sizes clear detailModalFits, so this covers a resize inside the
// floating regime: the box itself grows with the terminal (issue #1759 AC),
// not just the fullscreen-to-floating crossing the sibling test covers.
func TestUpdate_SizeChangedMsg_RewrapsFloatingModal_ToNewBoxInteriorWidth(t *testing.T) {
	if !detailModalFits(Model{Width: 60, Height: 30}) || !detailModalFits(Model{Width: 200, Height: 30}) {
		t.Fatalf("test setup invalid: both sizes must stay in the floating regime")
	}

	body := strings.TrimSpace(strings.Repeat("ab ", 40)) // 119 display columns unwrapped
	m := Update(NewModel(), SizeChangedMsg{Width: 60, Height: 30})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: body})
	narrowLines := len(m.DetailModal.Lines)

	m = Update(m, SizeChangedMsg{Width: 200, Height: 30})

	if len(m.DetailModal.Lines) >= narrowLines {
		t.Errorf("Lines = %d after widening within the floating regime, want fewer than the narrower box's %d lines", len(m.DetailModal.Lines), narrowLines)
	}
}

// Issue #1758 rewires every width-dependent piece of the old fullscreen
// renderer to key off the box interior instead of Model.Width.
func TestUpdate_DetailModalLoadedMsg_WrapsToBoxInteriorNotTerminalWidth(t *testing.T) {
	body := strings.TrimSpace(strings.Repeat("ab ", 40)) // 119 display columns unwrapped
	m := Update(NewModel(), SizeChangedMsg{Width: 200, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})

	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: body})

	if len(m.DetailModal.Lines) < 2 {
		t.Errorf("Lines = %d line(s) on a 200-column terminal, want the body wrapped across multiple lines at the box's much narrower interior width, not the raw terminal width", len(m.DetailModal.Lines))
	}
}

// The height half of the box-interior rewiring that
// TestUpdate_DetailModalLoadedMsg_WrapsToBoxInteriorNotTerminalWidth checks
// for width (issue #1758).
func TestUpdate_DetailModalScroll_ClampsToBoxInteriorHeightNotTerminalHeight(t *testing.T) {
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	body := strings.Join(lines, "\n")

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: body})
	m = Update(m, DetailModalScrollMsg{Delta: 1000})

	// The box interior at a 40-row terminal (detailModalBoxSize caps the box at
	// 30 rows, less 2 border rows, less the no-labels case's 1 label line and 1
	// footer line) falls well short of the fullscreen renderer's own budget, so
	// the two clamps disagree unless the offset clamp was actually rewired.
	if want := len(lines) - 26; m.DetailModal.Offset != want {
		t.Errorf("Offset = %d after scrolling past the end, want %d (clamped to the box interior's row budget)", m.DetailModal.Offset, want)
	}
}

// Labels that wrap onto further interior rows leave less room for the body, so
// the Offset clamp must fold in their real row count instead of assuming one
// row, or it targets a budget the render has no room to show (issue #1772).
func TestUpdate_DetailModalScroll_ClampsForWrappedLabelLines(t *testing.T) {
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	body := strings.Join(lines, "\n")
	// Each label is 45 display columns, under the 82-column box interior on its
	// own, but two plus their ", " separator (92) overflow it, so wrapText
	// places exactly one label per line, giving 3 label lines.
	labels := []string{strings.Repeat("a", 45), strings.Repeat("b", 45), strings.Repeat("c", 45)}

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: labels})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: body})
	m = Update(m, DetailModalScrollMsg{Delta: 1000})

	// Box interior height 28 (see the sibling test above), less 3 label lines
	// and 1 footer line, leaves a body budget of 24.
	if want := len(lines) - 24; m.DetailModal.Offset != want {
		t.Errorf("Offset = %d after scrolling past the end, want %d (clamped to the box interior's row budget with 3 wrapped label lines)", m.DetailModal.Offset, want)
	}
}

// Mirrors TestUpdate_DetailModalScroll_ClampsForWrappedLabelLines for the
// small-terminal fullscreen fallback, where the pinned label row now gets the
// same bracketed, wrapped treatment as the floating box (issue #1832).
func TestUpdate_DetailModalScroll_Fullscreen_ClampsForWrappedLabelLines(t *testing.T) {
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	body := strings.Join(lines, "\n")
	// Each label plus its bracket and comma is 32 display columns, under the
	// 39-column fullscreen width on its own, but two together (64) overflow it,
	// so wrapText places exactly one label per line, giving 3 label lines.
	labels := []string{strings.Repeat("a", 30), strings.Repeat("b", 30), strings.Repeat("c", 30)}

	m := Update(NewModel(), SizeChangedMsg{Width: detailModalBoxMinWidth - 1, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing", Labels: labels})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: body})
	m = Update(m, DetailModalScrollMsg{Delta: 1000})

	// The content budget (height 40, less 1 title line and 1 footer line) is 38,
	// and less 3 wrapped label lines leaves a body budget of 35.
	if want := len(lines) - 35; m.DetailModal.Offset != want {
		t.Errorf("Offset = %d after scrolling past the end, want %d (clamped to the fullscreen row budget with 3 wrapped label lines)", m.DetailModal.Offset, want)
	}
}

// Mirrors RebuildOutputJumpToFirstMsg's reset for the rebuild-output pane
// (issue #1795).
func TestUpdate_DetailModalJumpToFirstMsg_ResetsOffsetToZero(t *testing.T) {
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	body := strings.Join(lines, "\n")

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: body})
	m = Update(m, DetailModalScrollMsg{Delta: 10})

	m = Update(m, DetailModalJumpToFirstMsg{})
	if m.DetailModal.Offset != 0 {
		t.Errorf("Offset = %d, want 0", m.DetailModal.Offset)
	}
}

// "G" lands on the last page that still fills the box's scroll budget, not on
// the last line: the same page-capped clamp the DetailModal block applies on
// every Update (issue #1795).
func TestUpdate_DetailModalJumpToLastMsg_JumpsToLastPage(t *testing.T) {
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	body := strings.Join(lines, "\n")

	m := Update(NewModel(), SizeChangedMsg{Width: 100, Height: 40})
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: body})

	m = Update(m, DetailModalJumpToLastMsg{})

	// Same box-interior budget as TestUpdate_DetailModalScroll_
	// ClampsToBoxInteriorHeightNotTerminalHeight, 40 lines over 26 rows.
	if want := len(lines) - 26; m.DetailModal.Offset != want {
		t.Errorf("Offset = %d after \"G\", want %d (clamped to the box interior's row budget)", m.DetailModal.Offset, want)
	}
}

// Mirrors TestUpdate_RebuildOutputJumpMsgs_NoOpWhenPaneClosed (issue #1795).
func TestUpdate_DetailModalJumpMsgs_NoOpWhenNoModalOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, DetailModalJumpToFirstMsg{})
	if m.DetailModal != nil {
		t.Errorf("DetailModal = %+v, want nil", m.DetailModal)
	}

	m = Update(m, DetailModalJumpToLastMsg{})
	if m.DetailModal != nil {
		t.Errorf("DetailModal = %+v, want nil", m.DetailModal)
	}
}
