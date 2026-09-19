package console

// PickQueuedMsg carries a successfully-promoted pick onto the session queue.
type PickQueuedMsg struct {
	Number, Title string
	Kind          Kind
}

func (PickQueuedMsg) isConsoleMsg() {}

// PickDissolvedMsg carries a pick whose promotion failed, so Update lands it
// already dissolved rather than queued. PickFailed (pick.go) is the different
// case where a pick ran and exited non-zero; a dissolved pick never launched a
// Box at all.
type PickDissolvedMsg struct {
	Number, Title, Reason string
}

func (PickDissolvedMsg) isConsoleMsg() {}

// QueueSnapshotMsg carries the launcher's live Queue state into the pure core.
// Run syncs it per render because claim/run/settle/dissolve transitions happen
// on the background Queue, not through Update.
type QueueSnapshotMsg struct {
	Picks []Pick
}

func (QueueSnapshotMsg) isConsoleMsg() {}

// UnpickMsg asks Update to drop a queued-but-unlaunched pick. It touches no
// tracker.
type UnpickMsg struct {
	Number string
}

func (UnpickMsg) isConsoleMsg() {}

// QueueEnterNoticedMsg reports that Enter on a work-queue row lacking a
// Transcript did nothing, so the keystroke's outcome isn't silent (issue #998).
type QueueEnterNoticedMsg struct{}

func (QueueEnterNoticedMsg) isConsoleMsg() {}

// QueueEnterNoticeClearedMsg clears that notice on the operator's next
// keypress (issue #998).
type QueueEnterNoticeClearedMsg struct{}

func (QueueEnterNoticeClearedMsg) isConsoleMsg() {}

// CapMsg carries the session's live parallelism cap and current live count
// (issue #653). Run syncs it per render because both values live on the
// background Launcher.
type CapMsg struct {
	Cap, Live int
}

func (CapMsg) isConsoleMsg() {}
