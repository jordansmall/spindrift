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

// alwaysTerminated is a terminated func that fires on its very first call.
func alwaysTerminated() bool { return true }

// terminatedAfter returns a terminated func that returns false for its
// first n calls and true from call n+1 onward, letting a test allow exactly
// n polls to happen (observing their evidence) before abandonment fires.
func terminatedAfter(n int) func() bool {
	calls := 0
	return func() bool {
		calls++
		return calls > n
	}
}

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

// countingCheckState wraps a scripted checkState func, counting how many
// times it was called so a test can assert checkState was (or was not)
// invoked a specific number of times.
type countingCheckState struct {
	fn    func() (forge.RollupState, error)
	calls int
}

func newCountingCheckState(states []forge.RollupState, errAt map[int]error) *countingCheckState {
	return &countingCheckState{fn: scriptedCheckState(states, errAt)}
}

func (c *countingCheckState) check() (forge.RollupState, error) {
	c.calls++
	return c.fn()
}

// watchPollCase is one table-driven scenario for watch.poll's state
// machine: interval/window arithmetic, the registration split, error
// propagation, and abandonment.
type watchPollCase struct {
	name string

	pollInterval        int
	deadline            int
	requireRegistration bool

	states []forge.RollupState
	errAt  map[int]error

	terminated func() bool // defaults to neverTerminated when nil

	wantOutcome gateResult
	wantErr     error

	wantSawNonTerminal bool
	wantWindowElapsed  bool

	checkElapsed bool
	wantElapsed  int

	checkSleeps bool
	wantSleeps  []time.Duration

	// useCounting routes checkState through countingCheckState so
	// wantCallCount can be asserted.
	useCounting    bool
	checkCallCount bool
	wantCallCount  int
}

