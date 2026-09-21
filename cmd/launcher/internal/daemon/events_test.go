package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// failWriter always fails, simulating a closed or full stdout.
type failWriter struct{}

func (failWriter) Write(_ []byte) (int, error) { return 0, errors.New("write failed: broken pipe") }

func TestEmitterEmit(t *testing.T) {
	var buf bytes.Buffer
	fixed := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	e := NewEmitter(&buf, func() time.Time { return fixed })

	exitCode := 0
	e.Emit(Event{
		Event:    "child_finish",
		Kind:     KindDispatch,
		Issue:    "42",
		Revision: "abc123",
		Exit:     &exitCode,
		Outcome:  "dispatched",
	})

	line := buf.String()
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("Emit: expected trailing newline, got %q", line)
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("Emit: expected exactly one newline (one record), got %q", line)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), &got); err != nil {
		t.Fatalf("Emit: invalid JSON: %v (%q)", err, line)
	}
	if got["time"] != "2026-09-19T12:00:00Z" {
		t.Errorf("time = %v, want 2026-09-19T12:00:00Z", got["time"])
	}
	if got["event"] != "child_finish" {
		t.Errorf("event = %v, want child_finish", got["event"])
	}
	if got["kind"] != "dispatch" {
		t.Errorf("kind = %v, want dispatch", got["kind"])
	}
	if got["issue"] != "42" {
		t.Errorf("issue = %v, want 42", got["issue"])
	}
	if got["exit"] != float64(0) {
		t.Errorf("exit = %v, want 0", got["exit"])
	}
	// wait and reason were left unset and must be omitted, not emitted as "".
	if _, ok := got["wait"]; ok {
		t.Errorf("wait present in output, want omitted: %v", got)
	}
	if _, ok := got["reason"]; ok {
		t.Errorf("reason present in output, want omitted: %v", got)
	}
}

func TestEmitterOmitsEmptyExit(t *testing.T) {
	var buf bytes.Buffer
	e := NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	e.Emit(Event{Event: "idle", Wait: "30s"})

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSuffix(buf.Bytes(), []byte("\n")), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := got["exit"]; ok {
		t.Errorf("exit present in output when nil, want omitted: %v", got)
	}
	if got["wait"] != "30s" {
		t.Errorf("wait = %v, want 30s", got["wait"])
	}
}

func TestEmitterEncodeFailureReportsDiagnosticAndDoesNotPanic(t *testing.T) {
	var errBuf bytes.Buffer
	orig := emitErrW
	emitErrW = &errBuf
	t.Cleanup(func() { emitErrW = orig })

	e := NewEmitter(failWriter{}, func() time.Time { return time.Unix(0, 0).UTC() })
	e.Emit(Event{Event: "child_start"}) // must not panic despite the write failure

	if got := errBuf.String(); !strings.Contains(got, "daemon: event stream write failed") {
		t.Fatalf("emitErrW = %q, want it to contain %q", got, "daemon: event stream write failed")
	}
}

// eventDiff renders wantEvents' mismatch report and is exported (in test
// scope) as its own function so a self-test can inspect the rendering
// without going through a testing.T that would fail the suite on mismatch.
// It reports the first index where got and want diverge — "<missing>" for
// an index past got's end (want expected more events than arrived),
// "<none>" for an index past want's end (got carries more events than want
// asked for, so want has nothing at this index) — then both full
// sequences, then msg as the caller's "why this is the right sequence"
// context. Returns "" when the sequences match exactly.
func eventDiff(got, want []string, msg string) string {
	longest := len(got)
	if len(want) > longest {
		longest = len(want)
	}
	for i := 0; i < longest; i++ {
		g := "<missing>"
		if i < len(got) {
			g = got[i]
		}
		w := "<none>"
		if i < len(want) {
			w = want[i]
		}
		if g != w {
			return fmt.Sprintf("event sequence mismatch at index %d: got %q, want %q\n got:  %v\n want: %v\n%s",
				i, g, w, got, want, msg)
		}
	}
	return ""
}

// wantEvents decodes buf's JSON-lines event stream and asserts its event
// names are exactly want, in order. It returns the decoded events so a
// caller can go on to assert on their fields.
func wantEvents(t *testing.T, buf *bytes.Buffer, want []string, msg string) []Event {
	t.Helper()
	events := decodeEvents(t, buf)
	if diff := eventDiff(eventNames(events), want, msg); diff != "" {
		t.Fatalf("%s", diff)
	}
	return events
}

func TestWantEventsMatchingSequenceReturnsDecodedEvents(t *testing.T) {
	var buf bytes.Buffer
	e := NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	e.Emit(Event{Event: "child_start"})
	e.Emit(Event{Event: "child_finish"})

	events := wantEvents(t, &buf, []string{"child_start", "child_finish"}, "both events must appear in emission order")
	if len(events) != 2 {
		t.Fatalf("wantEvents returned %d events, want 2", len(events))
	}
}

func TestEventDiffNameMismatchReportsFirstDivergingIndex(t *testing.T) {
	diff := eventDiff([]string{"a", "b", "c"}, []string{"a", "x", "c"}, "why clause")
	if diff == "" {
		t.Fatalf("eventDiff = \"\", want a non-empty mismatch report")
	}
	if !strings.Contains(diff, "index 1") || !strings.Contains(diff, `"b"`) || !strings.Contains(diff, `"x"`) {
		t.Fatalf("eventDiff = %q, want it to name index 1, got \"b\", want \"x\"", diff)
	}
	if !strings.Contains(diff, "why clause") {
		t.Fatalf("eventDiff = %q, want it to include the msg", diff)
	}
}

func TestEventDiffLengthMismatchReportsMissingOrNone(t *testing.T) {
	shortGot := eventDiff([]string{"a"}, []string{"a", "b"}, "")
	if !strings.Contains(shortGot, "<missing>") {
		t.Fatalf("eventDiff (got shorter) = %q, want it to mark the missing tail with <missing>", shortGot)
	}
	longGot := eventDiff([]string{"a", "b"}, []string{"a"}, "")
	if !strings.Contains(longGot, "<none>") {
		t.Fatalf("eventDiff (got longer) = %q, want it to mark want's exhausted tail with <none>", longGot)
	}
}

func TestEventDiffMatchReturnsEmpty(t *testing.T) {
	if diff := eventDiff([]string{"a", "b"}, []string{"a", "b"}, "msg"); diff != "" {
		t.Fatalf("eventDiff on matching sequences = %q, want \"\"", diff)
	}
}

func TestEmitterMultipleEmitsOneLineEach(t *testing.T) {
	var buf bytes.Buffer
	e := NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	e.Emit(Event{Event: "child_start"})
	e.Emit(Event{Event: "halt", Reason: "host-tainted"})

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), buf.String())
	}
	for _, l := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(l), &got); err != nil {
			t.Fatalf("invalid JSON line %q: %v", l, err)
		}
	}
}
