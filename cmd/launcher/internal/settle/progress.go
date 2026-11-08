package settle

import (
	"os"
	"sync"

	"spindrift.dev/launcher/internal/dispatchrecord"
)

// Status is the machine-readable token after "status=" in a settle progress
// line. The Box's own outcome status tokens pass through as Status(o.Status)
// (gate.go, research.go) and so are not enumerated here.
type Status string

// StatusMergeByHand is the merge-by-hand token, shared with the settled
// record's reason.
const StatusMergeByHand = Status(dispatchrecord.ReasonMergeByHand)

const (
	StatusPostForcePushWait Status = "post-force-push-wait"
	StatusPostForcePushRed  Status = "post-force-push-red"
	StatusMarkReadyFailed   Status = "mark-ready-failed"

	StatusGateTerminal         Status = "gate-terminal"
	StatusBudgetExhausted      Status = "budget-exhausted"
	StatusCheckStateError      Status = "check-state-error"
	StatusLandingFailed        Status = "landing-failed"
	StatusLandingRefUnresolved Status = "landing-ref-unresolved"

	StatusFailureDetailUnavailable Status = "failure-detail-unavailable"
	StatusHeadShaUnavailable       Status = "head-sha-unavailable"
	StatusAlreadyInFlight          Status = "already-in-flight"
	StatusFixFailed                Status = "fix-failed"
	StatusFixNoOp                  Status = "fix-no-op"
	StatusRelayFailed              Status = "relay-failed"
	StatusFixRelayFailed           Status = "fix-relay-failed"
	StatusFixExhausted             Status = "fix-exhausted"

	StatusMergeGuardCheckError Status = "merge-guard-check-error"
	StatusMergeGuardHit        Status = "merge-guard-hit"
	StatusMergeableCheckError  Status = "mergeable-check-error"
	StatusMergeableUnknown     Status = "mergeable-unknown"
	StatusMergeBlocked         Status = "merge-blocked"
	StatusMergeBlockedByChecks Status = "merge-blocked-by-checks"
	StatusMergeTransientRetry  Status = "merge-transient-retry"
	StatusMergeRetrySettle     Status = "merge-retry-settle"

	StatusConflictUnconfirmed   Status = "conflict-unconfirmed"
	StatusHandoffConflict       Status = "handoff-conflict"
	StatusConflictResolve       Status = "conflict-resolve"
	StatusConflictResolveFailed Status = "conflict-resolve-failed"
	StatusMarkDraftFailed       Status = "mark-draft-failed"

	StatusRebaseRetry                Status = "rebase-retry"
	StatusRebasePushRetry            Status = "rebase-push-retry"
	StatusRebasePushRetriesExhausted Status = "rebase-push-retries-exhausted"
	StatusRebaseFailed               Status = "rebase-failed"
	StatusNeedsUpdateCheckError      Status = "needs-update-check-error"
	StatusStaleBaseRebase            Status = "stale-base-rebase"
	StatusStaleBaseRebaseFailed      Status = "stale-base-rebase-failed"

	StatusAutoMergeEnqueued      Status = "auto-merge-enqueued"
	StatusAutoMergeEnqueueFailed Status = "auto-merge-enqueue-failed"
	StatusAgentComplete          Status = "agent-complete"

	StatusFailed                   Status = "failed"
	StatusMissing                  Status = "missing"
	StatusBlocked                  Status = "blocked"
	StatusAdopted                  Status = "adopted"
	StatusVerifiedMerged           Status = "verified-merged"
	StatusMergeUnverifiedReadError Status = "merge-unverified-read-error"
	StatusMergeUnverifiedReadRetry Status = "merge-unverified-read-retry"
	StatusRecoverable              Status = "recoverable"
	StatusBundleRemoveFailed       Status = "bundle-remove-failed"
	StatusReconstructed            Status = "reconstructed"
	StatusDraftPRFailed            Status = "draft-pr-failed"
	StatusDraftPRRetry             Status = "draft-pr-retry"
	StatusDraftPRFinalLookup       Status = "draft-pr-final-lookup"
	StatusDraftPRLookupFailed      Status = "draft-pr-lookup-failed"
	StatusDraftPRAdopted           Status = "draft-pr-adopted"
	StatusRelayRetry               Status = "relay-retry"
)

// Step is one settle progress event. Key is the issue key without its "#".
// Detail carries the verbatim tail of the line (e.g. "!! <err>", "note=<x>").
type Step struct {
	Key     string
	Landing string
	Status  Status
	Detail  string
}

// Line renders the step as the operator-facing progress line, omitting each
// empty field. An empty Landing drops the "landing=" segment: every producer
// passes a non-empty landing (outcome.Parse rejects an empty one; AgentBranch
// and PR URLs are never empty), and steps that never had a landing (fix-pass,
// filed=, dedup lines) rely on the omission.
func (st Step) Line() string {
	line := "    #" + st.Key
	if st.Landing != "" {
		line += "  landing=" + st.Landing
	}
	if st.Status != "" {
		line += "  status=" + string(st.Status)
	}
	if st.Detail != "" {
		line += "  " + st.Detail
	}
	return line + "\n"
}

// Progress receives settle's progress steps.
type Progress interface {
	Step(Step)
}

// StdoutProgress prints each step's line to stdout.
type StdoutProgress struct{}

// Step resolves os.Stdout per write, never at construction: tests swap
// os.Stdout after the Settle is built.
func (StdoutProgress) Step(st Step) {
	os.Stdout.WriteString(st.Line())
}

// ProgressRecorder collects steps for tests. Settle runs concurrently, so it
// carries its own mutex.
type ProgressRecorder struct {
	mu    sync.Mutex
	steps []Step
}

// Step records st.
func (r *ProgressRecorder) Step(st Step) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, st)
}

// Steps returns a copy of the steps recorded so far.
func (r *ProgressRecorder) Steps() []Step {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Step(nil), r.steps...)
}
