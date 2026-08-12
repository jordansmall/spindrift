package main

import "fmt"

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
// actually reach maxReviewRounds -- so the minSlices formula depends on
// reviewPassEnabled.
//
// reviewPassEnabled == true (runWithReviewPass, run.go): implement and
// review are separate driver-exec invocations, and reaching a review pass
// where reviewRounds == maxReviewRounds, then still running the one
// terminal "land" pass after it (issue #2457), requires 2*maxReviewRounds+3
// total invocations: 1 initial implement pass + (maxReviewRounds+1) review
// passes + maxReviewRounds fix passes + 1 terminal land pass.
//
// reviewPassEnabled == false (the legacy single-loop path, run.go lines
// ~142-212): each pass does its own inline review instead of splitting
// implement/review into separate invocations, so reviewRounds reaches
// maxReviewRounds after just maxReviewRounds+1 invocations. That loop's own
// switch also checks its maxSlices case ahead of its maxReviewRounds case,
// so maxSlices must allow one more invocation than that (maxReviewRounds+2)
// for the review-round cap to ever fire with correct attribution instead of
// being shadowed by maxSlices stopping the loop first.
//
// In both cases, when maxSlices is too small to let reviewRounds ever reach
// maxReviewRounds, maxSlices always fires first and silently shadows the
// review-round cap -- the loop stops for the wrong reason and the
// review-round cap never gets attributed as the stop reason.
//
// Zero means "disabled" for either cap (run.go's existing convention): a
// pair where either cap is 0 is never incoherent, since there is no
// shadowing risk when one of the two caps doesn't exist.
func validateCaps(maxReviewRounds, maxSlices int, reviewPassEnabled bool) error {
	if maxReviewRounds <= 0 || maxSlices <= 0 {
		return nil
	}
	var minSlices int
	if reviewPassEnabled {
		minSlices = 2*maxReviewRounds + 3
	} else {
		minSlices = maxReviewRounds + 2
	}
	if maxSlices < minSlices {
		return fmt.Errorf("orchestrator: -max-slices=%d cannot reach -max-review-rounds=%d (need -max-slices >= %d, or -max-slices=0/-max-review-rounds=0 to disable a cap)", maxSlices, maxReviewRounds, minSlices)
	}
	return nil
}
