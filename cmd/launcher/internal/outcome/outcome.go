// Package outcome owns the SPINDRIFT_OUTCOME grammar, parsing, and log scan.
// It is the single source of truth for the per-Box result contract between
// the Agent and the Harness (see CONTEXT.md — Outcome line).
package outcome

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
)

// Token is the exact SPINDRIFT_OUTCOME marker literal. ADR 0035: the
// orchestrator's scanPassLog greps a rendered pass log for this literal via
// ParseAnywhere, and issue-prompt.md's OUTCOME contract must keep emitting it
// verbatim -- rewording either side without the other silently collapses the
// multi-pass loop to single-pass on ORCHESTRATOR_ENABLED runs. Exported so
// that guard (cmd/launcher/orchestrator's TestPromptMarkersMatchScanner) and
// this package's own Parse/ParseAnywhere/LastInLog share one literal instead
// of each redeclaring it.
const Token = "SPINDRIFT_OUTCOME"

// PRIntentToken is the exact SPINDRIFT_PR_INTENT marker literal (issue
// #2045, the #2036 fix): a read-only Box's draft-PR title/body hand-off,
// scanned host-side by LastPRIntentInLog below and, in-box, by
// entrypoint.sh's own required-marker-gate row that resumes the session
// once when this marker never showed up on a status=ready run. Exported so
// the marker-contract parity guard (cmd/launcher/orchestrator's
// TestPromptMarkersMatchScanner) can pin the two PR-intent-emitting
// fragments (open-pr-create-outbox.md, if-blocked-pr-outbox.md) against
// this one literal instead of a hardcoded string of its own.
const PRIntentToken = "SPINDRIFT_PR_INTENT"

// Outcome is the machine-readable result written by a Box as its final line.
// Grammar: SPINDRIFT_OUTCOME issue=<num> landing=<landing-ref> status=<status> note=<text>
// Note may contain spaces and '='; all other fields are space-delimited tokens.
type Outcome struct {
	Issue string
	// Landing is the landing reference: a PR URL under CODE_FORGE=github, a
	// branch ref (e.g. "agent/issue-42") under the push-only CODE_FORGE=git,
	// or a verdict-comment URL for the research dispatch kind.
	Landing string
	Status  string // ready | blocked | failed | merged | …
	Note    string // free text; may contain spaces and '='
}

// ErrNearMiss marks a Parse error where the SPINDRIFT_OUTCOME token is
// present in the line but the line still fails to parse — required fields
// missing or malformed, or the token embedded mid-sentence rather than
// leading a standalone line. Separable from the token being entirely absent
// so callers (e.g. a resume nudge) can react to "almost got it" differently
// from "never tried".
var ErrNearMiss = errors.New("outcome: near-miss")

// IsNearMiss reports whether err was returned because a SPINDRIFT_OUTCOME
// token was present but the line did not parse, as opposed to the token
// being entirely absent.
func IsNearMiss(err error) bool {
	return errors.Is(err, ErrNearMiss)
}

// Parse parses a single SPINDRIFT_OUTCOME line.
// Returns an error if the line lacks the required prefix or is missing the
// landing or status fields. The latter case, and a line where the token
// appears but not as a standalone-line prefix, are wrapped in ErrNearMiss
// (see IsNearMiss). Parse alone doesn't require a field marker for the
// mid-sentence case — that extra gate belongs to LastInLog, which scans
// whole logs and needs it to avoid mistaking a bare mention in prose for an
// attempt; a caller handing Parse a single already-selected line doesn't.
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
		Issue:   tokenField(rest, "issue"),
		Landing: tokenField(rest, "landing"),
		Status:  tokenField(rest, "status"),
		Note:    noteField(rest),
	}
	if o.Landing == "" {
		return Outcome{}, fmt.Errorf("%w: missing landing field", ErrNearMiss)
	}
	if o.Status == "" {
		return Outcome{}, fmt.Errorf("%w: missing or empty status field", ErrNearMiss)
	}
	return o, nil
}

