package console

// DrillInMsg carries a Dispatch's whole rendered transcript, every pass in order
// with boundaries marked, plus its byte-exact raw form; Err is set instead when
// the load or render failed. openSidebarCmd unwraps it into a SidebarLoadedMsg,
// so it never reaches the tea layer's Update dispatch; it stays a Msg only to
// keep DrillIn's signature unchanged (#1501).
type DrillInMsg struct {
	Number        string
	Rendered, Raw string
	Err           error
}

func (DrillInMsg) isConsoleMsg() {}

// SidebarLoadedMsg carries a Dispatch's Activity feed and its transcript in both
// rendered and raw form, loaded together on select so the Activity/Transcript
// and rendered/raw toggles need no further I/O (#1501). Err is set instead of
// everything else when nothing loaded at all; TranscriptErr is set when only the
// transcript failed, and Activity stays showable because it loads independently.
type SidebarLoadedMsg struct {
	Number, Title string
	Activity      []ActivityLine
	Rendered, Raw string
	Err           error
	TranscriptErr error
	// Notice explains an empty pane without an error: an orphan-flagged
	// Dispatch has no local pass log yet (issue #1621). "" for every other
	// open, including the claimed-but-not-yet-launched race, which keeps its
	// silent-empty contract.
	Notice string
}

func (SidebarLoadedMsg) isConsoleMsg() {}

// SidebarActivityMsg carries the open sidebar's Dispatch's re-derived Activity
// feed, scoped so I/O stays bounded with many Dispatches running (ADR 0030,
// issue #1502). It is a no-op when Number no longer matches the open sidebar,
// which the operator can switch or close in the same Update batch. It arrives
// even while the Transcript shows, keeping the feed cached (issue #1736).
type SidebarActivityMsg struct {
	Number   string
	Activity []ActivityLine
}

func (SidebarActivityMsg) isConsoleMsg() {}

// SidebarTranscriptMsg carries the open sidebar's Dispatch's freshly re-derived
// transcript render, sent only while ShowTranscript is active (issue #1736). A
// no-op when no sidebar is open or Number no longer matches it.
type SidebarTranscriptMsg struct {
	Number        string
	Rendered, Raw string
}

func (SidebarTranscriptMsg) isConsoleMsg() {}

// SidebarToggleMsg advances the sidebar's content one step around its cycle of
// Activity, rendered transcript, raw transcript, so a repeated "t" reaches every
// form without a second key (#1501). A no-op when no sidebar is open.
type SidebarToggleMsg struct{}

func (SidebarToggleMsg) isConsoleMsg() {}

// SidebarCloseMsg leaves the sidebar and returns focus to the list alone (#1501).
type SidebarCloseMsg struct{}

func (SidebarCloseMsg) isConsoleMsg() {}

// SidebarScrollMsg scrolls the focused sidebar by Delta lines, positive for
// later lines. Update clamps the result into the loaded content's line bounds,
// and it is a no-op when no sidebar is open (#1501).
type SidebarScrollMsg struct {
	Delta int
}

func (SidebarScrollMsg) isConsoleMsg() {}

// SidebarJumpToEndMsg ("G"/"End") re-attaches Follow and moves Offset to the
// last line, the way back to live-tailing after a scroll-up detached it (ADR
// 0030, issue #1502). A no-op when no sidebar is open.
type SidebarJumpToEndMsg struct{}

func (SidebarJumpToEndMsg) isConsoleMsg() {}

// SidebarJumpToBeginningMsg (the "gg" leader chord) moves Offset to 0 and
// detaches Follow, the same as a manual scroll-up (issue #1629). A no-op when no
// sidebar is open.
type SidebarJumpToBeginningMsg struct{}

func (SidebarJumpToBeginningMsg) isConsoleMsg() {}

// SidebarZoomToggleMsg ("z") toggles Model.SidebarZoom, forcing the sidebar
// fullscreen or releasing that force back to sidebarFits' own width check,
// independent of the narrow-terminal fallback (ADR 0030, issue #1502).
type SidebarZoomToggleMsg struct{}

func (SidebarZoomToggleMsg) isConsoleMsg() {}

// FocusListMsg ("h"/left) moves keyboard focus to the list, a no-op when it is
// already there (#1501, ADR 0030).
type FocusListMsg struct{}

func (FocusListMsg) isConsoleMsg() {}

// FocusSidebarMsg ("l"/right) moves keyboard focus to the sidebar, a no-op when
// no sidebar is open (#1501, ADR 0030).
type FocusSidebarMsg struct{}

func (FocusSidebarMsg) isConsoleMsg() {}
