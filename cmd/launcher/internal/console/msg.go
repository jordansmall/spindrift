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
// no logs on disk yet).
type DrillInMsg struct {
	Number        string
	Rendered, Raw string
	Err           error
}

func (DrillInMsg) isConsoleMsg() {}

// OrphanRecoveryMsg carries startup orphan recovery's failures into the pure
// core — OrphanedIssues() failing to enumerate orphaned sandboxes, or
// RecoverFn failing to adopt one of them, previously left the operator with
// no visible trace of either (issue #1218). Err is "" when recovery found no
// orphans or adopted every one cleanly.
type OrphanRecoveryMsg struct {
	Err string
}

func (OrphanRecoveryMsg) isConsoleMsg() {}

// DrillInToggleMsg is the run loop's signal that the operator asked to
// switch between the rendered transcript and the raw byte-exact log — a
// no-op when no drill-in is open.
type DrillInToggleMsg struct{}

func (DrillInToggleMsg) isConsoleMsg() {}

// DrillInCloseMsg is the run loop's signal that the operator asked to leave
// the transcript view and return to the backlog/queue view.
type DrillInCloseMsg struct{}

func (DrillInCloseMsg) isConsoleMsg() {}

// DrillInScrollMsg is the tea layer's signal that the operator pressed a
// scroll key while the drill-in transcript pane is open — Delta is the
// number of lines to move (positive scrolls down/later, negative scrolls
// up/earlier); Update clamps the result into the loaded content's line
// bounds, a no-op when no drill-in is open (issue #786).
type DrillInScrollMsg struct {
	Delta int
}

func (DrillInScrollMsg) isConsoleMsg() {}

// ScrollMsg is the tea layer's signal that the operator pressed a line-scroll
// key while the backlog/queue body is showing (no drill-in open) — Delta is
// the number of rows to move (positive scrolls down/later, negative scrolls
// up/earlier). It moves BacklogOffset or QueueOffset depending on Focus,
// clamped the same way DrillInScrollMsg clamps DrillIn.Offset (issue #1036).
type ScrollMsg struct {
	Delta int
}

func (ScrollMsg) isConsoleMsg() {}

// TerminateRequestedMsg is the run loop's signal that the operator typed
// "k"/"kill"/"terminate" <num> — arms a pending confirm (ADR 0024, issue
// #649) rather than acting immediately.
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

// PickPendingMsg is the tea layer's signal that "p" armed the "pa" leader
// window — renders a visible hint so the operator knows the keystroke landed
// while it waits out the trailing "a" (issue #835).
type PickPendingMsg struct{}

func (PickPendingMsg) isConsoleMsg() {}

// PickResolvedMsg is the tea layer's signal that a pending pick chord
// resolved — either "a" arrived, the 200ms leader window timed out, or any
// other key resolved it to a single-issue pick (issue #835).
type PickResolvedMsg struct{}

func (PickResolvedMsg) isConsoleMsg() {}

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
// and a rebuild's outcome (issue #652).
type StaleStatusMsg struct {
	Stale      bool
	Message    string
	Rebuilding bool
	RebuildErr string
	// RebuildOutput is the last rebuild's captured nix output (issue #765).
	RebuildOutput string
	// BranchSwitchNotice is the last rebuild's branch-switch notice, if any
	// — "" when pwd's checkout didn't move off the branch it was on (issue
	// #1141).
	BranchSwitchNotice string
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

// FocusToggleMsg is the tea layer's signal that the operator pressed Tab —
// flips Focus between the backlog and work-queue columns (issue #845).
type FocusToggleMsg struct{}

func (FocusToggleMsg) isConsoleMsg() {}

// PaneModeCycleMsg is the tea layer's signal that the operator pressed the
// pane-mode key — advances Model.PaneMode through docked -> floating ->
// fullscreen -> docked, a no-op when no drill-in is open (issue #846, ADR
// 0025).
type PaneModeCycleMsg struct{}

func (PaneModeCycleMsg) isConsoleMsg() {}

// SizeChangedMsg carries the terminal's current width/height — the tea
// layer's translation of Bubble Tea's WindowSizeMsg, sent on every resize
// including the initial size event (issue #842). Update clamps non-sensical
// values (zero/negative) to a safe floor.
type SizeChangedMsg struct {
	Width, Height int
}

func (SizeChangedMsg) isConsoleMsg() {}
