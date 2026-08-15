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
	// windowElapsed is true iff the registration window elapsed before
	// genuine non-terminal evidence appeared.
	windowElapsed bool
	// elapsed is poll-count * actualIv, in the same seconds unit as the
	// deadline.
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

// actualInterval is pollInterval floored to 1, used for elapsed tracking so
// the loop advances and terminates instead of hot-spinning forever. When
// pollInterval is 0 (test mode) the sleep duration is also 0, so elapsed
// still advances and the loop terminates.
func (w watch) actualInterval() int {
	if w.pollInterval <= 0 {
		return 1
	}
	return w.pollInterval
}

// registrationWindow is registrationWindowPolls*actualInterval, clamped to
// deadline — see registrationWindowPolls's doc. A deadline smaller than the
// unclamped window (e.g. MERGE_POLL_TIMEOUT < registrationWindowPolls*
// MERGE_POLL_INTERVAL) would otherwise never let the window elapse before
// the ci-timeout deadline hits, livelocking a legitimately-already-green
// adopted PR into gateTerminal instead of accepting it (issue #2475
// follow-up). deadline 0 (the "NONE times out immediately" case) already
// makes this a no-op-safe 0.
func (w watch) registrationWindow() int {
	window := registrationWindowPolls * w.actualInterval()
	if window > w.deadline {
		return w.deadline
	}
	return window
}

// pollState accumulates the evidence poll() has gathered across loop
// iterations. sawNonTerminal and windowElapsed are real, independently
// meaningful accumulated evidence that must persist across iterations;
// "registered" is deliberately not a third field alongside them — it is
// always derived from the two via the registered method, so it can never
// drift out of sync with the evidence it summarises.
type pollState struct {
	// sawNonTerminal tracks only genuine evidence that a real poll observed
	// a non-terminal state (PENDING/EXPECTED/NONE) — unlike registered, it
	// is never set true by the registrationWindow-elapsed fallback, so it
	// stays false when a deadline is reached with nothing but SUCCESS ever
	// actually observed. That distinguishes an ordinary ran-out-the-clock
	// timeout from one where the requireRegistration guard itself never
	// cleared on real evidence (issue #2476).
	sawNonTerminal bool
	// windowElapsed latches true exactly when the registrationWindow-elapsed
	// fallback is what established registration — i.e. the window ran out
	// before any genuine non-terminal evidence ever arrived. It never
	// un-latches.
	windowElapsed bool
	// elapsed is poll-count * actualIv, in the same seconds unit as the
	// deadline.
	elapsed int
}

// registered reports whether this run's own checks are considered to have
// registered on the head commit: registration was never required, or real
// evidence (sawNonTerminal) or the window-elapsed fallback (windowElapsed)
// has since established it.
func (s pollState) registered(requireRegistration bool) bool {
	return !requireRegistration || s.sawNonTerminal || s.windowElapsed
}

// observation builds the watchObservation this pollState's accumulated
// evidence corresponds to, for the given terminal outcome/err. Using this
// on every return path — including the abandoned path — ensures a
// watchObservation always carries through whatever evidence poll() had
// already accumulated in this call, never a zero-value literal.
func (s pollState) observation(outcome gateResult, err error) watchObservation {
	return watchObservation{
		outcome:        outcome,
		err:            err,
		sawNonTerminal: s.sawNonTerminal,
		windowElapsed:  s.windowElapsed,
		elapsed:        s.elapsed,
	}
}

// poll runs the bounded loop, calling checkState each iteration and
// terminated before each poll to detect abandonment. It is the extracted
// body of gateToGreen's former inline loop, unchanged in behavior.
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
		if !st.registered(w.requireRegistration) && st.elapsed >= registrationWindow {
			// The registration window elapsed with only a terminal state
			// (SUCCESS, in practice — FAILURE/ERROR return immediately
			// below) ever observed. Treat that as proof CI already
			// finished, not proof it's still mid-registration (issue
			// #2475).
			st.windowElapsed = true
		}

		switch state {
		case forge.StateSuccess:
			if !st.registered(w.requireRegistration) {
				// No evidence yet that this run's own checks registered —
				// wait rather than trust a possibly-inherited rollup.
				break
			}
			// Pause before confirming — back-to-back GraphQL calls return the
			// same snapshot, so a late-registered job would not yet appear.
			w.clock.Sleep(time.Duration(pollIv) * time.Second)
			// Re-poll to confirm the snapshot is stable. A partial check
			// registration can briefly show SUCCESS before all jobs appear.
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
			// Genuine red — signal caller so it can dispatch a fix pass.
			return st.observation(gateRedRetry, nil)
		}

		// PENDING, EXPECTED, NONE (no checks yet), or unrecognised — keep
		// waiting until timeout.
		if st.elapsed >= deadline {
			break
		}
		// Sleep 0 when pollIv is 0 (test mode) so tests run without real
		// delays; actualIv still advances elapsed to prevent a tight loop.
		w.clock.Sleep(time.Duration(pollIv) * time.Second)
		st.elapsed += actualIv
	}
	return st.observation(gateTerminal, nil)
}
