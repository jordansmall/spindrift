// Package outcome owns the SPINDRIFT_OUTCOME grammar, parsing, and log scan.
// It is the single source of truth for the per-Box result contract between
// the Agent and the Harness (see CONTEXT.md — Outcome line).
package outcome

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
)

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
	const token = "SPINDRIFT_OUTCOME"
	line = strings.TrimSpace(line)
	rest, ok := stripToken(line, token)
	if !ok {
		if containsToken(line, token) {
			return Outcome{}, fmt.Errorf("%w: line contains %q but does not match the standalone-line grammar", ErrNearMiss, token)
		}
		return Outcome{}, fmt.Errorf("outcome: line missing %q prefix", token+" ")
	}
	o := Outcome{
		Issue:   tokenField(rest, "issue"),
		Landing: tokenField(rest, "landing"),
		Status:  tokenField(rest, "status"),
		Note:    tailField(rest, "note"),
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
	return fmt.Sprintf("SPINDRIFT_OUTCOME issue=%s landing=%s status=%s note=%s",
		o.Issue, o.Landing, o.Status, o.Note)
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
func LastInLog(path string) (Outcome, bool, error) {
	const token = "SPINDRIFT_OUTCOME"
	var lastLeading, lastMention string
	err := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		if _, ok := stripToken(strings.TrimSpace(line), token); ok {
			lastLeading = line
			return
		}
		if containsToken(line, token) && looksLikeAttempt(line) {
			lastMention = line
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Outcome{}, false, nil
		}
		return Outcome{}, false, err
	}

	candidate := lastLeading
	if candidate == "" {
		candidate = lastMention
	}
	if candidate == "" {
		return Outcome{}, false, nil
	}
	o, err := Parse(candidate)
	if err != nil {
		return Outcome{}, false, err
	}
	return o, true, nil
}

// LastCommentInLog scans the file at path and returns the body of the last
// complete SPINDRIFT_COMMENT_BEGIN … SPINDRIFT_COMMENT_END block — the lines
// between the delimiters, joined with "\n" — using the same last-wins and
// oversized-line-skipping semantics as LastInLog. An unterminated BEGIN (no
// matching END before EOF or before a later BEGIN) is discarded rather than
// returned as a partial block.
//
// Returns ("", false, nil) when no complete block is present or the file
// does not exist. Returns ("", false, err) on I/O errors other than
// file-not-found or oversized lines.
func LastCommentInLog(path string) (string, bool, error) {
	const beginMarker = "SPINDRIFT_COMMENT_BEGIN"
	const endMarker = "SPINDRIFT_COMMENT_END"

	var last string
	var found bool
	var open bool
	var buf []string

	err := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		switch {
		case line == beginMarker:
			open = true
			buf = nil
		case line == endMarker:
			if open {
				last = strings.Join(buf, "\n")
				found = true
			}
			open = false
			buf = nil
		case open:
			buf = append(buf, line)
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return last, found, nil
}

// containsToken reports whether line contains token as a standalone word,
// not merely as a substring of a longer identifier (e.g. "SPINDRIFT_OUTCOMES"
// or "MY_SPINDRIFT_OUTCOME_THING" must not match).
func containsToken(line, token string) bool {
	for start := 0; ; {
		i := strings.Index(line[start:], token)
		if i < 0 {
			return false
		}
		begin := start + i
		end := begin + len(token)
		if (begin == 0 || !isTokenChar(line[begin-1])) && (end == len(line) || !isTokenChar(line[end])) {
			return true
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
		tailField(line, "note") != ""
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
