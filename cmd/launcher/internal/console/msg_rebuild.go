package console

// Msg types for the image-freshness/rebuild state and its output pane.

// StaleStatusMsg carries the launcher's live image-freshness/rebuild state
// into the pure core — Run's per-render sync, alongside QueueSnapshotMsg,
// since the background drain (not Update) is what learns the probe result
// and a rebuild's outcome (issue #652). One RebuildStatus value replaces
// the six scalar fields this used to carry (issue #1541).
type StaleStatusMsg struct {
	RebuildStatus RebuildStatus
}

func (StaleStatusMsg) isConsoleMsg() {}

// RebuildOutputOpenMsg is the tea layer's signal that the operator pressed
// "o" — opens the rebuild-output pane, RebuildOutput's only consumer; a
// no-op while RebuildOutput is "" (no rebuild has captured output yet),
// issue #1128.
type RebuildOutputOpenMsg struct{}

func (RebuildOutputOpenMsg) isConsoleMsg() {}

// RebuildOutputCloseMsg is the tea layer's signal that the operator pressed
// "x"/Esc while the rebuild-output pane is open — closes it, returning to
// the backlog/queue view (issue #1128).
type RebuildOutputCloseMsg struct{}

func (RebuildOutputCloseMsg) isConsoleMsg() {}

// RebuildOutputScrollMsg is the tea layer's signal that the operator pressed
// a scroll key while the rebuild-output pane is open — Delta is the number
// of lines to move, a no-op while the pane is closed (issue #1128).
type RebuildOutputScrollMsg struct {
	Delta int
}

func (RebuildOutputScrollMsg) isConsoleMsg() {}

// RebuildOutputJumpToFirstMsg is the tea layer's signal that "gg" completed
// in the rebuild-output pane — resets RebuildOutputOffset to 0, reusing the
// g-leader chord CursorJumpToFirstMsg introduced for the list body rather
// than duplicating it (issue #1630).
type RebuildOutputJumpToFirstMsg struct{}

func (RebuildOutputJumpToFirstMsg) isConsoleMsg() {}

// RebuildOutputJumpToLastMsg is the tea layer's signal that "G" was pressed
// in the rebuild-output pane — jumps RebuildOutputOffset to the last page,
// mirroring CursorJumpToLastMsg for the list body (issue #1630).
type RebuildOutputJumpToLastMsg struct{}

func (RebuildOutputJumpToLastMsg) isConsoleMsg() {}
