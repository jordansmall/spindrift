package main

import "fmt"

// validateCaps detects an incoherent (maxReviewRounds, maxSlices) pair
// (issue #2460). runWithReviewPass's switch (run.go) checks its maxSlices
// case ahead of its maxReviewRounds case, so when maxSlices is too small to
// let reviewRounds ever reach maxReviewRounds, maxSlices always fires first
// and silently shadows the review-round cap -- the loop stops for the wrong
// reason and the review-round cap never gets attributed as the stop reason.
//
// Reaching a review pass where reviewRounds == maxReviewRounds, and then
// still running the one terminal "land" pass after it (issue #2457),
// requires 2*maxReviewRounds+3 total driver-exec invocations: 1 initial
// implement pass + (maxReviewRounds+1) review passes + maxReviewRounds fix
// passes + 1 terminal land pass. So maxSlices must be at least that many
// invocations for the review-round cap to ever be reachable.
//
// Zero means "disabled" for either cap (run.go's existing convention): a
// pair where either cap is 0 is never incoherent, since there is no
// shadowing risk when one of the two caps doesn't exist.
func validateCaps(maxReviewRounds, maxSlices int) error {
	if maxReviewRounds <= 0 || maxSlices <= 0 {
		return nil
	}
	minSlices := 2*maxReviewRounds + 3
	if maxSlices < minSlices {
		return fmt.Errorf("orchestrator: -max-slices=%d cannot reach -max-review-rounds=%d (need -max-slices >= %d, or -max-slices=0/-max-review-rounds=0 to disable a cap)", maxSlices, maxReviewRounds, minSlices)
	}
	return nil
}
