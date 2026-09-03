package claude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
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

// TestExtractUsage_BreakdownByAgentError confirms ExtractUsage still returns
// the aggregate totals (and SummedByModel) when breakdownByAgent fails with a
// real I/O error, degrading only the per-agent section -- the same contract
// as TestExtractUsage_BreakdownByModelError, issue #674.
func TestExtractUsage_BreakdownByAgentError(t *testing.T) {
	line := `{"type":"result","num_turns":3,"total_cost_usd":0.5,"usage":{"input_tokens":100,"output_tokens":50}}`
	path := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := breakdownByAgent
	breakdownByAgent = func(string) ([]usage.AgentUsage, error) {
		return nil, errors.New("simulated I/O error")
	}
	defer func() { breakdownByAgent = orig }()

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
	if report.SummedByAgent != nil {
		t.Errorf("SummedByAgent: got %+v, want nil", report.SummedByAgent)
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

// TestExtractUsage_SummedByAgentPopulated confirms ExtractUsage wires
// breakdownByAgent's result into Report.SummedByAgent, the same way it
// already wires breakdownByModel into SummedByModel.
func TestExtractUsage_SummedByAgentPopulated(t *testing.T) {
	mainLine := `{"type":"assistant","message":{"id":"msg_main","content":[],"usage":{"input_tokens":10,"output_tokens":5}}}`
	result := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":1000,"usage":{"input_tokens":10,"output_tokens":5}}`
	path := WriteLog(t, mainLine, result)

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.SummedByAgent) != 1 {
		t.Fatalf("len(report.SummedByAgent) = %d, want 1: %+v", len(report.SummedByAgent), report.SummedByAgent)
	}
	if report.SummedByAgent[0].Agent != usage.MainLoopAgent {
		t.Errorf("SummedByAgent[0].Agent = %q, want %q", report.SummedByAgent[0].Agent, usage.MainLoopAgent)
	}
	if report.SummedByAgent[0].UncachedInputTokens != 10 || report.SummedByAgent[0].OutputTokens != 5 {
		t.Errorf("SummedByAgent[0] = %+v, want UncachedInputTokens=10 OutputTokens=5", report.SummedByAgent[0])
	}
}

// TestExtractUsage_MainLoopOutputFromResultEvent covers issue #3213: a
// message_start per-message output_tokens is a placeholder ~100x too low
// (#3183 dogfood-run evidence), so the main loop's SummedByAgent row must
// report the result event's ground-truth output_tokens, not the per-message
// sum.
func TestExtractUsage_MainLoopOutputFromResultEvent(t *testing.T) {
	mainLine := `{"type":"assistant","message":{"id":"msg_main","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`
	result := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":1000,"usage":{"input_tokens":10,"output_tokens":500}}`
	path := WriteLog(t, mainLine, result)

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.SummedByAgent) != 1 {
		t.Fatalf("len(report.SummedByAgent) = %d, want 1: %+v", len(report.SummedByAgent), report.SummedByAgent)
	}
	if report.SummedByAgent[0].OutputTokens != 500 {
		t.Errorf("SummedByAgent[0].OutputTokens = %d, want 500 (result event, not the 1-token per-message placeholder)", report.SummedByAgent[0].OutputTokens)
	}
}

// TestExtractUsage_SubagentOutputZeroed covers issue #3213: no ground-truth
// output token figure exists for a subagent on the stream (only the main
// loop gets a result event), so a subagent row's OutputTokens is always 0
// even when its assistant messages carry non-zero placeholder output_tokens.
func TestExtractUsage_SubagentOutputZeroed(t *testing.T) {
	spawnLine, result := scoutSpawnAndResultLines()
	subLine := `{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"id":"msg_sub","content":[],"usage":{"input_tokens":20,"output_tokens":99}}}`
	path := WriteLog(t, spawnLine, subLine, result)

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, sub := agentRows(t, report.SummedByAgent, "scout")
	if sub.OutputTokens != 0 {
		t.Errorf("subagent OutputTokens = %d, want 0 (no ground truth for subagent output)", sub.OutputTokens)
	}
	if sub.UncachedInputTokens != 20 {
		t.Errorf("subagent UncachedInputTokens = %d, want 20 (unchanged by this fix)", sub.UncachedInputTokens)
	}
}

// TestExtractUsage_OtherColumnsUnchangedBySubagentOutputFix covers issue
// #3213: zeroing OutputTokens on non-main-loop rows leaves the other four
// per-agent columns byte-identical to before the fix.
func TestExtractUsage_OtherColumnsUnchangedBySubagentOutputFix(t *testing.T) {
	spawnLine, result := scoutSpawnAndResultLines()
	subLine := `{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"id":"msg_sub","content":[],"usage":{"input_tokens":20,"output_tokens":99,"cache_read_input_tokens":7,"cache_creation_input_tokens":3}}}`
	path := WriteLog(t, spawnLine, subLine, result)

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, sub := agentRows(t, report.SummedByAgent, "scout")
	if sub.APICalls != 1 {
		t.Errorf("subagent APICalls = %d, want 1", sub.APICalls)
	}
	if sub.UncachedInputTokens != 20 {
		t.Errorf("subagent UncachedInputTokens = %d, want 20", sub.UncachedInputTokens)
	}
	if sub.CacheReadInputTokens != 7 {
		t.Errorf("subagent CacheReadInputTokens = %d, want 7", sub.CacheReadInputTokens)
	}
	if sub.CacheCreationInputTokens != 3 {
		t.Errorf("subagent CacheCreationInputTokens = %d, want 3", sub.CacheCreationInputTokens)
	}
}

