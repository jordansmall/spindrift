package dispatch

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
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

	// CommentRejected is how many SPINDRIFT_COMMENT lines looked like a
	// genuine attempt at the signal grammar but failed nonce verification (a
	// mismatched nonce or an undecodable payload) — a bare prose mention of
	// the token, or a one-field doc example, is excluded from this count
	// entirely (issue #2089). Never surfaced on Comment/CommentFound, but
	// distinguishing this from "no comment signal at all" lets a caller
	// settle-log a warning (issue #2976).
	CommentRejected int

	// PRIntent is the decoded "title\n\nbody" payload of the box log's last
	// nonce-verified SPINDRIFT_PR_INTENT line, populated when PRIntentFound
	// is true (issue #1919, single-line nonce-guarded form since issue
	// #1938) — the host-mediated write channel a read-only github or
	// forgejo Box hands settle its intended draft-PR title and body instead
	// of running `gh pr create` itself.
	PRIntent string

	// PRIntentFound reports whether a nonce-verified SPINDRIFT_PR_INTENT line
	// was present in the box's log. A line carrying the token with a
	// mismatched nonce, or an undecodable payload, is ignored rather than
	// surfaced here.
	PRIntentFound bool

	// PRIntentRejected is how many SPINDRIFT_PR_INTENT lines looked like a
	// genuine attempt at the signal grammar but failed nonce verification (a
	// mismatched nonce or an undecodable payload) — a bare prose mention of
	// the token, or a one-field doc example, is excluded from this count
	// entirely (issue #2089). Never surfaced on PRIntent/PRIntentFound, but
	// distinguishing this from "no PR-intent signal at all" lets a caller
	// settle-log a warning (issue #2976).
	PRIntentRejected int

	// Resolved is dispatch's own single outcome.Resolve-seam result for this
	// Result (issue #2268 slice 2). It replaces the former separate
	// Outcome/OutcomeFound/SelfReport/SelfReportFound fields:
	// Resolved.Found replaces OutcomeFound, Resolved.Outcome replaces
	// Outcome, Resolved.SelfReport/Resolved.SelfReportFound replace
	// SelfReport/SelfReportFound, and Resolved.Provenance ==
	// outcome.ProvenanceSynthetic replaces the old Outcome.Synthetic check
	// callers used.
	Resolved outcome.Resolved

	// IssueIntents holds the decoded payload of every nonce-verified
	// SPINDRIFT_ISSUE_INTENT line in the box log, in encounter order —
	// populated when IssueIntentsFound is true (issue #2018). Unlike
	// Comment/PRIntent's singleton "last verifying line wins" slot, issue
	// filing is 1-to-many: a run may want to file several issues, so every
	// verifying line contributes its own entry rather than only the last.
	IssueIntents []string

	// IssueIntentsFound reports whether at least one nonce-verified
	// SPINDRIFT_ISSUE_INTENT line was present in the box's log. A line
	// carrying the token with a mismatched nonce, or an undecodable payload,
	// is dropped rather than surfaced here.
	IssueIntentsFound bool

	// IssueIntentsRejected is how many SPINDRIFT_ISSUE_INTENT lines looked
	// like a genuine attempt at the signal grammar but failed nonce
	// verification (a mismatched nonce or an undecodable payload) — a bare
	// prose mention of the token, or a one-field doc example, is excluded
	// from this count entirely (issue #2089). Never surfaced on
	// IssueIntents/IssueIntentsFound, but distinguishing this from "no
	// issue-intent signal at all" lets a caller settle-log a warning (issue
	// #2976).
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

	// KilledBySignal reports whether the box's process exited via an external
	// kill signal (SIGTERM/143, SIGKILL/137 — runner.KilledBySignal's
	// convention) on this attempt, rather than a clean or driver-decided exit.
	// Populated only on a Terminal classification after a non-zero exit (issue
	// #2378) — settle uses it as recoverable evidence (alongside a bundle
	// actually sitting in the outbox) even when the driver never got to print
	// any self-report at all.
	KilledBySignal bool

	// Passes is the parsed pass manifest this Dispatch's Box wrote to its
	// outbox (issue #2983) -- Box-authored advisory evidence about the
	// internal implement/review/fix/land passes the orchestrator ran inside
	// this one box invocation, never consulted by Resolved's tier selection
	// or any settle decision. Nil when no manifest file exists (the common
	// case: no outbox mounted, or a legacy/non-orchestrator box) or the file
	// was malformed -- both degrade to the pre-#2983 pass-blind behavior,
	// never an error surfaced elsewhere on Result.
	Passes []passmanifest.Entry

	// Err is the error once() returned on a Terminal classification whose
	// log came back empty -- the box never launched at all (a pre-Box
	// registry-proxy or outbox-setup failure in runOnce, issue #3119) --
	// rather than exiting non-zero after actually running. Nil for every
	// other Success=false path: those either settled on a genuine outcome,
	// or already printed their own explanation (classify error, hold cap,
	// transient cap, quarantine cap) before returning.
	Err error
}

// ReportFailureReason prints r.Err, if set, on stderr next to the terse
// "!! #N FAILED" line a caller already printed -- r.Err is only ever
// populated for a box that never launched at all (retry.go, issue #3119);
// every other failure path already printed its own explanation, so this is
// a no-op there.
func (r Result) ReportFailureReason(num string) {
	if r.Err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: %v\n", num, r.Err)
	}
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
