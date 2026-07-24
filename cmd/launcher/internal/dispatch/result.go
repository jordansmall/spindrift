package dispatch

import (
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
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

	// Outcome is populated when OutcomeFound is true.
	Outcome outcome.Outcome

	// OutcomeFound reports whether a SPINDRIFT_OUTCOME line was present in
	// the box's log.
	OutcomeFound bool

	// Comment is the decoded body of the box log's last verified
	// SPINDRIFT_COMMENT line — a single nonce-bearing, base64-encoded
	// control signal (issue #1940) — populated when CommentFound is true
	// (ADR 0032, issue #1692). This is the host-mediated write channel a
	// local Dispatch's Box uses to hand settle its verdict or blocked-note
	// comment instead of posting it in-box; the nonce lets the host tell a
	// line the Box genuinely wrote from one an untrusted issue/comment
	// author echoed into the log.
	Comment string

	// CommentFound reports whether a SPINDRIFT_COMMENT line verified
	// against this run's own nonce was present in the box's log. A line
	// carrying the token with a mismatched nonce, or an undecodable
	// payload, is ignored rather than surfaced here.
	CommentFound bool

	// PRIntent is the decoded "title\n\nbody" payload of the box log's last
	// nonce-verified SPINDRIFT_PR_INTENT line, populated when PRIntentFound
	// is true (issue #1919, single-line nonce-guarded form since issue
	// #1938) — the host-mediated write channel a read-only github Box hands
	// settle its intended draft-PR title and body instead of running
	// `gh pr create` itself.
	PRIntent string

	// PRIntentFound reports whether a nonce-verified SPINDRIFT_PR_INTENT line
	// was present in the box's log. A line carrying the token with a
	// mismatched nonce, or an undecodable payload, is ignored rather than
	// surfaced here.
	PRIntentFound bool

	// ParseErr is non-nil when the box's log contained an unparseable
	// SPINDRIFT_OUTCOME line (as opposed to no line at all). No
	// classification is attempted in this case.
	ParseErr error

	// Classification and ClassifyErr are populated only when OutcomeFound is
	// false and ParseErr is nil, to explain what the box did instead of
	// reporting an outcome.
	Classification driver.Classification
	ClassifyErr    error
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

	// Close evicts this issue's driver-cache entry. Deferred by the
	// per-issue caller once the Dispatch is done with all its work.
	Close()
}
