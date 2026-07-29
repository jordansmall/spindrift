package main

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/waves"
)

// TestExitCodeFor verifies the full error-to-exit-code mapping, including
// the new exit 5 for errImageHostTainted (issue #2113) alongside the
// existing sentinels.
func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"errQueueEmpty", errQueueEmpty, 2},
		{"ErrOpenNoneDispatchable", waves.ErrOpenNoneDispatchable, 3},
		{"ErrImageStale", waves.ErrImageStale, 4},
		{"errImageHostTainted", errImageHostTainted, 5},
		{"other error", errors.New("boom"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}
