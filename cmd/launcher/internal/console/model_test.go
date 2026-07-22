package console

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

var errBoom = errors.New("boom")

// TestNewModel_Empty verifies a freshly constructed Model starts with no
// issues, no filter, and is not quitting — the zero state before the first
// IssuesLoadedMsg or dogfood-notice check arrives.
func TestNewModel_Empty(t *testing.T) {
	m := NewModel()
	if len(m.Visible()) != 0 {
		t.Errorf("Visible() = %v, want none", m.Visible())
	}
	if m.Quitting {
		t.Error("Quitting = true, want false")
	}
}

// TestUpdate_IssuesLoadedMsg_ReplacesAll verifies Update installs the
// refreshed backlog verbatim, in the order the adapter supplied it (oldest
// first per dispatch order is the adapter's responsibility, not Update's).
func TestUpdate_IssuesLoadedMsg_ReplacesAll(t *testing.T) {
	m := NewModel()
	issues := []forge.Issue{{Number: "1", Title: "first"}, {Number: "2", Title: "second"}}

	m = Update(m, IssuesLoadedMsg{Issues: issues})

	if len(m.Visible()) != 2 || m.Visible()[0].Number != "1" || m.Visible()[1].Number != "2" {
		t.Errorf("Visible() = %+v, want %+v", m.Visible(), issues)
	}
}

// TestUpdate_IssuesLoadedMsg_ErrKeepsStaleListAndRecordsErr verifies a
// failed refresh (Err set) leaves the last-good backlog on screen instead of
// blanking it, and records Err for View to surface — a failed refresh must
// never look like an empty backlog.
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

// TestUpdate_FilterChangedMsg_NarrowsAndClearingRestores verifies a label
// filter narrows Visible() to issues carrying a matching label, and setting
// the filter back to "" restores the full backlog — the two acceptance
// criteria ("narrows the list interactively" / "clearing it restores the
// full list") in one round trip.
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

// TestUpdate_CursorMoveMsg_MovesWithinVisibleBounds verifies a positive
// Delta moves the cursor down the visible list and clamps at the last row —
// the backlog cursor never walks past what's on screen (issue #784).
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

// TestUpdate_CursorMoveMsg_ClampsAtZero verifies a negative Delta never
// drives the cursor below the first row.
func TestUpdate_CursorMoveMsg_ClampsAtZero(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1"}, {Number: "2"}}})

	m = Update(m, CursorMoveMsg{Delta: -1})
	if m.Cursor != 0 {
		t.Errorf("Cursor = %d, want clamped at 0", m.Cursor)
	}
}

// TestUpdate_CursorMoveMsg_EmptyVisible_StaysZero verifies an empty backlog
// never leaves the cursor pointing past the (nonexistent) end.
func TestUpdate_CursorMoveMsg_EmptyVisible_StaysZero(t *testing.T) {
	m := NewModel()
	m = Update(m, CursorMoveMsg{Delta: 1})
	if m.Cursor != 0 {
		t.Errorf("Cursor = %d, want 0 with nothing visible", m.Cursor)
	}
}

// TestUpdate_FilterChangedMsg_NarrowingClampsCursor verifies narrowing the
// visible list via a filter pulls a cursor that was pointing past the new,
// shorter list back to its last row.
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

// TestModel_ActiveMode_DefaultsToList verifies a fresh Model's ActiveMode is
// ModeList, modePrecedence's last resort (issue #1543).
func TestModel_ActiveMode_DefaultsToList(t *testing.T) {
	m := NewModel()
	if got := m.ActiveMode(); got != ModeList {
		t.Errorf("ActiveMode() = %v, want ModeList", got)
	}
}

// TestModel_ActiveMode_SidebarBeatsEveryOtherMode verifies a focused sidebar
// owns the keyboard ahead of any Mode value — mirroring the old handleKey
// cascade's own sidebar-first check. Sidebar's ownership is a condition
// derived from Sidebar/Focus/SidebarZoom rather than folded into Mode
// itself (Mode's own doc comment), so a Model can still carry a stale Mode
// alongside an active Sidebar; ActiveMode must still resolve to ModeSidebar
// regardless (issue #1543).
func TestModel_ActiveMode_SidebarBeatsEveryOtherMode(t *testing.T) {
	m := NewModel()
	m.Sidebar = &SidebarState{Number: "42"}
	m.Focus = FocusSidebar
	m.Mode = ModeHelp

	if got := m.ActiveMode(); got != ModeSidebar {
		t.Errorf("ActiveMode() = %v, want ModeSidebar even with Mode = ModeHelp", got)
	}
}

// TestModel_ActiveMode_DockedSidebarDoesNotOwnKeyboard verifies a docked
// (fits, not zoomed, list-focused) sidebar leaves Mode in charge — only a
// focused, fullscreen-fallback, or zoomed sidebar competes for ownership
// (ADR 0030's own sidebarFits/Focus contract, unchanged by issue #1543).
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

// TestUpdate_HelpToggleMsg_TogglesModeHelp verifies "?" opens the help overlay
// and a second "?" closes it (issue #784).
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

// TestUpdate_RebuildOutputOpenMsg_OpensPaneWhenOutputPresent verifies "o"
// opens the rebuild-output pane once a rebuild has captured output — the
// field's only consumer (issue #1128).
func TestUpdate_RebuildOutputOpenMsg_OpensPaneWhenOutputPresent(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "building...\ndone"}})
	m = Update(m, RebuildOutputOpenMsg{})
	if m.Mode != ModeRebuildOutput {
		t.Errorf("Mode = %v after open with output present, want ModeRebuildOutput", m.Mode)
	}
}

