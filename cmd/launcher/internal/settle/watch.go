package settle

import (
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// watchObservation is what one watch.poll call learned, enough for the caller
// to pick a gateResult and name the failing guard on gateTerminal.
type watchObservation struct {
	outcome gateResult
	// err is non-nil only when outcome is gateTerminal because CheckState errored.
	err error
	// sawNonTerminal is true only when a real poll observed PENDING/EXPECTED/NONE,
	// never from the window-elapsed fallback.
	sawNonTerminal bool
	// windowElapsed is true only when the registration-window-elapsed fallback
	// established registration, which requires requireRegistration.
	windowElapsed bool
	// elapsed is poll-count * actualIv, in the same seconds unit as the deadline.
	elapsed int
}

// watch owns one bounded CI-gate poll: interval, timeout, registration window,
// and the clock it sleeps through.
type watch struct {
	pollInterval        int
	deadline            int
	requireRegistration bool
	clock               dispatch.Clock
}

// registrationWindowPolls bounds how many poll intervals watch.poll withholds
// trust in an inherited SUCCESS while requireRegistration is set (issue #2475).
// Once that many intervals pass with the rollup reading SUCCESS throughout and
// no non-terminal state ever seen, CI already finished and the SUCCESS is
// accepted. A non-terminal state seen inside the window still forces the wait (#1652).
const registrationWindowPolls = 3

// actualInterval floors pollInterval to 1 so elapsed advances and the loop
// terminates instead of hot-spinning when pollInterval is 0 (test mode).
func (w watch) actualInterval() int {
	if w.pollInterval <= 0 {
		return 1
	}
	return w.pollInterval
}

// registrationWindow is registrationWindowPolls*actualInterval, clamped to
// deadline. Without the clamp a deadline shorter than the unclamped window
// would hit the ci-timeout before the window could ever elapse, turning an
// already-green adopted PR into gateTerminal (issue #2475 follow-up).
func (w watch) registrationWindow() int {
	window := registrationWindowPolls * w.actualInterval()
	if window > w.deadline {
		return w.deadline
	}
	return window
}

// pollState accumulates the evidence poll() gathers across loop iterations. The
// registered method derives registration from these fields rather than storing
// it, so it cannot drift from the evidence. sawNonTerminal stays separate from
// windowElapsed so the caller can tell an ordinary ran-out-the-clock timeout
// from one where the guard never cleared on real evidence (#2476).
type pollState struct {
	sawNonTerminal bool
	// windowElapsed latches true when the window ran out before any
	// non-terminal evidence arrived, and never un-latches.
	windowElapsed bool
	elapsed       int
}

// registered reports whether this run's own checks count as registered on the
// head commit.
func (w watch) registered(s pollState) bool {
	return !w.requireRegistration || s.sawNonTerminal || s.windowElapsed
}

// observation builds the watchObservation for this state. Every return path in
// poll() uses it so an observation carries the evidence accumulated so far,
// never a zero value.
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
			// The window ran out with only terminal states observed
			// (SUCCESS in practice, since FAILURE and ERROR return below),
			// so CI already finished rather than being mid-registration (#2475).
			st.windowElapsed = true
		}

		switch state {
		case forge.StateSuccess:
			if !w.registered(st) {
				// No evidence yet that this run's own checks registered, so
				// wait rather than trust a possibly-inherited rollup.
				break
			}
			// Pause before confirming: back-to-back GraphQL calls return the
			// same snapshot, so a late-registered job would not yet appear and
			// a partial registration reads SUCCESS.
			w.clock.Sleep(time.Duration(pollIv) * time.Second)
			confirm, confirmErr := checkState()
			if confirmErr != nil {
				return st.observation(gateTerminal, confirmErr)
			}
			if confirm != forge.StateSuccess {
				if confirm == forge.StateFailure || confirm == forge.StateError {
					return st.observation(gateRedRetry, nil)
				}
				// The confirm poll read PENDING, EXPECTED, or NONE, so keep
				// waiting for the checks to settle.
				break
			}
			return st.observation(gateGreen, nil)
		case forge.StateFailure, forge.StateError:
			// Genuine red, so signal the caller to dispatch a fix pass.
			return st.observation(gateRedRetry, nil)
		}

		// The state is PENDING, EXPECTED, NONE (no checks yet), or
		// unrecognised, so keep waiting until the deadline.
		if st.elapsed >= deadline {
			break
		}
		// The sleep is 0 in test mode (pollIv 0), so actualIv rather than the
		// clock advances elapsed and prevents a tight loop.
		w.clock.Sleep(time.Duration(pollIv) * time.Second)
		st.elapsed += actualIv
	}
	return st.observation(gateTerminal, nil)
}
