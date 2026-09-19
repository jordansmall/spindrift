// Package outcome owns the SPINDRIFT_OUTCOME grammar, parsing, and log scan:
// the single source of truth for the per-Box result contract between the Agent
// and the Harness (see CONTEXT.md, Outcome line).
package outcome

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
)

// Token is the exact SPINDRIFT_OUTCOME marker literal, generated from
// lib/prompt-contract.nix's markerChannels registry (issue #2974). ADR 0035:
// issue-prompt.md's OUTCOME contract must keep emitting it verbatim, because
// rewording either side without the other silently collapses the multi-pass
// loop to single-pass on ORCHESTRATOR_ENABLED runs.
const Token = markerChannelOutcomeToken

// PRIntentToken is the exact SPINDRIFT_PR_INTENT marker literal (issue #2045,
// the #2036 fix): a read-only Box's draft-PR title/body hand-off, scanned
// host-side by LastPRIntentInLog and in-box by entrypoint.sh's
// required-marker gate. Generated from the markerChannels registry (#2974).
const PRIntentToken = markerChannelPRIntentToken

// CommentToken is the exact SPINDRIFT_COMMENT marker literal (issue #1940):
// the mid-run comment-relay channel LastCommentLineInLog scans for. Generated
// from the markerChannels registry (issue #2974).
const CommentToken = markerChannelCommentToken

// IssueIntentToken is the exact SPINDRIFT_ISSUE_INTENT marker literal (issue
// #2018): the file-an-issue relay channel AllIssueIntentLinesInLog scans for.
// Generated from the markerChannels registry (issue #2974).
const IssueIntentToken = markerChannelIssueIntentToken

// ReviewVerdictToken is the bare SPINDRIFT review-verdict channel token (issue
// #2974), distinct from the full "VERDICT: APPROVE" / "VERDICT: BLOCK" values
// orchestrator's VerdictApprove/VerdictBlock compose from it. Exported because
// orchestrator cannot see the unexported generated const it aliases.
const ReviewVerdictToken = markerChannelReviewVerdictToken

// Outcome is the machine-readable result written by a Box as its final line.
// Grammar: SPINDRIFT_OUTCOME issue=<num> landing=<landing-ref> status=<status> note=<text>
// Note may contain spaces and '='; all other fields are space-delimited tokens.
type Outcome struct {
	Issue string
	// Landing is a PR URL under CODE_FORGE=github, a branch ref (e.g.
	// "agent/issue-42") under the push-only CODE_FORGE=git, or a
	// verdict-comment URL for the research dispatch kind.
	Landing string
	Status  string // ready | blocked | failed | merged | …
	// Synthetic flags a line the outcome backstop generated (ADR 0036, issue
	// #2223) rather than the driver authoring it.
	Synthetic bool
	Note      string // free text; may contain spaces and '='
}

// ErrNearMiss marks a Parse error where the SPINDRIFT_OUTCOME token is present
// in the line but the line still fails to parse. It is separable from the
// token being entirely absent so a caller (e.g. a resume nudge) can react to
// "almost got it" differently from "never tried".
var ErrNearMiss = errors.New("outcome: near-miss")

// IsNearMiss reports whether err came from a present-but-unparseable
// SPINDRIFT_OUTCOME token rather than an absent one.
func IsNearMiss(err error) bool {
	return errors.Is(err, ErrNearMiss)
}

// Parse parses a single SPINDRIFT_OUTCOME line. A missing landing or status
// field, and a token that appears but does not prefix the line, are wrapped in
// ErrNearMiss. Deciding which line to hand Parse belongs to the caller:
// lastInLog only ever offers a leading-token line, so a bare mention in prose
// never reaches Parse from that path.
func Parse(line string) (Outcome, error) {
	line = strings.TrimSpace(line)
	rest, ok := stripToken(line, Token)
	if !ok {
		if containsToken(line, Token) {
			return Outcome{}, fmt.Errorf("%w: line contains %q but does not match the standalone-line grammar", ErrNearMiss, Token)
		}
		return Outcome{}, fmt.Errorf("outcome: line missing %q prefix", Token+" ")
	}
	o := Outcome{
		Issue:     tokenField(rest, "issue"),
		Landing:   tokenField(rest, "landing"),
		Status:    tokenField(rest, "status"),
		Synthetic: tokenField(rest, "synthetic") == "true",
		Note:      noteField(rest),
	}
	if o.Landing == "" {
		return Outcome{}, fmt.Errorf("%w: missing landing field", ErrNearMiss)
	}
	if o.Status == "" {
		return Outcome{}, fmt.Errorf("%w: missing or empty status field", ErrNearMiss)
	}
	return o, nil
}

