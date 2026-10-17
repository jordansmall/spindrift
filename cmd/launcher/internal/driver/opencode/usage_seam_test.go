package opencode

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
	"spindrift.dev/launcher/internal/usage"
)

// ExtractUsage still returns the aggregate totals it already summed from
// step_finish events when breakdownByModel fails with a real I/O error, rather
// than discarding them (issue #674, mirroring claude's ExtractUsage).
func TestExtractUsage_BreakdownByModelError(t *testing.T) {
	line := `{"type":"step_finish","part":{"messageID":"msg_a","modelID":"gpt-5","tokens":{"input":100,"output":50,"reasoning":0,"cache":{"write":0,"read":0}}}}`
	path := WriteLog(t, line)

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
