package settle

import (
	"errors"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// neverTerminated is a terminated func that never fires — the ordinary case
// for these tests, which are only about the poll-loop state machine itself.
func neverTerminated() bool { return false }

// scriptedCheckState returns a checkState func that walks states in order,
// returning err (if non-nil) instead of a state on the given zero-based
// call indices, and repeats the final entry once the script is exhausted so
// a test can let the loop run out the deadline without over-specifying every
// poll.
func scriptedCheckState(states []forge.RollupState, errAt map[int]error) func() (forge.RollupState, error) {
	i := 0
	return func() (forge.RollupState, error) {
		call := i
		if i < len(states)-1 {
			i++
		}
		if err, ok := errAt[call]; ok {
			return "", err
		}
		return states[call], nil
	}
}

// TestWatchPoll_ActualIvFloor verifies that a zero pollInterval still floors
// to 1 for elapsed tracking, so the loop advances and terminates at the
// deadline instead of hot-spinning forever.
func TestWatchPoll_ActualIvFloor(t *testing.T) {
	_, clock := recordingClock()
	w := watch{pollInterval: 0, deadline: 2, clock: clock}
	check := scriptedCheckState([]forge.RollupState{forge.StatePending}, nil)

	obs := w.poll(neverTerminated, check)

	if obs.outcome != gateTerminal {
		t.Fatalf("outcome = %v, want gateTerminal", obs.outcome)
	}
	if obs.elapsed != 2 {
		t.Fatalf("elapsed = %d, want 2 (actualIv floored to 1, deadline 2)", obs.elapsed)
	}
	if !obs.sawNonTerminal {
		t.Errorf("sawNonTerminal = false, want true (PENDING observed every poll)")
	}
	if obs.windowElapsed {
		t.Errorf("windowElapsed = true, want false (requireRegistration unset)")
	}
}

// TestWatchPoll_RegistrationWindowClamp verifies that a deadline smaller
// than registrationWindowPolls*actualIv still lets a settled-SUCCESS-only
// sequence resolve to gateGreen with windowElapsed true once the
// deadline-clamped window elapses, instead of falling through to
// gateTerminal (issue #2475 follow-up).
func TestWatchPoll_RegistrationWindowClamp(t *testing.T) {
	sleeps, clock := recordingClock()
	w := watch{pollInterval: 1, deadline: 1, requireRegistration: true, clock: clock}
	check := scriptedCheckState([]forge.RollupState{forge.StateSuccess}, nil)

	obs := w.poll(neverTerminated, check)

	if obs.outcome != gateGreen {
		t.Fatalf("outcome = %v, want gateGreen", obs.outcome)
	}
	if !obs.windowElapsed {
		t.Errorf("windowElapsed = false, want true (clamped window elapsed before any genuine evidence)")
	}
	if obs.sawNonTerminal {
		t.Errorf("sawNonTerminal = true, want false (only SUCCESS was ever observed)")
	}
	if obs.elapsed != 1 {
		t.Errorf("elapsed = %d, want 1", obs.elapsed)
	}
	want := []time.Duration{1 * time.Second, 1 * time.Second}
	if len(*sleeps) != len(want) {
		t.Fatalf("recorded %d sleeps, want %d: got %v", len(*sleeps), len(want), *sleeps)
	}
}

// TestWatchPoll_WindowFallbackVsGenuineRegistration covers the split between
// trusting SUCCESS because the registration window elapsed on nothing but
// SUCCESS (windowElapsed true, sawNonTerminal false) versus trusting it
// because a genuine non-terminal state registered this run's own checks
// before the window elapsed (sawNonTerminal true, windowElapsed false).
func TestWatchPoll_WindowFallbackVsGenuineRegistration(t *testing.T) {
	t.Run("window elapses on SUCCESS-only sequence", func(t *testing.T) {
		_, clock := recordingClock()
		w := watch{pollInterval: 1, deadline: 10, requireRegistration: true, clock: clock}
		check := scriptedCheckState([]forge.RollupState{forge.StateSuccess}, nil)

		obs := w.poll(neverTerminated, check)

		if obs.outcome != gateGreen {
			t.Fatalf("outcome = %v, want gateGreen", obs.outcome)
		}
		if obs.sawNonTerminal {
			t.Errorf("sawNonTerminal = true, want false")
		}
		if !obs.windowElapsed {
			t.Errorf("windowElapsed = false, want true")
		}
	})

	t.Run("genuine PENDING observed before window elapses", func(t *testing.T) {
		_, clock := recordingClock()
		w := watch{pollInterval: 1, deadline: 10, requireRegistration: true, clock: clock}
		check := scriptedCheckState(
			[]forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			nil,
		)

		obs := w.poll(neverTerminated, check)

		if obs.outcome != gateGreen {
			t.Fatalf("outcome = %v, want gateGreen", obs.outcome)
		}
		if !obs.sawNonTerminal {
			t.Errorf("sawNonTerminal = false, want true (PENDING observed)")
		}
		if obs.windowElapsed {
			t.Errorf("windowElapsed = true, want false (registered on genuine evidence, not the window fallback)")
		}
	})
}

// TestWatchPoll_NoRegistrationRequired_FirstPollConfirmsImmediately verifies
// the ordinary requireRegistration=false path: a first-poll SUCCESS confirms
// green without any window logic getting involved.
func TestWatchPoll_NoRegistrationRequired_FirstPollConfirmsImmediately(t *testing.T) {
	_, clock := recordingClock()
	w := watch{pollInterval: 1, deadline: 10, clock: clock}
	check := scriptedCheckState([]forge.RollupState{forge.StateSuccess, forge.StateSuccess}, nil)

	obs := w.poll(neverTerminated, check)

	if obs.outcome != gateGreen {
		t.Fatalf("outcome = %v, want gateGreen", obs.outcome)
	}
	if obs.elapsed != 0 {
		t.Errorf("elapsed = %d, want 0 (confirmed on the very first poll)", obs.elapsed)
	}
	if obs.sawNonTerminal || obs.windowElapsed {
		t.Errorf("sawNonTerminal=%v windowElapsed=%v, want both false", obs.sawNonTerminal, obs.windowElapsed)
	}
}

// TestWatchPoll_CheckStateError_FirstPoll verifies a CheckState error on the
// very first poll surfaces as gateTerminal with the error attached.
func TestWatchPoll_CheckStateError_FirstPoll(t *testing.T) {
	_, clock := recordingClock()
	w := watch{pollInterval: 1, deadline: 10, clock: clock}
	wantErr := errors.New("boom")
	check := scriptedCheckState([]forge.RollupState{""}, map[int]error{0: wantErr})

	obs := w.poll(neverTerminated, check)

	if obs.outcome != gateTerminal {
		t.Fatalf("outcome = %v, want gateTerminal", obs.outcome)
	}
	if !errors.Is(obs.err, wantErr) {
		t.Fatalf("err = %v, want %v", obs.err, wantErr)
	}
}

// TestWatchPoll_CheckStateError_ConfirmationPoll verifies a CheckState error
// on the confirmation re-poll (after an initial SUCCESS) also surfaces as
// gateTerminal with the error attached.
func TestWatchPoll_CheckStateError_ConfirmationPoll(t *testing.T) {
	_, clock := recordingClock()
	w := watch{pollInterval: 1, deadline: 10, clock: clock}
	wantErr := errors.New("boom-confirm")
	check := scriptedCheckState(
		[]forge.RollupState{forge.StateSuccess, ""},
		map[int]error{1: wantErr},
	)

	obs := w.poll(neverTerminated, check)

	if obs.outcome != gateTerminal {
		t.Fatalf("outcome = %v, want gateTerminal", obs.outcome)
	}
	if !errors.Is(obs.err, wantErr) {
		t.Fatalf("err = %v, want %v", obs.err, wantErr)
	}
}

// TestWatchPoll_Terminated verifies that terminated() returning true
// immediately yields gateAbandoned without ever calling checkState.
func TestWatchPoll_Terminated(t *testing.T) {
	_, clock := recordingClock()
	w := watch{pollInterval: 1, deadline: 10, clock: clock}
	called := false
	check := func() (forge.RollupState, error) {
		called = true
		return forge.StateSuccess, nil
	}

	obs := w.poll(func() bool { return true }, check)

	if obs.outcome != gateAbandoned {
		t.Fatalf("outcome = %v, want gateAbandoned", obs.outcome)
	}
	if called {
		t.Errorf("checkState was called, want it never called once terminated fires")
	}
}

// TestWatchPoll_GenuineRed verifies a FAILURE/ERROR rollup returns
// gateRedRetry immediately, without consulting the registration guard at
// all.
func TestWatchPoll_GenuineRed(t *testing.T) {
	_, clock := recordingClock()
	w := watch{pollInterval: 1, deadline: 10, requireRegistration: true, clock: clock}
	check := scriptedCheckState([]forge.RollupState{forge.StateFailure}, nil)

	obs := w.poll(neverTerminated, check)

	if obs.outcome != gateRedRetry {
		t.Fatalf("outcome = %v, want gateRedRetry", obs.outcome)
	}
	if obs.sawNonTerminal || obs.windowElapsed {
		t.Errorf("sawNonTerminal=%v windowElapsed=%v, want both false (returned before the window ever elapsed)", obs.sawNonTerminal, obs.windowElapsed)
	}
}
