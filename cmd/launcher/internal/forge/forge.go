// Package forge is the interface through which the Harness speaks to the
// Target repo's host. Adapters live in subpackages: github, forgejo, git,
// local, and jira.
package forge

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrMergeConflict is returned by Merge when the PR branch conflicts with the
// base branch. Callers may rebase the head branch and retry.
var ErrMergeConflict = errors.New("merge conflict")

// ErrMergeBlockedByChecks is returned by Merge when the PR has no content
// conflict but required checks are pending or failing. The gh CLI reports this
// with the same "not mergeable" wording as a genuine conflict, so callers must
// tell the two apart by the PR's mergeable state (see PRForge.Mergeable). A
// rebase does not help here; the only next step is to retry once checks settle.
var ErrMergeBlockedByChecks = errors.New("merge blocked by checks")

// ErrTransientPushFailure is returned by Rebase when its force-push fails with a
// transport or server fault (connection reset, timeout, HTTP 5xx) or is killed
// by the host's own push deadline. Auth, permission, and missing-repo failures,
// stale-lease or non-fast-forward rejections, and unrecognised stderr do not
// wrap it. Callers may retry a bounded number of times.
var ErrTransientPushFailure = errors.New("transient push failure")

// ErrWorkflowPermissionDenied is returned when GitHub refuses a push because it
// creates or updates a `.github/workflows/*` file and the credential lacks
// Workflows permission. Permanent: no retry can succeed, so it never wraps
// ErrTransientPushFailure.
var ErrWorkflowPermissionDenied = errors.New("workflow permission denied")

// WorkflowPermissionRemedy is the cause and remedy for a push refused because
// it touches `.github/workflows/` without Workflows permission. It is the one
// source of that prose for the first-settle park comment, the auto-recover
// give-up, and the hand-run recover note, so they never drift.
func WorkflowPermissionRemedy(num string) string {
	return fmt.Sprintf("the change creates or updates a file under `.github/workflows/`, and the push credential lacks Workflows permission, which is off by default on purpose. Retrying cannot land it: land the change by hand, or grant Workflows permission temporarily and run `spindrift recover %s`.", num)
}

// ErrMergeTransient is returned by Merge when the merge call fails for a
// transient transport or server reason rather than a genuine rejection
// (conflict, blocked by checks, branch protection). Callers may retry with
// backoff.
var ErrMergeTransient = errors.New("transient merge failure")

// ErrAuthFailure means the forge credentials are missing or invalid: Probe
// returns it, as do the REST adapters on a 401 or 403. Callers should name the
// selected backend's token (doctor's Config.TokenHint).
var ErrAuthFailure = errors.New("forge auth failure")

// ErrRepoNotFound is returned by Probe when the configured repository cannot be
// reached or does not exist under the authenticated account.
var ErrRepoNotFound = errors.New("forge repo not found")

// ErrUnknownIssueType is returned by IssueTypeChecker.CheckIssueType when the
// configured issue type is not one the tracker's project offers.
var ErrUnknownIssueType = errors.New("issue type not offered by the project")

// ErrBundleNotFound is returned by RelayBundle when the outbox bundle is absent
// because the Box's branch range was empty (issue #2096), a benign "nothing to
// relay" outcome. A present but corrupt bundle is a genuine error, not this.
var ErrBundleNotFound = errors.New("forge bundle not found")

// ErrNotFound is the generic per-resource sentinel for any REST lookup, unlike
// ErrRepoNotFound, which means the configured repository itself is unreachable.
var ErrNotFound = errors.New("forge: not found")

// ErrRateLimit is returned when GitHub rate-limits the caller, on either the
// primary hourly quota or the secondary abuse-detection limit. The GitHub
// adapter returns a *RateLimitError, which matches it under errors.Is and may
// carry the reset.
var ErrRateLimit = errors.New("forge rate limited")

// RateLimitError is the typed form of ErrRateLimit, so a caller that wants to
// wait out the limit can recover when it lifts.
type RateLimitError struct {
	// Reset is when the tracker says the limit lifts; zero when it reported none.
	Reset time.Time
	// Err is the adapter's own failure, kept for the log text.
	Err error
}

func (e *RateLimitError) Error() string {
	if e.Err == nil {
		return ErrRateLimit.Error()
	}
	return ErrRateLimit.Error() + ": " + e.Err.Error()
}

func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimit }

func (e *RateLimitError) Unwrap() error { return e.Err }

