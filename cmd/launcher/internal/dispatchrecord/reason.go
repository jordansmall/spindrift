package dispatchrecord

// The reason classes a terminal settle records on its dispatch_settled op,
// across every dispatch kind. Values are the on-disk vocabulary; the
// per-state lists are in docs/reference.md under "The settled outcome", and
// reasons is the table the docs test pins them against.
const (
	// Complete.
	ReasonMerged               = "merged"
	ReasonManual               = "manual"
	ReasonAutoMergeEnqueued    = "auto-merge-enqueued"
	ReasonMergeGuardHit        = "merge-guard-hit"
	ReasonMergeGuardCheckError = "merge-guard-check-error"
	ReasonMergeByHand          = "tuning-provenance" // a merge-by-hand Chore's finding, held for a human; Records persist the old spelling
	ReasonMergeBlocked         = "merge-blocked"
	ReasonAlreadyResolved      = "already-resolved"
	ReasonVerdict              = "verdict"
	ReasonFindingsFiled        = "findings-filed"
	ReasonRecoverDeclined      = "recover-declined"

	// Failed.
	ReasonLandingFailed      = "landing-failed"
	ReasonGateTerminal       = "gate-terminal"
	ReasonCIRed              = "ci-red"
	ReasonFixExhausted       = "fix-exhausted"
	ReasonBudgetExhausted    = "budget-exhausted"
	ReasonFixFailed          = "fix-failed"
	ReasonFixNoOp            = "fix-no-op"
	ReasonRelayFailed        = "relay-failed"
	ReasonDraftPRFailed      = "draft-pr-failed"
	ReasonBlocked            = "blocked"
	ReasonNoOutcome          = "no-outcome"
	ReasonMergeUnverified    = "merge-unverified"
	ReasonNoPR               = "no-pr"
	ReasonBoxFailed          = "box-failed"
	ReasonResearchFailed     = "research-failed"
	ReasonChoreFailed        = "chore-failed"
	ReasonLedgerFinishFailed = "ledger-finish-failed"
	ReasonStopped            = "stopped"
	ReasonUnreviewed         = "unreviewed" // see UnreviewedNote

	// States that name themselves.
	ReasonAmbiguous   = "ambiguous"
	ReasonRecoverable = "recoverable"

	// Any terminal state.
	ReasonTransitionRefused = "transition-refused"
)

// UnreviewedNote is why a run is not trusted (ReasonUnreviewed): its review
// loop stopped before its land pass finished, so nothing was landed. Either a
// status=ready came from another pass (issue #5973), or a run with no success
// self-report stopped in implement or fix (issue #5984).
const UnreviewedNote = "the review loop stopped before its land pass finished, so the work was never reviewed and landed"

// The on-disk lifecycle states a reason settles into. Plain strings, the
// spelling a Record carries, so State can also hold AnyState, which no forge
// state names; reason_forge_test.go pins each against forge's.
const (
	StateComplete    = "complete"
	StateFailed      = "failed"
	StateAmbiguous   = "ambiguous"
	StateRecoverable = "recoverable"
)

// AnyState is the State of a reason that can accompany any terminal state.
const AnyState = "any"

// Delivery is how a Complete settle counts toward delivered work. The zero
// value delivers, so it means something only once the state is Complete.
type Delivery int

const (
	Delivers Delivery = iota
	// DeliversNothing: the change was already on the default branch; nothing
	// was opened.
	DeliversNothing
	// DeliversWithPR: delivered only if a PR opened. Push-only forges never
	// latch a PR URL, so their merge-blocked run delivers nothing.
	DeliversWithPR
)

// ReasonRow is one reason class and the facts a consumer derives from it.
type ReasonRow struct {
	Name string
	// State is the lifecycle state the reason settles into (a State* const),
	// or AnyState.
	State string
	// LeavesPROpen marks a Complete that left its PR open: nothing re-settles
	// it when it merges later, so LateMerges watches for it.
	LeavesPROpen bool
	// Landed marks a reason that put code on the default branch. Stats counts
	// it as merged, so only merged sets it.
	Landed bool
	// Delivery is how a Complete reason counts toward delivered work; left
	// zero outside Complete, where it means nothing.
	Delivery Delivery
	Gloss    string
}

