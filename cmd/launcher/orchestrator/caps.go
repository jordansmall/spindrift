package main

import (
	"fmt"
	"math"

	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/promptassembly"
)

// The orchestrator's shipped --max-review-rounds / --max-slices defaults,
// aliased from promptassembly so this package's coherence test and
// assemble-prompt's flag defaults (which populate Handoff.Caps) cannot drift
// apart.
const (
	defaultMaxReviewRounds = promptassembly.DefaultMaxReviewRounds
	defaultMaxSlices       = promptassembly.DefaultMaxSlices
)

// validateCaps rejects an incoherent (maxReviewRounds, maxSlices) pair: when
// maxSlices is too small to ever let reviewRounds reach maxReviewRounds, it
// always fires first and silently shadows the review-round cap, so the loop
// stops for the wrong reason. The threshold depends on reviewPassEnabled,
// since run.go dispatches between two loop shapes on cfg.reviewPromptFile.
//
// minSlices is simulated through passmachine.Transition rather than derived
// as a formula: the hand-derived formula this replaces drifted out of sync
// with the loop it described (issue #2548).
//
// Zero means "disabled" for either cap (run.go's convention), and a disabled
// cap can never be shadowed.
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

// simulateReviewRoundCapPass returns the 1-indexed pass count at which the
// review-round cap fires for the caller's maxReviewRounds.
//
// It probes at two small fixed caps (1 and 2) and extrapolates linearly
// rather than simulating maxReviewRounds rounds directly, so validateCaps'
// runtime stays O(1) in an operator-supplied -max-review-rounds -- which may
// be hostile or mistyped as large as MaxInt.
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
		// The real threshold would overflow int; return MaxInt-1 so
		// validateCaps' +1 lands exactly on MaxInt and the comparison fails
		// closed, rather than wrapping negative and silently accepting.
		return math.MaxInt - 1, nil
	}
	return pass1 + extraRounds*perRound, nil
}

// capFiredPass drives passmachine.Transition forward, pass by pass, with
// maxSlices disabled and a reviewer that always BLOCKs, returning the
// 1-indexed pass at which the review-round cap fires for
// probeMaxReviewRounds (only ever 1 or 2, so the loop is bounded).
//
// Below, the review-pass branch compares the typed d.Cap field, never
// d.CapFired's prose string -- CapFired doubles as operator-facing prompt
// text and can be reworded independently. probeMaxPasses guards against a
// future Transition change introducing an earlier-firing stop condition on
// this synthetic BLOCK-forever input, which would otherwise spin forever.
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
	// Named phase, not landPhase, so it doesn't shadow run.go's
	// package-level landPhase() helper.
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
