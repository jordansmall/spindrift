package console

// StaleStatusMsg carries the launcher's image-freshness and rebuild state into
// the pure core (issue #1541). Run syncs it per render because the background
// drain, not Update, learns the probe result and a rebuild's outcome (#652).
type StaleStatusMsg struct {
	RebuildStatus RebuildStatus
}

func (StaleStatusMsg) isConsoleMsg() {}

// RebuildOutputOpenMsg opens the rebuild-output pane on "o" (issue #1128). It
// is a no-op while RebuildOutput is "", before any rebuild captures output.
type RebuildOutputOpenMsg struct{}

func (RebuildOutputOpenMsg) isConsoleMsg() {}

// RebuildOutputCloseMsg closes the rebuild-output pane on "x" or Esc (issue #1128).
type RebuildOutputCloseMsg struct{}

func (RebuildOutputCloseMsg) isConsoleMsg() {}

// RebuildOutputScrollMsg scrolls the rebuild-output pane by Delta lines, a
// no-op while the pane is closed (issue #1128).
type RebuildOutputScrollMsg struct {
	Delta int
}

func (RebuildOutputScrollMsg) isConsoleMsg() {}

// RebuildOutputJumpToFirstMsg resets RebuildOutputOffset to 0 on "gg" (issue #1630).
type RebuildOutputJumpToFirstMsg struct{}

func (RebuildOutputJumpToFirstMsg) isConsoleMsg() {}

// RebuildOutputJumpToLastMsg jumps RebuildOutputOffset to the last page on "G" (issue #1630).
type RebuildOutputJumpToLastMsg struct{}

func (RebuildOutputJumpToLastMsg) isConsoleMsg() {}