// ReadyBeforeNote reports whether line leads with the SPINDRIFT_OUTCOME token
// and carries the literal token "status=ready" before the first " note=".
// markergate.ShouldNudgePRIntent needs "is this line claiming ready" without
// Parse's full validity contract, which rejects an empty landing. Bounding at
// " note=" stops a "status=ready" mention inside the note text from counting.
func ReadyBeforeNote(line string) bool {
	line = strings.TrimSpace(line)
	rest, ok := stripToken(line, Token)
	if !ok {
		return false
	}
	before, _, _ := strings.Cut(rest, " note=")
	for _, tok := range strings.Fields(before) {
		if tok == "status=ready" {
			return true
		}
	}
	return false
}

// hasField reports whether line (already stripped of the leading token)
// carries key as a space-delimited field. Unlike tokenField, which returns ""
// for both an absent field and one present with an empty value, this answers
// presence alone, the distinction markergate's outcome-nudge gate needs.
func hasField(line, key string) bool {
	prefix := key + "="
	for _, tok := range strings.Fields(line) {
		if strings.HasPrefix(tok, prefix) {
			return true
		}
	}
	return false
}

// hasOutcomeFields reports whether rest (a SPINDRIFT_OUTCOME line's remainder
// after stripToken) carries both a landing= and a status= field marker, any
// value included. This is looser than Parse's full-grammar validity, which
// rejects an empty landing as ErrNearMiss. See LastFieldedOutcomeLine.
func hasOutcomeFields(rest string) bool {
	return hasField(rest, "landing") && hasField(rest, "status")
}

