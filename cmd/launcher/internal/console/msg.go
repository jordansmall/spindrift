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

// PickFailedMsg carries a pick whose promotion failed — the Pick adapter's
// error result. The issue never queues; Update instead lands it already
// dissolved so the operator sees why.
type PickFailedMsg struct {
	Number, Title, Reason string
}

func (PickFailedMsg) isConsoleMsg() {}

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

// DrillInToggleMsg is the run loop's signal that the operator asked to
// switch between the rendered transcript and the raw byte-exact log — a
// no-op when no drill-in is open.
type DrillInToggleMsg struct{}

func (DrillInToggleMsg) isConsoleMsg() {}

// DrillInCloseMsg is the run loop's signal that the operator asked to leave
// the transcript view and return to the backlog/queue view.
type DrillInCloseMsg struct{}

func (DrillInCloseMsg) isConsoleMsg() {}

// TerminateRequestedMsg is the run loop's signal that the operator typed
// "k"/"kill"/"terminate" <num> — arms a pending confirm (ADR 0024, issue
// #649) rather than acting immediately.
type TerminateRequestedMsg struct {
	Number string
}

func (TerminateRequestedMsg) isConsoleMsg() {}

// TerminateConfirmedMsg is the run loop's signal that the operator confirmed
// a pending terminate with "y"/"yes". The run loop has already called
// Launcher.Terminate by the time this reaches Update; Update only clears the
// pending confirm.
type TerminateConfirmedMsg struct {
	Number string
}

func (TerminateConfirmedMsg) isConsoleMsg() {}

// TerminateCancelledMsg is the run loop's signal that the operator declined
// a pending terminate confirm (anything other than "y"/"yes").
type TerminateCancelledMsg struct{}

func (TerminateCancelledMsg) isConsoleMsg() {}

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
}

func (StaleStatusMsg) isConsoleMsg() {}