// TestUpdate_RebuildOutputOpenMsg_NoOpWhenOutputEmpty verifies the pane never
// opens with nothing to show — no rebuild has run yet.
func TestUpdate_RebuildOutputOpenMsg_NoOpWhenOutputEmpty(t *testing.T) {
	m := NewModel()
	m = Update(m, RebuildOutputOpenMsg{})
	if m.Mode == ModeRebuildOutput {
		t.Error("Mode = ModeRebuildOutput with no RebuildOutput captured, want ModeList")
	}
}

// TestUpdate_RebuildOutputScrollMsg_NoOpWhenPaneClosed verifies scrolling
// with the pane closed does not move RebuildOutputOffset or open it.
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

// TestUpdate_RebuildOutputScrollMsg_MovesOffset verifies a scroll message
// moves RebuildOutputOffset by Delta, clamped into the captured output's
// line bounds the same way SidebarScrollMsg clamps Sidebar.Offset.
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

// TestUpdate_RebuildOutputJumpMsgs_NoOpWhenPaneClosed verifies both jump
// messages leave RebuildOutputOffset untouched while the pane is closed —
// RebuildOutputOpenMsg (case above) already refuses to open the pane while
// RebuildStatus.Output is "", so this is the reachable form of the "no-op
// on empty output" AC for a pane that was never opened (issue #1630 AC4).
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

// TestUpdate_RebuildOutputJumpToFirstMsg_ResetsOffsetToZero verifies "gg"
// resets RebuildOutputOffset to 0 from a scrolled-down position, mirroring
// CursorJumpToFirstMsg's own reset for the list body (issue #1630 AC2).
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

// TestUpdate_RebuildOutputJumpToLastMsg_JumpsToLastPage verifies "G" moves
// RebuildOutputOffset to the last page that still fills the viewport — not
// just the last line — the same page-capped clamp the ModeRebuildOutput
// clamp block already applies on every Update (issue #1630 AC1).
func TestUpdate_RebuildOutputJumpToLastMsg_JumpsToLastPage(t *testing.T) {
	m := NewModel()
	m = Update(m, SizeChangedMsg{Height: 6}) // minus headerFooterLines(2) = a 4-row viewport
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9"}})
	m = Update(m, RebuildOutputOpenMsg{})

	m = Update(m, RebuildOutputJumpToLastMsg{})
	if m.RebuildOutputOffset != 6 {
		t.Errorf("RebuildOutputOffset = %d, want 6 (10 lines, 4-row viewport: last page starts at line 6)", m.RebuildOutputOffset)
	}
}

// TestUpdate_RebuildOutputCloseMsg_ClosesPane verifies close returns Mode to
// ModeList so View falls back to rendering the backlog/queue.
func TestUpdate_RebuildOutputCloseMsg_ClosesPane(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1"}})
	m = Update(m, RebuildOutputOpenMsg{})
	m = Update(m, RebuildOutputCloseMsg{})
	if m.Mode == ModeRebuildOutput {
		t.Error("Mode = ModeRebuildOutput after close, want ModeList")
	}
}

// TestUpdate_StaleStatusMsg_ClosesOpenPaneWhenOutputEmpties verifies a later
// StaleStatusMsg that empties RebuildStatus.Output out from under an
// already-open pane also closes it, rather than leaving Mode at
// ModeRebuildOutput over blank content — the documented rough edge issue
// #1543 retires.
func TestUpdate_StaleStatusMsg_ClosesOpenPaneWhenOutputEmpties(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: "l0\nl1\nl2"}})
	m = Update(m, RebuildOutputOpenMsg{})

	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: ""}})
	if m.Mode == ModeRebuildOutput {
		t.Error("Mode = ModeRebuildOutput after Output emptied, want ModeList (pane auto-closes)")
	}
}

// TestUpdate_FilterEditStartMsg_EntersEditingMode verifies "/" arms
// ModeFilterEdit so the tea layer routes further keystrokes as filter text
// instead of navigation (issue #784).
func TestUpdate_FilterEditStartMsg_EntersEditingMode(t *testing.T) {
	m := NewModel()
	m = Update(m, FilterEditStartMsg{})
	if m.Mode != ModeFilterEdit {
		t.Errorf("Mode = %v after FilterEditStartMsg, want ModeFilterEdit", m.Mode)
	}
}

// TestUpdate_FilterEditConfirmMsg_KeepsFilterExitsEditing verifies Enter
// leaves the already-live-narrowed Filter untouched and just exits editing
// mode.
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

// TestUpdate_FilterEditCancelMsg_RevertsFilterExitsEditing verifies Esc
// restores whatever Filter was active before "/" was pressed, discarding
// the in-progress edit.
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

// TestUpdate_QuitMsg_SetsQuitting verifies QuitMsg is the sole way Quitting
// flips true — the run loop's signal to exit its read loop cleanly.
func TestUpdate_QuitMsg_SetsQuitting(t *testing.T) {
	m := NewModel()
	m = Update(m, QuitMsg{})
	if !m.Quitting {
		t.Error("Quitting = false after QuitMsg, want true")
	}
}

// TestUpdate_DogfoodNoticeMsg_SetsLive verifies DogfoodNoticeMsg{Live: true}
// records that a competing headless loop's pid-file is present, and
// {Live: false} clears it — the startup notice is informational only and
// must never block, so it is just a bit on Model that View can render.
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

// TestFormatActivityLine_RendersTextOnly verifies the Activity feed no
// longer prefixes a record with a rendered timestamp: ActivityFeed's only
// per-record clock is the pass log's on-disk mtime, which advances to ~now
// on every refresh rather than reflecting when the record actually
// happened, so a precise-looking HH:MM:SS prefix would be misleading (#1584).
func TestFormatActivityLine_RendersTextOnly(t *testing.T) {
	got := formatActivityLine(ActivityLine{Text: "#42 · hi"})
	want := "#42 · hi"
	if got != want {
		t.Errorf("formatActivityLine() = %q, want %q (no timestamp prefix)", got, want)
	}
}

