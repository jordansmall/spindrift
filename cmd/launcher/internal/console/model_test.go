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

// TestUpdate_HelpToggleMsg_FlipsShowHelp verifies "?" opens the help overlay
// and a second "?" closes it (issue #784).
func TestUpdate_HelpToggleMsg_FlipsShowHelp(t *testing.T) {
	m := NewModel()
	m = Update(m, HelpToggleMsg{})
	if !m.ShowHelp {
		t.Error("ShowHelp = false after one toggle, want true")
	}

	m = Update(m, HelpToggleMsg{})
	if m.ShowHelp {
		t.Error("ShowHelp = true after two toggles, want false")
	}
}

// TestUpdate_FilterEditStartMsg_EntersEditingMode verifies "/" arms
// FilterEditing so the tea layer routes further keystrokes as filter text
// instead of navigation (issue #784).
func TestUpdate_FilterEditStartMsg_EntersEditingMode(t *testing.T) {
	m := NewModel()
	m = Update(m, FilterEditStartMsg{})
	if !m.FilterEditing {
		t.Error("FilterEditing = false after FilterEditStartMsg, want true")
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
	if m.FilterEditing {
		t.Error("FilterEditing = true after confirm, want false")
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
	if m.FilterEditing {
		t.Error("FilterEditing = true after cancel, want false")
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

// TestUpdate_DrillInMsg_OpensTranscriptView verifies a DrillInMsg installs
// the drill-in's rendered and raw content on Model, so View can render the
// transcript instead of the backlog — the queue row's drill-in gesture
// (#648).
func TestUpdate_DrillInMsg_OpensTranscriptView(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInMsg{Number: "42", Rendered: "[implementor] hi\n", Raw: `{"type":"assistant"}` + "\n"})

	if m.DrillIn == nil {
		t.Fatal("DrillIn = nil, want non-nil after DrillInMsg")
	}
	if m.DrillIn.Number != "42" {
		t.Errorf("DrillIn.Number = %q, want %q", m.DrillIn.Number, "42")
	}
	if m.DrillIn.Rendered != "[implementor] hi\n" {
		t.Errorf("DrillIn.Rendered = %q, want the rendered transcript", m.DrillIn.Rendered)
	}
	if m.DrillIn.ShowRaw {
		t.Error("DrillIn.ShowRaw = true, want false (rendered is the default view)")
	}
}

// TestUpdate_DrillInMsg_CachesLineSplit verifies DrillInMsg pre-splits the
// active (rendered) form into DrillIn.Lines once, so clampDrillInOffset and
// the render functions consume the cache instead of each re-splitting the
// full content on every Update/View call (issue #722).
func TestUpdate_DrillInMsg_CachesLineSplit(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInMsg{Number: "42", Rendered: "l0\nl1\nl2", Raw: "r0\nr1"})

	want := []string{"l0", "l1", "l2"}
	if len(m.DrillIn.Lines) != len(want) {
		t.Fatalf("Lines = %v, want %v", m.DrillIn.Lines, want)
	}
	for i, line := range want {
		if m.DrillIn.Lines[i] != line {
			t.Errorf("Lines[%d] = %q, want %q", i, m.DrillIn.Lines[i], line)
		}
	}
}

// TestUpdate_DrillInToggleMsg_FlipsShowRaw verifies the toggle switches
// between rendered and raw with no I/O — both forms are already loaded on
// Model from the DrillInMsg that opened the view.
func TestUpdate_DrillInToggleMsg_FlipsShowRaw(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInMsg{Number: "42", Rendered: "rendered", Raw: "raw"})

	m = Update(m, DrillInToggleMsg{})
	if !m.DrillIn.ShowRaw {
		t.Error("ShowRaw = false after one toggle, want true")
	}

	m = Update(m, DrillInToggleMsg{})
	if m.DrillIn.ShowRaw {
		t.Error("ShowRaw = true after two toggles, want false")
	}
}

// TestUpdate_DrillInToggleMsg_RecachesLineSplit verifies toggling ShowRaw
// recomputes DrillIn.Lines against the newly active form instead of leaving
// it split against the form that was active before the toggle (issue #722).
func TestUpdate_DrillInToggleMsg_RecachesLineSplit(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInMsg{Number: "42", Rendered: "l0\nl1\nl2", Raw: "r0\nr1"})

	m = Update(m, DrillInToggleMsg{})

	want := []string{"r0", "r1"}
	if len(m.DrillIn.Lines) != len(want) {
		t.Fatalf("Lines = %v, want %v", m.DrillIn.Lines, want)
	}
	for i, line := range want {
		if m.DrillIn.Lines[i] != line {
			t.Errorf("Lines[%d] = %q, want %q", i, m.DrillIn.Lines[i], line)
		}
	}
}