// ErrAlreadyClaimed is returned by TransitionState when a claim (to ==
// InProgress) lands on an issue that already carries the InProgress marker,
// so a second claimer errors instead of silently re-swapping labels. Callers
// treat this as "someone else has it" and skip the issue rather than fail the
// run (#3887).
var ErrAlreadyClaimed = errors.New("forge: issue already claimed")

// TransitionRefusedError is returned by TransitionState when a tracker that
// owns its lifecycle natively (Jira with JIRA_LIFECYCLE=status) cannot move
// the issue: its workflow offers no transition to the target status (To
// names it; Offered lists the statuses the workflow does offer), or the
// tracker maps no status to the target dispatch state (State names it; To is
// empty). From is the issue's current status, empty when it could not be
// read. Error reads as a plain sentence because settle posts it verbatim as
// an issue comment; detect it with errors.As.
type TransitionRefusedError struct {
	Issue, From, To string
	State           string
	Offered         []string
}

func (e *TransitionRefusedError) Error() string {
	from := ""
	if e.From != "" {
		from = fmt.Sprintf(" from status %q", e.From)
	}
	if e.To == "" {
		return fmt.Sprintf("issue %s could not move%s: the tracker maps no status to state %s", e.Issue, from, e.State)
	}
	offers := "offers no transitions"
	if len(e.Offered) > 0 {
		offers = "offers transitions to " + QuoteList(e.Offered)
	}
	return fmt.Sprintf("issue %s could not move%s to %q: the workflow %s", e.Issue, from, e.To, offers)
}

// QuoteList renders each of items %q-quoted, joined by ", ".
func QuoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(quoted, ", ")
}

// Issue is a GitHub issue as seen by the launcher.
type Issue struct {
	Number string // launcher keeps issue numbers as strings
	Title  string
	Body   string
	State  IssueState
	Labels []string
	// Landing is the local adapter's immutable landing ref (ADR 0029, a PR URL
	// or push-only branch ref). Empty for github/jira, which have no such field.
	Landing string
	// Abandoned reports the local adapter's abandoned: axis (ADR 0029), set by
	// reconcile when the issue's landing PR closed without merging. Always false
	// for github/jira.
	Abandoned bool
	// Parent is the local adapter's parent: frontmatter field (ADR 0033), the
	// broad-ticket key CODE_FORGE=local resolves the Integration branch from.
	// Empty for github/jira, and for a parentless local seam, which is its own
	// broad ticket keyed on its own slug (see local.ResolveParent).
	Parent string
	// Priority is the canonical dispatch priority (ADR 0040), resolved by each
	// IssueTracker adapter from its own agent-priority-* labels. The sort that
	// consumes it is not part of this type.
	Priority Priority
	// ClosedAt is when the issue closed; zero when open or when the adapter
	// does not report it.
	ClosedAt time.Time
	// ClosedNotPlanned reports the tracker recorded the close as "not planned".
	// github, jira and local report a close reason; forgejo, which has none,
	// leaves it false.
	ClosedNotPlanned bool
	// Reopened reports an open issue that was reopened after it closed. Only
	// github's ListIssuesWithLabels reads it (stateReason REOPENED); every other
	// adapter and read path leaves it false.
	Reopened bool
}

// Priority is the canonical dispatch priority (ADR 0040): Critical > High >
// Normal > Low. Each IssueTracker adapter resolves it from its own
// agent-priority-* labels at its own edge. The zero value is PriorityNormal,
// the tier an unlabeled issue occupies, and the constants are ordered so that
// < and > already express the ADR's total order.
type Priority int

const (
	// PriorityLow runs only when the pool would otherwise idle.
	PriorityLow Priority = iota - 1
	PriorityNormal
	PriorityHigh
	// PriorityCritical wins if an issue somehow carries more than one priority
	// label.
	PriorityCritical
)

// String renders the priority as its lowercase tier name.
func (p Priority) String() string {
	switch p {
	case PriorityCritical:
		return "critical"
	case PriorityHigh:
		return "high"
	case PriorityNormal:
		return "normal"
	case PriorityLow:
		return "low"
	default:
		return "normal"
	}
}

// IssueState is the canonical open/closed state of an issue. Each IssueTracker
// adapter translates its own native representation at its own edge; no
// adapter's native literal leaks past that boundary.
type IssueState string

const (
	IssueOpen   IssueState = "OPEN"
	IssueClosed IssueState = "CLOSED"
	// IssueMerged is what gh issue view reports when a blocker ref resolves to a
	// merged PR rather than an agent-worked issue.
	IssueMerged IssueState = "MERGED"
)

