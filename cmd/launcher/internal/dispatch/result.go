package dispatch

import (
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/usage"
)

// Result is what Run and Fix return: the parsed Outcome line on success (or
// a zero-exit box that wrote no outcome line), and a best-effort transient
// Classification when no outcome line was found at all.
type Result struct {
	// Success is true when the box's final attempt exited zero.
	Success bool

	// AlreadyInFlight is true when the dispatch was skipped because a
	// container/sandbox named for this issue was already running -- a live
	// run, possibly orphaned by a killed launcher, still owns it. This is a
	// distinct outcome from failure: the caller must not transition the
	// issue's dispatch state (the live run's in-progress claim stands) and
	// must not retry (issue #562).
	AlreadyInFlight bool

	// The three host-mediated relay channels below (comment, PR intent,
	// issue intent) share one shape. Each *Found reports whether a line
	// verified against this run's own nonce, which is what lets the host tell
	// a line the Box genuinely wrote from one an untrusted issue/comment
	// author echoed into the log; a token-bearing line with a mismatched
	// nonce or undecodable payload is dropped, not surfaced. Each *Rejected
	// counts those dropped lines — excluding bare prose mentions and
	// one-field doc examples — so a caller can settle-log a warning rather
	// than mistaking a spoof for "no signal at all".

	// Comment is the decoded body of the last verified SPINDRIFT_COMMENT
	// line: the channel a local Dispatch's Box uses to hand settle its
	// verdict or blocked-note comment instead of posting it in-box (ADR 0032).
	Comment         string
	CommentFound    bool
	CommentRejected int

	// PRIntent is the decoded "title\n\nbody" payload of the last verified
	// SPINDRIFT_PR_INTENT line: how a read-only github or forgejo Box hands
	// settle its intended draft-PR title and body instead of running
	// `gh pr create` itself.
	PRIntent         string
	PRIntentFound    bool
	PRIntentRejected int

	// Resolved is dispatch's outcome.Resolve-seam result for this Result.
	Resolved outcome.Resolved

	// IssueIntents holds every verified SPINDRIFT_ISSUE_INTENT payload in
	// encounter order. Unlike the singleton last-line-wins channels above,
	// issue filing is 1-to-many: a run may file several issues, so every
	// verifying line contributes an entry.
	IssueIntents         []string
	IssueIntentsFound    bool
	IssueIntentsRejected int

	// ParseErr is non-nil when the box's log contained an unparseable
	// SPINDRIFT_OUTCOME line (as opposed to no line at all). No
	// classification is attempted in this case.
	ParseErr error

	// Classification and ClassifyErr are populated only when Resolved.Found is
	// false and ParseErr is nil, to explain what the box did instead of
	// reporting an outcome.
	Classification driver.Classification
	ClassifyErr    error

	// KilledBySignal reports an external kill (SIGTERM/143, SIGKILL/137)
	// rather than a clean or driver-decided exit. Populated only on a
	// Terminal classification after a non-zero exit; settle treats it as
	// recoverable evidence, alongside a bundle actually sitting in the
	// outbox, even when the driver never printed any self-report.
	KilledBySignal bool
}

// Dispatcher is the seam callers depend on so tests can inject a Fake
// instead of a real Dispatch.
type Dispatcher interface {
	// Run dispatches the initial box for this issue, retrying transient
	// failures per Config, and returns the parsed result.
	Run() Result

	// Fix dispatches a fix box for the 1-based pass number, forwarding
	// ciFailureSummary as CI_FAILURE_SUMMARY when non-empty. Subject to the
	// same retry policy as Run.
	Fix(pass int, ciFailureSummary string) Result

	// ResolveConflict dispatches a conflict-resolution box against pr. Not
	// retried: a short-lived rebase-conflict box never runs the main agent
	// prompt.
	ResolveConflict(pr string) error

	// UsageReport returns the Markdown usage-summary comment body for this
	// issue's initial run.
	UsageReport() string

	// CumulativeUsage sums token and cost usage across every pass log this
	// issue's Dispatch has produced so far (initial run plus each fix
	// pass) — selfHealGate's budget gate (issue #2001) reads this before
	// dispatching another fix pass.
	CumulativeUsage() usage.Usage

	// Close evicts this issue's driver-cache entry. Deferred by the
	// per-issue caller once the Dispatch is done with all its work.
	Close()
}
