package console

// TerminateRequestedMsg reports that the operator pressed "X". It arms a
// pending confirm (ADR 0024, issue #649) rather than terminating at once.
type TerminateRequestedMsg struct {
	Number string
}

func (TerminateRequestedMsg) isConsoleMsg() {}

// TerminateConfirmedMsg reports that the operator confirmed a pending
// terminate with "y"/"yes". The run loop has already called
// Launcher.TerminateAsync (issue #745), which may still be in flight, so
// Update only clears the pending confirm.
type TerminateConfirmedMsg struct {
	Number string
}

func (TerminateConfirmedMsg) isConsoleMsg() {}

// TerminateCancelledMsg reports that the operator declined a pending
// terminate confirm with anything other than "y"/"yes".
type TerminateCancelledMsg struct{}

func (TerminateCancelledMsg) isConsoleMsg() {}
