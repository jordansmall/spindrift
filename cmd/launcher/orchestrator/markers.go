package main

import "spindrift.dev/launcher/internal/outcome"

// VerdictApprove and VerdictBlock are the exact reviewer verdict markers
// scanPassLog/scanReviewLog (via passmachine.Scan, issue #2980) grep for in
// a pass's rendered log (review-prompt.md's documented output contract).
// ADR 0035: this wording is load-bearing on both sides -- review-prompt.md
// must keep emitting it verbatim, and passmachine.Scan must keep matching
// exactly this literal, or the multi-pass loop silently collapses to
// single-pass on ORCHESTRATOR_ENABLED runs. TestPromptMarkersMatchScanner
// (markers_test.go) pins the prompt side against these constants so a
// rewording on either side is caught pre-merge.
// Composed, not literal, since issue #2974: both start from
// outcome.ReviewVerdictToken, the review-verdict channel's generated-backed
// bare token (lib/prompt-contract.nix's markerChannels registry), so this
// package can't drift from the same source the other four channel tokens do.
const (
	VerdictApprove = outcome.ReviewVerdictToken + " APPROVE"
	VerdictBlock   = outcome.ReviewVerdictToken + " BLOCK"
)
