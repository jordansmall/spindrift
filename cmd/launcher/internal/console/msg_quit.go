package console

// Msg types for the quit confirm flow.

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