// Line returns the canonical SPINDRIFT_OUTCOME representation of o.
// Parse(o.Line()) == o for all valid Outcomes.
func (o Outcome) Line() string {
	return fmt.Sprintf("%s issue=%s landing=%s status=%s note=%s",
		Token, o.Issue, o.Landing, o.Status, o.Note)
}

// ParseAnywhere finds the SPINDRIFT_OUTCOME token anywhere in line -- not
// only as a leading prefix, per Parse's own stricter grammar -- and parses
// the outcome starting there. For a caller scanning already-rendered
// transcript text rather than a raw box log (issue #1998's orchestrator,
// reading a pass's own log through the claude Driver's RenderTranscript,
// which prefixes every line with a "[role] " tag): the token is genuinely
// present but never leads the line the way a Box's own bare final-message
// line does, so Parse alone would always report ErrNearMiss. Returns
// (Outcome{}, false) when the token is absent, or the text from that point
// on fails to parse.
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

// LastInLog scans the file at path for the SPINDRIFT_OUTCOME token and
// parses the result via Parse, so the same colon/whitespace tolerance and
// near-miss classification apply. It prefers the last line that leads with
// the token (a genuine attempt at the grammar, however it fares in Parse)
// over any line that merely carries the token mid-sentence alongside at
// least one field marker (issue=/landing=/status=/note=) — a genuine, if
// malformed, attempt wrapped in prose. A line that just names the token in
// passing, with no field marker at all, is not a candidate: agent
// reasoning routinely mentions "SPINDRIFT_OUTCOME" without attempting the
// grammar, and treating every such mention as a near-miss would abandon
// runs the prior no-outcome-found path handled fine. Only when no
// leading-token line exists at all does the last field-bearing mid-sentence
// mention become the near-miss candidate. Lines larger than the 4 MiB scan
// buffer are skipped rather than aborting the scan.
//
// Returns (Outcome{}, false, nil) when no qualifying line is present, or the
// file does not exist. Returns (Outcome{}, false, err) when the chosen
// candidate line fails to parse — err satisfies IsNearMiss in that case —
// or on an I/O error other than file-not-found or oversized lines.
//
// expectedNonce gates candidacy (issue #1939): when non-empty, a line that
// would otherwise qualify at either tier above but does not carry
// expectedNonce (per LineHasNonce) is not a candidate at all — the same
// treatment as a bare mention. This is what stops an OUTCOME-shaped line an
// untrusted issue/comment author echoed into the log, who wrote their text
// before this run's nonce was minted and so cannot carry it, from
// shadowing a genuine line via last-wins. An empty expectedNonce disables
// the gate entirely (every line is eligible regardless of nonce content),
// for callers with no per-run nonce to check against. skipped reports
// whether at least one line was excluded solely for failing this gate, so
// a caller can warn that a spoof attempt or misconfigured run occurred even
// when a valid outcome was ultimately found (or wasn't). A line excluded by
// the gate that is also a recognizable template/placeholder line — the
// prompt's own example OUTCOME line with unsubstituted ${...} fields, or an
// empty nonce= field — never sets skipped (issue #2088): such a line is
// never a plausible spoof, so warning about it would be noise. It is still
// excluded from candidacy either way; only the warning is suppressed.
func LastInLog(path string, expectedNonce string) (o Outcome, found bool, skipped bool, err error) {
	var lastLeading, lastMention string
	scanErr := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		if _, ok := stripToken(strings.TrimSpace(line), Token); ok {
			if expectedNonce != "" && !LineHasNonce(line, expectedNonce) {
				if !isTemplatePlaceholder(line) {
					skipped = true
				}
				return
			}
			lastLeading = line
			return
		}
		if containsToken(line, Token) && looksLikeAttempt(line) {
			if expectedNonce != "" && !LineHasNonce(line, expectedNonce) {
				if !isTemplatePlaceholder(line) {
					skipped = true
				}
				return
			}
			lastMention = line
		}
	})
	if scanErr != nil {
		if errors.Is(scanErr, os.ErrNotExist) {
			return Outcome{}, false, skipped, nil
		}
		return Outcome{}, false, skipped, scanErr
	}

	candidate := lastLeading
	if candidate == "" {
		candidate = lastMention
	}
	if candidate == "" {
		return Outcome{}, false, skipped, nil
	}
	o, err = Parse(candidate)
	if err != nil {
		return Outcome{}, false, skipped, err
	}
	return o, true, skipped, nil
}