// TestUpdate_SidebarLoadedMsg_OpensSidebar_ActivityDefault verifies a
// SidebarLoadedMsg installs the loaded Activity feed and Transcript content
// on Model and focuses the sidebar, with the Activity feed as the default
// view (not the Transcript) — the queue row's live-tail sidebar gesture
// (#648, #1501).
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

// TestUpdate_SidebarLoadedMsg_FollowDefaultsTrueOnOpen verifies a freshly
// opened sidebar (no retained position for this Dispatch yet) starts with
// Follow true — live-tailing is the default the moment a feed opens, not an
// opt-in the operator has to reach for (issue #1502, ADR 0030).
func TestUpdate_SidebarLoadedMsg_FollowDefaultsTrueOnOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})

	if !m.Sidebar.Follow {
		t.Error("Sidebar.Follow = false, want true on a freshly opened sidebar")
	}
}

// TestUpdate_SidebarLoadedMsg_FreshOpenWhileFollowing_StartsAtBottom
// verifies a brand new selection (no retained position at all), still
// following by default, opens at the bottom of the loaded feed rather than
// its top — ADR 0030's "follows the newest line by default" describes any
// opened feed, not only a reopen after a close (review finding on issue
// #1502).
func TestUpdate_SidebarLoadedMsg_FreshOpenWhileFollowing_StartsAtBottom(t *testing.T) {
	activity := make([]ActivityLine, 50)
	for i := range activity {
		activity[i] = ActivityLine{Text: fmt.Sprintf("l%d", i)}
	}

	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: activity})

	want := len(m.Sidebar.Lines) - (20 - headerFooterLines) // last page fills the viewport
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (a fresh open while following starts at the bottom)", m.Sidebar.Offset, want)
	}
}

// TestUpdate_SidebarLoadedMsg_CachesLineSplit verifies SidebarLoadedMsg
// pre-splits the active (Activity, by default) form into Sidebar.Lines once,
// so clampSidebarOffset and the render functions consume the cache instead
// of each re-splitting the full content on every Update/View call (issue
// #722, inherited from DrillInState.Lines).
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

// TestUpdate_SidebarToggleMsg_CyclesActivityTranscriptRaw verifies "t"
// advances the sidebar around its three-step cycle — Activity feed ->
// Transcript (rendered) -> Transcript (raw) -> Activity feed — so the
// byte-exact raw form stays reachable without a second key (#1501).
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

// TestUpdate_SidebarToggleMsg_BackToActivityWhileFollowing_SnapsToBottom
// verifies cycling the sidebar back to the Activity feed (the third "t") re-
// snaps Offset to the bottom when Follow is true — the Transcript view's own
// Offset (wherever that view's own scrolling or clamp left it) must not leak
// into the Activity view and read as "following" while showing non-bottom
// content (review finding on issue #1502).
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

	m = Update(m, SidebarToggleMsg{}) // -> Transcript (rendered), Offset clamps to 0 (short content)
	m = Update(m, SidebarToggleMsg{}) // -> Transcript (raw)
	m = Update(m, SidebarToggleMsg{}) // -> back to Activity

	if m.Sidebar.ShowTranscript {
		t.Fatal("test setup: three toggles must land back on the Activity feed")
	}
	want := len(m.Sidebar.Lines) - (20 - headerFooterLines) // last page fills the viewport
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (following, snapped back to the Activity feed's own bottom)", m.Sidebar.Offset, want)
	}
}

// TestUpdate_SidebarToggleMsg_NoOpWhenNoSidebarOpen verifies toggling with no
// sidebar open does not panic or fabricate a Sidebar state.
func TestUpdate_SidebarToggleMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarToggleMsg{})
	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil", m.Sidebar)
	}
}

// TestUpdate_SidebarCloseMsg_ReturnsToBacklog verifies close clears the
// sidebar state and returns focus to the list, so View falls back to
// rendering the backlog/queue alone.
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

// TestUpdate_SidebarScrollMsg_MovesOffset verifies a scroll message moves
// Sidebar.Offset by Delta, clamped into the loaded content's line bounds so
// paging past either end leaves the pane showing its first or last line
// instead of an invalid Offset (issue #786, inherited). No SizeChangedMsg is
// sent, so Height stays 0 and the final pgdown assertion below exercises the
// degenerate-height fallback, not the viewport-aware clamp added in #829 —
// see TestUpdate_SidebarScrollMsg_ClampsToViewportHeight and
// TestUpdate_SidebarScrollMsg_ShortTranscriptStaysAtTop for that. The
// Transcript (rendered) view is toggled on so Lines matches the plain
// "l0".."l4" content the old drill-in test exercised.
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

// TestUpdate_SidebarScrollMsg_ClampsToViewportHeight verifies a pgdown past
// the end of a transcript longer than the fullscreen viewport lands Offset
// at the last full page, not len(Lines)-1 — a large Delta must never leave
// the pane showing a single line with the rest of the viewport blank
// (issue #829, inherited).
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

	want := 100 - (20 - 2) // last page fills the 18-line fullscreen budget
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (last page fills viewport)", m.Sidebar.Offset, want)
	}
}

// TestUpdate_SidebarScrollMsg_DockedClampsToBodyBudgetNotFullHeight verifies
// a pgdown past the end of a transcript, docked beside the list (wide
// enough for sidebarFits), lands Offset against bodyBudget(m) — the same
// row budget renderSidebarDocked actually renders into — rather than the
// whole terminal Height, which the header/banner/tabs eat into (#1501
// review finding).
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
	want := 100 - (budget - 2)
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (last page fills the docked body budget, not the full terminal height)", m.Sidebar.Offset, want)
	}
}

