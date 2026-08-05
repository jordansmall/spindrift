package console

// Msg types for the terminate confirm flow.

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
