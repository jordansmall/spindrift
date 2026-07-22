package console

import "spindrift.dev/launcher/internal/forge"

// Msg is the console's message type — everything that can drive a state
// transition through Update. The adapter is the only producer of
// IssuesLoadedMsg; the run loop is the only producer of the input-driven
// messages (FilterChangedMsg, QuitMsg).
type Msg interface {
	isConsoleMsg()
}

// IssuesLoadedMsg carries the result of a backlog refresh: the adapter's
// translation of an IssueTracker.ListOpenIssues call into a message Update
// can apply without touching the network itself. Err is set instead of
// Issues when the refresh failed.
type IssuesLoadedMsg struct {
	Issues []forge.Issue
	Err    error
}

func (IssuesLoadedMsg) isConsoleMsg() {}

// FilterChangedMsg carries the operator's new label filter text, produced
// by the run loop as the operator types. An empty Filter clears it,
// restoring the full backlog.
type FilterChangedMsg struct {
	Filter string
}

func (FilterChangedMsg) isConsoleMsg() {}

// QuitMsg is the run loop's signal that the operator asked to exit — sent
// directly when no live Dispatches exist, or after a pending quit confirm's
// drain/terminate-all side effect has already run (issue #651).
type QuitMsg struct{}

func (QuitMsg) isConsoleMsg() {}

// QuitRequestedMsg is the run loop's signal that the operator asked to quit
// while live Dispatches exist — arms a pending confirm (drain/terminate-all/
// stay) rather than exiting immediately (issue #651, ADR 0023).
type QuitRequestedMsg struct{}

func (QuitRequestedMsg) isConsoleMsg() {}

// QuitCancelledMsg is the run loop's signal that the operator chose "stay"
// at a pending quit confirm — declines to quit, taking no action.
type QuitCancelledMsg struct{}

func (QuitCancelledMsg) isConsoleMsg() {}

// DogfoodNoticeMsg reports whether a live dogfood pid-file was found at
// startup — a headless loop competing for the same queue. Informational
// only: the console never blocks or gates on it.
type DogfoodNoticeMsg struct {
	Live bool
}

func (DogfoodNoticeMsg) isConsoleMsg() {}

// PickQueuedMsg carries a successfully-promoted pick onto the session
// queue — the Pick adapter's success result.
type PickQueuedMsg struct {
	Number, Title string
	Kind          Kind
}

func (PickQueuedMsg) isConsoleMsg() {}

// PickDissolvedMsg carries a pick whose promotion failed — the Pick adapter's
// error result. The issue never queues; Update instead lands it already
// dissolved (PickDissolved) so the operator sees why. Distinct from
// PickFailed (pick.go), the state a pick that ran and exited non-zero lands
// in — a PickDissolvedMsg promotion never launched a Box at all.
type PickDissolvedMsg struct {
	Number, Title, Reason string
}

func (PickDissolvedMsg) isConsoleMsg() {}

// QueueSnapshotMsg carries the launcher's live Queue state into the pure
// core — Run's per-render sync, since claim/run/settle/dissolve transitions
// happen on the background Queue, not through Update.
type QueueSnapshotMsg struct {
	Picks []Pick
}

func (QueueSnapshotMsg) isConsoleMsg() {}

// UnpickMsg is the run loop's signal that the operator asked to remove a
// queued-but-unlaunched pick. It carries no tracker interaction: Update
// only ever drops it from Model.Picks.
type UnpickMsg struct {
	Number string
}

func (UnpickMsg) isConsoleMsg() {}

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

// OrphanRecoveryMsg carries the explicit adopt gesture's failure into the
// pure core — RecoverFn failing to adopt an orphan-flagged issue (no open
// PR, a draft PR, or a resolve error), leaving the operator with no visible
// reason otherwise (issue #1218). Formerly startup's own signal too before
// issue #1619 retired startup auto-adopt; adoptOrphanCmd is the sole
// producer now. Err is "" only in the zero Model, never a value this
// message itself carries — a successful adopt sends OrphanAdoptedMsg
// instead, not this with an empty Err. Number clears the matching in-flight
// mark AdoptOrphanStartedMsg set, so a failed adopt can be retried by a
// later gesture instead of reading as permanently in flight.
type OrphanRecoveryMsg struct {
	Number string
	Err    string
}

func (OrphanRecoveryMsg) isConsoleMsg() {}

// OrphanDetectedMsg carries startup orphan detection's result: the issue
// numbers OrphanedIssues reported running with no live goroutine in this
// process to account for them. Startup never adopts these on its own
// anymore (issue #1619) — Update just flags them so the backlog can show
// them distinguishable from a Dispatch this session launched, leaving
// adoption to the operator's explicit gesture.
type OrphanDetectedMsg struct {
	Numbers []string
}

func (OrphanDetectedMsg) isConsoleMsg() {}

// OrphanHeartbeatsMsg carries the live status line syncQueue derived from
// each orphan-flagged issue's on-disk pass log, keyed by number — the
// Backlog row's analogue of a running Pick's own Heartbeat field, so an
// orphan the operator hasn't adopted still shows it is making progress
// (issue #1621).
type OrphanHeartbeatsMsg struct {
	Heartbeats map[string]string
}

func (OrphanHeartbeatsMsg) isConsoleMsg() {}

// AdoptOrphanStartedMsg marks Number as having an adopt in flight — sent
// synchronously by handleKey the instant "A" fires adoptOrphanCmd, before
// RecoverFn's network round-trip even starts. Model.IsAdoptingOrphan gates
// a second "A" press on the same row for the whole in-flight window, not
// just after RecoverFn returns: OrphanAdoptedMsg/OrphanRecoveryMsg only land
// once RecoverFn's goroutine completes, so gating on the orphan flag alone
// (which those two clear) left the window between the keypress and that
// completion open to a second concurrent RecoverFn call racing the first
// over the same PR (issue #1619 review finding).
type AdoptOrphanStartedMsg struct {
	Number string
}

