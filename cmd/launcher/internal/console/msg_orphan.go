package console

// OrphanRecoveryMsg carries an explicit adopt gesture's failure (issue #1218):
// RecoverFn found no open PR, or the resolve errored. Err is never empty in a
// real message, since a successful adopt sends OrphanAdoptedMsg instead. Number
// clears the in-flight mark AdoptOrphanStartedMsg set, so a failed adopt can be
// retried instead of reading as permanently in flight.
type OrphanRecoveryMsg struct {
	Number string
	Err    string
}

func (OrphanRecoveryMsg) isConsoleMsg() {}

// OrphanDetectedMsg carries the issue numbers OrphanedIssues reported running
// with no live goroutine in this process. Startup only flags them (issue #1619);
// adoption waits for the operator's explicit gesture.
type OrphanDetectedMsg struct {
	Numbers []string
}

func (OrphanDetectedMsg) isConsoleMsg() {}

// OrphanHeartbeatsMsg carries each orphan-flagged issue's status line, keyed by
// number, so an orphan nobody has adopted still shows progress in the backlog
// (issue #1621).
type OrphanHeartbeatsMsg struct {
	Heartbeats map[string]string
}

func (OrphanHeartbeatsMsg) isConsoleMsg() {}

// AdoptOrphanStartedMsg marks Number as having an adopt in flight. handleKey
// sends it the instant "A" fires adoptOrphanCmd, before RecoverFn's round-trip
// starts: gating a second "A" on the orphan flag alone leaves the window between
// the keypress and RecoverFn's return open to two concurrent calls racing over
// the same PR (issue #1619 review finding).
type AdoptOrphanStartedMsg struct {
	Number string
}

func (AdoptOrphanStartedMsg) isConsoleMsg() {}

// OrphanAdoptedMsg carries an explicit adopt gesture's success. Update clears
// Number from Model.OrphanNums so a second press cannot fire RecoverFn again and
// race a second same-process settle over the PR the first adopt claimed
// (issue #1619).
type OrphanAdoptedMsg struct {
	Number string
}

func (OrphanAdoptedMsg) isConsoleMsg() {}
