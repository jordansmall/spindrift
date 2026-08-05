package console

// Msg types for the ticket detail modal.

// DetailModalOpenMsg is the tea layer's signal that Enter opened a Backlog
// row's fullscreen ticket detail modal — carries exactly what the row
// already has in hand (number, title, labels), so the modal shows something
// useful the instant it opens, before the async body/blocker fetch
// (openDetailModalCmd) lands its own DetailModalLoadedMsg (issue #1632).
type DetailModalOpenMsg struct {
	Number, Title string
	Labels        []string
}

func (DetailModalOpenMsg) isConsoleMsg() {}

// DetailModalCloseMsg is the tea layer's signal that the operator pressed
// Esc while the ticket detail modal is open — closes it, discarding its
// scroll position (issue #1632). The loaded detail itself survives in
// Model.DetailCache, so reopening the same ticket is instant.
type DetailModalCloseMsg struct{}

func (DetailModalCloseMsg) isConsoleMsg() {}

// DetailModalLoadedMsg carries openDetailModalCmd's async result: the
// ticket's full body (a separate Issue fetch, since the backlog listing
// never carries it) plus its Blocked-by and Blocks lists, each resolved
// directly from the ticket's own dependency edge rather than a
// whole-backlog readiness graph (issue #1744). Number gates against a
// stale load landing after the operator closed the modal or opened a
// different ticket, mirroring SidebarLoadedMsg's own same-number guard.
// Err is set instead of Body when the Issue fetch itself failed —
// openDetailModalCmd returns as soon as that call errs, before ever
// resolving BlockedBy/Blocks, so both are empty alongside a non-nil Err;
// renderDetailModal's error branch reflects that by showing the failure in
// place of everything else rather than a partial render.
type DetailModalLoadedMsg struct {
	Number    string
	Body      string
	BlockedBy []BlockerRef
	Blocks    []BlockerRef
	Err       error
}

func (DetailModalLoadedMsg) isConsoleMsg() {}

// DetailModalScrollMsg is the tea layer's signal that the operator pressed
// a scroll key while the ticket detail modal is open — Delta is the number
// of lines to move (positive scrolls down/later, negative scrolls up/
// earlier); Update clamps the result into the loaded content's line bounds,
// a no-op when no modal is open (issue #1632).
type DetailModalScrollMsg struct {
	Delta int
}

func (DetailModalScrollMsg) isConsoleMsg() {}

// DetailModalJumpToFirstMsg is the tea layer's signal that "gg" completed
// while the ticket detail modal is open — resets DetailModal.Offset to 0,
// reusing the g-leader chord CursorJumpToFirstMsg introduced for the list
// body rather than duplicating it (issue #1795).
type DetailModalJumpToFirstMsg struct{}

func (DetailModalJumpToFirstMsg) isConsoleMsg() {}

// DetailModalJumpToLastMsg is the tea layer's signal that "G" was pressed
// while the ticket detail modal is open — jumps DetailModal.Offset to the
// last page, mirroring RebuildOutputJumpToLastMsg (issue #1795).
type DetailModalJumpToLastMsg struct{}

func (DetailModalJumpToLastMsg) isConsoleMsg() {}

// DetailCacheInvalidatedMsg is the tea layer's signal that the operator
// pressed "R" — clears Model.DetailCache, so a later ticket detail modal
// open re-fetches fresh data instead of replaying data "R" was meant to
// refresh (issue #1632; refresh moved from "r" to "R" in issue #1839).
// Fired alongside, not instead of, the ordinary refreshCmd "R" already
// triggers.
type DetailCacheInvalidatedMsg struct{}

func (DetailCacheInvalidatedMsg) isConsoleMsg() {}