// LastCommentLineInLog scans the file at path for the last line carrying the
// SPINDRIFT_COMMENT token and decodes its single-line grammar: SPINDRIFT_COMMENT
// <nonce> <base64-encoded-body> (issue #1940). See lastVerifiedSignalInLog for
// the verify-then-prefer selection semantics, which differ from LastInLog's
// always-take-the-last-line behavior for SPINDRIFT_OUTCOME.
//
// A stream-json JSONL box log collapses a printed line's trailing newline
// into a literal `\n` escape butted directly against the base64 payload with
// no whitespace in between; the payload is taken as the longest run of valid
// base64 characters after the nonce field, so that trailing JSON escaping
// never reaches the decoder.
//
// Returns ("", false, nil) when no line carries the token at all, or the
// file does not exist — there was never a comment to relay. Returns ("",
// false, err) only when every token-bearing line fails to verify — a spoof
// attempt or a corrupted line, for the caller to log; never treated as
// no-comment. Returns (body, true, nil) from the last line that verifies,
// even if a later, non-verifying line also carries the token — that later
// line is dropped silently rather than reported, since the one-result
// return contract can't carry both a successful relay and a warning at
// once, and the successful relay is what matters.
func LastCommentLineInLog(path, expectedNonce string) (string, bool, error) {
	return lastVerifiedSignalInLog(path, "SPINDRIFT_COMMENT", expectedNonce,
		"comment line found but did not verify: nonce mismatch or malformed payload")
}

// base64AlphabetPrefix returns the longest prefix of s consisting solely of
// standard-base64 alphabet characters (A-Z, a-z, 0-9, '+', '/', '=') — the
// boundary a JSON-escaped trailing `\n` (a literal backslash followed by
// 'n', neither a base64 character) or other JSON syntax never crosses.
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

// LastPRIntentInLog scans the file at path for the last line carrying the
// SPINDRIFT_PR_INTENT token and decodes its single-line grammar:
// SPINDRIFT_PR_INTENT <nonce> <base64-payload> (issue #1938) — the draft-PR
// title and body a read-only Box hands the launcher in place of its own
// `gh pr create` (issue #1919), replacing the retired
// SPINDRIFT_PR_INTENT_BEGIN/END block the same way LastCommentLineInLog
// replaced LastCommentInLog: a stream-json JSONL box log collapses a
// multi-line block onto one physical line, so an exact-line marker scan
// never finds it (issue #1921's dogfood failure).
//
// See lastVerifiedSignalInLog for the verify-then-prefer selection semantics,
// shared with LastCommentLineInLog.
//
// The decoded payload is the same "title\n\nbody" shape the retired block
// held: the first line is the PR title and the remainder, after a blank
// line, is the PR body; splitting title from body remains the caller's
// concern.
//
// Returns ("", false, nil) when no line carries the token at all, or the
// file does not exist — there was never a PR-intent to relay. Returns ("",
// false, err) only when every token-bearing line fails to verify — a spoof
// attempt or a corrupted line, for the caller to log; never conflated with
// no-PR-intent-found. Returns (payload, true, nil) from the last line that
// verifies, even if a later, non-verifying line also carries the token.
func LastPRIntentInLog(path, expectedNonce string) (string, bool, error) {
	return lastVerifiedSignalInLog(path, PRIntentToken, expectedNonce,
		"PR-intent line found but did not verify: nonce mismatch or malformed payload")
}

