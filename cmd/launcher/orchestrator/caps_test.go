package main

import "testing"

// TestValidateCaps guards the incoherent-cap-pair detection (issue #2460):
// runWithReviewPass's maxSlices case (run.go's switch) is checked ahead of
// its maxReviewRounds case, so a maxSlices value too small to ever let
// reviewRounds reach maxReviewRounds silently shadows the review-round cap
// instead of surfacing it as the stop reason. validateCaps rejects such a
// pair at startup instead of letting the loop silently misattribute why it
// stopped. The reachability math (2N+3 total invocations to let the review
// pass at reviewRounds==N actually fire, N=maxReviewRounds) is spelled out
// in run.go's own comments; this test just pins the boundary.
func TestValidateCaps(t *testing.T) {
	tests := []struct {
		name              string
		maxReviewRounds   int
		maxSlices         int
		reviewPassEnabled bool
		wantErr           bool
	}{
		{"both disabled", 0, 0, true, false},
		{"only maxReviewRounds set", 3, 0, true, false},
		{"only maxSlices set", 0, 5, true, false},
		{"coherent pair", 3, 9, true, false},
		{"incoherent pair, today's shipped defaults", 3, 5, true, true},
		{"boundary: exactly 2N+3 is coherent", 3, 2*3 + 3, true, false},
		{"boundary: one less than 2N+3 is incoherent", 3, 2*3 + 3 - 1, true, true},
		{"legacy: coherent pair matching real loop math", 3, 5, false, false},
		{"legacy: boundary exactly N+2 is coherent", 3, 5, false, false},
		{"legacy: boundary one less than N+2 is incoherent", 3, 4, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCaps(tt.maxReviewRounds, tt.maxSlices, tt.reviewPassEnabled)
			if tt.wantErr && err == nil {
				t.Errorf("validateCaps(%d, %d, %v) = nil, want error", tt.maxReviewRounds, tt.maxSlices, tt.reviewPassEnabled)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateCaps(%d, %d, %v) = %v, want nil", tt.maxReviewRounds, tt.maxSlices, tt.reviewPassEnabled, err)
			}
		})
	}
}

// TestValidateCapsAcceptsShippedDefaults pins the actual --max-review-rounds
// / --max-slices defaults main.go's flag.Int calls ship (issue #2460): a
// fresh run with no flags overridden must not fail validateCaps at startup.
// It references defaultMaxReviewRounds / defaultMaxSlices directly (caps.go,
// same package) rather than hardcoding duplicate values, so a future change
// to those constants can't drift out of sync with this test silently -- a
// later slice wires main.go's flag.Int calls to the same constants.
func TestValidateCapsAcceptsShippedDefaults(t *testing.T) {
	// true: the shipped defaults are tuned for the review-pass loop, which
	// is what ships by default.
	if err := validateCaps(defaultMaxReviewRounds, defaultMaxSlices, true); err != nil {
		t.Errorf("validateCaps(%d, %d, true) = %v, want nil (shipped defaults must be coherent)", defaultMaxReviewRounds, defaultMaxSlices, err)
	}
}
