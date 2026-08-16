package main

import "testing"

// TestSimulateReviewRoundCapPass pins simulateReviewRoundCapPass's own
// pass-count arithmetic against passmachine.Transition (issue #2548) --
// caps_test.go's TestValidateCaps boundary values (minSlices = 2N+3 for the
// review-pass loop, N+2 for the legacy loop) both derive from
// simulateReviewRoundCapPass(N, ...)+1, so this test pins the values that
// validateCaps's own arithmetic ultimately rests on.
func TestSimulateReviewRoundCapPass(t *testing.T) {
	tests := []struct {
		name              string
		maxReviewRounds   int
		reviewPassEnabled bool
		want              int
	}{
		{"review-pass loop, N=3", 3, true, 8},
		{"legacy loop, N=3", 3, false, 4},
		{"review-pass loop, N=1", 1, true, 4},
		{"legacy loop, N=1", 1, false, 2},
		{"review-pass loop, N=5", 5, true, 12},
		{"legacy loop, N=5", 5, false, 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := simulateReviewRoundCapPass(tt.maxReviewRounds, tt.reviewPassEnabled)
			if got != tt.want {
				t.Errorf("simulateReviewRoundCapPass(%d, %v) = %d, want %d", tt.maxReviewRounds, tt.reviewPassEnabled, got, tt.want)
			}
		})
	}
}
