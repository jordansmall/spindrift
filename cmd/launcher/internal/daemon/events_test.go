package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
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
