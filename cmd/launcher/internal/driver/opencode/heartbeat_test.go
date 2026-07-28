package opencode_test

import (
	"bytes"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/opencode"
)

// TestWriter_ForwardsRawBytesUnchanged verifies that every byte written to
// the Writer is forwarded to raw unchanged, regardless of what heartbeat
// parsing does with it — mirroring driver/claude/heartbeat.go's Writer
// contract.
func TestWriter_ForwardsRawBytesUnchanged(t *testing.T) {
	var raw, out bytes.Buffer
	w := opencode.New(&raw, "42", &out)

	ndjson := `{"type":"text","part":{"text":"Investigating."}}` + "\n" +
		`{"type":"error","error":"boom"}` + "\n"

	n, err := w.Write([]byte(ndjson))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(ndjson) {
		t.Errorf("n: got %d, want %d", n, len(ndjson))
	}
	if raw.String() != ndjson {
		t.Errorf("raw not byte-exact:\ngot:  %q\nwant: %q", raw.String(), ndjson)
	}
}

// TestWriter_EmitsHeartbeatOnTextEvent verifies that a type:"text" event with
// non-empty part.text produces a heartbeat line to out carrying the issue
// number.
func TestWriter_EmitsHeartbeatOnTextEvent(t *testing.T) {
	var raw, out bytes.Buffer
	w := opencode.New(&raw, "42", &out)

	if _, err := w.Write([]byte(`{"type":"text","part":{"text":"Investigating the issue."}}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.Contains(out.String(), "42") {
		t.Errorf("heartbeat output missing issue number: %q", out.String())
	}
}

// TestWriter_NoPanicOnMalformedLine verifies that a non-JSON or empty line
// doesn't panic the parser, only skips heartbeat emission for that line.
func TestWriter_NoPanicOnMalformedLine(t *testing.T) {
	var raw, out bytes.Buffer
	w := opencode.New(&raw, "42", &out)

	if _, err := w.Write([]byte("not json\n\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if raw.String() != "not json\n\n" {
		t.Errorf("raw not byte-exact: %q", raw.String())
	}
}
