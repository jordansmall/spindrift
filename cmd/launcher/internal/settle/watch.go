package settle

import (
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// watchObservation is what a watch.Poll call learned: enough for the caller
// to pick a gateResult-shaped outcome and, on gateTerminal, format an
// operator-facing reason string naming which guard failed.
type watchObservation struct {
	outcome gateResult
	// err is non-nil only when outcome == gateTerminal because CheckState
	// itself errored.
	err error
	// sawNonTerminal is true iff a real poll observed PENDING/EXPECTED/NONE
	// (never true from the window-elapsed fallback).
	sawNonTerminal bool
	// windowElapsed is true iff the registration-window-elapsed fallback is
	// what established registration. Only ever set when requireRegistration
	// was in play.
	windowElapsed bool
	// elapsed is poll-count * actualIv, in the deadline's seconds unit.
	elapsed int
}

// watch owns one bounded CI-gate poll: interval, timeout, registration
// window, and the clock it sleeps through.
type watch struct {
	pollInterval        int
	deadline            int
	requireRegistration bool
	clock               dispatch.Clock
}

// registrationWindowPolls bounds how many poll intervals watch.poll withholds
// trust in an inherited SUCCESS while requireRegistration is set. Once this
// many intervals elapse with the rollup reading SUCCESS throughout and no
// non-terminal state observed, that is treated as proof CI already finished
// rather than proof it is still mid-registration. A stale SUCCESS followed by
// a non-terminal state inside the window must still wait for fresh
// registration.
const registrationWindowPolls = 3

// actualInterval is pollInterval floored to 1, so elapsed tracking always
// advances and the loop terminates even in test mode's zero-sleep setting.
func (w watch) actualInterval() int {
	if w.pollInterval <= 0 {
		return 1
	}
	return w.pollInterval
}

// registrationWindow is registrationWindowPolls*actualInterval, clamped to
// deadline. Without the clamp, a deadline smaller than the unclamped window
// would never let the window elapse before the ci-timeout deadline hits,
// livelocking a legitimately-already-green adopted PR into gateTerminal.
func (w watch) registrationWindow() int {
	window := registrationWindowPolls * w.actualInterval()
	if window > w.deadline {
		return w.deadline
	}
	return window
}

// pollState accumulates the evidence poll() gathers across loop iterations.
// "registered" is deliberately not a third field here — it is always derived
// from these two via the registered method, so it can never drift out of sync
// with the evidence it summarises.
type pollState struct {
	// sawNonTerminal tracks only genuine evidence that a real poll observed a
	// non-terminal state; the registrationWindow-elapsed fallback never sets
	// it. That distinguishes an ordinary ran-out-the-clock timeout from one
	// where the requireRegistration guard never cleared on real evidence.
	sawNonTerminal bool
	// windowElapsed latches true when the registrationWindow-elapsed fallback
	// is what established registration. It never un-latches.
	windowElapsed bool
	// elapsed is poll-count * actualIv, in the deadline's seconds unit.
	elapsed int
}

// registered reports whether this run's own checks are considered to have
// registered on the head commit: registration was never required, or real
// evidence (sawNonTerminal) or the window-elapsed fallback (windowElapsed)
// has since established it.
func (w watch) registered(s pollState) bool {
	return !w.requireRegistration || s.sawNonTerminal || s.windowElapsed
}

// observation builds the watchObservation for the given terminal outcome/err.
// Used on every return path — including the abandoned one — so an observation
// always carries poll()'s accumulated evidence, never a zero-value literal.
func (s pollState) observation(outcome gateResult, err error) watchObservation {
	return watchObservation{
		outcome:        outcome,
		err:            err,
		sawNonTerminal: s.sawNonTerminal,
		windowElapsed:  s.windowElapsed,
		elapsed:        s.elapsed,
	}
}

// poll runs the bounded loop, calling checkState each iteration and terminated
// before each poll to detect abandonment.
func (w watch) poll(terminated func() bool, checkState func() (forge.RollupState, error)) watchObservation {
	pollIv := w.pollInterval
	actualIv := w.actualInterval()
	registrationWindow := w.registrationWindow()
	deadline := w.deadline

	var st pollState

	for {
		if terminated() {
			return st.observation(gateAbandoned, nil)
		}
		state, stateErr := checkState()
		if stateErr != nil {
			return st.observation(gateTerminal, stateErr)
		}
		if state != forge.StateSuccess && state != forge.StateFailure && state != forge.StateError {
			st.sawNonTerminal = true
		}
		if !w.registered(st) && st.elapsed >= registrationWindow {
			// The window elapsed with only a terminal state observed
			// (SUCCESS in practice; FAILURE/ERROR return immediately below).
			// That is proof CI already finished, not that it is still
			// mid-registration.
			st.windowElapsed = true
		}

		switch state {
		case forge.StateSuccess:
			if !w.registered(st) {
				// No evidence yet that this run's own checks registered —
				// wait rather than trust a possibly-inherited rollup.
				break
			}
			// Pause before confirming: back-to-back GraphQL calls return the
			// same snapshot, and a partial check registration can briefly
			// show SUCCESS before all jobs appear.
			w.clock.Sleep(time.Duration(pollIv) * time.Second)
			confirm, confirmErr := checkState()
			if confirmErr != nil {
				return st.observation(gateTerminal, confirmErr)
			}
			if confirm != forge.StateSuccess {
				if confirm == forge.StateFailure || confirm == forge.StateError {
					return st.observation(gateRedRetry, nil)
				}
				// PENDING/EXPECTED/NONE — keep waiting for checks to settle.
				break
			}
			return st.observation(gateGreen, nil)
		case forge.StateFailure, forge.StateError:
			return st.observation(gateRedRetry, nil)
		}

		// PENDING, EXPECTED, NONE (no checks yet), or unrecognised — keep
		// waiting until timeout.
		if st.elapsed >= deadline {
			break
		}
		// pollIv 0 (test mode) sleeps 0; actualIv still advances elapsed, so
		// the loop cannot spin forever.
		w.clock.Sleep(time.Duration(pollIv) * time.Second)
		st.elapsed += actualIv
	}
	return st.observation(gateTerminal, nil)
}