// LastFieldedOutcomeLine returns the last SPINDRIFT_OUTCOME-token-leading line
// in the file at path whose remainder satisfies hasOutcomeFields. Filtering
// before taking the last line matters to markergate.ShouldNudgeOutcome: an
// empty-landing fielded line must not nudge (as Parse would), and a later
// non-fielded line must not shadow a genuine one. A missing file is not found.
func LastFieldedOutcomeLine(path string) (line string, found bool, err error) {
	var last string
	scanErr := logscan.ForEachLine(path, logscan.SkipOversized, func(l string) {
		trimmed := strings.TrimSpace(l)
		rest, ok := stripToken(trimmed, Token)
		if !ok || !hasOutcomeFields(rest) {
			return
		}
		last = trimmed
	})
	if scanErr != nil {
		if errors.Is(scanErr, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, scanErr
	}
	if last == "" {
		return "", false, nil
	}
	return last, true, nil
}

// LastNearMissOutcomeLine returns the last SPINDRIFT_OUTCOME-token-leading
// line in the file at path whose remainder does NOT satisfy hasOutcomeFields,
// the complement LastFieldedOutcomeLine filters out. A caller quotes the
// offending line in the corrective resume prompt. A missing file is not found
// rather than an error.
func LastNearMissOutcomeLine(path string) (line string, found bool, err error) {
	var last string
	scanErr := logscan.ForEachLine(path, logscan.SkipOversized, func(l string) {
		trimmed := strings.TrimSpace(l)
		rest, ok := stripToken(trimmed, Token)
		if !ok || hasOutcomeFields(rest) {
			return
		}
		last = trimmed
	})
	if scanErr != nil {
		if errors.Is(scanErr, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, scanErr
	}
	if last == "" {
		return "", false, nil
	}
	return last, true, nil
}

// Line returns the canonical SPINDRIFT_OUTCOME representation of o.
// Parse(o.Line()) == o for all valid Outcomes.
func (o Outcome) Line() string {
	if o.Synthetic {
		return fmt.Sprintf("%s issue=%s landing=%s status=%s synthetic=true note=%s",
			Token, o.Issue, o.Landing, o.Status, o.Note)
	}
	return fmt.Sprintf("%s issue=%s landing=%s status=%s note=%s",
		Token, o.Issue, o.Landing, o.Status, o.Note)
}

// ParseAnywhere finds the SPINDRIFT_OUTCOME token anywhere in line, not only
// as a leading prefix, and parses from there. The claude Driver's
// RenderTranscript prefixes every line with a "[role] " tag (issue #1998), so
// Parse alone would always report ErrNearMiss on a rendered pass log. Returns
// false when the token is absent or the text from that point on fails to parse.
func ParseAnywhere(line string) (Outcome, bool) {
	idx := tokenIndex(line, Token)
	if idx < 0 {
		return Outcome{}, false
	}
	o, err := Parse(line[idx:])
	if err != nil {
		return Outcome{}, false
	}
	return o, true
}

// lastInLog parses, via Parse, the last line in the file at path that leads
// with the SPINDRIFT_OUTCOME token, so a mid-JSON echo of the token (e.g. a
// tool_result of issue text) is never a candidate. A missing file is not found
// rather than an error; err satisfies IsNearMiss when the chosen line fails to
// parse. No nonce gate (ADR 0039, issue #2274): the Box has exited by this read.
func lastInLog(path string) (o Outcome, found bool, err error) {
	var lastLeading string
	scanErr := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		if _, ok := stripToken(strings.TrimSpace(line), Token); ok {
			lastLeading = line
		}
	})
	if scanErr != nil {
		if errors.Is(scanErr, os.ErrNotExist) {
			return Outcome{}, false, nil
		}
		return Outcome{}, false, scanErr
	}

	if lastLeading == "" {
		return Outcome{}, false, nil
	}
	o, err = Parse(lastLeading)
	if err != nil {
		return Outcome{}, false, err
	}
	return o, true, nil
}

// SelfReport is the driver's own last non-synthetic leading-token
// SPINDRIFT_OUTCOME line, kept distinct from the resolved outcome so a
// synthetic backstop line (ADR 0036) can never shadow it via last-line-wins
// (issue #2223). It is unauthenticated and ungated (ADR 0039, issue #2274), so
// a consumer that acts on it owns weighing that trust.
type SelfReport struct {
	Raw     string  // the raw driver-authored leading-token line
	Status  string  // best-effort self-reported status (field value or bare word)
	Outcome Outcome // populated only when Parsed
	Parsed  bool    // whether Raw parsed the full SPINDRIFT_OUTCOME grammar
}

// lastSelfReportInLog returns the last leading-token SPINDRIFT_OUTCOME line in
// the log at path that is NOT flagged synthetic=true, so the backstop's own
// appended line, which wins lastInLog's last-line-wins, is skipped here and the
// driver's real signal survives. See SelfReport for the trust caveat; a missing
// file is not found rather than an error.
func lastSelfReportInLog(path string) (report SelfReport, found bool, err error) {
	var last string
	scanErr := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		trimmed := strings.TrimSpace(line)
		if _, ok := stripToken(trimmed, Token); !ok {
			return
		}
		if tokenField(trimmed, "synthetic") == "true" {
			return
		}
		last = trimmed
	})
	if scanErr != nil {
		if errors.Is(scanErr, os.ErrNotExist) {
			return SelfReport{}, false, nil
		}
		return SelfReport{}, false, scanErr
	}
	if last == "" {
		return SelfReport{}, false, nil
	}
	return selfReportFromLine(last), true, nil
}

