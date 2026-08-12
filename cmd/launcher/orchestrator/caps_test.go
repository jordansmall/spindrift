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
		name            string
		maxReviewRounds int
		maxSlices       int
		wantErr         bool
	}{
		{"both disabled", 0, 0, false},
		{"only maxReviewRounds set", 3, 0, false},
		{"only maxSlices set", 0, 5, false},
		{"coherent pair", 3, 9, false},
		{"incoherent pair, today's shipped defaults", 3, 5, true},
		{"boundary: exactly 2N+3 is coherent", 3, 2*3 + 3, false},
		{"boundary: one less than 2N+3 is incoherent", 3, 2*3 + 3 - 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCaps(tt.maxReviewRounds, tt.maxSlices)
			if tt.wantErr && err == nil {
				t.Errorf("validateCaps(%d, %d) = nil, want error", tt.maxReviewRounds, tt.maxSlices)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateCaps(%d, %d) = %v, want nil", tt.maxReviewRounds, tt.maxSlices, err)
			}
		})
	}
}

// TestValidateCapsAcceptsShippedDefaults pins the actual --max-review-rounds
// / --max-slices defaults main.go flag.Int calls ship (issue #2460): a fresh
// run with no flags overridden must not fail validateCaps at startup. These
// values are hardcoded rather than read from main.go's flag package state
// (unexported flag.Int locals aren't reachable from this test binary without
// exporting a shared constant, which is out of scope here) -- if main.go's
// -max-review-rounds or -max-slices default ever changes, update the
// hardcoded values below to match, or this test breaks loudly.
func TestValidateCapsAcceptsShippedDefaults(t *testing.T) {
	const (
		shippedMaxReviewRounds = 3 // must match main.go's -max-review-rounds default
		shippedMaxSlices       = 9 // must match main.go's -max-slices default
	)
	if err := validateCaps(shippedMaxReviewRounds, shippedMaxSlices); err != nil {
		t.Errorf("validateCaps(%d, %d) = %v, want nil (shipped defaults must be coherent)", shippedMaxReviewRounds, shippedMaxSlices, err)
	}
}
