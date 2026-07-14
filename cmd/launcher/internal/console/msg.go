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

// QuitMsg is the run loop's signal that the operator asked to exit.
type QuitMsg struct{}

func (QuitMsg) isConsoleMsg() {}

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
