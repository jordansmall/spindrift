package driver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
	"spindrift.dev/launcher/internal/usage"
)

// writeClaudeResultLog writes a one-result-event log, the smallest one the
// claude Driver reports as Found.
func writeClaudeResultLog(t *testing.T) string {
	t.Helper()
	line := `{"type":"result","num_turns":3,"total_cost_usd":0.5,"usage":{"input_tokens":100,"output_tokens":50}}`
	path := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// extractClaudeUsage runs the claude Driver's ExtractUsage on path and checks
// the one warning it prints names the path and the simulated error.
func extractClaudeUsage(t *testing.T, path string) usage.Report {
	t.Helper()
	d, err := New("claude")
	if err != nil {
		t.Fatal(err)
	}
	var report usage.Report
	stderr := testutil.CaptureStderr(t, func() {
		report, err = d.ExtractUsage(path)
	})
	if !strings.Contains(stderr, path) || !strings.Contains(stderr, "simulated I/O error") {
		t.Errorf("stderr = %q, want it to mention the log path and the error", stderr)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Found {
		t.Fatal("expected Found=true")
	}
	if report.Totals.InputTokens != 100 || report.Totals.OutputTokens != 50 {
		t.Errorf("Usage: got %+v, want InputTokens=100 OutputTokens=50", report.Totals)
	}
	return report
}

// A claudeUsageByModel I/O error degrades only the per-model section; the
// aggregate totals already parsed survive (issue #674).
func TestClaudeDriverExtractUsage_UsageByModelError(t *testing.T) {
	path := writeClaudeResultLog(t)

	orig := claudeUsageByModel
	claudeUsageByModel = func(string) ([]usage.ModelUsage, error) {
		return nil, errors.New("simulated I/O error")
	}
	defer func() { claudeUsageByModel = orig }()

	report := extractClaudeUsage(t, path)
	if report.SummedByModel != nil {
		t.Errorf("Models: got %+v, want nil", report.SummedByModel)
	}
}

// A claudeUsageByAgent I/O error degrades only the per-agent section; the
// aggregate totals and SummedByModel survive. Same contract as
// TestClaudeDriverExtractUsage_UsageByModelError, issue #674.
func TestClaudeDriverExtractUsage_UsageByAgentError(t *testing.T) {
	path := writeClaudeResultLog(t)

	orig := claudeUsageByAgent
	claudeUsageByAgent = func(string) ([]usage.AgentUsage, error) {
		return nil, errors.New("simulated I/O error")
	}
	defer func() { claudeUsageByAgent = orig }()

	report := extractClaudeUsage(t, path)
	if report.SummedByAgent != nil {
		t.Errorf("SummedByAgent: got %+v, want nil", report.SummedByAgent)
	}
}
