package dispatch

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// writeRunLog writes lines directly to a Dispatch's run log path, simulating
// a box that already ran and reported them.
func writeRunLog(t *testing.T, d *Dispatch, lines ...string) {
	t.Helper()
	var parts []string
	for _, l := range lines {
		if l != "" {
			parts = append(parts, l)
		}
	}
	if err := writeFile(d.logPath(), strings.Join(parts, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
}

// TestUsageReport_HumanReadableDurations verifies that wall time and API
// time are formatted as h/m/s strings, not raw milliseconds.
func TestUsageReport_HumanReadableDurations(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("88", "test issue")

	// duration_ms=3665000 → "1h 1m 5s"; duration_api_ms=65000 → "1m 5s"
	resultEvent := `{"type":"result","num_turns":3,"total_cost_usd":0.10,"duration_ms":3665000,"duration_api_ms":65000,"usage":{"input_tokens":100,"output_tokens":50}}`
	writeRunLog(t, d, resultEvent)

	body := d.UsageReport()
	if !strings.Contains(body, "1h 1m 5s") {
		t.Errorf("report should contain wall time %q; got: %q", "1h 1m 5s", body)
	}
	if !strings.Contains(body, "1m 5s") {
		t.Errorf("report should contain API time %q; got: %q", "1m 5s", body)
	}
	if strings.Contains(body, "3665000ms") || strings.Contains(body, "65000ms") {
		t.Errorf("report should NOT contain raw ms values; got: %q", body)
	}
}

// TestUsageReport_ContainsCostAndTokens verifies the report surfaces cost,
// tokens, and turn count.
func TestUsageReport_ContainsCostAndTokens(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("42", "test issue")

	resultEvent := `{"type":"result","num_turns":5,"total_cost_usd":0.25,"duration_ms":3000,"duration_api_ms":2000,"usage":{"input_tokens":500,"output_tokens":100,"cache_read_input_tokens":50,"cache_creation_input_tokens":10}}`
	writeRunLog(t, d, resultEvent)

	body := d.UsageReport()
	if !strings.Contains(body, "0.25") {
		t.Errorf("report should contain cost 0.25; got: %q", body)
	}
	if !strings.Contains(body, "500") {
		t.Errorf("report should contain input_tokens 500; got: %q", body)
	}
	if !strings.Contains(body, "5") {
		t.Errorf("report should contain num_turns 5; got: %q", body)
	}
}

// TestUsageReport_MissingResultEvent_ReportsUnavailable verifies that when
// no result event is in the log, UsageReport degrades gracefully rather than
// erroring.
func TestUsageReport_MissingResultEvent_ReportsUnavailable(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("7", "test issue")

	// No result event written at all -- the log doesn't even exist yet.
	body := d.UsageReport()
	if !strings.Contains(body, "unavailable") {
		t.Errorf("report should say unavailable when usage missing; got: %q", body)
	}
}

// TestUsageReport_WithBreakdown verifies that when the log contains scout
// and reviewer subagent messages, the report includes a per-role breakdown
// table.
func TestUsageReport_WithBreakdown(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("55", "test issue")

	resultEvent := `{"type":"result","num_turns":5,"total_cost_usd":0.50,"duration_ms":4000,"duration_api_ms":3000,"usage":{"input_tokens":600,"output_tokens":200}}`
	implMain1 := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_scout","name":"Task","input":{"subagent_type":"scout"}}],"usage":{"input_tokens":100,"output_tokens":30}}}`
	scoutMsg := `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":200,"output_tokens":60}},"parent_tool_use_id":"toolu_scout"}`
	implMain2 := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_reviewer","name":"Task","input":{"subagent_type":"reviewer"}}],"usage":{"input_tokens":150,"output_tokens":50}}}`
	reviewerMsg := `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":150,"output_tokens":60}},"parent_tool_use_id":"toolu_reviewer"}`
	writeRunLog(t, d, resultEvent, implMain1, scoutMsg, implMain2, reviewerMsg)

	body := d.UsageReport()
	if !strings.Contains(body, "breakdown") && !strings.Contains(body, "Breakdown") {
		t.Errorf("report should contain breakdown section; got: %q", body)
	}
	if !strings.Contains(body, "scout") {
		t.Errorf("report should contain scout row; got: %q", body)
	}
	if !strings.Contains(body, "reviewer") {
		t.Errorf("report should contain reviewer row; got: %q", body)
	}
	if !strings.Contains(body, "implementor") {
		t.Errorf("report should contain implementor row; got: %q", body)
	}
}
