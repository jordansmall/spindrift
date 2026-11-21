package daemon

import (
	"encoding/json"
	"errors"
	"fmt"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/report"
)

// Record is an alias of internal/report.Record, the writing half of this
// wire shape (one JSON line per event over SPINDRIFT_REPORT_FD), so both
// halves share one struct definition (issue #3627). The alias does not make
// them ship together: after an in-place restart (ADR 0063) an inherited child
// can be older than the daemon, so the reader must keep decoding older shapes
// (TestParseRecord_OlderChildShapesStillDecode).
type Record = report.Record

// maxIssueLen caps a parsed issue number's digit run: generous enough for
// any real GitHub issue number, tight enough that a malformed or hostile
// line can't hand the rest of the daemon an unbounded string.
const maxIssueLen = 10

// maxChoreLen caps a parsed Chore name's length, maxIssueLen's Chore-keyed
// counterpart.
const maxChoreLen = 64

// ErrKindMismatch wraps ParseRecord's error for a well-formed record whose
// key shape doesn't match the emitting child's kind, so a reader can tell an
// emitter regression apart from a merely malformed line.
var ErrKindMismatch = errors.New("record key does not match child kind")

// ParseRecord decodes one line of the report protocol, emitted by a child of
// the given kind. A blank line or unknown event reports (Record{}, false,
// nil), so the reader stays forward-compatible. Malformed JSON (including a
// record naming both issue and chore, which report.Record's decoder
// rejects), a missing or invalid key, a key shape that doesn't match kind
// (wrapping ErrKindMismatch), or a field the event requires being absent is
// an error. image_build is keyless: the child sends it from bootstrap,
// before any Dispatch key exists, so a key or a state other than started or
// finished is an error.
func ParseRecord(line string, kind Kind) (Record, bool, error) {
	if line == "" {
		return Record{}, false, nil
	}
	var rec Record
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return Record{}, false, err
	}
	switch rec.Event {
	case report.EventBox, report.EventSettled, report.EventNotDue, report.EventModel, report.EventCIWait:
	case report.EventImageBuild:
		// The wire value is untrusted, so the typed constants don't vouch for it.
		if state := report.ImageBuildState(rec.State); state != report.ImageBuildStarted && state != report.ImageBuildFinished {
			return Record{}, false, fmt.Errorf("record: image_build: invalid state %q", rec.State)
		}
		if !rec.Key.IsZero() {
			return Record{}, false, errors.New("record: image_build: carries a key")
		}
		return rec, true, nil
	default:
		return Record{}, false, nil
	}
	if rec.Key.IsZero() {
		return Record{}, false, fmt.Errorf("record: carries neither issue nor chore")
	}
	issue, chore := rec.Key.Fields()
	if issue != "" && !validIssue(issue) {
		return Record{}, false, fmt.Errorf("record: invalid issue %q", issue)
	}
	if chore != "" && !validChore(chore) {
		return Record{}, false, fmt.Errorf("record: invalid chore %q", chore)
	}
	if rec.Key.IsChore() != kind.choreKeyed() {
		shape := "issue-keyed"
		if rec.Key.IsChore() {
			shape = "chore-keyed"
		}
		return Record{}, false, fmt.Errorf("record: %s child sent %s record %q: %w", kind, shape, rec.Key, ErrKindMismatch)
	}
	if rec.Event == report.EventNotDue && rec.NextDue.IsZero() {
		return Record{}, false, fmt.Errorf("record: not_due for %q: missing next_due", rec.Key)
	}
	if rec.Event == report.EventModel && rec.Model == "" {
		return Record{}, false, fmt.Errorf("record: model for %q: missing model", rec.Key)
	}
	if rec.Event == report.EventCIWait && rec.PRURL == "" {
		return Record{}, false, fmt.Errorf("record: ci_wait for %q: missing pr_url", rec.Key)
	}
	return rec, true, nil
}

// validIssue reports whether s is a non-empty run of ASCII digits no longer
// than maxIssueLen.
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
