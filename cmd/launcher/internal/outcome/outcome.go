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

// Parse parses a single SPINDRIFT_OUTCOME line.
// Returns an error if the line lacks the required prefix or is missing the
// landing or status fields.
func Parse(line string) (Outcome, error) {
	const prefix = "SPINDRIFT_OUTCOME "
	if !strings.HasPrefix(line, prefix) {
		return Outcome{}, fmt.Errorf("outcome: line missing %q prefix", prefix)
	}
	o := Outcome{
		Issue:   tokenField(line, "issue"),
		Landing: tokenField(line, "landing"),
		Status:  tokenField(line, "status"),
		Note:    tailField(line, "note"),
	}
	if o.Landing == "" {
		return Outcome{}, errors.New("outcome: missing landing field")
	}
	if o.Status == "" {
		return Outcome{}, errors.New("outcome: missing or empty status field")
	}
	return o, nil
}

// Line returns the canonical SPINDRIFT_OUTCOME representation of o.
// Parse(o.Line()) == o for all valid Outcomes.
func (o Outcome) Line() string {
	return fmt.Sprintf("SPINDRIFT_OUTCOME issue=%s landing=%s status=%s note=%s",
		o.Issue, o.Landing, o.Status, o.Note)
}

// LastInLog scans the file at path and returns the last SPINDRIFT_OUTCOME
// line parsed as an Outcome. Lines larger than the 4 MiB scan buffer are
// skipped rather than aborting the scan; the last outcome line wins.
//
// Returns (Outcome{}, false, nil) when no outcome line is present or the
// file does not exist. Returns (Outcome{}, false, err) on I/O errors other
// than file-not-found or oversized lines.
func LastInLog(path string) (Outcome, bool, error) {
	var last string
	err := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		if strings.HasPrefix(line, "SPINDRIFT_OUTCOME ") {
			last = line
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Outcome{}, false, nil
		}
		return Outcome{}, false, err
	}

	if last == "" {
		return Outcome{}, false, nil
	}
	o, err := Parse(last)
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