// TestExtractUsage_MainRowSynthesizedWhenMainLoopLineOversized covers the
// case where logscan.SkipOversized drops every main-loop assistant line (a
// single line over the 4 MiB scan buffer) while the subagent lines and the
// result event survive: without a synthesized row the result event's
// ground-truth output would be silently dropped and the op would report 0 out.
func TestExtractUsage_MainRowSynthesizedWhenMainLoopLineOversized(t *testing.T) {
	oversizedSpawn := `{"type":"assistant","message":{"id":"msg_spawn","content":[{"type":"tool_use","id":"toolu_1","name":"Task","input":{"subagent_type":"scout"}},{"type":"text","text":"` +
		strings.Repeat("x", 5*1024*1024) + `"}],"usage":{"input_tokens":10,"output_tokens":1}}}`
	subLine := `{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"id":"msg_sub","content":[],"usage":{"input_tokens":20,"output_tokens":99}}}`
	path := WriteLog(t, oversizedSpawn, subLine, resultLine())

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.SummedByAgent) != 2 {
		t.Fatalf("len(report.SummedByAgent) = %d, want 2: %+v", len(report.SummedByAgent), report.SummedByAgent)
	}
	// The synthesized row leads, preserving the main-loop-first ordering.
	main := report.SummedByAgent[0]
	if main.Agent != usage.MainLoopAgent {
		t.Fatalf("SummedByAgent[0].Agent = %q, want %q: %+v", main.Agent, usage.MainLoopAgent, report.SummedByAgent)
	}
	if main.OutputTokens != 500 {
		t.Errorf("main OutputTokens = %d, want 500 (result event ground truth)", main.OutputTokens)
	}
	if main.APICalls != 0 || main.UncachedInputTokens != 0 || main.CacheReadInputTokens != 0 || main.CacheCreationInputTokens != 0 {
		t.Errorf("synthesized main row = %+v, want zeros outside OutputTokens (its lines were dropped)", main)
	}
	// The dropped spawn line takes the subagent_type with it, so the
	// surviving subagent messages bucket under driverkit.DefaultRole.
	if sub := report.SummedByAgent[1]; sub.Agent != driverkit.DefaultRole || sub.UncachedInputTokens != 20 {
		t.Errorf("SummedByAgent[1] = %+v, want Agent=%q UncachedInputTokens=20", sub, driverkit.DefaultRole)
	}
}

// TestExtractUsage_NoMainRowSynthesizedForEmptyBreakdown pins the other half
// of the synthesis rule: a log with no assistant messages at all keeps a nil
// SummedByAgent, so FormatSpindriftOp does not grow a per-agent tail for a
// pass that has no per-agent data.
func TestExtractUsage_NoMainRowSynthesizedForEmptyBreakdown(t *testing.T) {
	path := WriteLog(t, resultLine())

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.SummedByAgent != nil {
		t.Errorf("SummedByAgent = %+v, want nil", report.SummedByAgent)
	}
}

// scoutSpawnAndResultLines returns the main-loop line spawning a "scout"
// subagent (a Task tool_use whose id the subagent lines carry as
// parent_tool_use_id) and the run's result event, shared by the issue #3213
// subagent tests; each test supplies its own subagent assistant line.
func scoutSpawnAndResultLines() (spawnLine, result string) {
	return `{"type":"assistant","message":{"id":"msg_spawn","content":[{"type":"tool_use","id":"toolu_1","name":"Task","input":{"subagent_type":"scout"}}],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		resultLine()
}

// resultLine returns the result event the issue #3213 tests share as their
// main-loop output ground truth, whether or not they also need a spawn line.
func resultLine() string {
	return `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":1000,"usage":{"input_tokens":30,"output_tokens":500}}`
}

// agentRows picks the main-loop row and the row for the named subagent out
// of rows, failing the test if either is missing.
func agentRows(t *testing.T, rows []usage.AgentUsage, subagent string) (main, sub *usage.AgentUsage) {
	t.Helper()
	for i := range rows {
		switch rows[i].Agent {
		case usage.MainLoopAgent:
			main = &rows[i]
		case subagent:
			sub = &rows[i]
		}
	}
	if main == nil {
		t.Fatalf("no %s row found: %+v", usage.MainLoopAgent, rows)
	}
	if sub == nil {
		t.Fatalf("no %s row found: %+v", subagent, rows)
	}
	return main, sub
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
