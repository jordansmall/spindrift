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
	ReasonMergeByHand          = "tuning-provenance" // a merge-by-hand Chore's finding, held for a human; Records persist the old spelling
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
	ReasonDraftPRFailed      = "draft-pr-failed"
	ReasonBlocked            = "blocked"
	ReasonNoOutcome          = "no-outcome"       // the Box printed no outcome line
	ReasonMergeUnverified    = "merge-unverified" // verifyMerged's successful reads contradicted the merge
	ReasonNoPR               = "no-pr"            // status=merged named a branch with no PR to verify
	ReasonBoxFailed          = "box-failed"
	ReasonResearchFailed     = "research-failed"
	ReasonChoreFailed        = "chore-failed"
	ReasonLedgerFinishFailed = "ledger-finish-failed"
	ReasonStopped            = "stopped"
	ReasonUnreviewed         = "unreviewed" // see UnreviewedNote

	// States that name themselves.
	ReasonAmbiguous   = "ambiguous"
	ReasonRecoverable = "recoverable"
)

// UnreviewedNote is why a run is not trusted (ReasonUnreviewed): its review
// loop stopped before its land pass finished, so nothing was landed. Either a
// status=ready came from another pass (issue #5973), or a run with no success
// self-report stopped in implement or fix (issue #5984).
const UnreviewedNote = "the review loop stopped before its land pass finished, so the work was never reviewed and landed"

// ReasonLeavesPROpen reports whether reason names a Complete settle that left
// its PR open: nothing re-settles such a PR when it merges later, so
// LateMerges watches for it.
func ReasonLeavesPROpen(reason string) bool {
	switch reason {
	case ReasonManual, ReasonAutoMergeEnqueued, ReasonMergeGuardHit,
		ReasonMergeGuardCheckError, ReasonMergeByHand, ReasonMergeBlocked:
		return true
	}
	return false
}
