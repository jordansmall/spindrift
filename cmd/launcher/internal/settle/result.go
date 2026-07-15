package settle

// gateResult names gateToGreen's outcome, replacing the (green, genuineRed
// bool) pair whose third combination meant "terminal" only by doc-comment
// convention.
type gateResult int

const (
	// gateTerminal is the zero value: a non-retriable outcome (poll timeout
	// or a CheckState API error). No label swap is performed; the caller
	// swaps to failedLabel.
	gateTerminal gateResult = iota
	// gateRedRetry is a genuine CI failure (FAILURE or ERROR); the caller
	// decides whether to dispatch a fix box.
	gateRedRetry
	// gateGreen is confirmed green CI. agent-complete is not swapped yet —
	// the caller (selfHeal) swaps it once the landing path settles.
	gateGreen
	// gateAbandoned is the operator's Terminate (ADR 0024, issue #649)
	// landing while gateToGreen was polling. No label swap is performed —
	// Terminate already transitioned the issue to Dispatchable itself.
	gateAbandoned
)

func (g gateResult) String() string {
	switch g {
	case gateGreen:
		return "green"
	case gateRedRetry:
		return "red-retry"
	case gateTerminal:
		return "terminal"
	case gateAbandoned:
		return "abandoned"
	default:
		return "unknown"
	}
}

// landingResult names the outcome of a landing attempt (selfHeal or
// landPushOnly), replacing the (ok, merged bool) pair whose "merged" bit
// only meant anything under MERGE_MODE=immediate.
type landingResult int

const (
	// landingFailed is the zero value: CI never reached green (genuine red
	// exhausted or a gate timeout). The issue is swapped to failedLabel.
	landingFailed landingResult = iota
	// landingManual is CI green but not merged — manual/auto mode, a merge
	// guard hit, a merge-guard check error, or a merge failure after green
	// (PR or push-only). The issue stays at agent-complete.
	landingManual
	// landingMerged is CI green and the PR (or push-only branch) actually
	// merged. The issue stays at agent-complete.
	landingMerged
	// landingAbandoned is the operator's Terminate (ADR 0024, issue #649)
	// landing somewhere inside selfHeal — CI watch, a fix pass, or the merge
	// gate. Terminate already did the transition, comment, and log line;
	// callers must take no further action (no verifyMerged, no usage
	// comment, no failure print).
	landingAbandoned
)

func (l landingResult) String() string {
	switch l {
	case landingMerged:
		return "merged"
	case landingManual:
		return "manual"
	case landingFailed:
		return "failed"
	case landingAbandoned:
		return "abandoned"
	default:
		return "unknown"
	}
}
