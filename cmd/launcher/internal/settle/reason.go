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
	ReasonMergeBlocked         = "merge-blocked" // the merge, or auto-merge enqueue, failed after green; the PR stays open
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
	ReasonMissing            = "missing" // the Box printed no outcome line
	ReasonFailed             = "failed"  // a merge verifyMerged could not confirm
	ReasonBoxFailed          = "box-failed"
	ReasonResearchFailed     = "research-failed"
	ReasonChoreFailed        = "chore-failed"
	ReasonLedgerFinishFailed = "ledger-finish-failed"
	ReasonStopped            = "stopped"

	// States that name themselves.
	ReasonAmbiguous   = "ambiguous"
	ReasonRecoverable = "recoverable"
)
