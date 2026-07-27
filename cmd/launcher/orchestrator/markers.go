package main

// VerdictApprove and VerdictBlock are the exact reviewer verdict markers
// scanPassLog's findVerdict greps for in a pass's rendered log
// (review-prompt.md's documented output contract). ADR 0035: this wording is
// load-bearing on both sides -- review-prompt.md must keep emitting it
// verbatim, and findVerdict must keep matching exactly this literal, or the
// multi-pass loop silently collapses to single-pass on ORCHESTRATOR_ENABLED
// runs. TestPromptMarkersMatchScanner (markers_test.go) pins the prompt side
// against these constants so a rewording on either side is caught pre-merge.
const (
	VerdictApprove = "VERDICT: APPROVE"
	VerdictBlock   = "VERDICT: BLOCK"
)