// Wire is the "open"/"closed" spelling the signal and Connector wires use. A
// merged state reads as open, as it did where this was open-coded.
func (s IssueState) Wire() string {
	if s == IssueClosed {
		return "closed"
	}
	return "open"
}

// PR is a GitHub pull request as seen by the launcher.
type PR struct {
	URL string
}

// PRState is the canonical state of a pull request. The push-only git adapter
// has no PR concept and never returns one.
type PRState string

const (
	PROpen   PRState = "OPEN"
	PRMerged PRState = "MERGED"
	PRClosed PRState = "CLOSED"
)

// MergeableState is GitHub's classification of whether a PR's changes conflict
// with its base branch. It is distinct from RollupState (CI results) and from
// required-review or branch-protection gating: Merge can still refuse a
// MergeableMergeable PR whose checks have not passed.
type MergeableState string

// Known MergeableState values returned by the GitHub API or by the fake.
const (
	MergeableUnknown     MergeableState = "UNKNOWN"
	MergeableMergeable   MergeableState = "MERGEABLE"
	MergeableConflicting MergeableState = "CONFLICTING"
)

// ClassifyMergeFailure maps a PR's MergeableState to the sentinel a failed
// Merge should return, so every adapter makes the conflict-vs-checks
// distinction in one place. ok is false for a state the caller must not mask
// behind a sentinel (MergeableUnknown, or an unrecognized adapter-native
// value), telling it to build its own raw error instead.
func ClassifyMergeFailure(state MergeableState) (err error, ok bool) {
	switch state {
	case MergeableConflicting:
		return ErrMergeConflict, true
	case MergeableMergeable:
		return ErrMergeBlockedByChecks, true
	default:
		return nil, false
	}
}

// RollupState is the aggregate CI status of a PR's head commit.
type RollupState string

// Known RollupState values returned by the GitHub API or by the fake.
const (
	StateSuccess  RollupState = "SUCCESS"
	StatePending  RollupState = "PENDING"
	StateExpected RollupState = "EXPECTED"
	StateFailure  RollupState = "FAILURE"
	StateError    RollupState = "ERROR"
	StateNone     RollupState = "NONE" // no checks registered on this commit
)

// MaxFailureDetailBytes bounds the string RenderFailureDetail returns, so a
// PR with many failing checks, long summaries, or job logs cannot blow the fix
// Box's env/prompt budget. It is injected as one env string, so it must stay
// far below Linux's 128 KiB MAX_ARG_STRLEN. It leaves room for a few
// MaxLogExcerptBytes excerpts.
const MaxFailureDetailBytes = 12000

// MaxLogExcerptBytes bounds the slice of one failing job's log that
// RenderFailureDetail keeps; failures print last, so it keeps the tail.
const MaxLogExcerptBytes = 4000

// JobLogKeepBytes is the tail of a job log an adapter hands the renderer: over
// MaxLogExcerptBytes so RenderFailureDetail still sees a log past its cap and
// says it trimmed, yet bounded however long the job ran.
const JobLogKeepBytes = 2 * MaxLogExcerptBytes

// TailBuffer is an io.Writer that keeps at least the last JobLogKeepBytes
// written to it, so a job log can be streamed without being held whole. The
// zero value is ready to use.
type TailBuffer struct{ buf []byte }

// Write appends p and drops older bytes once the buffer doubles. It never fails.
func (t *TailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	// Trim only once the buffer doubles, so a run of small writes does not
	// copy on every call.
	if len(t.buf) > 2*JobLogKeepBytes {
		t.buf = bytes.Clone(t.buf[len(t.buf)-JobLogKeepBytes:])
	}
	return len(p), nil
}

// String is the retained tail: everything written while that is under
// 2*JobLogKeepBytes, else between JobLogKeepBytes and 2*JobLogKeepBytes bytes.
func (t *TailBuffer) String() string { return string(t.buf) }

// FailureDetailEntry is one failing check or status, normalized from an
// adapter's native shape into the fields RenderFailureDetail formats.
type FailureDetailEntry struct {
	Name    string
	State   string
	Summary string
	Log     string // raw log text of the failing job; "" when the adapter has none
}

// MaxSummaryBytes and MaxNameBytes bound one entry's summary and name in
// RenderFailureDetail, so a single verbose third-party check (CheckRun
// summaries reach 64 KiB) cannot starve every job log of room. The head of a
// summary is kept: it leads with the conclusion.
const (
	MaxSummaryBytes = 1000
	MaxNameBytes    = 200
)

