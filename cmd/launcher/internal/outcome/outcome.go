// Package outcome owns the SPINDRIFT_OUTCOME grammar, parsing, and log scan.
// It is the single source of truth for the per-Box result contract between
// the Agent and the Harness (see CONTEXT.md — Outcome line).
package outcome

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Outcome is the machine-readable result written by a Box as its final line.
// Grammar: SPINDRIFT_OUTCOME issue=<num> pr=<landing-ref> status=<status> note=<text>
// Note may contain spaces and '='; all other fields are space-delimited tokens.
type Outcome struct {
	Issue string
	// PR is the landing reference: a PR URL under CODE_FORGE=github, or a
	// branch ref (e.g. "agent/issue-42") under the push-only CODE_FORGE=git,
	// which has no PR concept.
	PR     string
	Status string // ready | blocked | failed | merged | …
	Note   string // free text; may contain spaces and '='
}

// Parse parses a single SPINDRIFT_OUTCOME line.
// Returns an error if the line lacks the required prefix or is missing the
// pr or status fields.
func Parse(line string) (Outcome, error) {
	const prefix = "SPINDRIFT_OUTCOME "
	if !strings.HasPrefix(line, prefix) {
		return Outcome{}, fmt.Errorf("outcome: line missing %q prefix", prefix)
	}
	o := Outcome{
		Issue:  tokenField(line, "issue"),
		PR:     tokenField(line, "pr"),
		Status: tokenField(line, "status"),
		Note:   tailField(line, "note"),
	}
	if o.PR == "" {
		return Outcome{}, errors.New("outcome: missing pr field")
	}
	if o.Status == "" {
		return Outcome{}, errors.New("outcome: missing or empty status field")
	}
	return o, nil
}

// Line returns the canonical SPINDRIFT_OUTCOME representation of o.
// Parse(o.Line()) == o for all valid Outcomes.
func (o Outcome) Line() string {
	return fmt.Sprintf("SPINDRIFT_OUTCOME issue=%s pr=%s status=%s note=%s",
		o.Issue, o.PR, o.Status, o.Note)
}

// LastInLog scans the file at path and returns the last SPINDRIFT_OUTCOME
// line parsed as an Outcome. Lines larger than the 4 MiB scan buffer are
// skipped rather than aborting the scan; the last outcome line wins.
//
// Returns (Outcome{}, false, nil) when no outcome line is present or the
// file does not exist. Returns (Outcome{}, false, err) on I/O errors other
// than file-not-found or oversized lines.
func LastInLog(path string) (Outcome, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Outcome{}, false, nil
		}
		return Outcome{}, false, err
	}
	defer f.Close()

	const bufSize = 4 * 1024 * 1024
	r := bufio.NewReaderSize(f, bufSize)
	var last string
	for {
		line, isPrefix, err := r.ReadLine()
		if isPrefix {
			// Oversized line — drain remaining chunks and skip.
			for isPrefix {
				_, isPrefix, err = r.ReadLine()
				if err != nil {
					break
				}
			}
		} else if err == nil {
			if s := string(line); strings.HasPrefix(s, "SPINDRIFT_OUTCOME ") {
				last = s
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Outcome{}, false, err
		}
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
