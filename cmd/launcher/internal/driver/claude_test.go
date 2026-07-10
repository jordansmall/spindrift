package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// TestClaudeDriverHeartbeatWriterForwardsRaw verifies that the claude
// Driver's heartbeat writer passes all bytes to the raw sink unchanged while
// also emitting a heartbeat line to out, matching heartbeat.New's contract.
func TestClaudeDriverHeartbeatWriterForwardsRaw(t *testing.T) {
	d, err := New("claude")
	if err != nil {
		t.Fatalf("New(claude): %v", err)
	}

	var raw, out bytes.Buffer
	w := d.NewHeartbeatWriter(&raw, "42", &out)

	streamJSON := `{"type":"result","num_turns":2}` + "\n"
	if _, err := w.Write([]byte(streamJSON)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if raw.String() != streamJSON {
		t.Errorf("raw not byte-exact:\ngot:  %q\nwant: %q", raw.String(), streamJSON)
	}
	if !strings.Contains(out.String(), "#42") {
		t.Errorf("heartbeat output missing issue prefix: %q", out.String())
	}
}

// TestClaudeDriverClassifyTransientDelegatesToOutcomeClassify verifies the
// claude Driver's classifier matches outcome.Classify's own behavior on a
// known transient marker.
func TestClaudeDriverClassifyTransientDelegatesToOutcomeClassify(t *testing.T) {
	d, err := New("claude")
	if err != nil {
		t.Fatalf("New(claude): %v", err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue-1.log")
	if err := os.WriteFile(logPath, []byte("some output\nrate_limit_error occurred\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := d.ClassifyTransient(logPath)
	if err != nil {
		t.Fatalf("ClassifyTransient: %v", err)
	}
	if got.Class != outcome.Transient || got.Reason != outcome.RateLimit {
		t.Errorf("got %+v, want Class=%s Reason=%s", got, outcome.Transient, outcome.RateLimit)
	}
}