// minLogExcerptBytes is the least of a job log RenderFailureDetail will print;
// an entry that cannot get at least this much is omitted, never shown with a
// "last 0 bytes" excerpt.
const minLogExcerptBytes = 400

// RenderFailureDetail formats failing entries into a bounded report. Names and
// summaries are capped (MaxNameBytes, MaxSummaryBytes); the room
// MaxFailureDetailBytes leaves beyond the headers is split evenly across the
// entries that have a log, each capped at MaxLogExcerptBytes, so every job
// keeps the tail of its log where the error is. A trimmed excerpt says so,
// naming its job and the cap it got. When the entries cannot all fit with at
// least minLogExcerptBytes per log, the trailing ones are omitted whole and a
// closing line names the first and counts them. Callers must filter entries
// down to failing ones first.
func RenderFailureDetail(entries []FailureDetailEntry) string {
	const sep = "---\n"
	names := make([]string, len(entries))
	headers := make([]string, len(entries))
	logs := make([]string, len(entries))
	costs := make([]int, len(entries))
	for i, e := range entries {
		names[i] = capHead(e.Name, MaxNameBytes)
		headers[i] = names[i] + ": " + e.State + "\n"
		if e.Summary != "" {
			headers[i] += capHead(e.Summary, MaxSummaryBytes) + "\n"
		}
		costs[i] = len(headers[i]) + len(sep)
		// Kept as delivered: the trim notice compares against the adapter's cut
		// log, so trailing whitespace must not hide a cut head.
		if strings.TrimSpace(e.Log) != "" {
			logs[i] = e.Log
			// Reserve the widest excerpt notice and the line end.
			costs[i] += len(excerptNotice(names[i], MaxFailureDetailBytes)) + 1
		}
	}
	// fit counts the leading entries whose headers and minimum excerpts fit.
	fit := func(limit int) (int, int, int) {
		var n, fixed, logged int
		for i := range entries {
			need := fixed + costs[i] + (logged+min(1, len(logs[i])))*minLogExcerptBytes
			if need > limit {
				return n, fixed, logged
			}
			fixed += costs[i]
			if logs[i] != "" {
				logged++
			}
			n++
		}
		return n, fixed, logged
	}
	limit := MaxFailureDetailBytes
	n, fixed, logged := fit(limit)
	if n < len(entries) {
		// Longest possible omission line: %q expands a NUL to four bytes, and
		// capHead appends "...[cut]" to a name it cuts.
		limit -= len(omitNotice(capHead(strings.Repeat("\x00", MaxNameBytes+1), MaxNameBytes), len(entries)))
		n, fixed, logged = fit(limit)
	}
	excerptCap := MaxLogExcerptBytes
	if logged > 0 {
		excerptCap = min(excerptCap, (limit-fixed)/logged)
	}

	var b strings.Builder
	for i := range n {
		b.WriteString(headers[i])
		if log := logs[i]; log != "" {
			// Tail the log without its trailing whitespace so a whitespace-only
			// last excerptCap bytes cannot leave the excerpt empty; the notice
			// still tracks the delivered length.
			excerpt := logTail(strings.TrimRightFunc(log, unicode.IsSpace), excerptCap)
			if len(log) > excerptCap {
				b.WriteString(excerptNotice(names[i], excerptCap))
			}
			b.WriteString(strings.TrimSpace(excerpt))
			b.WriteString("\n")
		}
		b.WriteString(sep)
	}
	if n < len(entries) {
		b.WriteString(omitNotice(names[n], len(entries)-n))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func excerptNotice(name string, n int) string {
	return fmt.Sprintf("[log excerpt for %q trimmed to its last %d bytes or fewer]\n", name, n)
}

func omitNotice(first string, n int) string {
	return fmt.Sprintf("[%d further failing entries omitted to fit %d bytes, starting with %q]", n, MaxFailureDetailBytes, first)
}

// capHead returns s cut to at most n bytes on a rune boundary, marked when cut.
func capHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return cutAtRune(s, n) + "...[cut]"
}

// logTail returns log's last limit or fewer bytes, starting on a rune
// boundary and, when the cut falls inside a line, at the next line start.
func logTail(log string, limit int) string {
	if len(log) <= limit {
		return log
	}
	tail := log[len(log)-limit:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	if log[len(log)-len(tail)-1] != '\n' {
		if i := strings.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
			tail = tail[i+1:]
		}
	}
	return tail
}

// cutAtRune returns s[:n] backed off to a rune boundary.
func cutAtRune(s string, n int) string {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