// TestUpdate_DrillInToggleMsg_NoOpWhenNoDrillInOpen verifies toggling with
// no transcript open does not panic or fabricate a DrillIn state.
func TestUpdate_DrillInToggleMsg_NoOpWhenNoDrillInOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInToggleMsg{})
	if m.DrillIn != nil {
		t.Errorf("DrillIn = %+v, want nil", m.DrillIn)
	}
}

// TestUpdate_DrillInCloseMsg_ReturnsToBacklog verifies close clears the
// drill-in state so View falls back to rendering the backlog/queue.
func TestUpdate_DrillInCloseMsg_ReturnsToBacklog(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInMsg{Number: "42", Rendered: "rendered"})
	m = Update(m, DrillInCloseMsg{})
	if m.DrillIn != nil {
		t.Errorf("DrillIn = %+v, want nil after close", m.DrillIn)
	}
}

// TestUpdate_DrillInScrollMsg_MovesOffset verifies a scroll message moves
// DrillIn.Offset by Delta, clamped into the loaded content's line bounds so
// paging past either end leaves the pane showing its first or last line
// instead of an invalid Offset (issue #786).
func TestUpdate_DrillInScrollMsg_MovesOffset(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3\nl4"})

	m = Update(m, DrillInScrollMsg{Delta: 2})
	if m.DrillIn.Offset != 2 {
		t.Errorf("Offset = %d, want 2", m.DrillIn.Offset)
	}

	m = Update(m, DrillInScrollMsg{Delta: -1})
	if m.DrillIn.Offset != 1 {
		t.Errorf("Offset = %d, want 1", m.DrillIn.Offset)
	}

	m = Update(m, DrillInScrollMsg{Delta: -100})
	if m.DrillIn.Offset != 0 {
		t.Errorf("Offset = %d, want 0 (clamped at the top)", m.DrillIn.Offset)
	}

	m = Update(m, DrillInScrollMsg{Delta: 100})
	if m.DrillIn.Offset != 4 {
		t.Errorf("Offset = %d, want 4 (clamped to the last line)", m.DrillIn.Offset)
	}
}

// TestUpdate_DrillInScrollMsg_ClampsToViewportHeight verifies a pgdown past
// the end of a transcript longer than the fullscreen viewport lands Offset
// at the last full page, not len(Lines)-1 — a large Delta must never leave
// the pane showing a single line with the rest of the viewport blank
// (issue #829).
func TestUpdate_DrillInScrollMsg_ClampsToViewportHeight(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, DrillInMsg{Number: "42", Rendered: strings.Join(lines, "\n")})

	m = Update(m, DrillInScrollMsg{Delta: 1000})

	want := 100 - (20 - 2) // last page fills the 18-line fullscreen budget
	if m.DrillIn.Offset != want {
		t.Errorf("Offset = %d, want %d (last page fills viewport)", m.DrillIn.Offset, want)
	}
}

