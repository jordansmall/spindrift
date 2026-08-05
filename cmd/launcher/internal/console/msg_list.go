package console

import "spindrift.dev/launcher/internal/forge"

// Msg types for the backlog list: loading, filtering, cursor movement, and scrolling.

// IssuesLoadedMsg carries the result of a backlog refresh: the adapter's
// translation of an IssueTracker.ListOpenIssues call into a message Update
// can apply without touching the network itself. Err is set instead of
// Issues when the refresh failed.
type IssuesLoadedMsg struct {
	Issues []forge.Issue
	Err    error
	// RecoverableCount is how many of Issues carry the tracker's Recoverable
	// dispatch-state label — derived from this same ListOpenIssues call, no
	// extra round trip (issue #2255, ADR 0039 slice S4). Zero when the
	// tracker doesn't implement forge.LabeledTracker or leaves Recoverable
	// unmapped (empty label string), never every issue — the same
	// "unmapped state matches everything, so treat as zero" caution
	// issueInState documents.
	RecoverableCount int
}

func (IssuesLoadedMsg) isConsoleMsg() {}

// FilterChangedMsg carries the operator's new label filter text, produced
// by the run loop as the operator types. An empty Filter clears it,
// restoring the full backlog.
type FilterChangedMsg struct {
	Filter string
}

func (FilterChangedMsg) isConsoleMsg() {}

// CursorMoveMsg is the tea layer's signal that the operator pressed a
// navigation key (j/down, or the up arrow — "k" moved to Terminate in
// #785) — Delta is +1 (down) or -1 (up); Update clamps the result into
// Visible()'s bounds (issue #784).
type CursorMoveMsg struct {
	Delta int
}

func (CursorMoveMsg) isConsoleMsg() {}

// CursorJumpToFirstMsg is the tea layer's signal that "gg" completed — moves
// the cursor to the active Section's first row and resets the scroll offset
// to 0, unlike CursorMoveMsg's minimal-drag-into-view (issue #1628).
type CursorJumpToFirstMsg struct{}

func (CursorJumpToFirstMsg) isConsoleMsg() {}

// CursorJumpToLastMsg is the tea layer's signal that "G" was pressed — moves
// the cursor to the active Section's last row, dragging the scroll offset
// just far enough to keep it on screen, the same follow behavior
// CursorMoveMsg uses (issue #1628).
type CursorJumpToLastMsg struct{}

func (CursorJumpToLastMsg) isConsoleMsg() {}

// SectionPrevMsg is the tea layer's signal that the operator pressed "H" —
// switches ActiveSection to the previous Section, wrapping from Backlog to
// Failed (ADR 0030).
type SectionPrevMsg struct{}

func (SectionPrevMsg) isConsoleMsg() {}

// SectionNextMsg is the tea layer's signal that the operator pressed "L" —
// switches ActiveSection to the next Section, wrapping from Failed to
// Backlog (ADR 0030).
type SectionNextMsg struct{}

func (SectionNextMsg) isConsoleMsg() {}

// SectionJumpMsg is the tea layer's signal that the operator pressed a
// direct Section key ("1"-"5") — switches ActiveSection straight to Section,
// regardless of which Section is currently active (ADR 0030).
type SectionJumpMsg struct {
	Section Section
}

func (SectionJumpMsg) isConsoleMsg() {}

// ScrollMsg is the tea layer's signal that the operator pressed a line-scroll
// key while the body is showing (no sidebar focused) — Delta is the number of
// rows to move (positive scrolls down/later, negative scrolls up/earlier).
// It moves Model.Offset within the active Section, clamped the same way
// SidebarScrollMsg clamps Sidebar.Offset (issue #1036, ADR 0030).
type ScrollMsg struct {
	Delta int
}

func (ScrollMsg) isConsoleMsg() {}
