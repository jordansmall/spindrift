package console

// Msg types for the work queue: picks, promotion, and the live cap.

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