// TestUpdate_DrillInScrollMsg_ShortTranscriptStaysAtTop verifies a pgdown
// past the end of a transcript shorter than the fullscreen viewport lands
// Offset at 0, not len(Lines)-1 — content that already fits the viewport in
// full at Offset 0 must never get pushed to a higher Offset that shows only
// its last line over an otherwise-blank pane (issue #829).
func TestUpdate_DrillInScrollMsg_ShortTranscriptStaysAtTop(t *testing.T) {
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 20})
	m = Update(m, DrillInMsg{Number: "42", Rendered: "l0\nl1\nl2\nl3\nl4"})

	m = Update(m, DrillInScrollMsg{Delta: 1000})

	if m.DrillIn.Offset != 0 {
		t.Errorf("Offset = %d, want 0 (content already fits the viewport)", m.DrillIn.Offset)
	}
}

// TestUpdate_DrillInScrollMsg_NoOpWhenNoDrillInOpen verifies scrolling with
// no transcript open does not panic or fabricate a DrillIn state.
func TestUpdate_DrillInScrollMsg_NoOpWhenNoDrillInOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInScrollMsg{Delta: 1})
	if m.DrillIn != nil {
		t.Errorf("DrillIn = %+v, want nil", m.DrillIn)
	}
}

// TestUpdate_ScrollMsg_MovesBacklogOffset verifies a scroll message moves
// Model.BacklogOffset by Delta while the backlog is focused, clamped into
// the visible list's line bounds the same way DrillInScrollMsg clamps
// DrillIn.Offset (issue #1036).
func TestUpdate_ScrollMsg_MovesBacklogOffset(t *testing.T) {
	m := NewModel()
	issues := make([]forge.Issue, 5)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	m = Update(m, ScrollMsg{Delta: 2})
	if m.BacklogOffset != 2 {
		t.Errorf("BacklogOffset = %d, want 2", m.BacklogOffset)
	}

	m = Update(m, ScrollMsg{Delta: -1})
	if m.BacklogOffset != 1 {
		t.Errorf("BacklogOffset = %d, want 1", m.BacklogOffset)
	}

	m = Update(m, ScrollMsg{Delta: -100})
	if m.BacklogOffset != 0 {
		t.Errorf("BacklogOffset = %d, want 0 (clamped at the top)", m.BacklogOffset)
	}

	m = Update(m, ScrollMsg{Delta: 100})
	if m.BacklogOffset != 4 {
		t.Errorf("BacklogOffset = %d, want 4 (clamped to the last row)", m.BacklogOffset)
	}
}

// TestUpdate_ScrollMsg_MovesQueueOffsetWhenQueueFocused verifies a scroll
// message moves Model.QueueOffset instead when the work-queue column has
// focus, and clamps into range when the picks queue shrinks (issue #1036).
func TestUpdate_ScrollMsg_MovesQueueOffsetWhenQueueFocused(t *testing.T) {
	m := NewModel()
	m = Update(m, FocusToggleMsg{})
	picks := make([]Pick, 5)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m.Picks = picks

	m = Update(m, ScrollMsg{Delta: 3})
	if m.QueueOffset != 3 {
		t.Errorf("QueueOffset = %d, want 3", m.QueueOffset)
	}
	if m.BacklogOffset != 0 {
		t.Errorf("BacklogOffset = %d, want 0 (queue focused, backlog untouched)", m.BacklogOffset)
	}

	m.Picks = picks[:1]
	m = Update(m, ScrollMsg{Delta: 0})
	if m.QueueOffset != 0 {
		t.Errorf("QueueOffset = %d, want 0 (clamped after the queue shrank)", m.QueueOffset)
	}
}

