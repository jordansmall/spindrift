package daemon

import (
	"encoding/json"
	"fmt"

	"spindrift.dev/launcher/internal/report"
)

// Record is a type alias, not a copy, of internal/report.Record: internal/
// report is the writing half of this wire shape (one JSON line per event,
// piped from the child over SPINDRIFT_REPORT_FD), this package is the
// reading half, and aliasing means there is only one struct definition for
// the two halves to (dis)agree about — a field added on one side is visible
// on the other by construction, not by a parity test someone has to remember
// to keep passing. Safe because both halves are built from the same source
// tree: there is never a daemon binary reading a wire shape an older or
// newer internal/report wrote. See issue #3627's review finding: this alias
// is what replaced the deleted parity test (TestAnnounceLine_ParsesBack).
type Record = report.Record

// maxIssueLen caps a parsed issue number's digit run: generous enough for
// any real GitHub issue number, tight enough that a malformed or hostile
// line can't hand the rest of the daemon an unbounded string.
const maxIssueLen = 10

// ParseRecord decodes one line of the report protocol. An unknown event
// name or a blank line is not an error — it's ignored, since a forward-
// compatible reader must tolerate an event it doesn't understand yet — and
// reports (Record{}, false, nil). Malformed JSON, or a known event whose
// issue is not a plain non-empty run of ASCII digits within maxIssueLen,
// reports (Record{}, false, err): the caller decides how to surface that.
func ParseRecord(line string) (Record, bool, error) {
	if line == "" {
		return Record{}, false, nil
	}
	var rec Record
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return Record{}, false, err
	}
	switch rec.Event {
	case report.EventBox, report.EventSettled:
	default:
		return Record{}, false, nil
	}
	if !validIssue(rec.Issue) {
		return Record{}, false, fmt.Errorf("daemon: record: invalid issue %q", rec.Issue)
	}
	return rec, true, nil
}

// validIssue reports whether s is a plain non-empty run of ASCII digits no
// longer than maxIssueLen — never a sign, separator, or leading/trailing
// space, since the wire shape has no use for anything but a bare issue
// number.
func validIssue(s string) bool {
	if s == "" || len(s) > maxIssueLen {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
