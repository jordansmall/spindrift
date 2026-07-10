package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestClaudeDriverClassifyTransient covers the four outcomes the claude
// strategy must surface through the Driver seam: rate-limit (with resetsAt),
// overloaded, network, and terminal.
func TestClaudeDriverClassifyTransient(t *testing.T) {
	d, err := New("claude")
	if err != nil {
		t.Fatalf("New(claude): %v", err)
	}

	writeLog := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "box.log")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("RateLimit_WithResetsAt", func(t *testing.T) {
		logPath := writeLog(t, `{"type":"error","error":{"type":"rate_limit_error"},"resetsAt":1783192800}`+"\n")
		got, err := d.ClassifyTransient(logPath)
		if err != nil {
			t.Fatalf("ClassifyTransient: %v", err)
		}
		if got.Class != outcome.Transient || got.Reason != outcome.RateLimit {
			t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, outcome.Transient, outcome.RateLimit)
		}
		if got.ResetAt == nil {
			t.Fatal("ResetAt: got nil, want non-nil")
		}
		want := time.Unix(1783192800, 0).UTC()
		if !got.ResetAt.Equal(want) {
			t.Errorf("ResetAt: got %v, want %v", *got.ResetAt, want)
		}
	})

	t.Run("Overloaded", func(t *testing.T) {
		logPath := writeLog(t, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`+"\n")
		got, err := d.ClassifyTransient(logPath)
		if err != nil {
			t.Fatalf("ClassifyTransient: %v", err)
		}
		if got.Class != outcome.Transient || got.Reason != outcome.Overloaded {
			t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, outcome.Transient, outcome.Overloaded)
		}
		if got.ResetAt != nil {
			t.Errorf("ResetAt: got %v, want nil", got.ResetAt)
		}
	})

	t.Run("Network", func(t *testing.T) {
		logPath := writeLog(t, "dial tcp 1.2.3.4:443: connection refused\n")
		got, err := d.ClassifyTransient(logPath)
		if err != nil {
			t.Fatalf("ClassifyTransient: %v", err)
		}
		if got.Class != outcome.Transient || got.Reason != outcome.Network {
			t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, outcome.Transient, outcome.Network)
		}
		if got.ResetAt != nil {
			t.Errorf("ResetAt: got %v, want nil", got.ResetAt)
		}
	})

	t.Run("Terminal", func(t *testing.T) {
		logPath := writeLog(t, "Agent completed with no valid outcome.\n")
		got, err := d.ClassifyTransient(logPath)
		if err != nil {
			t.Fatalf("ClassifyTransient: %v", err)
		}
		if got.Class != outcome.Terminal || got.Reason != outcome.TaskFailed {
			t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, outcome.Terminal, outcome.TaskFailed)
		}
		if got.ResetAt != nil {
			t.Errorf("ResetAt: got %v, want nil", got.ResetAt)
		}
	})
}