// TestUpdate_ScrollMsg_BacklogOffsetScrollsPastEndWhenContentFitsOnScreen
// verifies pgdown's behavior when the whole backlog already fits within one
// screen (issue #1060): the offset still advances to the last row instead of
// no-op'ing, scrolling the earlier, already-fully-visible rows off screen.
func TestUpdate_ScrollMsg_BacklogOffsetScrollsPastEndWhenContentFitsOnScreen(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	issues := make([]forge.Issue, 3)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	delta := focusedPageSize(m)
	if delta != len(issues) {
		t.Fatalf("focusedPageSize = %d, want %d (test setup: all issues must fit within one screen)", delta, len(issues))
	}

	m = Update(m, ScrollMsg{Delta: delta})
	if m.BacklogOffset != len(issues)-1 {
		t.Errorf("BacklogOffset = %d, want %d (pgdown scrolls to the last row even though every row already fit on screen)", m.BacklogOffset, len(issues)-1)
	}
}

// TestUpdate_ScrollMsg_QueueOffsetScrollsPastEndWhenContentFitsOnScreen
// mirrors TestUpdate_ScrollMsg_BacklogOffsetScrollsPastEndWhenContentFitsOnScreen
// for the picks queue column (issue #1060).
func TestUpdate_ScrollMsg_QueueOffsetScrollsPastEndWhenContentFitsOnScreen(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, FocusToggleMsg{})
	picks := make([]Pick, 3)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m.Picks = picks

	delta := focusedPageSize(m)
	if delta != len(picks) {
		t.Fatalf("focusedPageSize = %d, want %d (test setup: all picks must fit within one screen)", delta, len(picks))
	}

	m = Update(m, ScrollMsg{Delta: delta})
	if m.QueueOffset != len(picks)-1 {
		t.Errorf("QueueOffset = %d, want %d (pgdown scrolls to the last row even though every pick already fit on screen)", m.QueueOffset, len(picks)-1)
	}
}

