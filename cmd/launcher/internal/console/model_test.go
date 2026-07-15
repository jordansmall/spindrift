package console

import (
	"errors"
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

// TestUpdate_DrillInScrollMsg_NoOpWhenNoDrillInOpen verifies scrolling with
// no transcript open does not panic or fabricate a DrillIn state.
func TestUpdate_DrillInScrollMsg_NoOpWhenNoDrillInOpen(t *testing.T) {
	m := NewModel()
	m = Update(m, DrillInScrollMsg{Delta: 1})
	if m.DrillIn != nil {
		t.Errorf("DrillIn = %+v, want nil", m.DrillIn)
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
