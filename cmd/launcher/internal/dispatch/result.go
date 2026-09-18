package dispatch

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/usage"
)

// Result is what Run and Fix return: the parsed outcome on success, and a
// best-effort transient Classification when the box wrote no outcome line.
type Result struct {
	// Success is true when the box's final attempt exited zero.
	Success bool

	// AlreadyInFlight is true when a container or sandbox named for this issue
	// was already running, so the dispatch was skipped. The caller must not
	// transition the issue's dispatch state (the live run's in-progress claim
	// stands) and must not retry (issue #562).
	AlreadyInFlight bool

	// Comment is the decoded body of the box log's last nonce-verified
	// SPINDRIFT_COMMENT line (ADR 0032, issues #1692 and #1940), how a local
	// Dispatch's Box hands settle its verdict or blocked-note comment. The
	// nonce separates a line the Box wrote from one an untrusted issue or
	// comment author echoed into the log.
	Comment string

	// CommentFound reports whether a nonce-verified SPINDRIFT_COMMENT line was
	// present. A mismatched nonce or an undecodable payload is ignored.
	CommentFound bool

	// CommentRejected counts SPINDRIFT_COMMENT lines that attempted the signal
	// grammar but failed nonce verification; a prose mention of the token or a
	// one-field doc example does not count (issue #2089). Callers settle-log a
	// warning from it (issue #2976).
	CommentRejected int

	// PRIntent is the decoded "title\n\nbody" payload of the box log's last
	// nonce-verified SPINDRIFT_PR_INTENT line (issue #1919, single-line
	// nonce-guarded form since issue #1938), how a read-only github or forgejo
	// Box hands settle its draft-PR title and body.
	PRIntent string

	// PRIntentFound reports whether a nonce-verified SPINDRIFT_PR_INTENT line
	// was present. A mismatched nonce or an undecodable payload is ignored.
	PRIntentFound bool

	// PRIntentRejected counts SPINDRIFT_PR_INTENT lines that attempted the
	// signal grammar but failed nonce verification, on CommentRejected's terms
	// (issues #2089 and #2976).
	PRIntentRejected int

	// Resolved is dispatch's single outcome.Resolve-seam result for this
	// Result (issue #2268 slice 2).
	Resolved outcome.Resolved

	// IssueIntents holds the decoded payload of every nonce-verified
	// SPINDRIFT_ISSUE_INTENT line, in encounter order (issue #2018). Filing is
	// 1-to-many, unlike Comment and PRIntent's "last verifying line wins"
	// slot, so every verifying line contributes an entry.
	IssueIntents []string

	// IssueIntentsFound reports whether at least one nonce-verified
	// SPINDRIFT_ISSUE_INTENT line was present. A mismatched nonce or an
	// undecodable payload is dropped.
	IssueIntentsFound bool

	// IssueIntentsRejected counts SPINDRIFT_ISSUE_INTENT lines that attempted
	// the signal grammar but failed nonce verification, on CommentRejected's
	// terms (issues #2089 and #2976).
	IssueIntentsRejected int

	// ParseErr is non-nil when the box's log held an unparseable
	// SPINDRIFT_OUTCOME line, as opposed to no line at all. Classification is
	// not attempted in that case.
	ParseErr error

	// Classification and ClassifyErr are populated only when Resolved.Found is
	// false and ParseErr is nil, to explain what the box did instead of
	// reporting an outcome.
	Classification driver.Classification
	ClassifyErr    error

	// KilledBySignal reports whether the box exited via an external kill signal
	// (SIGTERM/143 or SIGKILL/137) rather than a clean or driver-decided exit.
	// Populated only on a Terminal classification after a non-zero exit (issue
	// #2378); settle reads it as recoverable evidence alongside a bundle
	// sitting in the outbox.
	KilledBySignal bool

	// Passes is the pass manifest the Box wrote to its outbox (issue #2983),
	// advisory only: Resolved's tier selection and settle never consult it.
	// Nil when no manifest file exists or the file was malformed; both degrade
	// to the pre-#2983 pass-blind behavior rather than an error.
	Passes []passmanifest.Entry

	// Err is the error once() returned on a Terminal classification whose log
	// came back empty, meaning the box never launched (issue #3119). Nil for
	// every other Success=false path: those settled on a genuine outcome or
	// already printed their own explanation.
	Err error
}

// ReportFailureReason prints r.Err, if set, on stderr next to the terse
// failure line a caller already printed. r.Err is only ever set for a box that
// never launched (retry.go, issue #3119), so this is a no-op elsewhere.
func (r Result) ReportFailureReason(num string) {
	if r.Err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: %v\n", num, r.Err)
	}
}

// Dispatcher is the seam callers depend on so tests can inject a Fake instead
// of a real Dispatch.
type Dispatcher interface {
	// Run dispatches the initial box, retrying transient failures per Config.
	Run() Result

	// Fix dispatches a fix box for the 1-based pass number, forwarding
	// ciFailureSummary as CI_FAILURE_SUMMARY when non-empty. Subject to the
	// same retry policy as Run.
	Fix(pass int, ciFailureSummary string) Result

	// ResolveConflict dispatches a conflict-resolution box against pr. Not
	// retried: a short-lived rebase-conflict box never runs the main agent
	// prompt.
	ResolveConflict(pr string) error

	// UsageReport returns the Markdown usage-summary comment body for the
	// initial run.
	UsageReport() string

	// CumulativeUsage sums token and cost usage across the initial run and
	// every fix pass. selfHealGate's budget gate (issue #2001) reads it before
	// dispatching another fix pass.
	CumulativeUsage() usage.Usage

	// Close evicts this issue's driver-cache entry; the per-issue caller
	// defers it once the Dispatch is done.
	Close()
}