// TestUpdate_CursorMoveMsg_BacklogOffsetFollowsCursor verifies the backlog
// viewport advances/rewinds by one as the cursor crosses its bottom/top
// visible row, keeping the highlighted row always on screen (issue #1036
// AC1). visibleRows is derived from the same bodyColumnBudgets /
// columnItemBudget / windowedRowCount composition renderBody uses (issue
// #1056), so a future geometry change doesn't require re-deriving a
// hand-computed constant — this test drives the cursor across two screens'
// worth of rows and back to exercise both directions.
func TestUpdate_CursorMoveMsg_BacklogOffsetFollowsCursor(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	issues := make([]forge.Issue, 50)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})

	backlogBudget, _ := bodyColumnBudgets(m)
	visibleRows := windowedRowCount(len(issues), columnItemBudget(backlogBudget))

	// Rows 0..visibleRows-1 are visible at offset 0; moving down within that
	// window must not scroll.
	for step := 1; step <= visibleRows-1; step++ {
		m = Update(m, CursorMoveMsg{Delta: 1})
		if m.BacklogOffset != 0 {
			t.Fatalf("after %d down-moves: BacklogOffset = %d, want 0 (cursor %d still on screen)", step, m.BacklogOffset, m.Cursor)
		}
	}

	// Cursor now at the last visible row; one more down-move pushes it past
	// the bottom, advancing the offset by exactly one.
	m = Update(m, CursorMoveMsg{Delta: 1})
	if m.Cursor != visibleRows || m.BacklogOffset != 1 {
		t.Fatalf("Cursor = %d, BacklogOffset = %d, want Cursor %d, BacklogOffset 1 (scrolled to keep the cursor visible)", m.Cursor, m.BacklogOffset, visibleRows)
	}

	m = Update(m, CursorMoveMsg{Delta: 1})
	if m.Cursor != visibleRows+1 || m.BacklogOffset != 2 {
		t.Fatalf("Cursor = %d, BacklogOffset = %d, want Cursor %d, BacklogOffset 2 (offset advances one more)", m.Cursor, m.BacklogOffset, visibleRows+1)
	}

	// Moving back up: the offset must not rewind while the cursor is still
	// within the current window (offset 2, visibleRows rows tall), including
	// its own top row.
	for step := 1; step <= visibleRows-1; step++ {
		m = Update(m, CursorMoveMsg{Delta: -1})
		if m.BacklogOffset != 2 {
			t.Fatalf("after %d up-moves: BacklogOffset = %d, want 2 (cursor %d still on screen)", step, m.BacklogOffset, m.Cursor)
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
	if m.Cursor != 1 || m.BacklogOffset != 1 {
		t.Fatalf("Cursor = %d, BacklogOffset = %d, want Cursor 1, BacklogOffset 1 (scrolled up to keep the cursor visible)", m.Cursor, m.BacklogOffset)
	}

	m = Update(m, CursorMoveMsg{Delta: -100})
	if m.Cursor != 0 || m.BacklogOffset != 0 {
		t.Fatalf("Cursor = %d, BacklogOffset = %d, want Cursor 0, BacklogOffset 0 (clamped to the top)", m.Cursor, m.BacklogOffset)
	}
}

// TestUpdate_CursorMoveMsg_QueueOffsetFollowsCursorWhenQueueFocused verifies
// the work-queue column's viewport follows QueueCursor the same way the
// backlog column's follows Cursor — cursor-follows-viewport covers whichever
// column Tab has focused (issue #1036 AC1/AC3). visibleRows is derived the
// same way as TestUpdate_CursorMoveMsg_BacklogOffsetFollowsCursor's, from
// the queue side of bodyColumnBudgets rather than the backlog side (issue
// #1056).
func TestUpdate_CursorMoveMsg_QueueOffsetFollowsCursorWhenQueueFocused(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 10})
	m = Update(m, FocusToggleMsg{})
	picks := make([]Pick, 50)
	for i := range picks {
		picks[i] = Pick{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("pick %d", i), State: PickQueued}
	}
	m.Picks = picks

	_, queueBudget := bodyColumnBudgets(m)
	visibleRows := windowedRowCount(len(picks), columnItemBudget(queueBudget))

	for step := 1; step <= visibleRows-1; step++ {
		m = Update(m, CursorMoveMsg{Delta: 1})
		if m.QueueOffset != 0 {
			t.Fatalf("after %d down-moves: QueueOffset = %d, want 0 (cursor %d still on screen)", step, m.QueueOffset, m.QueueCursor)
		}
	}

	m = Update(m, CursorMoveMsg{Delta: 1})
	if m.QueueCursor != visibleRows || m.QueueOffset != 1 {
		t.Fatalf("QueueCursor = %d, QueueOffset = %d, want QueueCursor %d, QueueOffset 1 (scrolled to keep the cursor visible)", m.QueueCursor, m.QueueOffset, visibleRows)
	}
	if m.BacklogOffset != 0 {
		t.Errorf("BacklogOffset = %d, want 0 (queue focused, backlog untouched)", m.BacklogOffset)
	}
}

// TestUpdate_FilterChangedMsg_ClampsBacklogOffsetOnShrink verifies a filter
// that narrows the backlog pulls a scroll offset now past the shrunken
// list's end back into range, so a subsequent render never windows past what
// Visible() actually holds (issue #1036 AC — offset clamped when the list
// shrinks).
func TestUpdate_FilterChangedMsg_ClampsBacklogOffsetOnShrink(t *testing.T) {
	m := NewModel()
	issues := make([]forge.Issue, 10)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i), Labels: []string{"keep"}}
	}
	issues[0].Labels = []string{"only-match"}
	m = Update(m, IssuesLoadedMsg{Issues: issues})
	m = Update(m, ScrollMsg{Delta: 8})
	if m.BacklogOffset != 8 {
		t.Fatalf("BacklogOffset = %d, want 8 before filtering", m.BacklogOffset)
	}

	m = Update(m, FilterChangedMsg{Filter: "only-match"})

	if len(m.Visible()) != 1 {
		t.Fatalf("Visible() = %d issues, want 1 after filtering", len(m.Visible()))
	}
	if m.BacklogOffset != 0 {
		t.Errorf("BacklogOffset = %d, want 0 (clamped after the filtered list shrank to 1 row)", m.BacklogOffset)
	}
}

