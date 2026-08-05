package console

// Msg types for the Dispatch sidebar: load, activity/transcript toggle, scroll, and focus.

// DrillInMsg carries a Dispatch's whole rendered transcript — every pass
// concatenated in order, with pass boundaries marked — plus its byte-exact
// raw form, loaded together so the raw toggle needs no further I/O. Err is
// set instead when loading or rendering failed (e.g. no Driver configured,
// no logs on disk yet). Purely DrillIn's own return payload since #1501 —
// openSidebarCmd unwraps it internally to build a SidebarLoadedMsg, so it
// never reaches the tea layer's Update dispatch directly; it still satisfies
// Msg so DrillIn's signature (and transcript_test.go's assertions against
// it) can stay unchanged.
type DrillInMsg struct {
	Number        string
	Rendered, Raw string
	Err           error
}

func (DrillInMsg) isConsoleMsg() {}

// SidebarLoadedMsg carries a Dispatch's loaded sidebar content: its Activity
// feed (ActivityFeed's derivation) and its whole rendered transcript plus
// byte-exact raw form (DrillIn's load), fetched together on select so the
// Activity/Transcript and rendered/raw toggles need no further I/O (#1501).
// Err is set instead of everything above when nothing could load at all
// (e.g. no Driver configured) — Activity and TranscriptErr are both
// meaningless then. TranscriptErr is set when only the Transcript's own load
// failed (DrillIn's error) — Activity still loaded independently and stays
// showable, since the sidebar's default view never depends on the
// Transcript's own success.
type SidebarLoadedMsg struct {
	Number, Title string
	Activity      []ActivityLine
	Rendered, Raw string
	Err           error
	TranscriptErr error
	// Notice is a graceful, non-error explanation to show in place of an
	// empty pane — set when an orphan-flagged Dispatch has no local pass
	// log yet (issue #1621). "" for every other open, including the
	// session-launched claimed-but-not-yet-launched race, which keeps its
	// existing silent-empty contract.
	Notice string
}

func (SidebarLoadedMsg) isConsoleMsg() {}

// SidebarActivityMsg carries the open sidebar's Dispatch's freshly re-derived
// Activity feed — refreshPickDecorations's per-Msg refresh, piggybacking
// the existing per-Msg sync tick (ADR 0030) and scoped to whichever
// Dispatch the sidebar has open so I/O stays bounded even with many
// Dispatches running (issue #1502). A no-op when no sidebar is open or
// Number no longer matches it — the operator may have switched or closed
// the sidebar in the same Update batch that produced this message. Sent
// regardless of ShowTranscript, since the Activity feed stays cached for an
// instant toggle back to it; SidebarTranscriptMsg is the Transcript's own
// analogue, sent only while ShowTranscript is active (issue #1736).
type SidebarActivityMsg struct {
	Number   string
	Activity []ActivityLine
}

func (SidebarActivityMsg) isConsoleMsg() {}

// SidebarTranscriptMsg carries the open sidebar's Dispatch's freshly
// re-derived Transcript render — refreshPickDecorations's per-Msg refresh,
// the Transcript's own analogue of SidebarActivityMsg, scoped to whichever
// Dispatch the sidebar has open and only sent while ShowTranscript is active
// (issue #1736). A no-op when no sidebar is open or Number no longer matches
// it, mirroring SidebarActivityMsg's own race guard.
type SidebarTranscriptMsg struct {
	Number        string
	Rendered, Raw string
}

func (SidebarTranscriptMsg) isConsoleMsg() {}

// SidebarToggleMsg is the run loop's signal that the operator pressed "t" —
// advances the sidebar's content one step around its Activity -> Transcript
// (rendered) -> Transcript (raw) -> Activity cycle, so a repeated "t" reaches
// every form the byte-exact raw log included, without a second key (#1501).
// A no-op when no sidebar is open.
type SidebarToggleMsg struct{}

func (SidebarToggleMsg) isConsoleMsg() {}

// SidebarCloseMsg is the run loop's signal that the operator asked to leave
// the sidebar and return focus to the list alone (#1501).
type SidebarCloseMsg struct{}

func (SidebarCloseMsg) isConsoleMsg() {}

// SidebarScrollMsg is the tea layer's signal that the operator pressed a
// scroll key while the sidebar is focused — Delta is the number of lines to
// move (positive scrolls down/later, negative scrolls up/earlier); Update
// clamps the result into the loaded content's line bounds, a no-op when no
// sidebar is open (#1501).
type SidebarScrollMsg struct {
	Delta int
}

func (SidebarScrollMsg) isConsoleMsg() {}

// SidebarJumpToEndMsg is the tea layer's signal that the operator pressed
// "G"/"End" while the sidebar has focus — re-attaches Follow and moves
// Offset to the last line, the way back to live-tailing after a scroll-up
// detached it (issue #1502, ADR 0030). A no-op when no sidebar is open.
type SidebarJumpToEndMsg struct{}

func (SidebarJumpToEndMsg) isConsoleMsg() {}

// SidebarJumpToBeginningMsg is the tea layer's signal that the operator
// completed the "gg" leader chord while the sidebar has focus — moves Offset
// to 0 and detaches Follow, the same as a manual scroll-up, so the operator
// parks at the start of the buffer (issue #1629). A no-op when no sidebar is
// open.
type SidebarJumpToBeginningMsg struct{}

func (SidebarJumpToBeginningMsg) isConsoleMsg() {}

// SidebarZoomToggleMsg is the tea layer's signal that the operator pressed
// "z" — toggles Model.SidebarZoom, forcing the sidebar to render fullscreen
// (or releasing that force, falling back to sidebarFits' own width check) for
// deep reading, independent of the narrow-terminal fallback (issue #1502,
// ADR 0030).
type SidebarZoomToggleMsg struct{}

func (SidebarZoomToggleMsg) isConsoleMsg() {}

// FocusListMsg is the tea layer's signal that the operator pressed "h"/left —
// moves keyboard focus to the list, a no-op when it's already there (#1501,
// ADR 0030).
type FocusListMsg struct{}

func (FocusListMsg) isConsoleMsg() {}

// FocusSidebarMsg is the tea layer's signal that the operator pressed
// "l"/right — moves keyboard focus to the sidebar, a no-op when no sidebar
// is open (#1501, ADR 0030).
type FocusSidebarMsg struct{}

func (FocusSidebarMsg) isConsoleMsg() {}