// TestUpdate_SidebarScrollMsg_ZoomedClampsToFullHeightNotBodyBudget verifies
// a pgdown past the end of a transcript, zoomed on a terminal wide enough to
// dock, lands Offset against the whole terminal Height — the row budget
// renderSidebarFullscreen actually renders into once SidebarZoom forces it —
// not bodyBudget(m), which only applies to the docked render the operator
// zoomed away from (review finding on issue #1502).
func TestUpdate_SidebarScrollMsg_ZoomedClampsToFullHeightNotBodyBudget(t *testing.T) {
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
	if budget >= 20 {
		t.Fatalf("test setup: bodyBudget(m) = %d, want it under Height (20) — the docked/zoomed budgets must actually differ to distinguish them", budget)
	}
	want := 100 - (20 - headerFooterLines) // last page fills the full terminal height, not the docked budget
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (last page fills the zoomed fullscreen's full height, not the docked body budget)", m.Sidebar.Offset, want)
	}
}

// TestUpdate_SidebarScrollMsg_ShortTranscriptStaysAtTop verifies a pgdown
// past the end of a transcript shorter than the fullscreen viewport lands
// Offset at 0, not len(Lines)-1 — content that already fits the viewport in
// full at Offset 0 must never get pushed to a higher Offset that shows only
// its last line over an otherwise-blank pane (issue #829, inherited).
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

// TestUpdate_SidebarScrollMsg_ScrollUpDetachesFollow verifies scrolling up
// (a negative Delta) detaches Follow, so the operator can review frozen
// history while the Dispatch keeps working without the feed yanking them
// back to the bottom (issue #1502, ADR 0030).
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

// TestUpdate_SidebarScrollMsg_ScrollDownDoesNotDetachFollow verifies
// scrolling down (a positive Delta) leaves Follow untouched — only scrolling
// up (reviewing history) detaches it (issue #1502, ADR 0030).
func TestUpdate_SidebarScrollMsg_ScrollDownDoesNotDetachFollow(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3\nl4"})
	m = Update(m, SidebarToggleMsg{})

	m = Update(m, SidebarScrollMsg{Delta: 1})

	if !m.Sidebar.Follow {
		t.Error("Follow = false, want true — a downward scroll must not detach it")
	}
}

// TestUpdate_SidebarScrollMsg_NoOpWhenNoSidebarOpen verifies scrolling with
// no sidebar open does not panic or fabricate a Sidebar state.
func TestUpdate_SidebarScrollMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarScrollMsg{Delta: 1})
	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil", m.Sidebar)
	}
}

// TestUpdate_SidebarJumpToEndMsg_ReattachesFollowAndJumpsToBottom verifies
// G/End re-attaches Follow and moves Offset to the last line, the operator's
// way back to live-tailing after scrolling up to review history (issue
// #1502, ADR 0030).
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

// TestUpdate_SidebarJumpToEndMsg_NoOpWhenNoSidebarOpen verifies G/End with no
// sidebar open does not panic or fabricate a Sidebar state.
func TestUpdate_SidebarJumpToEndMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarJumpToEndMsg{})
	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil", m.Sidebar)
	}
}

// TestUpdate_SidebarJumpToBeginningMsg_DetachesFollowAndJumpsToTop verifies
// gg moves Offset to 0 and detaches Follow, the same way scrolling up with
// "k" does — the operator parks at the start of the buffer (issue #1629).
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

// TestUpdate_SidebarJumpToBeginningMsg_NoOpWhenNoSidebarOpen verifies gg with
// no sidebar open does not panic or fabricate a Sidebar state.
func TestUpdate_SidebarJumpToBeginningMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarJumpToBeginningMsg{})
	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil", m.Sidebar)
	}
}

// TestUpdate_SidebarActivityMsg_UpdatesActivityAndLines verifies a
// SidebarActivityMsg for the open sidebar's own Number installs the
// refreshed Activity feed and recomputes Lines — syncQueue's per-Msg live
// advance (issue #1502, ADR 0030).
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

// TestUpdate_SidebarActivityMsg_FollowSnapsToBottom verifies growth of the
// Activity feed, while Follow is true, moves Offset to show the newest
// line — the live-tail default (issue #1502, ADR 0030).
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

	want := len(m.Sidebar.Lines) - (20 - headerFooterLines) // last page fills the viewport (issue #829's convention)
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (the last page, following the growth)", m.Sidebar.Offset, want)
	}
}

// TestUpdate_SidebarActivityMsg_PreservesOffsetWhenNotFollowing verifies
// growth of the Activity feed, while Follow is false (detached by an earlier
// scroll-up), leaves Offset where the operator left it instead of yanking
// them back to the bottom (issue #1502, ADR 0030).
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

// TestUpdate_SidebarActivityMsg_UnchangedContentPreservesManualOffset
// verifies a refresh that carries the exact same Activity feed as before
// (syncQueue's per-Msg re-derive against an unchanged on-disk log — most
// calls, between actual writes) leaves Offset alone even while Follow is
// still true — a positive-Delta scroll (pgdown) moves Offset without
// detaching Follow, and a same-content refresh right afterward must not
// yank it back to the bottom (issue #1502).
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