// TestUpdate_StaleStatusMsg_SetsFields verifies StaleStatusMsg installs the
// launcher's live freshness/rebuild state onto Model verbatim — the
// per-render sync View's stale banner reads from (issue #652).
func TestUpdate_StaleStatusMsg_SetsFields(t *testing.T) {
	m := NewModel()
	m = Update(m, StaleStatusMsg{Stale: true, Message: "rebuild needed", Rebuilding: true, RebuildErr: "boom"})

	if !m.Stale {
		t.Error("Stale = false, want true")
	}
	if m.StaleMessage != "rebuild needed" {
		t.Errorf("StaleMessage = %q, want %q", m.StaleMessage, "rebuild needed")
	}
	if !m.Rebuilding {
		t.Error("Rebuilding = false, want true")
	}
	if m.RebuildErr != "boom" {
		t.Errorf("RebuildErr = %q, want %q", m.RebuildErr, "boom")
	}
}

// TestUpdate_DrillInMsg_RefreshSameNumber_PreservesShowRaw verifies a
// second DrillInMsg for the same pick (a refresh while live-tailing) keeps
// the operator's raw/rendered toggle instead of resetting to rendered.
func TestUpdate_DrillInMsg_RefreshSameNumber_PreservesShowRaw(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInMsg{Number: "42", Rendered: "first"})
	m = Update(m, DrillInToggleMsg{})

	m = Update(m, DrillInMsg{Number: "42", Rendered: "second (grew)"})
	if !m.DrillIn.ShowRaw {
		t.Error("ShowRaw reset to false on refresh, want it preserved as true")
	}
	if m.DrillIn.Rendered != "second (grew)" {
		t.Errorf("Rendered = %q, want the refreshed content", m.DrillIn.Rendered)
	}
}

// TestUpdate_DrillInMsg_NewNumber_ResetsPaneMode verifies a DrillInMsg for a
// different Number resets PaneMode to PaneDocked even after the operator
// cycled the previous drill-in to PaneFullscreen (issue #999, #846 AC1).
func TestUpdate_DrillInMsg_NewNumber_ResetsPaneMode(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInMsg{Number: "42", Rendered: "first"})
	m = Update(m, PaneModeCycleMsg{})
	m = Update(m, PaneModeCycleMsg{})
	if m.PaneMode != PaneFullscreen {
		t.Fatalf("PaneMode = %v, want PaneFullscreen before the new drill-in", m.PaneMode)
	}

	m = Update(m, DrillInMsg{Number: "43", Rendered: "other"})
	if m.PaneMode != PaneDocked {
		t.Errorf("PaneMode = %v, want PaneDocked reset on a new Number", m.PaneMode)
	}
}

