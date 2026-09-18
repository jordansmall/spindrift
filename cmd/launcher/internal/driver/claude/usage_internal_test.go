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

// A BreakdownByModel I/O error degrades only the per-model section; the
// aggregate totals LastInLog already parsed survive (issue #674).
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

// A breakdownByAgent I/O error degrades only the per-agent section; the
// aggregate totals and SummedByModel survive. Same contract as
// TestExtractUsage_BreakdownByModelError, issue #674.
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

// Issue #2575: timestamped assistant and user lines expose the earliest and
// latest timestamps, so a caller can derive a wall-time span across several
// logs the way sumInLog already does across sessions within one log.
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

// Issue #3213: a message_start per-message output_tokens is a placeholder
// roughly 100x too low (#3183 dogfood-run evidence), so the main loop's
// SummedByAgent row reports the result event's output_tokens instead.
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

// Issue #3213: only the main loop gets a result event, so no ground-truth
// output figure exists for a subagent. A subagent row's OutputTokens stays 0
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

// Issue #3213: zeroing OutputTokens on non-main-loop rows leaves the other
// four per-agent columns untouched.
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

// logscan.SkipOversized can drop every main-loop assistant line (one line
// over the 4 MiB scan buffer) while the subagent lines and the result event
// survive. Without a synthesized row the result event's output would be
// dropped and the op would report 0 out.
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

// The other half of the synthesis rule: a log with no assistant messages keeps
// a nil SummedByAgent, so FormatSpindriftOp grows no per-agent tail for a pass
// that has no per-agent data.
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

// The spawn line is a Task tool_use whose id the subagent lines must carry as
// parent_tool_use_id, which is what buckets them under "scout" (issue #3213).
func scoutSpawnAndResultLines() (spawnLine, result string) {
	return `{"type":"assistant","message":{"id":"msg_spawn","content":[{"type":"tool_use","id":"toolu_1","name":"Task","input":{"subagent_type":"scout"}}],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		resultLine()
}

// The result event is the main-loop output ground truth for the issue #3213
// tests.
func resultLine() string {
	return `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":1000,"usage":{"input_tokens":30,"output_tokens":500}}`
}

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
