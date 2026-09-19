package settle

import (
	"errors"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

func neverTerminated() bool { return false }

func alwaysTerminated() bool { return true }

// terminatedAfter stays false for its first n calls, so a test can let
// exactly n polls happen, and their evidence accumulate, before abandonment
// fires.
func terminatedAfter(n int) func() bool {
	calls := 0
	return func() bool {
		calls++
		return calls > n
	}
}

// scriptedCheckState walks states in order, returning an errAt error instead
// of a state on those zero-based call indices. It repeats the final entry once
// the script runs out, so a test can let the loop run the deadline out without
// spelling every poll.
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

	// useCounting routes checkState through countingCheckState so a case can
	// assert wantCallCount.
	useCounting    bool
	checkCallCount bool
	wantCallCount  int
}

func TestWatchPoll(t *testing.T) {
	cases := []watchPollCase{
		{
			// Flooring a zero pollInterval to 1 keeps elapsed advancing, so the
			// loop ends at the deadline instead of hot-spinning forever.
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
			// A deadline smaller than registrationWindowPolls*actualIv clamps
			// the window, so a SUCCESS-only sequence still resolves to gateGreen
			// instead of falling through to gateTerminal (issue #2475 follow-up).
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
			// poll treats the window elapsing with no non-terminal state ever
			// seen as proof CI already finished.
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
			// Real non-terminal evidence registers this run's own checks, so the
			// window fallback stays unused.
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
			name:         "CheckState error on first poll",
			pollInterval: 1,
			deadline:     10,
			states:       []forge.RollupState{""},
			errAt:        map[int]error{0: errFirstPollBoom},
			wantOutcome:  gateTerminal,
			wantErr:      errFirstPollBoom,
		},
		{
			name:         "CheckState error on confirmation poll",
			pollInterval: 1,
			deadline:     10,
			states:       []forge.RollupState{forge.StateSuccess, ""},
			errAt:        map[int]error{1: errConfirmPollBoom},
			wantOutcome:  gateTerminal,
			wantErr:      errConfirmPollBoom,
		},
		{
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
			// poll returns gateRedRetry for a red rollup without consulting the
			// registration guard.
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
			// Abandonment must return the evidence the earlier polls gathered
			// (sawNonTerminal true, elapsed 2), not a zero-value observation.
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
