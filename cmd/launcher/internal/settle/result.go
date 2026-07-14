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
	// gateGreen is confirmed green CI; agent-complete is already swapped.
	gateGreen
)

func (g gateResult) String() string {
	switch g {
	case gateGreen:
		return "green"
	case gateRedRetry:
		return "red-retry"
	case gateTerminal:
		return "terminal"
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
)

func (l landingResult) String() string {
	switch l {
	case landingMerged:
		return "merged"
	case landingManual:
		return "manual"
	case landingFailed:
		return "failed"
	default:
		return "unknown"
	}
}
