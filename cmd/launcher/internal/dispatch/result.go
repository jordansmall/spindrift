package dispatch

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/usage"
)

// Result is the payload behind a Disposition: the parsed outcome, and a
// best-effort transient Classification when the box wrote no outcome line.
type Result struct {
	// Comment is the decoded body of the box log's last nonce-verified
	// SPINDRIFT_COMMENT line (ADR 0032, issues #1692 and #1940), how a local
	// Dispatch's Box hands settle its verdict or blocked-note comment. The
	// nonce separates a line the Box wrote from one an untrusted issue or
	// comment author echoed into the log.
	Comment string

	// CommentFound reports whether a nonce-verified SPINDRIFT_COMMENT line was
	// present. A mismatched nonce or an undecodable payload is ignored.
	CommentFound bool

	// CommentRejected splits, by cause, the SPINDRIFT_COMMENT lines that
	// attempted the signal grammar but failed to verify; a prose mention of
	// the token or a one-field doc example does not count (issue #2089).
	// Callers settle-log a warning from it (issue #2976, split #3670).
	CommentRejected outcome.Rejections

	// PRIntent is the decoded "title\n\nbody" payload of the box log's last
	// nonce-verified SPINDRIFT_PR_INTENT line (issue #1919, single-line
	// nonce-guarded form since issue #1938), how a read-only github or forgejo
	// Box hands settle its draft-PR title and body.
	PRIntent string

	// PRIntentFound reports whether a nonce-verified SPINDRIFT_PR_INTENT line
	// was present. A mismatched nonce or an undecodable payload is ignored.
	PRIntentFound bool

	// PRIntentRejected splits, by cause, the SPINDRIFT_PR_INTENT lines that
	// attempted the signal grammar but failed to verify, on CommentRejected's
	// terms (issues #2089, #2976, split #3670).
	PRIntentRejected outcome.Rejections

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

	// IssueIntentsRejected splits, by cause, the SPINDRIFT_ISSUE_INTENT lines
	// that attempted the signal grammar but failed to verify, on
	// CommentRejected's terms (issues #2089, #2976, split #3670).
	IssueIntentsRejected outcome.Rejections

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

	// Warnings holds the settle-scan warnings outcomeResult printed, each the
	// text PrintWarning wrote. The sidecar may prefix fix-pass entries with
	// "fix pass N: " (issue #3744).
	Warnings []string

	// Err is the error once() returned on a Terminal classification whose log
	// came back empty, meaning the box never launched (issue #3119). Nil for
	// every other failed path: those settled on a genuine outcome or
	// already printed their own explanation.
	Err error
}

// Disposition is what Run and Fix return: a Result payload tagged as skipped,
// failed or succeeded. It is opaque on purpose. Route is the only way to the
// payload and takes an arm for every kind, so omitting the skip arm is a
// compile error (an explicit nil is not). A bare "if !success" check cannot
// tell a skip from a failure, and a caller that conflated them took the
// destructive failure path against a live run (issues #562, #3633, #3655,
// #3705).
//
// The skip contract: another run already holds this issue (its container or
// sandbox is live, or its claim is). A skip is a distinct successful no-op,
// never a failure. The caller must not transition the issue's dispatch state
// (the live run's in-progress claim stands), must not retry, and must not
// comment.
//
// Skip surfaces in three channels, and only the first is compiler-enforced:
//  1. Route's onSkip arm for Run and Fix, produced by runner.ErrAlreadyRunning
//     in dispatchWithRetry and by ErrIssueClaimed from ClaimIssue in Run.
//  2. ResolveConflict's error return, which can be runner.ErrAlreadyRunning;
//     callers must errors.Is it ahead of their generic error arm (settle's
//     ready.go does).
//  3. ErrIssueClaimed from a direct ClaimIssue caller, such as the
//     package-level recoverIssue in cmd/launcher/main.go (issue #4364).
type Disposition struct {
	kind   dispositionKind
	result Result
}

type dispositionKind int

