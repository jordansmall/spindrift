package console

import "spindrift.dev/launcher/internal/forge"

// IssuesLoadedMsg carries the result of a backlog refresh. Err is set instead
// of Issues when the refresh failed.
type IssuesLoadedMsg struct {
	Issues []forge.Issue
	Err    error
	// RecoverableCount is how many of Issues carry the tracker's Recoverable
	// dispatch-state label, derived from the same ListOpenIssues call with no
	// extra round trip (issue #2255, ADR 0039 slice S4). It is zero, never
	// every issue, when the tracker is not a forge.LabeledTracker or leaves
	// Recoverable unmapped, the same caution issueInState documents.
	RecoverableCount int
}

func (IssuesLoadedMsg) isConsoleMsg() {}

// FilterChangedMsg carries the operator's new label filter text; an empty
// Filter restores the full backlog.
type FilterChangedMsg struct {
	Filter string
}

func (FilterChangedMsg) isConsoleMsg() {}

// CursorMoveMsg moves the cursor one row: Delta is +1 (down) or -1 (up), and
// Update clamps the result into Visible()'s bounds (issue #784). "k" is not a
// source of it; that key moved to Terminate in #785.
type CursorMoveMsg struct {
	Delta int
}

func (CursorMoveMsg) isConsoleMsg() {}

// CursorJumpToFirstMsg moves the cursor to the active Section's first row and
// resets the scroll offset to 0, unlike CursorMoveMsg's minimal drag into view
// (issue #1628).
type CursorJumpToFirstMsg struct{}

func (CursorJumpToFirstMsg) isConsoleMsg() {}

// CursorJumpToLastMsg moves the cursor to the active Section's last row,
// dragging the scroll offset just far enough to keep it on screen (issue #1628).
type CursorJumpToLastMsg struct{}

func (CursorJumpToLastMsg) isConsoleMsg() {}

// SectionPrevMsg switches ActiveSection to the previous Section, wrapping from
// Backlog to Failed (ADR 0030).
type SectionPrevMsg struct{}

func (SectionPrevMsg) isConsoleMsg() {}

// SectionNextMsg switches ActiveSection to the next Section, wrapping from
// Failed to Backlog (ADR 0030).
type SectionNextMsg struct{}

func (SectionNextMsg) isConsoleMsg() {}

// SectionJumpMsg switches ActiveSection straight to Section, regardless of
// which Section is currently active (ADR 0030).
type SectionJumpMsg struct {
	Section Section
}

func (SectionJumpMsg) isConsoleMsg() {}

// ScrollMsg moves Model.Offset within the active Section by Delta rows
// (positive scrolls down), clamped the same way SidebarScrollMsg clamps
// Sidebar.Offset (issue #1036, ADR 0030).
type ScrollMsg struct {
	Delta int
}

func (ScrollMsg) isConsoleMsg() {}
