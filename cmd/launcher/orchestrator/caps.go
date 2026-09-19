package main

import (
	"fmt"
	"math"

	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/promptassembly"
)

// defaultMaxReviewRounds and defaultMaxSlices alias the promptassembly
// defaults so this package's coherence test and assemble-prompt's own flag
// defaults, which populate Handoff.Caps, cannot drift apart (issues #2460,
// #2975).
const (
	defaultMaxReviewRounds = promptassembly.DefaultMaxReviewRounds
	defaultMaxSlices       = promptassembly.DefaultMaxSlices
)

// validateCaps rejects a (maxReviewRounds, maxSlices) pair where maxSlices
// is too small for reviewRounds to ever reach maxReviewRounds, so maxSlices
// fires first and the loop stops for the wrong reason (issue #2460). The
// threshold comes from simulating the real passmachine loop, not a formula,
// which drifted out of sync once (issue #2548). Zero disables either cap.
func validateCaps(maxReviewRounds, maxSlices int, reviewPassEnabled bool) error {
	if maxReviewRounds <= 0 || maxSlices <= 0 {
		return nil
	}
	simPass, err := simulateReviewRoundCapPass(maxReviewRounds, reviewPassEnabled)
	if err != nil {
		return err
	}
	minSlices := simPass + 1
	if maxSlices < minSlices {
		return fmt.Errorf("orchestrator: -max-slices=%d cannot reach -max-review-rounds=%d (need -max-slices >= %d, or -max-slices=0/-max-review-rounds=0 to disable a cap)", maxSlices, maxReviewRounds, minSlices)
	}
	return nil
}

// simulateReviewRoundCapPass returns the 1-indexed pass at which the
// review-round cap fires for maxReviewRounds (issue #2548). It probes caps
// 1 and 2 and extrapolates the per-round cost linearly to stay O(1):
// looping Transition maxReviewRounds times would let an operator-supplied
// cap, including a mistyped MaxInt, drive this function's own runtime.
func simulateReviewRoundCapPass(maxReviewRounds int, reviewPassEnabled bool) (int, error) {
	pass1, err := capFiredPass(1, reviewPassEnabled)
	if err != nil {
		return 0, err
	}
	if maxReviewRounds == 1 {
		return pass1, nil
	}
	pass2, err := capFiredPass(2, reviewPassEnabled)
	if err != nil {
		return 0, err
	}
	perRound := pass2 - pass1
	if perRound <= 0 {
		return 0, fmt.Errorf("orchestrator: internal error: cap probe found a non-positive per-round pass cost (%d) between review-round caps 1 and 2", perRound)
	}
	extraRounds := maxReviewRounds - 1
	if extraRounds > (math.MaxInt-pass1)/perRound {
		// The real threshold would overflow int, so return MaxInt-1:
		// validateCaps' minSlices then lands exactly on MaxInt and the
		// comparison still rejects the pair instead of passing on a wrapped
		// negative.
		return math.MaxInt - 1, nil
	}
	return pass1 + extraRounds*perRound, nil
}

// capFiredPass drives passmachine.Transition with maxSlices disabled and a
// reviewer that always BLOCKs until the review-round cap fires, returning
// the 1-indexed pass. probeMaxReviewRounds is only ever 1 or 2, so the loop
// is bounded regardless of the operator's cap; probeMaxPasses and the
// Continue check catch a Transition that stops earlier (issue #2548).
func capFiredPass(probeMaxReviewRounds int, reviewPassEnabled bool) (int, error) {
	const probeMaxPasses = 64

	caps := passmachine.Caps{MaxSlices: 0, MaxReviewRounds: probeMaxReviewRounds}
	pass := 0
	reviewRounds := 0

	if !reviewPassEnabled {
		for {
			pass++
			if pass > probeMaxPasses {
				return 0, fmt.Errorf("orchestrator: internal error: legacy-loop cap probe (max-review-rounds=%d) did not reach the review-round cap within %d passes", probeMaxReviewRounds, probeMaxPasses)
			}
			d := passmachine.Transition(passmachine.Input{
				PassJustExecuted: passmachine.KindLegacy,
				Verdict:          passmachine.VerdictBlock,
				Pass:             pass,
				ReviewRounds:     reviewRounds,
				Caps:             caps,
			})
			if d.Stop == passmachine.StopMaxReviewRoundsReached {
				return pass, nil
			}
			if !d.Continue {
				return 0, fmt.Errorf("orchestrator: internal error: legacy-loop cap probe (max-review-rounds=%d) stopped unexpectedly at pass %d: %s", probeMaxReviewRounds, pass, d.Reason)
			}
			if d.IncrementReviewRounds {
				reviewRounds++
			}
		}
	}

	passKind := passmachine.KindImplement
	// Named phase so it does not shadow run.go's package-level landPhase()
	// helper (issue #2548 review).
	phase := passmachine.LandPhaseActive
	lastVerdict := passmachine.VerdictNone
	for {
		pass++
		if pass > probeMaxPasses {
			return 0, fmt.Errorf("orchestrator: internal error: review-pass-loop cap probe (max-review-rounds=%d) did not reach the review-round cap within %d passes", probeMaxReviewRounds, probeMaxPasses)
		}
		var d passmachine.Decision
		if passKind == passmachine.KindReview {
			d = passmachine.Transition(passmachine.Input{
				PassJustExecuted: passmachine.KindReview,
				Verdict:          passmachine.VerdictBlock,
				Pass:             pass,
				ReviewRounds:     reviewRounds,
				Caps:             caps,
				LandPhase:        phase,
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
				LandPhase:        phase,
				LastVerdict:      lastVerdict,
			})
		}
		// Compare the typed Cap field, not CapFired's prose string: that
		// string doubles as operator-facing prompt text and can be reworded
		// independently of this comparison (issue #2548 finding 3).
		if d.Cap == passmachine.StopMaxReviewRoundsReached {
			return pass, nil
		}
		if !d.Continue {
			return 0, fmt.Errorf("orchestrator: internal error: review-pass-loop cap probe (max-review-rounds=%d) stopped unexpectedly at pass %d: %s", probeMaxReviewRounds, pass, d.Reason)
		}
		if d.LandPhase == passmachine.LandPhaseTerminalCommitted {
			phase = passmachine.LandPhaseTerminalCommitted
		}
		passKind = d.NextPass
	}
}
