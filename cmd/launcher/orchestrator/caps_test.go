package main

import "testing"

// TestValidateCaps guards the incoherent-cap-pair detection (issue #2460):
// run.go's switch checks maxSlices ahead of maxReviewRounds, so a maxSlices
// value too small to ever let reviewRounds reach maxReviewRounds silently
// shadows the review-round cap as the stop reason. run.go spells out the
// 2N+3 reachability math; this test pins the boundary.
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

// TestValidateCapsAcceptsShippedDefaults pins the --max-review-rounds and
// --max-slices defaults main.go ships (issue #2460): a fresh run with no
// flags overridden must not fail validateCaps at startup. It reads
// defaultMaxReviewRounds and defaultMaxSlices from caps.go rather than
// hardcoding copies, so the constants cannot drift out of sync with the test.
func TestValidateCapsAcceptsShippedDefaults(t *testing.T) {
	// true: the shipped defaults are tuned for the review-pass loop.
	if err := validateCaps(defaultMaxReviewRounds, defaultMaxSlices, true); err != nil {
		t.Errorf("validateCaps(%d, %d, true) = %v, want nil (shipped defaults must be coherent)", defaultMaxReviewRounds, defaultMaxSlices, err)
	}
}