func TestWatchPoll(t *testing.T) {
	cases := []watchPollCase{
		{
			// A zero pollInterval still floors to 1 for elapsed tracking, so
			// the loop advances and terminates at the deadline instead of
			// hot-spinning forever.
			name:               "actualIv floors to 1 when pollInterval is 0",
			pollInterval:       0,
			deadline:           2,
			states:             []forge.RollupState{forge.StatePending},
			wantOutcome:        gateTerminal,
			wantSawNonTerminal: true,
			wantWindowElapsed:  false,
			checkElapsed:       true,
			wantElapsed:        2,
		},
		{
			// A deadline smaller than registrationWindowPolls*actualIv still
			// lets a settled-SUCCESS-only sequence resolve to gateGreen with
			// windowElapsed true once the deadline-clamped window elapses,
			// instead of falling through to gateTerminal (issue #2475
			// follow-up).
			name:                "registration window clamps to a smaller deadline",
			pollInterval:        1,
			deadline:            1,
			requireRegistration: true,
			states:              []forge.RollupState{forge.StateSuccess},
			wantOutcome:         gateGreen,
			wantSawNonTerminal:  false,
			wantWindowElapsed:   true,
			checkElapsed:        true,
			wantElapsed:         1,
			checkSleeps:         true,
			wantSleeps:          []time.Duration{1 * time.Second, 1 * time.Second},
		},
		{
			// The registration window elapsing on a SUCCESS-only sequence
			// (windowElapsed true, sawNonTerminal false) is trusted as proof
			// CI already finished.
			name:                "window elapses on SUCCESS-only sequence",
			pollInterval:        1,
			deadline:            10,
			requireRegistration: true,
			states:              []forge.RollupState{forge.StateSuccess},
			wantOutcome:         gateGreen,
			wantSawNonTerminal:  false,
			wantWindowElapsed:   true,
		},
		{
			// A genuine non-terminal state observed before the window
			// elapses registers this run's own checks on real evidence
			// (sawNonTerminal true, windowElapsed false) rather than via the
			// window fallback.
			name:                "genuine PENDING observed before window elapses",
			pollInterval:        1,
			deadline:            10,
			requireRegistration: true,
			states:              []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess},
			wantOutcome:         gateGreen,
			wantSawNonTerminal:  true,
			wantWindowElapsed:   false,
		},
		{
			// The ordinary requireRegistration=false path: a first-poll
			// SUCCESS confirms green without any window logic getting
			// involved.
			name:               "no registration required confirms on first poll",
			pollInterval:       1,
			deadline:           10,
			states:             []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			wantOutcome:        gateGreen,
			wantSawNonTerminal: false,
			wantWindowElapsed:  false,
			checkElapsed:       true,
			wantElapsed:        0,
		},
		{
			// A CheckState error on the very first poll surfaces as
			// gateTerminal with the error attached.
			name:         "CheckState error on first poll",
			pollInterval: 1,
			deadline:     10,
			states:       []forge.RollupState{""},
			errAt:        map[int]error{0: errFirstPollBoom},
			wantOutcome:  gateTerminal,
			wantErr:      errFirstPollBoom,
		},
		{
			// A CheckState error on the confirmation re-poll (after an
			// initial SUCCESS) also surfaces as gateTerminal with the error
			// attached.
			name:         "CheckState error on confirmation poll",
			pollInterval: 1,
			deadline:     10,
			states:       []forge.RollupState{forge.StateSuccess, ""},
			errAt:        map[int]error{1: errConfirmPollBoom},
			wantOutcome:  gateTerminal,
			wantErr:      errConfirmPollBoom,
		},
		{
			// terminated() returning true immediately yields gateAbandoned
			// without ever calling checkState.
			name:           "terminated before any poll never calls checkState",
			pollInterval:   1,
			deadline:       10,
			states:         []forge.RollupState{forge.StateSuccess},
			terminated:     alwaysTerminated,
			wantOutcome:    gateAbandoned,
			useCounting:    true,
			checkCallCount: true,
			wantCallCount:  0,
		},
		{
			// A FAILURE/ERROR rollup returns gateRedRetry immediately,
			// without consulting the registration guard at all.
			name:                "genuine red returns immediately",
			pollInterval:        1,
			deadline:            10,
			requireRegistration: true,
			states:              []forge.RollupState{forge.StateFailure},
			wantOutcome:         gateRedRetry,
			wantSawNonTerminal:  false,
			wantWindowElapsed:   false,
		},
		{
			// Abandonment after a couple of successful polls have already
			// observed non-terminal evidence must carry that accumulated
			// evidence through in the returned observation
			// (sawNonTerminal=true, elapsed=2), not a zero-value literal.
			name:               "abandonment after prior polls preserves accumulated evidence",
			pollInterval:       1,
			deadline:           10,
			states:             []forge.RollupState{forge.StatePending, forge.StatePending, forge.StatePending},
			terminated:         terminatedAfter(2),
			wantOutcome:        gateAbandoned,
			wantSawNonTerminal: true,
			wantWindowElapsed:  false,
			checkElapsed:       true,
			wantElapsed:        2,
			useCounting:        true,
			checkCallCount:     true,
			wantCallCount:      2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sleeps, clock := recordingClock()
			w := watch{
				pollInterval:        tc.pollInterval,
				deadline:            tc.deadline,
				requireRegistration: tc.requireRegistration,
				clock:               clock,
			}

			terminated := tc.terminated
			if terminated == nil {
				terminated = neverTerminated
			}

			var counter *countingCheckState
			var check func() (forge.RollupState, error)
			if tc.useCounting {
				counter = newCountingCheckState(tc.states, tc.errAt)
				check = counter.check
			} else {
				check = scriptedCheckState(tc.states, tc.errAt)
			}

			obs := w.poll(terminated, check)

			if obs.outcome != tc.wantOutcome {
				t.Fatalf("outcome = %v, want %v", obs.outcome, tc.wantOutcome)
			}
			if tc.wantErr != nil {
				if !errors.Is(obs.err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", obs.err, tc.wantErr)
				}
			}
			if obs.sawNonTerminal != tc.wantSawNonTerminal {
				t.Errorf("sawNonTerminal = %v, want %v", obs.sawNonTerminal, tc.wantSawNonTerminal)
			}
			if obs.windowElapsed != tc.wantWindowElapsed {
				t.Errorf("windowElapsed = %v, want %v", obs.windowElapsed, tc.wantWindowElapsed)
			}
			if tc.checkElapsed && obs.elapsed != tc.wantElapsed {
				t.Errorf("elapsed = %d, want %d", obs.elapsed, tc.wantElapsed)
			}
			if tc.checkSleeps {
				if len(*sleeps) != len(tc.wantSleeps) {
					t.Fatalf("recorded %d sleeps, want %d: got %v", len(*sleeps), len(tc.wantSleeps), *sleeps)
				}
				for i, d := range tc.wantSleeps {
					if (*sleeps)[i] != d {
						t.Errorf("sleep[%d] = %v, want %v", i, (*sleeps)[i], d)
					}
				}
			}
			if tc.checkCallCount && counter.calls != tc.wantCallCount {
				t.Errorf("checkState called %d times, want %d", counter.calls, tc.wantCallCount)
			}
		})
	}
}

var (
	errFirstPollBoom   = errors.New("boom")
	errConfirmPollBoom = errors.New("boom-confirm")
)
