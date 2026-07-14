package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestClaudeDriverClassifyTransientDelegatesToClaudeClassify verifies the
// claude Driver's classifier matches the claude subpackage's own Classify
// behavior on a known transient marker.
func TestClaudeDriverClassifyTransientDelegatesToClaudeClassify(t *testing.T) {
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
	if got.Class != Transient || got.Reason != RateLimit {
		t.Errorf("got %+v, want Class=%s Reason=%s", got, Transient, RateLimit)
	}
}

// TestClaudeDriverExtractUsage verifies the claude Driver's fourth method
// extracts the aggregate result-event usage and the per-role breakdown from
// a Box log in one report, replacing the former usage.LastInLog +
// usage.BreakdownByRole two-call dance at the dispatch call site.
func TestClaudeDriverExtractUsage(t *testing.T) {
	d, err := New("claude")
	if err != nil {
		t.Fatalf("New(claude): %v", err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue-1.log")
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_scout","name":"Task","input":{"subagent_type":"scout"}}],"usage":{"input_tokens":100,"output_tokens":30}}}`,
		`{"type":"assistant","message":{"content":[],"usage":{"input_tokens":200,"output_tokens":60}},"parent_tool_use_id":"toolu_scout"}`,
		`{"type":"result","num_turns":5,"total_cost_usd":0.25,"duration_ms":3000,"duration_api_ms":2000,"usage":{"input_tokens":300,"output_tokens":90}}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := d.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if !report.Found {
		t.Fatal("Found: got false, want true")
	}
	if report.NumTurns != 5 {
		t.Errorf("NumTurns: got %d, want 5", report.NumTurns)
	}
	if report.TotalCostUSD != 0.25 {
		t.Errorf("TotalCostUSD: got %f, want 0.25", report.TotalCostUSD)
	}

	var scout, implementor bool
	for _, r := range report.Roles {
		if r.Role == "scout" {
			scout = true
			if r.InputTokens != 200 {
				t.Errorf("scout InputTokens: got %d, want 200", r.InputTokens)
			}
		}
		if r.Role == "implementor" {
			implementor = true
			if r.InputTokens != 100 {
				t.Errorf("implementor InputTokens: got %d, want 100", r.InputTokens)
			}
		}
	}
	if !scout {
		t.Error("Roles: missing scout bucket")
	}
	if !implementor {
		t.Error("Roles: missing implementor bucket")
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
		if got.Class != Transient || got.Reason != RateLimit {
			t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, Transient, RateLimit)
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
		if got.Class != Transient || got.Reason != Overloaded {
			t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, Transient, Overloaded)
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
		if got.Class != Transient || got.Reason != Network {
			t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, Transient, Network)
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
		if got.Class != Terminal || got.Reason != TaskFailed {
			t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, Terminal, TaskFailed)
		}
		if got.ResetAt != nil {
			t.Errorf("ResetAt: got %v, want nil", got.ResetAt)
		}
	})
}