// AllIssueIntentLinesInLog scans the file at path for every line carrying
// the SPINDRIFT_ISSUE_INTENT token and decodes each verifying line's
// single-line grammar: SPINDRIFT_ISSUE_INTENT <nonce> <base64-payload>
// (issue #2018) — a read-only Box's file-an-issue request, host-mediated the
// same way LastCommentLineInLog/LastPRIntentInLog relay a comment or
// draft-PR intent. Unlike those two singleton "last verifying line wins"
// scanners, issue filing is 1-to-many — a run may want to file several
// issues — so every verifying line's decoded payload is collected, in the
// order encountered, rather than only the last surviving. Payloads are
// deduped by their decoded byte identity (issue #2068): a Filer subagent
// echoes its one intent line twice into the raw stream-json log — once as its
// own `assistant` event, once in the parent's `tool_result` event — and the
// two decode identically, so the second is dropped rather than filed as a
// duplicate issue. A line carrying the token that fails to verify (wrong
// nonce, malformed base64) is silently dropped from the result, exactly as a
// non-verifying PR-intent/comment line is dropped from those scanners' single
// result — an untrusted issue/comment author's echo of the token must never
// be mistaken for a genuine filed-issue request.
//
// Returns (nil, nil) when no line carries the token at all, or the file does
// not exist — there was never an issue to file. Returns (nil, err) only on a
// genuine I/O error other than file-not-found or an oversized skipped line;
// a token-bearing line that fails to verify is dropped, not reported as an
// error, since a caller has no single result to attach a warning to the way
// the singleton scanners do.
func AllIssueIntentLinesInLog(path, expectedNonce string) ([]string, error) {
	const token = "SPINDRIFT_ISSUE_INTENT"
	var payloads []string
	// seen dedups by the host-side decoded payload's byte identity — see the
	// doc comment above for why the subagent Filer echoes each intent line
	// twice (issue #2068). The key is the host-decoded payload itself, never
	// the Box-supplied DedupTerms field, which stays untrusted and
	// parsed-but-ignored downstream.
	seen := make(map[string]bool)
	err := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		if !containsToken(line, token) {
			return
		}
		if body, ok := parseSignalLine(line, token, expectedNonce); ok {
			if seen[body] {
				return
			}
			seen[body] = true
			payloads = append(payloads, body)
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return payloads, nil
}

// lastVerifiedSignalInLog is the shared "last verifying signal wins" scanner
// backing both LastCommentLineInLog and LastPRIntentInLog: among lines
// carrying token, the last one that actually verifies (right nonce, valid
// strict base64, via parseSignalLine) wins over a later line that merely
// carries the token without verifying — an untrusted issue/comment author's
// echo of either channel's token, since they wrote their text before this
// run's nonce was minted, must not be able to shadow an earlier genuine
// line. notVerifiedErr is the per-channel error text returned when at least
// one token-bearing line was found but none verified.
func lastVerifiedSignalInLog(path, token, expectedNonce, notVerifiedErr string) (string, bool, error) {
	var lastValid string
	var found bool
	var rejected bool
	err := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		if !containsToken(line, token) {
			return
		}
		if body, ok := parseSignalLine(line, token, expectedNonce); ok {
			lastValid = body
			found = true
			return
		}
		if looksLikeSignalAttempt(line, token) {
			rejected = true
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	if found {
		return lastValid, true, nil
	}
	if rejected {
		return "", false, errors.New(notVerifiedErr)
	}
	return "", false, nil
}

// looksLikeSignalAttempt reports whether line is a genuine (if malformed or
// spoofed) attempt at the "<token> <nonce> <base64-payload>" grammar rather
// than prose that merely names the token or a one-field doc example: the token
// must lead the line (nothing but whitespace, or a stream-json-escaped newline,
// before it) and be followed by at least the two fields a real signal carries.
// Only such a line, when it fails to verify, is the echo/spoof case worth
// warning about (issue #2089).
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

// parseSignalLine extracts and strictly decodes the payload of a
// single-line "<token> <nonce> <base64-payload>" control signal — the
// shared grammar lastVerifiedSignalInLog pins to one token per call, on
// behalf of both LastCommentLineInLog and LastPRIntentInLog. line must
// carry expectedNonce as the field structurally following
// the token (word-bounded via strings.Fields, so an empty expectedNonce can
// never match), and the base64 payload — the field after that — is decoded
// with the standard strict decoder, rejecting any decode error outright
// rather than stripping whitespace or best-effort decoding.
func parseSignalLine(line, token, expectedNonce string) (string, bool) {
	idx := tokenIndex(line, token)
	if idx < 0 {
		return "", false
	}
	// fields[0] must equal expectedNonce exactly: strings.Fields already
	// splits on whitespace, so this is itself a word-bounded check, and an
	// empty expectedNonce can never match since Fields never yields an
	// empty token -- no separate LineHasNonce gate needed.
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
// (issue #1937) — the shared check a caller uses to tell a genuine
// control-signal line, produced by this run's own Box, from one an
// untrusted issue/comment author echoed into the log verbatim: the author
// writes their text before the per-run nonce is minted, so they cannot know
// its value. Word-bounded via containsToken, so a longer token that merely
// contains expected as a substring does not false-positive. An empty
// expected never matches.
func LineHasNonce(line, expected string) bool {
	if expected == "" {
		return false
	}
	return containsToken(line, expected)
}

// containsToken reports whether line contains token as a standalone word,
// not merely as a substring of a longer identifier (e.g. "SPINDRIFT_OUTCOMES"
// or "MY_SPINDRIFT_OUTCOME_THING" must not match).
func containsToken(line, token string) bool {
	return tokenIndex(line, token) >= 0
}

// tokenIndex returns the index of token's first standalone-word occurrence
// in line, or -1 if none exists. Standalone means not preceded or followed
// by an identifier character (see isTokenChar), so a longer identifier that
// merely contains token as a substring never matches. A literal `\n`
// (backslash then 'n') immediately before the token counts as a left
// boundary too, alongside a genuine non-token-char: it's JSON's escaping of
// a real newline the Box's own text wrote right before the token — e.g. a
// line of narration flowing straight into a control-signal line within the
// same stream-json text field — and a real newline is unambiguously a
// boundary, so its escaped form must be too. Without this, the escaped
// sequence's trailing 'n' (itself a token char) would look like it extends
// the token into a longer identifier and reject a placement no different
// from the token simply starting its own physical line.
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

// looksLikeAttempt reports whether line carries at least one recognizable
// outcome field marker, distinguishing a genuine (if malformed or
// mid-sentence) attempt at the grammar from prose that merely names the
// token.
func looksLikeAttempt(line string) bool {
	return tokenField(line, "issue") != "" ||
		tokenField(line, "landing") != "" ||
		tokenField(line, "status") != "" ||
		noteField(line) != ""
}

// isTemplatePlaceholder reports whether line is a recognizable prompt
// template/example line rather than a genuine (or spoofed) attempt (issue
// #2088): either it contains an unsubstituted `${...}` substitution field —
// the prompt's own grammar example, never a real value a Box or an untrusted
// echo would produce — or it carries a standalone `nonce=` field token with
// no value at all. Used only to decide whether an excluded nonce-gate line
// is worth warning about; it never affects candidacy, since a line that
// fails the nonce gate is excluded from candidacy either way.
func isTemplatePlaceholder(line string) bool {
	if strings.Contains(line, "${") {
		return true
	}
	for _, tok := range strings.Fields(line) {
		if tok == "nonce=" {
			return true
		}
	}
	return false
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

// noteField is tailField("note") with the trailing " nonce=<value>" field
// (issue #1939) stripped back off, so it never ends up inside Note: the
// grammar places nonce last, after note's own greedy tail, and a run's note
// gets posted as a public comment on status=blocked (settle.postBlockedNoteComment)
// — leaking the nonce there would let a comment author replay it against a
// later retry of the same Dispatch, which reuses one nonce across all its
// attempts. A trailing "nonce=<value>" only counts as the field, not as note
// text that happens to contain it, when <value> itself has no spaces —
// exactly the shape a genuine, single-token nonce always has.
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