// reasons lists every reason class, in the order of the const block.
var reasons = []ReasonRow{
	{Name: ReasonMerged, State: StateComplete, Landed: true, Gloss: "the PR merged"},
	{Name: ReasonManual, State: StateComplete, LeavesPROpen: true, Gloss: "a green PR left open for a human"},
	{Name: ReasonAutoMergeEnqueued, State: StateComplete, LeavesPROpen: true, Gloss: "auto-merge was enqueued on the green PR"},
	{Name: ReasonMergeGuardHit, State: StateComplete, LeavesPROpen: true, Gloss: "the merge guard held the PR for a human"},
	{Name: ReasonMergeGuardCheckError, State: StateComplete, LeavesPROpen: true, Gloss: "the merge guard could not read the changed paths or the closing issue"},
	{Name: ReasonMergeByHand, State: StateComplete, LeavesPROpen: true, Gloss: "the PR closes an agent-tuning-finding issue, so a human merges it"},
	{Name: ReasonMergeBlocked, State: StateComplete, LeavesPROpen: true, Delivery: DeliversWithPR, Gloss: "the merge or the auto-merge enqueue failed after green; the PR stays open"},
	{Name: ReasonAlreadyResolved, State: StateComplete, Delivery: DeliversNothing, Gloss: "the change was already on the default branch"},
	{Name: ReasonVerdict, State: StateComplete, Gloss: "research posted its verdict; the verdict itself rides in the note"},
	{Name: ReasonFindingsFiled, State: StateComplete, Gloss: "a butler Chore filed its findings"},
	{Name: ReasonRecoverDeclined, State: StateComplete, Gloss: "recover declined the stranded bundle"},

	{Name: ReasonLandingFailed, State: StateFailed, Gloss: "the PR could not land"},
	{Name: ReasonGateTerminal, State: StateFailed, Gloss: "a gate failed terminally"},
	{Name: ReasonCIRed, State: StateFailed, Gloss: "CI red with no fix pass budget"},
	{Name: ReasonFixExhausted, State: StateFailed, Gloss: "the fix passes ran out with CI still red"},
	{Name: ReasonBudgetExhausted, State: StateFailed, Gloss: "the run's budget ran out before another fix pass"},
	{Name: ReasonFixFailed, State: StateFailed, Gloss: "a fix pass failed"},
	{Name: ReasonFixNoOp, State: StateFailed, Gloss: "a fix pass changed nothing"},
	{Name: ReasonRelayFailed, State: StateFailed, Gloss: "the relay failed (also recover's park)"},
	{Name: ReasonDraftPRFailed, State: StateFailed, Gloss: "the relay landed, the draft-PR create failed"},
	{Name: ReasonBlocked, State: StateFailed, Gloss: "the Box reported itself blocked"},
	{Name: ReasonNoOutcome, State: StateFailed, Gloss: "the Box printed no outcome line"},
	{Name: ReasonMergeUnverified, State: StateFailed, Gloss: "verifyMerged's successful reads contradicted the merge"},
	{Name: ReasonNoPR, State: StateFailed, Gloss: "status=merged named a branch with no PR to verify"},
	{Name: ReasonBoxFailed, State: StateFailed, Gloss: "the Box failed before settle"},
	{Name: ReasonResearchFailed, State: StateFailed, Gloss: "research produced no verdict"},
	{Name: ReasonChoreFailed, State: StateFailed, Gloss: "a butler Chore failed"},
	{Name: ReasonLedgerFinishFailed, State: StateFailed, Gloss: "the butler ledger's finish write failed"},
	{Name: ReasonStopped, State: StateFailed, Gloss: "the run was stopped"},
	{Name: ReasonUnreviewed, State: StateFailed, Gloss: UnreviewedNote},

	{Name: ReasonAmbiguous, State: StateAmbiguous, Gloss: "the Box stopped on an ambiguous or contradictory spec"},
	{Name: ReasonRecoverable, State: StateRecoverable, Gloss: "a stranded read-only bundle that spindrift recover can retry"},

	// JIRA_LIFECYCLE=status found no workflow transition to the mapped status
	// (or no mapped status for the state), so the tracker was left unchanged.
	// It replaces the path's own reason; the state is still the host's
	// decision.
	{Name: ReasonTransitionRefused, State: AnyState, Gloss: "the tracker's workflow offered no transition to the mapped status"},
}

func reasonRow(name string) (ReasonRow, bool) {
	for _, r := range reasons {
		if r.Name == name {
			return r, true
		}
	}
	return ReasonRow{}, false
}

// ReasonLeavesPROpen reports whether name is a Complete reason that left its
// PR open. An unknown name reports false.
func ReasonLeavesPROpen(name string) bool {
	r, _ := reasonRow(name)
	return r.LeavesPROpen
}

// ReasonLanded reports whether name is a reason that put code on the default
// branch. An unknown name reports false.
func ReasonLanded(name string) bool {
	r, _ := reasonRow(name)
	return r.Landed
}

// ReasonDelivery reports how name counts toward delivered work once the
// settle is Complete; check the state first. An unknown name delivers.
func ReasonDelivery(name string) Delivery {
	r, _ := reasonRow(name)
	return r.Delivery
}
