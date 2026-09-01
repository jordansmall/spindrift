package console

// Msg types for the Dispatch sidebar: load, activity/transcript toggle,
// scroll, and focus. The keypress-driven ones below are the tea layer's
// signals to Update; each is a no-op when no sidebar is open.

// DrillInMsg carries a Dispatch's whole rendered transcript — every pass
// concatenated in order, with pass boundaries marked — plus its byte-exact
// raw form, loaded together so the raw toggle needs no further I/O. Err is
// set instead when loading or rendering failed (e.g. no Driver configured,
// no logs on disk yet). It never reaches Update: openSidebarCmd unwraps it
// internally to build a SidebarLoadedMsg. It still satisfies Msg so DrillIn's
// signature can stay unchanged.
type DrillInMsg struct {
	Number        string
	Rendered, Raw string
	Err           error
}

func (DrillInMsg) isConsoleMsg() {}

// SidebarLoadedMsg carries a Dispatch's loaded sidebar content: its Activity
// feed and its rendered transcript plus byte-exact raw form, fetched together
// on select so the Activity/Transcript and rendered/raw toggles need no
// further I/O. Err is set instead of everything above when nothing could load
// at all (e.g. no Driver configured) — Activity and TranscriptErr are both
// meaningless then. TranscriptErr is set when only the Transcript's load
// failed; Activity loaded independently and stays showable, since the
// sidebar's default view never depends on the Transcript.
type SidebarLoadedMsg struct {
	Number, Title string
	Activity      []ActivityLine
	Rendered, Raw string
	Err           error
	TranscriptErr error
	// Notice is a graceful, non-error explanation to show in place of an empty
	// pane — set when an orphan-flagged Dispatch has no local pass log yet.
	// "" for every other open, including the claimed-but-not-yet-launched
	// race, which keeps its silent-empty contract.
	Notice string
}

func (SidebarLoadedMsg) isConsoleMsg() {}

// SidebarActivityMsg carries the open sidebar's Dispatch's freshly re-derived
// Activity feed — refreshPickDecorations's per-Msg refresh, piggybacking the
// per-Msg sync tick (ADR 0030) and scoped to the open Dispatch so I/O stays
// bounded with many running. Also a no-op when Number no longer matches the
// open sidebar: the operator may have switched or closed it in the same
// Update batch that produced this message. Sent regardless of ShowTranscript,
// since the Activity feed stays cached for an instant toggle back to it.
type SidebarActivityMsg struct {
	Number   string
	Activity []ActivityLine
}

func (SidebarActivityMsg) isConsoleMsg() {}

// SidebarTranscriptMsg is SidebarActivityMsg's analogue for the Transcript
// render, with the same race guard, but sent only while ShowTranscript is
// active.
type SidebarTranscriptMsg struct {
	Number        string
	Rendered, Raw string
}

func (SidebarTranscriptMsg) isConsoleMsg() {}

// SidebarToggleMsg ("t") advances the sidebar's content one step around its
// Activity -> Transcript (rendered) -> Transcript (raw) cycle, so a repeated
// "t" reaches every form, raw log included, without a second key.
type SidebarToggleMsg struct{}

func (SidebarToggleMsg) isConsoleMsg() {}

// SidebarCloseMsg leaves the sidebar and returns focus to the list alone.
type SidebarCloseMsg struct{}

func (SidebarCloseMsg) isConsoleMsg() {}

// SidebarScrollMsg moves the focused sidebar by Delta lines (positive scrolls
// down/later, negative up/earlier); Update clamps the result into the loaded
// content's line bounds.
type SidebarScrollMsg struct {
	Delta int
}

func (SidebarScrollMsg) isConsoleMsg() {}

// SidebarJumpToEndMsg ("G"/"End") re-attaches Follow and moves Offset to the
// last line — the way back to live-tailing after a scroll-up detached it
// (ADR 0030).
type SidebarJumpToEndMsg struct{}

func (SidebarJumpToEndMsg) isConsoleMsg() {}

// SidebarJumpToBeginningMsg (the "gg" chord) moves Offset to 0 and detaches
// Follow, the same as a manual scroll-up, parking at the start of the buffer.
type SidebarJumpToBeginningMsg struct{}

func (SidebarJumpToBeginningMsg) isConsoleMsg() {}

// SidebarZoomToggleMsg ("z") toggles Model.SidebarZoom, forcing the sidebar
// fullscreen for deep reading or releasing that force back to sidebarFits'
// own width check — independent of the narrow-terminal fallback (ADR 0030).
type SidebarZoomToggleMsg struct{}

func (SidebarZoomToggleMsg) isConsoleMsg() {}

// FocusListMsg ("h"/left) moves keyboard focus to the list.
type FocusListMsg struct{}

func (FocusListMsg) isConsoleMsg() {}

// FocusSidebarMsg ("l"/right) moves keyboard focus to the sidebar.
type FocusSidebarMsg struct{}

func (FocusSidebarMsg) isConsoleMsg() {}