// TestUpdate_DrillInMsg_RefreshSameNumber_PreservesPaneMode verifies a
// second DrillInMsg for the same pick (a refresh while live-tailing) leaves
// PaneMode untouched instead of resetting it to PaneDocked (issue #999).
func TestUpdate_DrillInMsg_RefreshSameNumber_PreservesPaneMode(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInMsg{Number: "42", Rendered: "first"})
	m = Update(m, PaneModeCycleMsg{})
	if m.PaneMode != PaneFloating {
		t.Fatalf("PaneMode = %v, want PaneFloating before the refresh", m.PaneMode)
	}

	m = Update(m, DrillInMsg{Number: "42", Rendered: "second (grew)"})
	if m.PaneMode != PaneFloating {
		t.Errorf("PaneMode = %v, want PaneFloating preserved on same-Number refresh", m.PaneMode)
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

// TestUpdate_FocusToggleMsg_SwitchesColumn verifies Tab's message flips
// Focus between the backlog and work-queue columns, starting on the backlog
// (the zero value) — the two-column focus split (issue #845).
func TestUpdate_FocusToggleMsg_SwitchesColumn(t *testing.T) {
	m := NewModel()
	if m.Focus != FocusBacklog {
		t.Errorf("Focus = %v, want FocusBacklog as the zero value", m.Focus)
	}

	m = Update(m, FocusToggleMsg{})
	if m.Focus != FocusQueue {
		t.Errorf("Focus = %v, want FocusQueue after one toggle", m.Focus)
	}

	m = Update(m, FocusToggleMsg{})
	if m.Focus != FocusBacklog {
		t.Errorf("Focus = %v, want FocusBacklog after a second toggle", m.Focus)
	}
}

// TestUpdate_CursorMoveMsg_RoutesToFocusedColumnOnly verifies a cursor move
// while Focus is FocusQueue moves QueueCursor and leaves the backlog's
// Cursor untouched — each column owns an independent cursor (issue #845).
func TestUpdate_CursorMoveMsg_RoutesToFocusedColumnOnly(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1"}, {Number: "2"}}})
	m.Picks = []Pick{{Number: "10"}, {Number: "11"}}

	m = Update(m, FocusToggleMsg{})
	m = Update(m, CursorMoveMsg{Delta: 1})

	if m.QueueCursor != 1 {
		t.Errorf("QueueCursor = %d, want 1 after one down-move while focused on the queue", m.QueueCursor)
	}
	if m.Cursor != 0 {
		t.Errorf("Cursor = %d, want unchanged at 0 while focused on the queue", m.Cursor)
	}
}

// TestUpdate_CursorMoveMsg_QueueCursorClampsToPicksLength verifies the
// work-queue cursor clamps into [0, len(Picks)-1], mirroring the backlog
// cursor's own clamp against Visible() (issue #845).
func TestUpdate_CursorMoveMsg_QueueCursorClampsToPicksLength(t *testing.T) {
	m := NewModel()
	m.Picks = []Pick{{Number: "10"}}
	m = Update(m, FocusToggleMsg{})

	m = Update(m, CursorMoveMsg{Delta: 5})
	if m.QueueCursor != 0 {
		t.Errorf("QueueCursor = %d, want clamped at 0 (single-row queue)", m.QueueCursor)
	}

	m = Update(m, CursorMoveMsg{Delta: -5})
	if m.QueueCursor != 0 {
		t.Errorf("QueueCursor = %d, want clamped at 0", m.QueueCursor)
	}
}

// TestUpdate_PaneModeCycleMsg_AdvancesDockedFloatingFullscreenDocked verifies
// the pane-mode key's message steps PaneMode through the fixed cycle
// docked -> floating -> fullscreen -> docked, only while a drill-in is open
// (issue #846, ADR 0025).
func TestUpdate_PaneModeCycleMsg_AdvancesDockedFloatingFullscreenDocked(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInMsg{Number: "42", Rendered: "hi"})
	if m.PaneMode != PaneDocked {
		t.Errorf("PaneMode = %v, want PaneDocked as the zero value", m.PaneMode)
	}

	m = Update(m, PaneModeCycleMsg{})
	if m.PaneMode != PaneFloating {
		t.Errorf("PaneMode = %v, want PaneFloating after one cycle", m.PaneMode)
	}

	m = Update(m, PaneModeCycleMsg{})
	if m.PaneMode != PaneFullscreen {
		t.Errorf("PaneMode = %v, want PaneFullscreen after two cycles", m.PaneMode)
	}

	m = Update(m, PaneModeCycleMsg{})
	if m.PaneMode != PaneDocked {
		t.Errorf("PaneMode = %v, want PaneDocked after three cycles (wraps)", m.PaneMode)
	}
}

// TestUpdate_PaneModeCycleMsg_NoOpWhenNoDrillInOpen verifies cycling with no
// transcript open leaves PaneMode untouched (issue #846).
func TestUpdate_PaneModeCycleMsg_NoOpWhenNoDrillInOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, PaneModeCycleMsg{})
	if m.PaneMode != PaneDocked {
		t.Errorf("PaneMode = %v, want PaneDocked (unchanged, no drill-in open)", m.PaneMode)
	}
}

