package main

import (
	"math"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/passmachine"
)

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
			got, err := simulateReviewRoundCapPass(tt.maxReviewRounds, tt.reviewPassEnabled)
			if err != nil {
				t.Fatalf("simulateReviewRoundCapPass(%d, %v) returned error: %v", tt.maxReviewRounds, tt.reviewPassEnabled, err)
			}
			if got != tt.want {
				t.Errorf("simulateReviewRoundCapPass(%d, %v) = %d, want %d", tt.maxReviewRounds, tt.reviewPassEnabled, got, tt.want)
			}
		})
	}
}

// TestSimulateReviewRoundCapPassBoundedRuntime is the issue #2548 finding 1
// regression test: simulateReviewRoundCapPass used to loop maxReviewRounds
// times directly, so a huge -max-review-rounds value (an operator typo, or
// an adversarial one) made validateCaps hang at orchestrator startup
// instead of returning promptly. math.MaxInt32 passes would have taken the
// old O(N) implementation on the order of 2^31 passmachine.Transition
// calls; the probe-and-extrapolate rewrite is O(1) in maxReviewRounds, so
// this must return within a few seconds regardless.
func TestSimulateReviewRoundCapPassBoundedRuntime(t *testing.T) {
	for _, reviewPassEnabled := range []bool{true, false} {
		reviewPassEnabled := reviewPassEnabled
		t.Run("", func(t *testing.T) {
			type result struct {
				pass int
				err  error
			}
			done := make(chan result, 1)
			go func() {
				pass, err := simulateReviewRoundCapPass(math.MaxInt32, reviewPassEnabled)
				done <- result{pass, err}
			}()
			select {
			case r := <-done:
				if r.err != nil {
					t.Errorf("simulateReviewRoundCapPass(MaxInt32, %v) returned error: %v", reviewPassEnabled, r.err)
				}
				if r.pass <= 0 {
					t.Errorf("simulateReviewRoundCapPass(MaxInt32, %v) = %d, want a positive pass count", reviewPassEnabled, r.pass)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("simulateReviewRoundCapPass(MaxInt32, %v) did not return within 5s -- not O(1) in maxReviewRounds", reviewPassEnabled)
			}
		})
	}
}

// groundTruthCapFiredPass independently drives passmachine.Transition
// forward pass by pass -- the pre-#2548-fix O(N) shape, run here only up to
// a small, test-scoped maxReviewRounds -- to serve as ground truth for
// TestSimulateReviewRoundCapPassLinearity below. It deliberately does NOT
// call capFiredPass/simulateReviewRoundCapPass: reusing the production
// probe helper here would only prove the extrapolation is self-consistent
// with itself, not that it actually reproduces passmachine.Transition's
// real per-round pass cost.
func groundTruthCapFiredPass(t *testing.T, maxReviewRounds int, reviewPassEnabled bool) int {
	t.Helper()
	const groundTruthMaxPasses = 1000

	caps := passmachine.Caps{MaxSlices: 0, MaxReviewRounds: maxReviewRounds}
	pass := 0
	reviewRounds := 0

	if !reviewPassEnabled {
		for {
			pass++
			if pass > groundTruthMaxPasses {
				t.Fatalf("groundTruthCapFiredPass(%d, %v): exceeded %d passes without the cap firing", maxReviewRounds, reviewPassEnabled, groundTruthMaxPasses)
			}
			d := passmachine.Transition(passmachine.Input{
				PassJustExecuted: passmachine.KindLegacy,
				Verdict:          passmachine.VerdictBlock,
				Pass:             pass,
				ReviewRounds:     reviewRounds,
				Caps:             caps,
			})
			if d.Stop == passmachine.StopMaxReviewRoundsReached {
				return pass
			}
			if !d.Continue {
				t.Fatalf("groundTruthCapFiredPass(%d, %v): stopped unexpectedly at pass %d: %s", maxReviewRounds, reviewPassEnabled, pass, d.Reason)
			}
			if d.IncrementReviewRounds {
				reviewRounds++
			}
		}
	}

	passKind := passmachine.KindImplement
	landPhase := passmachine.LandPhaseActive
	lastVerdict := passmachine.VerdictNone
	for {
		pass++
		if pass > groundTruthMaxPasses {
			t.Fatalf("groundTruthCapFiredPass(%d, %v): exceeded %d passes without the cap firing", maxReviewRounds, reviewPassEnabled, groundTruthMaxPasses)
		}
		var d passmachine.Decision
		if passKind == passmachine.KindReview {
			d = passmachine.Transition(passmachine.Input{
				PassJustExecuted: passmachine.KindReview,
				Verdict:          passmachine.VerdictBlock,
				Pass:             pass,
				ReviewRounds:     reviewRounds,
				Caps:             caps,
				LandPhase:        landPhase,
			})
			if d.IncrementReviewRounds {
				reviewRounds++
			}
		} else {
			d = passmachine.Transition(passmachine.Input{
				PassJustExecuted: passKind,
				HasOutcome:       false,
				Pass:             pass,
				Caps:             caps,
				LandPhase:        landPhase,
				LastVerdict:      lastVerdict,
			})
		}
		if d.Cap == passmachine.StopMaxReviewRoundsReached {
			return pass
		}
		if !d.Continue {
			t.Fatalf("groundTruthCapFiredPass(%d, %v): stopped unexpectedly at pass %d: %s", maxReviewRounds, reviewPassEnabled, pass, d.Reason)
		}
		if d.LandPhase == passmachine.LandPhaseTerminalCommitted {
			landPhase = passmachine.LandPhaseTerminalCommitted
		}
		passKind = d.NextPass
	}
}

// TestSimulateReviewRoundCapPassLinearity checks simulateReviewRoundCapPass's
// probe-and-extrapolate result against groundTruthCapFiredPass's own
// independent, real drive-forward simulation across a range of
// maxReviewRounds values, for both loop shapes -- self-consistency evidence
// that the linear extrapolation (issue #2548 finding 1) actually reproduces
// passmachine.Transition's real per-round pass cost, without hardcoding a
// closed-form pass-count formula anywhere in this test.
func TestSimulateReviewRoundCapPassLinearity(t *testing.T) {
	const maxN = 15
	for _, reviewPassEnabled := range []bool{true, false} {
		for n := 1; n <= maxN; n++ {
			want := groundTruthCapFiredPass(t, n, reviewPassEnabled)
			got, err := simulateReviewRoundCapPass(n, reviewPassEnabled)
			if err != nil {
				t.Fatalf("simulateReviewRoundCapPass(%d, %v) returned error: %v", n, reviewPassEnabled, err)
			}
			if got != want {
				t.Errorf("simulateReviewRoundCapPass(%d, %v) = %d, want %d (ground truth)", n, reviewPassEnabled, got, want)
			}
		}
	}
}
