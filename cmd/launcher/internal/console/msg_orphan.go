package console

// Msg types for orphan detection and adoption.

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
