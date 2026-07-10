package dispatch

import "spindrift.dev/launcher/internal/outcome"

// Result is what Run and Fix return: the parsed Outcome line on success (or
// a zero-exit box that wrote no outcome line), and a best-effort transient
// Classification when no outcome line was found at all.
type Result struct {
	// Success is true when the box's final attempt exited zero.
	Success bool

	// Outcome is populated when OutcomeFound is true.
	Outcome outcome.Outcome

	// OutcomeFound reports whether a SPINDRIFT_OUTCOME line was present in
	// the box's log.
	OutcomeFound bool

	// ParseErr is non-nil when the box's log contained an unparseable
	// SPINDRIFT_OUTCOME line (as opposed to no line at all). No
	// classification is attempted in this case.
	ParseErr error

	// Classification and ClassifyErr are populated only when OutcomeFound is
	// false and ParseErr is nil, to explain what the box did instead of
	// reporting an outcome.
	Classification outcome.Classification
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
