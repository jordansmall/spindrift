package daemon

import (
	"encoding/json"
	"errors"
	"fmt"

	"spindrift.dev/launcher/internal/promptassembly"
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

// maxChoreLen caps a parsed Chore name's length: generous for any real
// butler Chore (bugs, refactor, docs-drift), tight enough that a malformed
// or hostile line can't hand the rest of the daemon an unbounded string —
// the Chore-keyed counterpart of maxIssueLen.
const maxChoreLen = 64

// ErrKindMismatch wraps ParseRecord's error for a well-formed record whose
// key shape doesn't match the emitting child's kind, so a reader can tell an
// emitter regression apart from a merely malformed line.
var ErrKindMismatch = errors.New("record key does not match child kind")

// ParseRecord decodes one line of the report protocol, emitted by a child of
// the given kind. An unknown event name or a blank line is not an error —
// it's ignored, since a forward-compatible reader must tolerate an event it
// doesn't understand yet — and reports (Record{}, false, nil). Malformed
// JSON reports (Record{}, false, err), and so does a known event whose
// Issue/Chore pair isn't exactly one valid one of the two (issue #3878): an
// issue-keyed record and a Chore-keyed butler record (ADR 0056) share this
// wire shape, and a record naming both or neither is never valid — and nor
// is a record whose key shape doesn't match kind: a Chore record from a
// non-butler child, or an issue record from a butler child.
func ParseRecord(line string, kind Kind) (Record, bool, error) {
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
	hasIssue := rec.Issue != ""
	hasChore := rec.Chore != ""
	switch {
	case hasIssue && hasChore:
		return Record{}, false, fmt.Errorf("daemon: record: carries both issue %q and chore %q", rec.Issue, rec.Chore)
	case hasIssue:
		if !validIssue(rec.Issue) {
			return Record{}, false, fmt.Errorf("daemon: record: invalid issue %q", rec.Issue)
		}
		if kind == KindButler {
			return Record{}, false, fmt.Errorf("daemon: record: butler child sent issue-keyed record %q: %w", rec.Issue, ErrKindMismatch)
		}
	case hasChore:
		if !validChore(rec.Chore) {
			return Record{}, false, fmt.Errorf("daemon: record: invalid chore %q", rec.Chore)
		}
		if kind != KindButler {
			return Record{}, false, fmt.Errorf("daemon: record: %s child sent chore-keyed record %q: %w", kind, rec.Chore, ErrKindMismatch)
		}
	default:
		return Record{}, false, fmt.Errorf("daemon: record: carries neither issue nor chore")
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

// validChore reports whether s is within maxChoreLen and passes
// promptassembly.ValidChoreName — the same check a Chore name must already
// satisfy before it ever reaches a Box (ADR 0056). maxChoreLen is an extra,
// wire-only bound ValidChoreName itself doesn't impose.
func validChore(s string) bool {
	return len(s) <= maxChoreLen && promptassembly.ValidChoreName(s)
}