// dispositionFailed is the zero value so a zero Disposition routes to the
// failure arm, matching the pre-#3705 zero Result.
const (
	dispositionFailed dispositionKind = iota
	dispositionSucceeded
	dispositionSkipped
)

// Skipped reports that another run already holds this issue; see Disposition
// for what the caller must not do.
func Skipped() Disposition { return Disposition{kind: dispositionSkipped} }

// Failed wraps the Result of a dispatch whose final attempt did not exit zero
// (or never ran).
func Failed(r Result) Disposition { return Disposition{kind: dispositionFailed, result: r} }

// Succeeded wraps the Result of a dispatch whose final attempt exited zero, or
// settled on a genuine outcome the box printed before dying (issue #2075).
func Succeeded(r Result) Disposition { return Disposition{kind: dispositionSucceeded, result: r} }

// Route calls exactly one arm for d and returns its value. It is the only
// accessor for a Disposition's Result; see Disposition for the skip contract
// onSkip must honor.
func Route[T any](d Disposition, onSkip func() T, onFailure func(Result) T, onSuccess func(Result) T) T {
	switch d.kind {
	case dispositionSkipped:
		return onSkip()
	case dispositionSucceeded:
		return onSuccess(d.result)
	default:
		return onFailure(d.result)
	}
}

// ReportFailureReason prints r.Err, if set, on stderr next to the terse
// failure line a caller already printed. r.Err is only ever set for a box that
// never launched (retry.go, issue #3119), so this is a no-op elsewhere.
func (r Result) ReportFailureReason(num string) {
	if r.Err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: %v\n", num, r.Err)
	}
}

// FailureNote returns r.Err's text, or "" when unset, the same condition
// ReportFailureReason gates on. Sharing that one source (issue #3627 review
// finding) keeps the stderr line and the settled record's note from drifting.
func (r Result) FailureNote() string {
	if r.Err == nil {
		return ""
	}
	return r.Err.Error()
}

// Dispatcher is the seam callers depend on so tests can inject a Fake instead
// of a real Dispatch.
type Dispatcher interface {
	// Run dispatches the initial box, retrying transient failures per Config.
	Run() Disposition

	// Fix dispatches a fix box for the 1-based pass number, forwarding
	// ciFailureSummary as CI_FAILURE_SUMMARY when non-empty. Subject to the
	// same retry policy as Run.
	Fix(pass int, ciFailureSummary string) Disposition

	// ResolveConflict dispatches a conflict-resolution box against pr. Not
	// retried: a short-lived rebase-conflict box never runs the main agent
	// prompt. Its error can be runner.ErrAlreadyRunning, a skip: see
	// Disposition for that contract.
	ResolveConflict(pr string) error

	// UsageReport returns the Markdown usage-summary comment body for the
	// initial run.
	UsageReport() string

	// CumulativeUsage sums every usage field — tokens, cost, durations, and
	// turns — across the initial run and every fix pass. selfHealGate's
	// budget gate (issue #2001) reads it before dispatching another fix pass.
	CumulativeUsage() usage.Usage

	// RecordID returns the Dispatch's Record ID, "" until one is minted.
	RecordID() string

	// LogPath returns the Dispatch's primary Pass log, the file its
	// dispatch_settled op is appended to.
	LogPath() string

	// RecordWarnings adds a batch of scan warnings to this Dispatch's sidecar
	// (issue #3744): the first call replaces an earlier run's file, later
	// calls (fix passes) append; no warnings at all clears it. Best-effort:
	// it never fails the settle.
	RecordWarnings(warnings []string)

	// Close evicts this issue's driver-cache entry and releases the claim
	// Run took (issue #4364); the per-issue caller defers it once the
	// Dispatch is done.
	Close()
}

// PrintWarning writes one settle warning to stderr in the "    ?? #<n>: "
// form, the single source of that format for every settle warning (issue
// #3744).
func PrintWarning(number, w string) {
	fmt.Fprintf(os.Stderr, "    ?? #%s: %s\n", number, w)
}