// TestUpdate_SidebarActivityMsg_PassRolloverUpdatesLinesEvenWhenShorter
// verifies a refresh whose feed is SHORTER than what's currently shown still
// updates Lines and (while Follow is true) snaps to the new bottom — a
// Dispatch rolling from its initial run onto a fix pass gets a fresh,
// shorter pass log (LogPaths/ActivityFeed key on only the latest pass), so
// gating the refresh on "grew" alone would leave the sidebar frozen on the
// finished pass's stale lines while still labeled following (review finding
// on issue #1502).
func TestUpdate_SidebarActivityMsg_PassRolloverUpdatesLinesEvenWhenShorter(t *testing.T) {
	initial := make([]ActivityLine, 20)
	for i := range initial {
		initial[i] = ActivityLine{Text: fmt.Sprintf("pass0-l%d", i)}
	}
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: initial})

	// A fresh fix-pass log starts over — shorter than the finished initial
	// pass, and with entirely different content.
	nextPass := []ActivityLine{{Text: "pass1-l0"}}
	m = Update(m, SidebarActivityMsg{Number: "42", Activity: nextPass})

	if len(m.Sidebar.Lines) != 1 || !strings.Contains(m.Sidebar.Lines[0], "pass1-l0") {
		t.Errorf("Lines = %v, want the new (shorter) pass's content, not the stale finished pass", m.Sidebar.Lines)
	}
	if m.Sidebar.Offset != 0 {
		t.Errorf("Offset = %d, want 0 (following, snapped to the new pass's own bottom)", m.Sidebar.Offset)
	}
}

// TestUpdate_SidebarActivityMsg_NoOpWhenNumberMismatch verifies a
// SidebarActivityMsg for a Dispatch other than the one the sidebar has open
// is dropped — a stale in-flight refresh racing a Dispatch switch must never
// clobber the newly selected Dispatch's feed (issue #1502).
func TestUpdate_SidebarActivityMsg_NoOpWhenNumberMismatch(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "l0"}}})

	m = Update(m, SidebarActivityMsg{Number: "43", Activity: []ActivityLine{{Text: "l0"}, {Text: "l1"}}})

	if len(m.Sidebar.Activity) != 1 {
		t.Errorf("Activity = %v, want the original single entry, untouched by a mismatched Number", m.Sidebar.Activity)
	}
}

// TestUpdate_SidebarActivityMsg_NoOpWhenNoSidebarOpen verifies a
// SidebarActivityMsg with no sidebar open does not panic or fabricate a
// Sidebar state.
func TestUpdate_SidebarActivityMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarActivityMsg{Number: "42", Activity: []ActivityLine{{Text: "l0"}}})
	if m.Sidebar != nil {
		t.Errorf("Sidebar = %+v, want nil", m.Sidebar)
	}
}

// TestUpdate_SidebarActivityMsg_DoesNotOverwriteTranscriptLines verifies a
// SidebarActivityMsg arriving while the sidebar shows the Transcript updates
// the stored Activity feed (so toggling back to it later reflects the
// growth) but leaves Lines showing the Transcript untouched — the refresh
// must never yank the operator's Transcript view back to Activity content
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

// TestUpdate_SidebarTranscriptMsg_UpdatesTranscriptAndLinesWhileShowing
// verifies a SidebarTranscriptMsg for the open sidebar's own Number installs
// the refreshed Transcript render and recomputes Lines while ShowTranscript
// is true — the Transcript's own live-tail advance, the counterpart to
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

// TestUpdate_SidebarTranscriptMsg_FollowSnapsToBottom verifies growth of the
// Transcript, while Follow is true and ShowTranscript is active, moves
// Offset to show the newest line — the live-tail default already true of
// the Activity feed (issue #1502), extended to the Transcript view (#1736
// AC2: "honours follow").
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

	want := len(m.Sidebar.Lines) - (20 - headerFooterLines) // last page fills the viewport (issue #829's convention)
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (the last page, following the growth)", m.Sidebar.Offset, want)
	}
}

// TestUpdate_SidebarTranscriptMsg_NoOpWhenNumberMismatch verifies a
// SidebarTranscriptMsg for a Dispatch other than the one the sidebar has
// open is dropped — a stale in-flight refresh racing a Dispatch switch must
// never clobber the newly selected Dispatch's Transcript (mirrors
// SidebarActivityMsg's own guard, issue #1736).
func TestUpdate_SidebarTranscriptMsg_NoOpWhenNumberMismatch(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: "l0"})
	m = Update(m, SidebarToggleMsg{})

	m = Update(m, SidebarTranscriptMsg{Number: "43", Rendered: "l0\nl1"})

	if m.Sidebar.TranscriptRendered != "l0" {
		t.Errorf("TranscriptRendered = %q, want the original, untouched by a mismatched Number", m.Sidebar.TranscriptRendered)
	}
}

// TestUpdate_FocusSidebarMsg_MovesFocus verifies "l"/right moves focus to an
// open sidebar (#1501, ADR 0030).
func TestUpdate_FocusSidebarMsg_MovesFocus(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42"})
	m = Update(m, FocusListMsg{}) // opening already focused the sidebar; force it back to the list first
	m = Update(m, FocusSidebarMsg{})
	if m.Focus != FocusSidebar {
		t.Errorf("Focus = %v, want FocusSidebar", m.Focus)
	}
}

// TestUpdate_FocusSidebarMsg_NoOpWhenNoSidebarOpen verifies "l"/right does
// not move focus into a sidebar that isn't open.
func TestUpdate_FocusSidebarMsg_NoOpWhenNoSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, FocusSidebarMsg{})
	if m.Focus != FocusList {
		t.Errorf("Focus = %v, want FocusList (no sidebar to focus)", m.Focus)
	}
}

// TestUpdate_FocusListMsg_MovesFocus verifies "h"/left returns focus to the
// list while a sidebar is open and focused (#1501, ADR 0030).
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

// TestUpdate_ScrollMsg_MovesOffset verifies a scroll message moves
// Model.Offset by Delta while the Backlog Section is active, clamped into
// the visible list's line bounds the same way SidebarScrollMsg clamps
// Sidebar.Offset (issue #1036, ADR 0030).
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