// LastSelfReport exposes lastSelfReportInLog's tier, and its found/error
// contract, outside this package, for a caller that wants the self-report
// alongside a possibly-also-present genuine outcome. Resolve reaches that tier
// only when the genuine one found nothing at all; this always returns the
// self-report and never adjudicates the two.
func LastSelfReport(path string) (report SelfReport, found bool, err error) {
	return lastSelfReportInLog(path)
}

// selfReportFromLine builds a SelfReport from a leading-token line: a full
// parse when the grammar holds, otherwise a best-effort bare status word (the
// first field after the token delimiter that is not itself a key=value field).
func selfReportFromLine(line string) SelfReport {
	r := SelfReport{Raw: line}
	if o, perr := Parse(line); perr == nil {
		r.Outcome = o
		r.Parsed = true
		r.Status = o.Status
		return r
	}
	if rest, ok := stripToken(line, Token); ok {
		fields := strings.Fields(rest)
		if len(fields) > 0 && !strings.Contains(fields[0], "=") {
			r.Status = fields[0]
		}
	}
	return r
}

// Provenance names which tier of the SPINDRIFT_OUTCOME selection policy
// produced a Resolved value.
type Provenance string

const (
	// ProvenanceGenuine is a driver-authored, non-synthetic outcome line.
	ProvenanceGenuine Provenance = "genuine"
	// ProvenanceSynthetic is the outcome backstop's appended line (ADR 0036,
	// issue #2223): the driver itself never emitted an outcome.
	ProvenanceSynthetic Provenance = "synthetic"
	// ProvenanceSelfReport is the driver's unauthenticated self-report
	// fallback (see SelfReport), Resolve's last resort.
	ProvenanceSelfReport Provenance = "self-report"
)

// PassLog names one pass's log file for Resolve to scan, in the order the
// passes ran. dispatch.PassLog aliases this type: outcome importing dispatch
// would cycle, so the alias lives on the dispatch side.
type PassLog struct {
	Label string
	Path  string
}

// Resolved is the result of Resolve: the selected outcome, the tier that
// produced it, and the normalized dispatch kind the caller asked about.
type Resolved struct {
	Outcome    Outcome
	Provenance Provenance
	// Kind is the normalized dispatch kind ("" becomes "work"). It does not
	// affect selection: ADR 0022 rejected a kind-specific outcome grammar, so
	// Resolve is kind-agnostic by design.
	Kind  string
	Found bool
	// SelfReport and SelfReportFound carry the driver's unauthenticated
	// self-report alongside whichever tier won Outcome above, on every Found
	// true return, so a caller can weigh both at once: a later synthetic
	// backstop line can win Outcome while an earlier driver-authored
	// self-report stays readable here. Both stay zero when none was found.
	SelfReport      SelfReport
	SelfReportFound bool
	// SelfReportError is the last I/O error lastSelfReportAcrossLogs hit while
	// scanning (issue #2343 slice 1), distinct from Resolve's returned error,
	// which reflects only the genuine/synthetic tier. It is observability
	// alone: that walk never aborts on the error and keeps trying later logs,
	// so this never changes which report, if any, lands on SelfReport.
	SelfReportError error
}

// IsGenuineOrSynthetic reports whether r settled on the genuine or synthetic
// tier. A caller deciding whether to settle on r's Outcome must exclude
// ProvenanceSelfReport: that tier is unauthenticated, so a self-report-only
// match must fall through to the caller's own classification instead.
func (r Resolved) IsGenuineOrSynthetic() bool {
	return r.Provenance == ProvenanceGenuine || r.Provenance == ProvenanceSynthetic
}

