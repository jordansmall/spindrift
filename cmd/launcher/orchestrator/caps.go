package main

import (
	"fmt"
	"math"

	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/promptassembly"
)

// defaultMaxReviewRounds and defaultMaxSlices are the orchestrator's shipped
// --max-review-rounds / --max-slices defaults (issue #2460), aliased from
// promptassembly.DefaultMaxReviewRounds/DefaultMaxSlices so this package's
// own coherence test (TestValidateCapsAcceptsShippedDefaults) and
// assemble-prompt's own flag defaults (which populate Handoff.Caps) can
// never drift apart (issue #2975).
const (
	defaultMaxReviewRounds = promptassembly.DefaultMaxReviewRounds
	defaultMaxSlices       = promptassembly.DefaultMaxSlices
)

// validateCaps detects an incoherent (maxReviewRounds, maxSlices) pair
// (issue #2460). Two different driver loops can run depending on whether a
// review pass is configured (run.go's run(), around line 130, dispatches
// between them on cfg.reviewPromptFile), and each has its own reachability
// math for how many maxSlices invocations it takes to let reviewRounds
// actually reach maxReviewRounds -- so the minSlices threshold depends on
// reviewPassEnabled.
//
// Rather than hand-deriving that threshold as a formula (issue #2548: that
// formula silently drifted out of sync with the loop it was describing
// once), minSlices is computed by simulateReviewRoundCapPass, which drives
// passmachine.Transition -- the same pure decision function run.go's own
// loops call -- to find how many passes it takes the review-round cap
// itself to fire. That keeps this check anchored to the loop's real
// transition logic instead of a derivation that can go stale.
//
// In both loop shapes, when maxSlices is too small to let reviewRounds ever
// reach maxReviewRounds, maxSlices always fires first and silently shadows
// the review-round cap -- the loop stops for the wrong reason and the
// review-round cap never gets attributed as the stop reason.
//
// Zero means "disabled" for either cap (run.go's existing convention): a
// pair where either cap is 0 is never incoherent, since there is no
// shadowing risk when one of the two caps doesn't exist.
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
// review-round cap itself fires for the caller's own maxReviewRounds
// (issue #2548). Looping passmachine.Transition maxReviewRounds times
// directly, as an earlier version of this function did, made validateCaps'
// own runtime scale with an operator-supplied -max-review-rounds value --
// including a hostile or mistyped one as large as MaxInt -- instead of
// staying O(1). Instead, this probes capFiredPass at exactly two small,
// fixed review-round caps (1 and 2), derives the per-round pass cost as the
// delta between those two probes, and extrapolates linearly for the real
// maxReviewRounds -- staying anchored to passmachine.Transition's real
// logic (not a hand-derived formula) while doing only a handful of
// Transition calls regardless of how large maxReviewRounds is.
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
		// The real minSlices threshold would overflow int here; return
		// MaxInt-1 (validateCaps adds 1 to get minSlices, landing exactly on
		// MaxInt) instead of a wrapped/negative value, so the maxSlices <
		// minSlices comparison still fails closed (rejects the pair) rather
		// than silently passing an incoherent one.
		return math.MaxInt - 1, nil
	}
	return pass1 + extraRounds*perRound, nil
}

// capFiredPass drives passmachine.Transition forward, pass by pass, with
// maxSlices disabled and a reviewer that always BLOCKs, until the
// review-round cap itself fires at the given probeMaxReviewRounds --
// returning the 1-indexed pass count at which that happens.
// simulateReviewRoundCapPass only ever calls this with probeMaxReviewRounds
// 1 or 2, never the caller's own, possibly huge, -max-review-rounds value,
// so this loop's own length is bounded independent of that value.
//
// The review-pass loop branch below compares the typed d.Cap field against
// passmachine.StopMaxReviewRoundsReached, not d.CapFired's prose string
// (issue #2548 finding 3) -- CapFired doubles as operator-facing prompt
// text (run.go's seedPromptFromState) that can be reworded independently of
// this comparison. probeMaxPasses additionally guards against an
// unexpected non-cap stop: the pre-fix version of this loop never checked
// Decision.Continue at all (issue #2548 finding 2), so a future
// passmachine.Transition change that introduced a new, earlier-firing stop
// condition on this synthetic BLOCK-forever input would have spun this
// loop forever instead of erroring.
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
	// Named phase, not landPhase, so it doesn't shadow the package-level
	// landPhase() helper in run.go (issue #2548 review).
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