// TestUpdate_CursorJumpToFirstMsg_ResetsCursorAndOffset verifies "gg" moves
// the cursor back to the first row and resets the scroll offset to 0, even
// when both had scrolled well past the top (issue #1628 AC2).
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

// TestUpdate_CursorJumpToLastMsg_MovesCursorAndDragsOffsetIntoView verifies
// "G" moves the cursor straight to the active Section's last row and drags
// the scroll offset just far enough to keep it on screen (issue #1628 AC1).
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

// TestUpdate_GPendingMsg_ArmsPendingG verifies a lone "g" arms the pending-g
// leader on the Model, mirroring ModePick's own arm/resolve toggle (issue
// #1628 AC3).
func TestUpdate_GPendingMsg_ArmsPendingG(t *testing.T) {
	m := NewModel()

	m = Update(m, GPendingMsg{})

	if !m.PendingG {
		t.Error("PendingG = false, want true after GPendingMsg")
	}
}

// TestUpdate_GResolvedMsg_ClearsPendingG verifies GResolvedMsg — sent when
// the chord completes, cancels, or times out — clears the pending-g leader
// (issue #1628 AC4).
func TestUpdate_GResolvedMsg_ClearsPendingG(t *testing.T) {
	m := NewModel()
	m = Update(m, GPendingMsg{})

	m = Update(m, GResolvedMsg{})

	if m.PendingG {
		t.Error("PendingG = true, want false after GResolvedMsg")
	}
}

// TestUpdate_CursorJump_EmptySection_NoOp verifies both "G" and "gg" are
// no-ops against an empty active Section — neither leaves Cursor/Offset
// pointing past the (nonexistent) end (issue #1628 AC6).
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

// TestUpdate_ScrollMsg_MovesOffsetWithinActiveWorkSection verifies a scroll
// message moves Model.Offset against whichever work Section is active, not
// the Backlog — switching Sections resets Offset to 0 (issue #1500), so a
// scroll sent afterward must move that fresh 0, not a stale Backlog offset
// (ADR 0030).
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

// TestUpdate_ScrollMsg_OffsetScrollsPastEndWhenContentFitsOnScreen verifies
// pgdown's behavior when the active Section's whole content already fits
// within one screen (issue #1060): the offset still advances to the last row
// instead of no-op'ing, scrolling the earlier, already-fully-visible rows
// off screen.
func TestUpdate_ScrollMsg_OffsetScrollsPastEndWhenContentFitsOnScreen(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	issues := make([]forge.Issue, 3)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	delta := sectionPageSize(m)
	if delta != len(issues) {
		t.Fatalf("sectionPageSize = %d, want %d (test setup: all issues must fit within one screen)", delta, len(issues))
	}

	m = Update(m, ScrollMsg{Delta: delta})
	if m.Offset != len(issues)-1 {
		t.Errorf("Offset = %d, want %d (pgdown scrolls to the last row even though every row already fit on screen)", m.Offset, len(issues)-1)
	}
}

// TestUpdate_CursorMoveMsg_OffsetFollowsCursor verifies the active Section's
// viewport advances/rewinds by one as the cursor crosses its bottom/top
// visible row, keeping the highlighted row always on screen (issue #1036
// AC1). visibleRows is derived from sectionPageSize, the same
// bodyBudget/columnItemBudget/Viewport.Window composition renderBody uses
// (issue #1056), so a future geometry change doesn't require re-deriving a
// hand-computed constant — this test drives the cursor across two screens'
// worth of rows and back to exercise both directions.
func TestUpdate_CursorMoveMsg_OffsetFollowsCursor(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	visibleRows := sectionPageSize(m)

	// Rows 0..visibleRows-1 are visible at offset 0; moving down within that
	// window must not scroll.
	for step := 1; step <= visibleRows-1; step++ {
		m = Update(m, CursorMoveMsg{Delta: 1})
		if m.Offset != 0 {
			t.Fatalf("after %d down-moves: Offset = %d, want 0 (cursor %d still on screen)", step, m.Offset, m.Cursor)
		}
	}

	// Cursor now at the last visible row; one more down-move pushes it past
	// the bottom, advancing the offset by exactly one.
	m = Update(m, CursorMoveMsg{Delta: 1})
	if m.Cursor != visibleRows || m.Offset != 1 {
		t.Fatalf("Cursor = %d, Offset = %d, want Cursor %d, Offset 1 (scrolled to keep the cursor visible)", m.Cursor, m.Offset, visibleRows)
	}

	m = Update(m, CursorMoveMsg{Delta: 1})
	if m.Cursor != visibleRows+1 || m.Offset != 2 {
		t.Fatalf("Cursor = %d, Offset = %d, want Cursor %d, Offset 2 (offset advances one more)", m.Cursor, m.Offset, visibleRows+1)
	}

	// Moving back up: the offset must not rewind while the cursor is still
	// within the current window (offset 2, visibleRows rows tall), including
	// its own top row.
	for step := 1; step <= visibleRows-1; step++ {
		m = Update(m, CursorMoveMsg{Delta: -1})
		if m.Offset != 2 {
			t.Fatalf("after %d up-moves: Offset = %d, want 2 (cursor %d still on screen)", step, m.Offset, m.Cursor)
		}
	}
	// The 2 here (and the 1s below) count how many times the window has
	// crossed a boundary, not a row position derived from visibleRows — they
	// hold regardless of geometry, unlike the visibleRows-derived values
	// above.
	if m.Cursor != 2 {
		t.Fatalf("Cursor = %d, want 2", m.Cursor)
	}

	// Cursor now at row 2, the window's top row; one more up-move pushes it
	// above the top, rewinding the offset by exactly one.
	m = Update(m, CursorMoveMsg{Delta: -1})
	if m.Cursor != 1 || m.Offset != 1 {
		t.Fatalf("Cursor = %d, Offset = %d, want Cursor 1, Offset 1 (scrolled up to keep the cursor visible)", m.Cursor, m.Offset)
	}

	m = Update(m, CursorMoveMsg{Delta: -100})
	if m.Cursor != 0 || m.Offset != 0 {
		t.Fatalf("Cursor = %d, Offset = %d, want Cursor 0, Offset 0 (clamped to the top)", m.Cursor, m.Offset)
	}
}

