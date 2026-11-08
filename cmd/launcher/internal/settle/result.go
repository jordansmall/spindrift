package settle

import "fmt"

// gateTerminalReason builds the prefixed reason string for a gateTerminal
// outcome. A CheckState error wins over the deadline case because it fires
// first in the poll loop. deadline is MergePollTimeout, which is stored in
// seconds, so the "s" suffix must move with any change to that field's unit.
func gateTerminalReason(stateErr error, deadline int) string {
	if stateErr != nil {
		return fmt.Sprintf("ci-check-error: %v", stateErr)
	}
	return fmt.Sprintf("ci-timeout: CI-watch deadline reached after %ds", deadline)
}

// gateTerminalReasonRegistration names the deadline case where the
// requireRegistration guard (issue #1652, bounded by registrationWindowPolls
// per issue #2475) never cleared, so a caller can tell it apart from an
// ordinary ran-out-the-clock timeout. deadline is in seconds.
func gateTerminalReasonRegistration(deadline int) string {
	return fmt.Sprintf("ci-timeout: registration guard never cleared after %ds", deadline)
}

// gateResult names gateToGreen's outcome.
type gateResult int

const (
	// gateTerminal is the zero value: a poll timeout or a CheckState API
	// error. No label swap here; the caller swaps to failedLabel, except when
	// the observation's err is errAbandoned (a stop landed): another actor owns
	// the issue, so the caller swaps nothing.
	gateTerminal gateResult = iota
	// gateRedRetry is a genuine CI failure (FAILURE or ERROR); the caller
	// decides whether to dispatch a fix box.
	gateRedRetry
	// gateGreen is confirmed green CI. The caller (selfHeal) swaps
	// agent-complete once the landing path settles.
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
// landPushOnly), meaningful only when the attempt's error is nil: errAbandoned
// means no landing took place.
type landingResult int

const (
	// landingFailed is the zero value: CI never reached green. The issue is
	// swapped to failedLabel.
	landingFailed landingResult = iota
	// landingManual is CI green but not merged (manual/auto mode, a merge
	// guard, a merge-guard check error, or a merge failure after green). The
	// issue stays at agent-complete.
	landingManual
	// landingMerged is CI green and the PR or push-only branch merged. The
	// issue stays at agent-complete.
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
