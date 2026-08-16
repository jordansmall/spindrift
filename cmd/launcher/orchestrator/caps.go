package main

import (
	"fmt"

	"spindrift.dev/launcher/internal/passmachine"
)

// defaultMaxReviewRounds and defaultMaxSlices are the orchestrator's shipped
// --max-review-rounds / --max-slices flag defaults (issue #2460). They live
// here, rather than as bare literals in main.go's flag.Int calls, so this
// package's own coherence test (TestValidateCapsAcceptsShippedDefaults) is
// pinned to the exact values that ship, instead of a hand-copied duplicate
// that can silently drift. main.go's flag.Int calls reference these same
// constants as their default value.
const (
	defaultMaxReviewRounds = 3
	defaultMaxSlices       = 9
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
// loops call -- forward pass by pass until the review-round cap itself
// fires. That keeps this check anchored to the loop's real transition logic
// instead of a derivation that can go stale.
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
	minSlices := simulateReviewRoundCapPass(maxReviewRounds, reviewPassEnabled) + 1
	if maxSlices < minSlices {
		return fmt.Errorf("orchestrator: -max-slices=%d cannot reach -max-review-rounds=%d (need -max-slices >= %d, or -max-slices=0/-max-review-rounds=0 to disable a cap)", maxSlices, maxReviewRounds, minSlices)
	}
	return nil
}

// simulateReviewRoundCapPass drives passmachine.Transition forward, pass by
// pass, with maxSlices disabled and a reviewer that always BLOCKs, until the
// review-round cap itself fires -- returning the 1-indexed pass count at
// which that happens. validateCaps uses this instead of a hand-derived
// formula (issue #2548), so a change to the loop's own transition logic can
// never silently invalidate this arithmetic.
func simulateReviewRoundCapPass(maxReviewRounds int, reviewPassEnabled bool) int {
	caps := passmachine.Caps{MaxSlices: 0, MaxReviewRounds: maxReviewRounds}
	pass := 0
	reviewRounds := 0

	if !reviewPassEnabled {
		for {
			pass++
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
			if d.IncrementReviewRounds {
				reviewRounds++
			}
		}
	}

	passKind := passmachine.KindImplement
	terminalLand := false
	lastVerdict := passmachine.VerdictNone
	for {
		pass++
		var d passmachine.Decision
		if passKind == passmachine.KindReview {
			d = passmachine.Transition(passmachine.Input{
				PassJustExecuted: passmachine.KindReview,
				Verdict:          passmachine.VerdictBlock,
				Pass:             pass,
				ReviewRounds:     reviewRounds,
				Caps:             caps,
				TerminalLand:     terminalLand,
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
				TerminalLand:     terminalLand,
				LastVerdict:      lastVerdict,
			})
		}
		if d.CapFired == "max review rounds reached" {
			return pass
		}
		if d.SetTerminalLand {
			terminalLand = true
		}
		passKind = d.NextPass
	}
}

// validateMaxParallelWorkers rejects a non-positive -max-parallel-workers
// value (issue #2495). Unlike maxReviewRounds/maxSlices, where zero is a
// legitimate "no cap" sentinel handled by validateCaps above, there is no
// meaningful "disabled" value for a concurrency semaphore's capacity --
// LaunchWorkers' own runBounded (workers.go) already self-heals a <=0
// MaxParallel by silently substituting defaultMaxParallelWorkers, so a
// caller relying on that alone would see a mistyped flag silently
// renormalized rather than reported. This check exists so that
// misconfiguration is instead a fast, attributed failure at orchestrator
// startup -- unlike validateCaps' own warn-and-continue precedent (issue
// #2460), this error is fatal: mainRun aborts the run rather than
// proceeding with a value the Consumer never actually asked for.
func validateMaxParallelWorkers(maxParallelWorkers int) error {
	if maxParallelWorkers <= 0 {
		return fmt.Errorf("orchestrator: -max-parallel-workers=%d must be positive (omit the flag, or set it >= 1, to use the default %d)", maxParallelWorkers, defaultMaxParallelWorkers)
	}
	return nil
}
