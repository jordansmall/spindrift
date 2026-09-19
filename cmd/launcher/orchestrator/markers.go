package main

import "spindrift.dev/launcher/internal/outcome"

// VerdictApprove and VerdictBlock are the reviewer verdict markers passmachine.Scan
// greps for in a pass log (ADR 0035, issue #2980). review-prompt.md must emit them
// verbatim or the multi-pass loop silently collapses to single-pass on
// ORCHESTRATOR_ENABLED runs; TestPromptMarkersMatchScanner pins the prompt side.
// Both compose outcome.ReviewVerdictToken (issue #2974), the shared channel source.
const (
	VerdictApprove = outcome.ReviewVerdictToken + " APPROVE"
	VerdictBlock   = outcome.ReviewVerdictToken + " BLOCK"
)