// TestUpdate_CursorMoveMsg_DoesNotRecapOffsetLeftPastFoldByScroll verifies a
// CursorMoveMsg never re-applies the sidebar/rebuild-output panes'
// last-page-fills-the-viewport cap to the backlog/queue Offset. pgup/pgdown
// deliberately leaves Offset non-page-capped (issue #1060, tracked
// separately as #1053); a later cursor move that doesn't itself need to
// scroll must not silently pull a scroll-inflated Offset back toward
// Viewport.SetHeight's clamp-on-shrink (issue #1540 review finding).
func TestUpdate_CursorMoveMsg_DoesNotRecapOffsetLeftPastFoldByScroll(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 100)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	// Drive the cursor to the end so follow settles Offset at its own
	// natural resting point.
	m = Update(m, CursorMoveMsg{Delta: 1000})
	settled := m.Offset
	if settled <= 0 || settled >= 99 {
		t.Fatalf("test setup: Offset settled at %d, want a real intermediate resting point", settled)
	}

	// pgdown past that resting point — issue #1060's deliberately
	// non-page-capped scroll — landing one row further than follow alone
	// ever would.
	m = Update(m, ScrollMsg{Delta: 1})
	if m.Offset != settled+1 {
		t.Fatalf("test setup: Offset = %d, want %d after nudging one row past the follow rest point", m.Offset, settled+1)
	}

	// The cursor (still at the end, still comfortably inside the nudged
	// window) doesn't need to move; the resulting CursorMoveMsg must leave
	// Offset alone rather than re-capping it back to the follow rest point.
	m = Update(m, CursorMoveMsg{Delta: 0})
	if m.Offset != settled+1 {
		t.Errorf("Offset = %d, want %d (unchanged — pgdown's uncapped overshoot must survive a cursor move that doesn't need to scroll)", m.Offset, settled+1)
	}
}

// TestUpdate_FilterChangedMsg_ClampsOffsetOnShrink verifies a filter that
// narrows the backlog pulls a scroll offset now past the shrunken list's end
// back into range, so a subsequent render never windows past what Visible()
// actually holds (issue #1036 AC — offset clamped when the list shrinks).
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

// TestUpdate_StaleStatusMsg_SetsFields verifies StaleStatusMsg installs the
// launcher's live freshness/rebuild state onto Model verbatim — the
// per-render sync View's stale banner reads from (issue #652).
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

// TestUpdate_OrphanRecoveryMsg_SetsErr verifies OrphanRecoveryMsg installs
// its Err onto Model.OrphanRecoveryErr verbatim — the per-render sync View's
// orphan-recovery banner reads from (issue #1218).
func TestUpdate_OrphanRecoveryMsg_SetsErr(t *testing.T) {
	m := NewModel()
	m = Update(m, OrphanRecoveryMsg{Err: "failed to adopt orphan #42: boom"})

	if m.OrphanRecoveryErr != "failed to adopt orphan #42: boom" {
		t.Errorf("OrphanRecoveryErr = %q, want %q", m.OrphanRecoveryErr, "failed to adopt orphan #42: boom")
	}
}

// TestUpdate_OrphanDetectedMsg_FlagsIsOrphan verifies OrphanDetectedMsg
// installs the reported issue numbers so IsOrphan reports them flagged,
// leaving every other number unflagged — Update's detect-only half of
// #1619's demotion (startup only ever detects now, never adopts).
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

// TestUpdate_OrphanAdoptedMsg_ClearsIsOrphan verifies a successful adopt
// clears the issue's orphan flag — leaving it set would let a second press
// of the adopt gesture on the same, now-adopted row fire RecoverFn again,
// racing a second same-process settle over the one PR the first adopt
// already claimed (issue #1619 review finding).
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

// TestUpdate_StaleStatusMsg_PropagatesCapturedRebuildOutput verifies a
// non-empty StaleStatusMsg.RebuildStatus.Output lands on
// Model.RebuildStatus.Output verbatim — the sibling
// TestUpdate_StaleStatusMsg_SetsFields only ever threads the zero value,
// which never exercised this leg (issue #1129).
func TestUpdate_StaleStatusMsg_PropagatesCapturedRebuildOutput(t *testing.T) {
	m := NewModel()
	const wantOutput = "nix: building '/nix/store/abc-spindrift-1.2.3.drv'...\n"
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Output: wantOutput}})

	if m.RebuildStatus.Output != wantOutput {
		t.Errorf("Output = %q, want %q", m.RebuildStatus.Output, wantOutput)
	}
}

// TestUpdate_SidebarLoadedMsg_RefreshSameNumber_PreservesToggleState verifies
// a second SidebarLoadedMsg for the same pick (a refresh while live-tailing)
// keeps the operator's Activity/Transcript/raw toggle instead of resetting
// to the Activity feed, and does not re-yank focus away from wherever the
// operator moved it.
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

// TestUpdate_SidebarLoadedMsg_RetainsPositionAcrossDispatchSwitch verifies
// scrolling and detaching Follow on one Dispatch's sidebar, then switching to
// another Dispatch and back, restores exactly where the operator left the
// first one — hopping between running Dispatches never loses their place
// (issue #1502, ADR 0030).
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

