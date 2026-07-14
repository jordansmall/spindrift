package settle

// GateResult names gateToGreen's outcome, replacing the (green, genuineRed
// bool) pair whose third combination meant "terminal" only by doc-comment
// convention.
type GateResult int

const (
	// GateTerminal is the zero value: a non-retriable outcome (poll timeout
	// or a CheckState API error). No label swap is performed; the caller
	// swaps to failedLabel.
	GateTerminal GateResult = iota
	// GateRedRetry is a genuine CI failure (FAILURE or ERROR); the caller
	// decides whether to dispatch a fix box.
	GateRedRetry
	// GateGreen is confirmed green CI; agent-complete is already swapped.
	GateGreen
)

func (g GateResult) String() string {
	switch g {
	case GateGreen:
		return "green"
	case GateRedRetry:
		return "red-retry"
	case GateTerminal:
		return "terminal"
	default:
		return "unknown"
	}
}

// LandingResult names the outcome of a landing attempt (selfHeal or
// landPushOnly), replacing the (ok, merged bool) pair whose "merged" bit
// only meant anything under MERGE_MODE=immediate.
type LandingResult int

const (
	// LandingFailed is the zero value: CI never reached green (genuine red
	// exhausted or a gate timeout). The issue is swapped to failedLabel.
	LandingFailed LandingResult = iota
	// LandingManual is CI green but not merged — manual/auto mode, a merge
	// guard hit, a merge-guard check error, or a merge failure after green
	// (PR or push-only). The issue stays at agent-complete.
	LandingManual
	// LandingMerged is CI green and the PR (or push-only branch) actually
	// merged. The issue stays at agent-complete.
	LandingMerged
)

func (l LandingResult) String() string {
	switch l {
	case LandingMerged:
		return "merged"
	case LandingManual:
		return "manual"
	case LandingFailed:
		return "failed"
	default:
		return "unknown"
	}
}