// Resolve picks among the SPINDRIFT_OUTCOME tiers (a genuine driver-authored
// line, the backstop's synthetic one, then the unauthenticated self-report as a
// last resort) and reports the winner as Resolved.Provenance, so no caller
// reimplements that order. Every walk is last-pass-wins. An unparsed
// self-report fills only Outcome.Status, so a caller must not assume the rest.
func Resolve(logs []PassLog, kind string) (Resolved, error) {
	if kind == "" {
		kind = "work"
	}

	var (
		winner  Outcome
		found   bool
		lastErr error
	)
	for _, log := range logs {
		o, ok, err := lastInLog(log.Path)
		switch {
		case err != nil:
			// Last pass wins applies to a failed attempt too: a later log's
			// near-miss overrides an earlier log's successful match.
			lastErr = err
			found = false
		case ok:
			winner = o
			found = true
			lastErr = nil
		}
		// Neither case: this log had no candidate at all, so it leaves the
		// running state from prior logs untouched.
	}
	// Runs even when the tier above errored, so a caller wanting only the
	// self-report (e.g. `spindrift recover`'s relayed-branch adopt arm) still
	// gets it off the zero-Outcome Resolved returned alongside a near-miss
	// error (issue #2268 slice 1).
	report, reportFound, reportErr := lastSelfReportAcrossLogs(logs)

	if lastErr != nil {
		return Resolved{Kind: kind, SelfReport: report, SelfReportFound: reportFound, SelfReportError: reportErr}, lastErr
	}

	if found {
		provenance := ProvenanceGenuine
		if winner.Synthetic {
			provenance = ProvenanceSynthetic
		}
		return Resolved{
			Outcome:         winner,
			Provenance:      provenance,
			Kind:            kind,
			Found:           true,
			SelfReport:      report,
			SelfReportFound: reportFound,
			SelfReportError: reportErr,
		}, nil
	}

	if !reportFound {
		return Resolved{Kind: kind, SelfReportError: reportErr}, nil
	}
	if report.Parsed {
		return Resolved{
			Outcome:         report.Outcome,
			Provenance:      ProvenanceSelfReport,
			Kind:            kind,
			Found:           true,
			SelfReport:      report,
			SelfReportFound: true,
			SelfReportError: reportErr,
		}, nil
	}
	return Resolved{
		Outcome:         Outcome{Status: report.Status},
		Provenance:      ProvenanceSelfReport,
		Kind:            kind,
		Found:           true,
		SelfReport:      report,
		SelfReportFound: true,
		SelfReportError: reportErr,
	}, nil
}

// lastSelfReportAcrossLogs walks logs in order calling lastSelfReportInLog,
// keeping the last one that reports a match ("last pass wins"). Because this
// is the last-resort tier, a single unreadable log is skipped rather than
// aborting the selection; the last I/O error seen anywhere in the walk is
// returned regardless of which log wins, so it stays observable.
func lastSelfReportAcrossLogs(logs []PassLog) (SelfReport, bool, error) {
	var (
		winner  SelfReport
		found   bool
		lastErr error
	)
	for _, log := range logs {
		report, ok, err := lastSelfReportInLog(log.Path)
		if err != nil {
			lastErr = err
		}
		if err != nil || !ok {
			continue
		}
		winner = report
		found = true
	}
	return winner, found, lastErr
}

// LastCommentLineInLog decodes the last verifying line in the file at path
// carrying the grammar SPINDRIFT_COMMENT <nonce> <base64-body> (issue #1940).
// See lastVerifiedSignalInLog for the verify-then-prefer selection, which
// differs from lastInLog's take-the-last-line behaviour. err is set only when
// every token-bearing line fails to verify, never when there was no comment.
func LastCommentLineInLog(path, expectedNonce string) (string, bool, int, error) {
	return lastVerifiedSignalInLog(path, CommentToken, expectedNonce,
		"comment line found but did not verify: nonce mismatch or malformed payload")
}

// base64AlphabetPrefix returns the longest prefix of s made only of
// standard-base64 characters, the boundary a JSON-escaped trailing `\n`
// (backslash then 'n', neither a base64 character) never crosses.
func base64AlphabetPrefix(s string) string {
	for i := 0; i < len(s); i++ {
		if !isBase64Char(s[i]) {
			return s[:i]
		}
	}
	return s
}

func isBase64Char(b byte) bool {
	return ('A' <= b && b <= 'Z') || ('a' <= b && b <= 'z') || ('0' <= b && b <= '9') || b == '+' || b == '/' || b == '='
}