// TestFocusedCursor_ReturnsBacklogCursorWhenBacklogFocused verifies
// focusedCursor points at Model.Cursor when the backlog column has focus —
// the zero-value Focus, matching FocusBacklog (issue #1062).
func TestFocusedCursor_ReturnsBacklogCursorWhenBacklogFocused(t *testing.T) {
	m := NewModel()
	m.Cursor = 3
	m.QueueCursor = 7

	got := focusedCursor(&m)
	if got != &m.Cursor {
		t.Errorf("focusedCursor = &%d, want &m.Cursor (%d)", *got, m.Cursor)
	}
}

// TestFocusedCursor_ReturnsQueueCursorWhenQueueFocused verifies focusedCursor
// points at Model.QueueCursor instead once Tab has moved focus to the
// work-queue column (issue #1062).
func TestFocusedCursor_ReturnsQueueCursorWhenQueueFocused(t *testing.T) {
	m := NewModel()
	m.Focus = FocusQueue
	m.Cursor = 3
	m.QueueCursor = 7

	got := focusedCursor(&m)
	if got != &m.QueueCursor {
		t.Errorf("focusedCursor = &%d, want &m.QueueCursor (%d)", *got, m.QueueCursor)
	}
}

// TestFocusedOffset_ReturnsBacklogOffsetWhenBacklogFocused verifies
// focusedOffset points at Model.BacklogOffset when the backlog column has
// focus (issue #1062).
func TestFocusedOffset_ReturnsBacklogOffsetWhenBacklogFocused(t *testing.T) {
	m := NewModel()
	m.BacklogOffset = 3
	m.QueueOffset = 7

	got := focusedOffset(&m)
	if got != &m.BacklogOffset {
		t.Errorf("focusedOffset = &%d, want &m.BacklogOffset (%d)", *got, m.BacklogOffset)
	}
}

// TestFocusedOffset_ReturnsQueueOffsetWhenQueueFocused verifies focusedOffset
// points at Model.QueueOffset instead once Tab has moved focus to the
// work-queue column (issue #1062).
func TestFocusedOffset_ReturnsQueueOffsetWhenQueueFocused(t *testing.T) {
	m := NewModel()
	m.Focus = FocusQueue
	m.BacklogOffset = 3
	m.QueueOffset = 7

	got := focusedOffset(&m)
	if got != &m.QueueOffset {
		t.Errorf("focusedOffset = &%d, want &m.QueueOffset (%d)", *got, m.QueueOffset)
	}
}

// TestFocusedTotal_ReturnsVisibleCountWhenBacklogFocused verifies
// focusedTotal returns len(m.Visible()) while the backlog column has focus
// (issue #1062).
func TestFocusedTotal_ReturnsVisibleCountWhenBacklogFocused(t *testing.T) {
	m := NewModel()
	m.All = []forge.Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}
	m.Picks = []Pick{{Number: "9", State: PickQueued}}

	if got, want := focusedTotal(m), 3; got != want {
		t.Errorf("focusedTotal = %d, want %d (len(Visible()))", got, want)
	}
}

// TestFocusedTotal_ReturnsPicksCountWhenQueueFocused verifies focusedTotal
// returns len(m.Picks) instead once Tab has moved focus to the work-queue
// column (issue #1062).
func TestFocusedTotal_ReturnsPicksCountWhenQueueFocused(t *testing.T) {
	m := NewModel()
	m.Focus = FocusQueue
	m.All = []forge.Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}
	m.Picks = []Pick{{Number: "9", State: PickQueued}}

	if got, want := focusedTotal(m), 1; got != want {
		t.Errorf("focusedTotal = %d, want %d (len(Picks))", got, want)
	}
}
