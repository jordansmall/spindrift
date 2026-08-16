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

// TestValidateMaxParallelWorkers guards the fail-fast check on
// -max-parallel-workers (issue #2495): unlike validateCaps' 0-means-disabled
// convention, a concurrency semaphore's capacity has no meaningful
// "disabled" value, so any value <= 0 is rejected.
func TestValidateMaxParallelWorkers(t *testing.T) {
	tests := []struct {
		name               string
		maxParallelWorkers int
		wantErr            bool
	}{
		{"zero is rejected", 0, true},
		{"negative is rejected", -1, true},
		{"one is accepted", 1, false},
		{"two is accepted", 2, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMaxParallelWorkers(tt.maxParallelWorkers)
			if tt.wantErr && err == nil {
				t.Errorf("validateMaxParallelWorkers(%d) = nil, want error", tt.maxParallelWorkers)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateMaxParallelWorkers(%d) = %v, want nil", tt.maxParallelWorkers, err)
			}
		})
	}
}

// TestValidateMaxParallelWorkersAcceptsShippedDefault pins the actual
// -max-parallel-workers default main.go's flag.Int call ships
// (defaultMaxParallelWorkers, workers.go): a fresh run with no flag override
// must not fail validateMaxParallelWorkers at startup.
func TestValidateMaxParallelWorkersAcceptsShippedDefault(t *testing.T) {
	if err := validateMaxParallelWorkers(defaultMaxParallelWorkers); err != nil {
		t.Errorf("validateMaxParallelWorkers(%d) = %v, want nil (shipped default must be valid)", defaultMaxParallelWorkers, err)
	}
}

// TestValidateBudgetCaps guards the fail-fast check on -max-budget-tokens/
// -max-budget-usd (issue #2694 review finding): unlike
// -max-parallel-workers, 0 IS accepted here -- budget's own legitimate
// "disabled" sentinel -- but a negative value is rejected, since
// budgetExceeded's own >= comparison would otherwise silently never fire
// against it, behaving exactly like (and so masking) a disabled cap.
func TestValidateBudgetCaps(t *testing.T) {
	tests := []struct {
		name            string
		maxBudgetTokens int
		maxBudgetUSD    float64
		wantErr         bool
	}{
		{"both zero (disabled) is accepted", 0, 0, false},
		{"positive tokens, zero usd is accepted", 100, 0, false},
		{"zero tokens, positive usd is accepted", 0, 4.44, false},
		{"negative tokens is rejected", -1, 0, true},
		{"negative usd is rejected", 0, -0.01, true},
		{"both negative is rejected", -1, -0.01, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBudgetCaps(tt.maxBudgetTokens, tt.maxBudgetUSD)
			if tt.wantErr && err == nil {
				t.Errorf("validateBudgetCaps(%d, %v) = nil, want error", tt.maxBudgetTokens, tt.maxBudgetUSD)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateBudgetCaps(%d, %v) = %v, want nil", tt.maxBudgetTokens, tt.maxBudgetUSD, err)
			}
		})
	}
}