// TestUpdate_SidebarLoadedMsg_ReopenWhileFollowing_SnapsToNewBottom verifies
// reopening a Dispatch that was closed while still Follow-ing lands at the
// freshly loaded content's actual bottom, not the stale Offset saved before
// close — the Dispatch kept working while the sidebar was shut, so "still
// following" must mean "at today's bottom," not "wherever it happened to be
// last time" (review finding on issue #1502).
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

	want := len(m.Sidebar.Lines) - (20 - headerFooterLines) // last page fills the viewport
	if m.Sidebar.Offset != want {
		t.Errorf("Offset = %d, want %d (the new bottom, not the stale %d saved before close)", m.Sidebar.Offset, want, staleOffset)
	}
}

// TestUpdate_SidebarCloseMsg_RetainsPositionForReopen verifies closing the
// sidebar after scrolling and detaching Follow, then reopening the same
// Dispatch, restores that position rather than resetting to the top with
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

// TestUpdate_SidebarZoomToggleMsg_TogglesZoom verifies "z" flips
// Model.SidebarZoom on and back off — the fullscreen zoom toggle for deep
// reading, independent of the narrow-terminal fallback (issue #1502, ADR
// 0030).
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

// TestUpdate_SidebarCloseMsg_ResetsZoom verifies closing the sidebar clears
// SidebarZoom, so reopening a different (or the same) Dispatch on a wide
// terminal starts docked rather than still forced fullscreen from a prior
// session (issue #1502).
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

// TestUpdate_SectionNextMsg_ClosesSidebarWhenOpen verifies switching Sections
// with SectionNextMsg while a sidebar is docked and focus already back on
// the list (the common state after "h") dismisses the sidebar instead of
// leaving it pinned to the old Dispatch under the new Section's list (issue
// #1581).
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

// TestUpdate_SectionPrevMsg_ClosesSidebarWhenOpen mirrors
// TestUpdate_SectionNextMsg_ClosesSidebarWhenOpen for the "H" direction
// (issue #1581).
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

// TestUpdate_SectionJumpMsg_ClosesSidebarWhenOpen mirrors
// TestUpdate_SectionNextMsg_ClosesSidebarWhenOpen for a direct "1"-"5" jump,
// and additionally verifies SidebarZoom resets and the position is saved so
// a later reopen restores scroll/follow (issue #1581).
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

// TestUpdate_SectionJumpMsg_SameSectionLeavesSidebarOpen verifies jumping to
// the Section that's already active does not close a Sidebar the operator
// just opened, consistent with switchSection's existing same-section guard
// on Cursor/Offset (issue #1581).
func TestUpdate_SectionJumpMsg_SameSectionLeavesSidebarOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "hi"}}})
	m = Update(m, FocusListMsg{})

	m = Update(m, SectionJumpMsg{Section: m.ActiveSection})

	if m.Sidebar == nil {
		t.Error("Sidebar = nil, want unchanged (jumped to the already-active Section)")
	}
}

// TestUpdate_SizeChangedMsg_AppliesWidthHeight verifies a SizeChangedMsg
// lands its Width/Height straight onto Model (issue #842).
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

// TestUpdate_SizeChangedMsg_ClampsNonPositive verifies a zero or negative
// width/height clamps to the safe floor instead of landing on Model
// unchanged (issue #842).
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

// TestUpdate_DetailModalLoadedMsg_FillsBodyAndClearsLoading verifies the
// async body fetch's result lands on the still-open modal: Loading drops to
// false and Body is filled in, once DetailModalOpenMsg has already opened it
// with just the number/title/labels a Backlog row has in hand (issue #1632).
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

// TestUpdate_DetailModalLoadedMsg_StaleNumberIgnored verifies a
// DetailModalLoadedMsg for a ticket the operator has since closed (or
// switched away from, to a different ticket) never overwrites whatever the
// modal shows now — the same same-number guard SidebarLoadedMsg's handling
// applies (issue #1632).
func TestUpdate_DetailModalLoadedMsg_StaleNumberIgnored(t *testing.T) {
	m := NewModel()
	m = Update(m, DetailModalOpenMsg{Number: "42", Title: "fix the thing"})
	m = Update(m, DetailModalCloseMsg{})

	m = Update(m, DetailModalLoadedMsg{Number: "42", Body: "arrives after close"})

	if m.DetailModal != nil {
		t.Errorf("DetailModal = %+v after close, want nil — a stale load must not reopen it", m.DetailModal)
	}
}

// TestUpdate_DetailCacheInvalidatedMsg_ClearsCache verifies "r"
// (DetailCacheInvalidatedMsg) drops the per-ticket detail cache, so the
// next modal open re-fetches rather than replaying data that may now be
// stale (issue #1632).
func TestUpdate_DetailCacheInvalidatedMsg_ClearsCache(t *testing.T) {
	m := NewModel()
	m.DetailCache = map[string]DetailModalCache{"42": {Body: "stale"}}

	m = Update(m, DetailCacheInvalidatedMsg{})

	if m.DetailCache != nil {
		t.Errorf("DetailCache = %v after DetailCacheInvalidatedMsg, want nil", m.DetailCache)
	}
}

// TestUpdate_SizeChangedMsg_RewrapsOpenDetailModal verifies a terminal
// resize while the ticket detail modal is open re-wraps its body against
// the new width — Lines is width-dependent (unlike SidebarState.Lines,
// which never wraps), so a stale narrower-width wrap left in place would
// either overflow the new, wider viewport's unused columns or, worse, still
// carry line breaks sized for a width the modal no longer has (issue
// #1632 review finding).
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
