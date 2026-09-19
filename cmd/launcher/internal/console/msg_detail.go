package console

// DetailModalOpenMsg opens a Backlog row's fullscreen ticket detail modal
// (issue #1632). It carries what the row already has, so the modal renders
// before openDetailModalCmd's DetailModalLoadedMsg lands.
type DetailModalOpenMsg struct {
	Number, Title string
	Labels        []string
}

func (DetailModalOpenMsg) isConsoleMsg() {}

// DetailModalCloseMsg closes the ticket detail modal and discards its scroll
// position (issue #1632). Model.DetailCache keeps the loaded detail, so
// reopening the same ticket is instant.
type DetailModalCloseMsg struct{}

func (DetailModalCloseMsg) isConsoleMsg() {}

// DetailModalLoadedMsg carries openDetailModalCmd's async result (issue #1744).
type DetailModalLoadedMsg struct {
	// Number guards against a stale load landing after the operator closed
	// the modal or opened a different ticket.
	Number string
	// Body needs its own Issue fetch; the backlog listing never carries it.
	Body string
	// BlockedBy and Blocks come from the ticket's own dependency edge, not
	// from a whole-backlog readiness graph.
	BlockedBy []BlockerRef
	Blocks    []BlockerRef
	// Err is set instead of Body. openDetailModalCmd returns as soon as the
	// Issue fetch errs, so BlockedBy and Blocks are empty whenever Err is
	// non-nil.
	Err error
}

func (DetailModalLoadedMsg) isConsoleMsg() {}

// DetailModalScrollMsg scrolls the open ticket detail modal by Delta lines,
// positive for down (issue #1632). Update clamps the result to the loaded
// content's line bounds and does nothing when no modal is open.
type DetailModalScrollMsg struct {
	Delta int
}

func (DetailModalScrollMsg) isConsoleMsg() {}

// DetailModalJumpToFirstMsg resets DetailModal.Offset to 0 when "gg" completes
// while the ticket detail modal is open (issue #1795).
type DetailModalJumpToFirstMsg struct{}

func (DetailModalJumpToFirstMsg) isConsoleMsg() {}

// DetailModalJumpToLastMsg jumps DetailModal.Offset to the last page when the
// operator presses "G" while the ticket detail modal is open (issue #1795).
type DetailModalJumpToLastMsg struct{}

func (DetailModalJumpToLastMsg) isConsoleMsg() {}

// DetailCacheInvalidatedMsg clears Model.DetailCache so the next ticket detail
// modal open re-fetches (issue #1632; refresh moved from "r" to "R" in issue
// #1839). It fires alongside, not instead of, the refreshCmd "R" already
// triggers.
type DetailCacheInvalidatedMsg struct{}

func (DetailCacheInvalidatedMsg) isConsoleMsg() {}