// LastPRIntentInLog decodes the last verifying line in the file at path
// carrying SPINDRIFT_PR_INTENT <nonce> <base64-payload> (issue #1938): the
// "title\n\nbody" a read-only Box hands the launcher in place of its own
// `gh pr create` (issue #1919). The grammar is single-line because stream-json
// collapses a multi-line block onto one line, hiding it (issue #1921).
func LastPRIntentInLog(path, expectedNonce string) (string, bool, int, error) {
	return lastVerifiedSignalInLog(path, PRIntentToken, expectedNonce,
		"PR-intent line found but did not verify: nonce mismatch or malformed payload")
}

// AllIssueIntentLinesInLog decodes every verifying line in the file at path
// carrying SPINDRIFT_ISSUE_INTENT <nonce> <base64-payload> (issue #2018), in
// encounter order, deduped by decoded bytes because a Filer subagent echoes its
// one intent line twice (issue #2068). A non-verifying line is dropped, so an
// untrusted author's echo is never filed, but counted in rejectedCount (#2976).
func AllIssueIntentLinesInLog(path, expectedNonce string) ([]string, int, error) {
	return scanSignalLines(path, IssueIntentToken, expectedNonce, true)
}

// lastVerifiedSignalInLog is the shared "last verifying signal wins" scanner
// behind LastCommentLineInLog and LastPRIntentInLog: the last verifying line
// wins over a later line that merely carries the token, because an untrusted
// author wrote their echo before this run's nonce was minted. The int counts
// non-verifying lines; notVerifiedErr is the "found but none verified" text.
func lastVerifiedSignalInLog(path, token, expectedNonce, notVerifiedErr string) (string, bool, int, error) {
	matches, rejectedCount, err := scanSignalLines(path, token, expectedNonce, false)
	if err != nil {
		return "", false, 0, err
	}
	if len(matches) > 0 {
		return matches[len(matches)-1], true, rejectedCount, nil
	}
	if rejectedCount > 0 {
		return "", false, rejectedCount, errors.New(notVerifiedErr)
	}
	return "", false, 0, nil
}

// scanSignalLines is the one scanning skeleton behind all three public signal
// scanners (issue #2976). collectAll false is "last verifying line wins"; true
// collects every verifying line, deduped by decoded payload identity, in
// encounter order (issue #2018/#2068). A non-verifying line is never an error
// here: last-wins and collect-all callers weigh rejectedCount differently.
func scanSignalLines(path, token, expectedNonce string, collectAll bool) (matches []string, rejectedCount int, err error) {
	// seen dedups collectAll's payloads by decoded byte identity (issue #2068,
	// see AllIssueIntentLinesInLog). Left nil in last-wins mode.
	var seen map[string]bool
	if collectAll {
		seen = make(map[string]bool)
	}
	scanErr := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		if !containsToken(line, token) {
			return
		}
		if body, ok := parseSignalLine(line, token, expectedNonce); ok {
			if collectAll {
				if seen[body] {
					return
				}
				seen[body] = true
			}
			matches = append(matches, body)
			return
		}
		if looksLikeSignalAttempt(line, token) {
			rejectedCount++
		}
	})
	if scanErr != nil {
		if errors.Is(scanErr, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, scanErr
	}
	return matches, rejectedCount, nil
}

// looksLikeSignalAttempt reports whether line is a real attempt at the
// "<token> <nonce> <base64-payload>" grammar rather than prose naming the
// token or a one-field doc example: the token must lead the line (only
// whitespace or a stream-json-escaped newline before it) and carry at least
// two fields. Only such a line, when it fails to verify, is worth a warning (#2089).
func looksLikeSignalAttempt(line, token string) bool {
	idx := tokenIndex(line, token)
	if idx < 0 {
		return false
	}
	prefix := strings.TrimSpace(line[:idx])
	if prefix != "" && !strings.HasSuffix(prefix, "\\n") {
		return false
	}
	return len(strings.Fields(line[idx+len(token):])) >= 2
}

