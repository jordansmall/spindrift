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

// poll runs the bounded loop, calling checkState each iteration and
// terminated before each poll to detect abandonment. It is the extracted
// body of gateToGreen's former inline loop, unchanged in behavior.
func (w watch) poll(terminated func() bool, checkState func() (forge.RollupState, error)) watchObservation {
	pollIv := w.pollInterval
	deadline := w.deadline
	// actualIv is used for elapsed tracking; floor to 1 so we don't
	// hot-spin. When pollIv is 0 (test mode) the sleep duration is also 0,
	// so elapsed still advances and the loop terminates.
	actualIv := pollIv
	if actualIv <= 0 {
		actualIv = 1
	}
	// registrationWindow — see registrationWindowPolls's doc.
	registrationWindow := registrationWindowPolls * actualIv
	// A deadline smaller than the unclamped window (e.g. MERGE_POLL_TIMEOUT <
	// registrationWindowPolls*MERGE_POLL_INTERVAL) would otherwise never let
	// the window elapse before the ci-timeout deadline hits, livelocking a
	// legitimately-already-green adopted PR into gateTerminal instead of
	// accepting it (issue #2475 follow-up). deadline 0 (the "NONE times out
	// immediately" case) already makes this a no-op-safe 0.
	if registrationWindow > deadline {
		registrationWindow = deadline
	}
	elapsed := 0
	registered := !w.requireRegistration
	// sawNonTerminal tracks only genuine evidence that a real poll observed a
	// non-terminal state (PENDING/EXPECTED/NONE) — unlike registered, it is
	// never flipped by the registrationWindow-elapsed fallback below, so it
	// stays false when a deadline is reached with nothing but SUCCESS ever
	// actually observed. That distinguishes an ordinary ran-out-the-clock
	// timeout from one where the requireRegistration guard itself never
	// cleared on real evidence (issue #2476).
	sawNonTerminal := false
	// windowElapsed latches true exactly when the registrationWindow-elapsed
	// fallback below is what set registered — i.e. the window ran out before
	// any genuine non-terminal evidence ever arrived. It never un-latches.
	windowElapsed := false

	for {
		if terminated() {
			return watchObservation{outcome: gateAbandoned}
		}
		state, stateErr := checkState()
		if stateErr != nil {
			return watchObservation{
				outcome:        gateTerminal,
				err:            stateErr,
				sawNonTerminal: sawNonTerminal,
				windowElapsed:  windowElapsed,
				elapsed:        elapsed,
			}
		}
		if state != forge.StateSuccess && state != forge.StateFailure && state != forge.StateError {
			registered = true
			sawNonTerminal = true
		}
		if !registered && elapsed >= registrationWindow {
			// The registration window elapsed with only a terminal state
			// (SUCCESS, in practice — FAILURE/ERROR return immediately
			// below) ever observed. Treat that as proof CI already
			// finished, not proof it's still mid-registration (issue
			// #2475).
			registered = true
			windowElapsed = true
		}

		switch state {
		case forge.StateSuccess:
			if !registered {
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
				return watchObservation{
					outcome:        gateTerminal,
					err:            confirmErr,
					sawNonTerminal: sawNonTerminal,
					windowElapsed:  windowElapsed,
					elapsed:        elapsed,
				}
			}
			if confirm != forge.StateSuccess {
				if confirm == forge.StateFailure || confirm == forge.StateError {
					return watchObservation{
						outcome:        gateRedRetry,
						sawNonTerminal: sawNonTerminal,
						windowElapsed:  windowElapsed,
						elapsed:        elapsed,
					}
				}
				// PENDING/EXPECTED/NONE — keep waiting for checks to settle.
				break
			}
			return watchObservation{
				outcome:        gateGreen,
				sawNonTerminal: sawNonTerminal,
				windowElapsed:  windowElapsed,
				elapsed:        elapsed,
			}
		case forge.StateFailure, forge.StateError:
			// Genuine red — signal caller so it can dispatch a fix pass.
			return watchObservation{
				outcome:        gateRedRetry,
				sawNonTerminal: sawNonTerminal,
				windowElapsed:  windowElapsed,
				elapsed:        elapsed,
			}
		}

		// PENDING, EXPECTED, NONE (no checks yet), or unrecognised — keep
		// waiting until timeout.
		if elapsed >= deadline {
			break
		}
		// Sleep 0 when pollIv is 0 (test mode) so tests run without real
		// delays; actualIv still advances elapsed to prevent a tight loop.
		w.clock.Sleep(time.Duration(pollIv) * time.Second)
		elapsed += actualIv
	}
	return watchObservation{
		outcome:        gateTerminal,
		sawNonTerminal: sawNonTerminal,
		windowElapsed:  windowElapsed,
		elapsed:        elapsed,
	}
}
