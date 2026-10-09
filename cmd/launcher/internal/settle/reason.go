package settle

// The reason classes a terminal settle records on its dispatch_settled op,
// across every dispatch kind. Values are the on-disk vocabulary; the
// per-state lists are in docs/reference.md under "The settled outcome".
const (
	// Complete.
	ReasonMerged               = "merged"
	ReasonManual               = "manual" // a green PR left open for a human
	ReasonAutoMergeEnqueued    = "auto-merge-enqueued"
	ReasonMergeGuardHit        = "merge-guard-hit"
	ReasonMergeGuardCheckError = "merge-guard-check-error"
	ReasonTuningProvenance     = "tuning-provenance" // the closing issue is a tuning finding; the merge is held for a human
	ReasonMergeBlocked         = "merge-blocked"     // the merge, or auto-merge enqueue, failed after green; the PR stays open
	ReasonAlreadyResolved      = "already-resolved"
	ReasonVerdict              = "verdict" // research: the verdict itself rides in the note
	ReasonFindingsFiled        = "findings-filed"
	ReasonRecoverDeclined      = "recover-declined"

	// Failed.
	ReasonLandingFailed      = "landing-failed"
	ReasonGateTerminal       = "gate-terminal"
	ReasonCIRed              = "ci-red" // CI red with no fix pass budget
	ReasonFixExhausted       = "fix-exhausted"
	ReasonBudgetExhausted    = "budget-exhausted"
	ReasonFixFailed          = "fix-failed"
	ReasonFixNoOp            = "fix-no-op"
	ReasonRelayFailed        = "relay-failed"
	ReasonBlocked            = "blocked"
	ReasonNoOutcome          = "no-outcome"       // the Box printed no outcome line
	ReasonMergeUnverified    = "merge-unverified" // verifyMerged's successful reads contradicted the merge
	ReasonNoPR               = "no-pr"            // status=merged named a branch with no PR to verify
	ReasonBoxFailed          = "box-failed"
	ReasonResearchFailed     = "research-failed"
	ReasonChoreFailed        = "chore-failed"
	ReasonLedgerFinishFailed = "ledger-finish-failed"
	ReasonStopped            = "stopped"

	// States that name themselves.
	ReasonAmbiguous   = "ambiguous"
	ReasonRecoverable = "recoverable"
)

// ReasonLeavesPROpen reports whether reason names a Complete settle that left
// its PR open: nothing re-settles such a PR when it merges later, so
// LateMerges watches for it.
func ReasonLeavesPROpen(reason string) bool {
	switch reason {
	case ReasonManual, ReasonAutoMergeEnqueued, ReasonMergeGuardHit,
		ReasonMergeGuardCheckError, ReasonTuningProvenance, ReasonMergeBlocked:
		return true
	}
	return false
}