// parseSignalLine extracts and strictly decodes the payload of a single-line
// "<token> <nonce> <base64-payload>" control signal. line must carry
// expectedNonce as the field structurally following the token, and the payload
// after it is decoded with the strict standard decoder, rejecting any decode
// error outright rather than stripping whitespace or decoding best-effort.
func parseSignalLine(line, token, expectedNonce string) (string, bool) {
	idx := tokenIndex(line, token)
	if idx < 0 {
		return "", false
	}
	// strings.Fields is already word-bounded and never yields an empty token,
	// so an empty expectedNonce can never match: no separate LineHasNonce gate
	// is needed here.
	fields := strings.Fields(line[idx+len(token):])
	if len(fields) < 2 || fields[0] != expectedNonce {
		return "", false
	}
	payload := base64AlphabetPrefix(fields[1])
	decoded, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

// LineHasNonce reports whether line carries expected as a standalone token
// (issue #1937): the check that separates a control-signal line produced by
// this run's own Box from one an untrusted issue/comment author echoed
// verbatim, since the author writes their text before the per-run nonce is
// minted. An empty expected never matches.
func LineHasNonce(line, expected string) bool {
	if expected == "" {
		return false
	}
	return containsToken(line, expected)
}

// containsToken reports whether line contains token as a standalone word, not
// as a substring of a longer identifier ("SPINDRIFT_OUTCOMES" must not match).
func containsToken(line, token string) bool {
	return tokenIndex(line, token) >= 0
}

// tokenIndex returns the index of token's first standalone-word occurrence in
// line, or -1. A literal `\n` (backslash then 'n') immediately before the
// token counts as a left boundary alongside a genuine non-token character: it
// is JSON's escaping of a real newline the Box wrote, and without this its
// trailing 'n' would look like it extends the token into a longer identifier.
func tokenIndex(line, token string) int {
	for start := 0; ; {
		i := strings.Index(line[start:], token)
		if i < 0 {
			return -1
		}
		begin := start + i
		end := begin + len(token)
		leftOK := begin == 0 || !isTokenChar(line[begin-1]) ||
			(line[begin-1] == 'n' && begin >= 2 && line[begin-2] == '\\')
		rightOK := end == len(line) || !isTokenChar(line[end])
		if leftOK && rightOK {
			return begin
		}
		start = begin + 1
	}
}

func isTokenChar(b byte) bool {
	return b == '_' || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z') || ('0' <= b && b <= '9')
}

// stripToken reports whether line begins with token followed by a space or a
// colon (the tolerated delimiters), and returns the remainder after the
// delimiter for field extraction.
func stripToken(line, token string) (string, bool) {
	if rest, ok := strings.CutPrefix(line, token+" "); ok {
		return rest, true
	}
	if rest, ok := strings.CutPrefix(line, token+":"); ok {
		return rest, true
	}
	return "", false
}

// tokenField extracts the value of key=<val> from a space-delimited line.
// val ends at the next space; use tailField for the note field.
func tokenField(line, key string) string {
	prefix := key + "="
	for _, tok := range strings.Fields(line) {
		if strings.HasPrefix(tok, prefix) {
			return tok[len(prefix):]
		}
	}
	return ""
}

// tailField returns everything after the first " key=" in line, allowing the
// value to contain spaces and '=' (used for the note field).
func tailField(line, key string) string {
	marker := " " + key + "="
	if idx := strings.Index(line, marker); idx >= 0 {
		return line[idx+len(marker):]
	}
	return ""
}

// noteField is tailField("note") with a trailing " nonce=<value>" stripped off
// (issue #1939): the grammar puts nonce after note's greedy tail, and
// settle.postBlockedNoteComment posts the note publicly on status=blocked,
// where a leaked nonce lets a comment author replay it against a later retry,
// which reuses one nonce. Only a space-free <value> counts as the field.
func noteField(line string) string {
	v := tailField(line, "note")
	const marker = " nonce="
	if idx := strings.LastIndex(v, marker); idx >= 0 {
		if nonce := v[idx+len(marker):]; nonce != "" && !strings.Contains(nonce, " ") {
			return v[:idx]
		}
	}
	return v
}