func (AdoptOrphanStartedMsg) isConsoleMsg() {}

// OrphanAdoptedMsg carries the explicit adopt gesture's success: Number is
// no longer an orphan, since RecoverFn just settled its PR through this
// session. Update clears it out of Model.OrphanNums so a second press of
// the same gesture on the same, now-adopted row can't fire RecoverFn again
// and race a second same-process settle over the PR the first adopt already
// claimed (issue #1619).
type OrphanAdoptedMsg struct {
	Number string
}

func (OrphanAdoptedMsg) isConsoleMsg() {}

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

// ScrollMsg is the tea layer's signal that the operator pressed a line-scroll
// key while the body is showing (no sidebar focused) — Delta is the number of
// rows to move (positive scrolls down/later, negative scrolls up/earlier).
// It moves Model.Offset within the active Section, clamped the same way
// SidebarScrollMsg clamps Sidebar.Offset (issue #1036, ADR 0030).
type ScrollMsg struct {
	Delta int
}

func (ScrollMsg) isConsoleMsg() {}

// TerminateRequestedMsg is the run loop's signal that the operator pressed
// "X" — arms a pending confirm (ADR 0024, issue #649) rather than acting
// immediately.
type TerminateRequestedMsg struct {
	Number string
}

func (TerminateRequestedMsg) isConsoleMsg() {}

// TerminateConfirmedMsg is the run loop's signal that the operator confirmed
// a pending terminate with "y"/"yes". The run loop has already fired
// Launcher.TerminateAsync (issue #745) by the time this reaches Update, but
// that call only starts the background Terminate — it may still be in
// flight; Update only clears the pending confirm.
type TerminateConfirmedMsg struct {
	Number string
}

func (TerminateConfirmedMsg) isConsoleMsg() {}

// TerminateCancelledMsg is the run loop's signal that the operator declined
// a pending terminate confirm (anything other than "y"/"yes").
type TerminateCancelledMsg struct{}

func (TerminateCancelledMsg) isConsoleMsg() {}

// GPendingMsg is the tea layer's signal that "g" armed the "gg" leader
// window (issue #1628).
type GPendingMsg struct{}

func (GPendingMsg) isConsoleMsg() {}

// GResolvedMsg is the tea layer's signal that a pending "gg" chord
// resolved — either a trailing "g" completed it, the leader window timed
// out, or any other key cancelled it (issue #1628).
type GResolvedMsg struct{}

func (GResolvedMsg) isConsoleMsg() {}

// QueueEnterNoticedMsg is the tea layer's signal that Enter, focused on the
// work queue, was a no-op on a row lacking a Transcript — renders a
// human-readable notice so the keystroke's outcome isn't silent (issue
// #998).
type QueueEnterNoticedMsg struct{}

func (QueueEnterNoticedMsg) isConsoleMsg() {}

// QueueEnterNoticeClearedMsg is the tea layer's signal that the operator's
// next keypress after a QueueEnterNoticedMsg arrived — clears the notice
// (issue #998).
type QueueEnterNoticeClearedMsg struct{}

func (QueueEnterNoticeClearedMsg) isConsoleMsg() {}

// ToastDismissedMsg is the tea layer's signal that a pick-transition toast
// (Model.Toast, issue #1830) should clear — fired by the operator's next
// keypress or the generation-pinned auto-dismiss timer, whichever comes
// first.
type ToastDismissedMsg struct{}

func (ToastDismissedMsg) isConsoleMsg() {}

// CapMsg carries the session's live parallelism cap and current live count
// (issue #653) — Run's per-render sync, the same pattern QueueSnapshotMsg
// uses, since both live entirely on the background Launcher rather than
// through an operator command.
type CapMsg struct {
	Cap, Live int
}

func (CapMsg) isConsoleMsg() {}

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

// CursorMoveMsg is the tea layer's signal that the operator pressed a
// navigation key (j/down, or the up arrow — "k" moved to Terminate in
// #785) — Delta is +1 (down) or -1 (up); Update clamps the result into
// Visible()'s bounds (issue #784).
type CursorMoveMsg struct {
	Delta int
}

func (CursorMoveMsg) isConsoleMsg() {}

// HelpToggleMsg is the tea layer's signal that the operator pressed "?" —
// opens the help overlay, or closes it if already open (issue #784).
type HelpToggleMsg struct{}

func (HelpToggleMsg) isConsoleMsg() {}

// FilterEditStartMsg is the tea layer's signal that the operator pressed
// "/" — arms filter-input mode, saving the current Filter so Esc can revert
// to it (issue #784).
type FilterEditStartMsg struct{}

func (FilterEditStartMsg) isConsoleMsg() {}

// FilterEditConfirmMsg is the tea layer's signal that the operator pressed
// Enter while editing the filter — leaves the already-live-narrowed Filter
// as-is and exits editing mode.
type FilterEditConfirmMsg struct{}

func (FilterEditConfirmMsg) isConsoleMsg() {}

// FilterEditCancelMsg is the tea layer's signal that the operator pressed
// Esc while editing the filter — restores the Filter active before editing
// started and exits editing mode.
type FilterEditCancelMsg struct{}

func (FilterEditCancelMsg) isConsoleMsg() {}

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

// SizeChangedMsg carries the terminal's current width/height — the tea
// layer's translation of Bubble Tea's WindowSizeMsg, sent on every resize
// including the initial size event (issue #842). Update clamps non-sensical
// values (zero/negative) to a safe floor.
type SizeChangedMsg struct {
	Width, Height int
}

func (SizeChangedMsg) isConsoleMsg() {}
