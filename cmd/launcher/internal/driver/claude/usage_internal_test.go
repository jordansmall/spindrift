package claude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
	"spindrift.dev/launcher/internal/usage"
)

// TestExtractUsage_BreakdownByModelError confirms ExtractUsage still returns
// the aggregate totals it already parsed via LastInLog when BreakdownByModel
// fails with a real I/O error, rather than discarding them (issue #674).
func TestExtractUsage_BreakdownByModelError(t *testing.T) {
	line := `{"type":"result","num_turns":3,"total_cost_usd":0.5,"usage":{"input_tokens":100,"output_tokens":50}}`
	path := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := breakdownByModel
	breakdownByModel = func(string) ([]usage.ModelUsage, error) {
		return nil, errors.New("simulated I/O error")
	}
	defer func() { breakdownByModel = orig }()

	var report usage.Report
	var err error
	stderr := testutil.CaptureStderr(t, func() {
		report, err = ExtractUsage(path)
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
	if report.SummedByModel != nil {
		t.Errorf("Models: got %+v, want nil", report.SummedByModel)
	}
}

// TestExtractUsage_EventSpanFromTimestampedLines covers issue #2575: a log
// carrying timestamped assistant/user lines exposes the earliest/latest
// timestamps seen via Report.EarliestEventMs/LatestEventMs, so a caller can
// derive a wall-time span across MULTIPLE logs the same way sumInLog already
// derives one across multiple sessions within this single log.
func TestExtractUsage_EventSpanFromTimestampedLines(t *testing.T) {
	assistantStart := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	userMid := `{"type":"user","timestamp":"2026-08-11T19:20:00.000Z"}`
	result := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":600000,"usage":{"input_tokens":10,"output_tokens":5}}`
	assistantEnd := `{"type":"assistant","timestamp":"2026-08-11T19:45:00.000Z"}`
	path := WriteLog(t, assistantStart, userMid, result, assistantEnd)

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Found {
		t.Fatal("expected Found=true")
	}
	if !report.HasEventSpan {
		t.Fatal("expected HasEventSpan=true")
	}
	wantEarliest := int64(1786474800000) // 2026-08-11T19:00:00.000Z
	wantLatest := int64(1786477500000)   // 2026-08-11T19:45:00.000Z
	if report.EarliestEventMs != wantEarliest {
		t.Errorf("EarliestEventMs: got %d, want %d", report.EarliestEventMs, wantEarliest)
	}
	if report.LatestEventMs != wantLatest {
		t.Errorf("LatestEventMs: got %d, want %d", report.LatestEventMs, wantLatest)
	}
}

// TestExtractUsage_NoEventSpanWithoutTimestamps covers the negative case: a
// single result event with no timestamped lines at all leaves HasEventSpan
// false and EarliestEventMs/LatestEventMs at their zero value.
func TestExtractUsage_NoEventSpanWithoutTimestamps(t *testing.T) {
	line := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":300000,"usage":{"input_tokens":10,"output_tokens":5}}`
	path := WriteLog(t, line)

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Found {
		t.Fatal("expected Found=true")
	}
	if report.HasEventSpan {
		t.Fatal("expected HasEventSpan=false")
	}
	if report.EarliestEventMs != 0 {
		t.Errorf("EarliestEventMs: got %d, want 0", report.EarliestEventMs)
	}
	if report.LatestEventMs != 0 {
		t.Errorf("LatestEventMs: got %d, want 0", report.LatestEventMs)
	}
}

// TestExtractUsage_NoResultEventReturnsZeroReport covers the no-result-event
// case: ExtractUsage returns a zero-valued Report (Found=false and the new
// event-span fields untouched) unchanged by this slice's addition.
func TestExtractUsage_NoResultEventReturnsZeroReport(t *testing.T) {
	path := WriteLog(t, "some output", "no result event here")

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Found {
		t.Error("expected Found=false")
	}
	if report.HasEventSpan {
		t.Error("expected HasEventSpan=false")
	}
	if report.EarliestEventMs != 0 || report.LatestEventMs != 0 {
		t.Errorf("EarliestEventMs/LatestEventMs: got %d/%d, want 0/0", report.EarliestEventMs, report.LatestEventMs)
	}
	if report.SummedByModel != nil {
		t.Errorf("SummedByModel: got %+v, want nil", report.SummedByModel)
	}
}
