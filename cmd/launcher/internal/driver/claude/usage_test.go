package claude_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/usage"
)

// totals is the Driver's aggregate over path: the Report's Totals, whether it
// carried an event span, and whether any result event was found.
func totals(t *testing.T, path string) (usage.Usage, bool, bool, error) {
	t.Helper()
	report, err := newDriver(t).ExtractUsage(path)
	return report.Totals, report.HasEventSpan, report.Found, err
}

// agentBreakdown returns the per-agent rows the Driver reports for lines plus a
// trailing result event: the Report carries nothing without one, and the result
// event's output_tokens (500) lands on the main-loop row only (issue #3213).
func agentBreakdown(t *testing.T, lines ...string) []usage.AgentUsage {
	t.Helper()
	report, err := newDriver(t).ExtractUsage(claude.WriteLog(t, append(lines, resultLine())...))
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	return report.SummedByAgent
}

// The per-agent rows sum every column but OutputTokens (issue #3213); the
// Report patches the MainLoopAgent row from the result event, so only the
// subagent row carries 0 here.
func TestExtractUsage_AgentBreakdown_MainLoopVsSubagent(t *testing.T) {
	mainLine := `{"type":"assistant","message":{"id":"msg_main","model":"claude-opus-4-8","content":[{"type":"tool_use","id":"toolu_1","name":"Task","input":{"subagent_type":"scout"}}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`
	scoutLine := `{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"id":"msg_scout","model":"claude-haiku-4-5","content":[],"usage":{"input_tokens":20,"output_tokens":10,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}`

	got := agentBreakdown(t, mainLine, scoutLine)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}

	main := got[0]
	if main.Agent != usage.MainLoopAgent {
		t.Errorf("got[0].Agent = %q, want %q", main.Agent, usage.MainLoopAgent)
	}
	if main.APICalls != 1 {
		t.Errorf("main.APICalls = %d, want 1", main.APICalls)
	}
	if main.UncachedInputTokens != 100 || main.OutputTokens != 500 || main.CacheReadInputTokens != 10 || main.CacheCreationInputTokens != 5 {
		t.Errorf("main = %+v, want {100 500 10 5}", main)
	}

	scout := got[1]
	if scout.Agent != "scout" {
		t.Errorf("got[1].Agent = %q, want %q", scout.Agent, "scout")
	}
	if scout.APICalls != 1 {
		t.Errorf("scout.APICalls = %d, want 1", scout.APICalls)
	}
	if scout.UncachedInputTokens != 20 || scout.OutputTokens != 0 || scout.CacheReadInputTokens != 2 || scout.CacheCreationInputTokens != 1 {
		t.Errorf("scout = %+v, want {20 0 2 1}", scout)
	}
}

// A missing log file degrades to a zero Report, for a pass that crashes before
// producing any usage events.
func TestExtractUsage_AgentBreakdown_FileNotFound(t *testing.T) {
	report, err := newDriver(t).ExtractUsage("/nonexistent/x.log")
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if report.Found || report.SummedByAgent != nil {
		t.Errorf("report = %+v, want zero Report", report)
	}
}

// A log with no assistant events at all also degrades to a nil SummedByAgent,
// not a zero-valued but non-nil slice.
func TestExtractUsage_AgentBreakdown_EmptyLog(t *testing.T) {
	if got := agentBreakdown(t, "some output", "no assistant event here"); got != nil {
		t.Errorf("got = %+v, want nil", got)
	}
}

// usage.MainLoopAgent always sorts first even though it is not the costliest
// agent here, and among subagents the costliest by TotalTokens sorts first, so
// a single expensive worker stays identifiable.
func TestExtractUsage_AgentBreakdown_OrderingCostliestSubagentFirst(t *testing.T) {
	spawnScout := `{"type":"tool_use","id":"toolu_scout","name":"Task","input":{"subagent_type":"scout"}}`
	spawnWorker := `{"type":"tool_use","id":"toolu_worker","name":"Task","input":{"subagent_type":"worker"}}`
	mainLine := `{"type":"assistant","message":{"id":"msg_main","content":[` + spawnScout + `,` + spawnWorker + `],"usage":{"input_tokens":1,"output_tokens":1}}}`
	// Output is never summed here, so totals are input only: scout 5, worker
	// 500, the expensive one.
	scoutLine := `{"type":"assistant","parent_tool_use_id":"toolu_scout","message":{"id":"msg_scout","content":[],"usage":{"input_tokens":5,"output_tokens":5}}}`
	workerLine := `{"type":"assistant","parent_tool_use_id":"toolu_worker","message":{"id":"msg_worker","content":[],"usage":{"input_tokens":500,"output_tokens":500}}}`

	got := agentBreakdown(t, mainLine, scoutLine, workerLine)
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3: %+v", len(got), got)
	}
	wantOrder := []string{usage.MainLoopAgent, "worker", "scout"}
	for i, a := range got {
		if a.Agent != wantOrder[i] {
			t.Errorf("got[%d].Agent = %q, want %q (order: %+v)", i, a.Agent, wantOrder[i], got)
		}
	}
}

// Two lines sharing a message.id (claude-code's one-line-per-content-block
// re-emit) count once, in APICalls and every summed token field, not once per
// line. OutputTokens comes from the result event, not those lines (issue #3213).
func TestExtractUsage_AgentBreakdown_DedupByMessageID(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"id":"msg_a","model":"claude-opus-4-8","content":[{"type":"text","text":"block 1"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`,
		`{"type":"assistant","message":{"id":"msg_a","model":"claude-opus-4-8","content":[{"type":"text","text":"block 2"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`,
	}

	got := agentBreakdown(t, lines...)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	main := got[0]
	if main.APICalls != 1 {
		t.Errorf("main.APICalls = %d, want 1 (deduped by message.id)", main.APICalls)
	}
	if main.UncachedInputTokens != 100 || main.OutputTokens != 500 || main.CacheReadInputTokens != 10 || main.CacheCreationInputTokens != 5 {
		t.Errorf("main = %+v, want {100 500 10 5} (deduped, not doubled)", main)
	}
}

func TestExtractUsage_Totals_FullResultEvent(t *testing.T) {
	line := `{"type":"result","num_turns":7,"total_cost_usd":0.1234,"duration_ms":5000,"duration_api_ms":3000,"usage":{"input_tokens":800,"output_tokens":200,"cache_read_input_tokens":150,"cache_creation_input_tokens":50}}`
	path := claude.WriteLog(t, "some output", line)

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.NumTurns != 7 {
		t.Errorf("NumTurns: got %d, want 7", u.NumTurns)
	}
	if u.TotalCostUSD != 0.1234 {
		t.Errorf("TotalCostUSD: got %f, want 0.1234", u.TotalCostUSD)
	}
	if u.DurationMs != 5000 {
		t.Errorf("DurationMs: got %d, want 5000", u.DurationMs)
	}
	if u.DurationApiMs != 3000 {
		t.Errorf("DurationApiMs: got %d, want 3000", u.DurationApiMs)
	}
	if u.InputTokens != 800 {
		t.Errorf("InputTokens: got %d, want 800", u.InputTokens)
	}
	if u.OutputTokens != 200 {
		t.Errorf("OutputTokens: got %d, want 200", u.OutputTokens)
	}
	if u.CacheReadInputTokens != 150 {
		t.Errorf("CacheReadInputTokens: got %d, want 150", u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != 50 {
		t.Errorf("CacheCreationInputTokens: got %d, want 50", u.CacheCreationInputTokens)
	}
}

func TestExtractUsage_Totals_SumsAcrossMultipleResultEvents(t *testing.T) {
	first := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":100,"usage":{"input_tokens":10,"output_tokens":5}}`
	last := `{"type":"result","num_turns":9,"total_cost_usd":0.99,"duration_ms":9000,"usage":{"input_tokens":900,"output_tokens":90}}`
	path := claude.WriteLog(t, first, "some other output", last)

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.NumTurns != 10 {
		t.Errorf("NumTurns: got %d, want 10 (sum of both events)", u.NumTurns)
	}
}

func TestExtractUsage_Totals_AdditiveFieldsSumAcrossResultEvents(t *testing.T) {
	first := `{"type":"result","num_turns":2,"total_cost_usd":0.5,"duration_ms":1000,"duration_api_ms":500,"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}}`
	second := `{"type":"result","num_turns":3,"total_cost_usd":0.25,"duration_ms":2000,"duration_api_ms":700,"usage":{"input_tokens":200,"output_tokens":30,"cache_read_input_tokens":6,"cache_creation_input_tokens":4}}`
	path := claude.WriteLog(t, first, second)

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.NumTurns != 5 {
		t.Errorf("NumTurns: got %d, want 5", u.NumTurns)
	}
	const wantCost = 0.75
	if u.TotalCostUSD != wantCost {
		t.Errorf("TotalCostUSD: got %f, want %f", u.TotalCostUSD, wantCost)
	}
	if u.DurationApiMs != 1200 {
		t.Errorf("DurationApiMs: got %d, want 1200", u.DurationApiMs)
	}
	if u.InputTokens != 300 {
		t.Errorf("InputTokens: got %d, want 300", u.InputTokens)
	}
	if u.OutputTokens != 50 {
		t.Errorf("OutputTokens: got %d, want 50", u.OutputTokens)
	}
	if u.CacheReadInputTokens != 11 {
		t.Errorf("CacheReadInputTokens: got %d, want 11", u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != 7 {
		t.Errorf("CacheCreationInputTokens: got %d, want 7", u.CacheCreationInputTokens)
	}
	// No timestamps present anywhere in the log: summing per-session
	// duration_ms would overstate concurrent wall time (AC#2), so fall back
	// to the longest individual session's own duration_ms instead.
	if u.DurationMs != 2000 {
		t.Errorf("DurationMs: got %d, want 2000 (longest session's own duration_ms, no timestamps present)", u.DurationMs)
	}
}

// When a multi-session log's timestamped lines all land on the same instant
// the span is 0, but the run's wall time is not, so it floors to the longest
// individual session's own duration_ms rather than a collapsed 0ms.
func TestExtractUsage_Totals_IdenticalTimestampsDoesNotCollapseMultiSessionDuration(t *testing.T) {
	sameInstant := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	resultOne := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":300000,"usage":{"input_tokens":10,"output_tokens":5}}`
	resultTwo := `{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":500000,"usage":{"input_tokens":20,"output_tokens":10}}`
	path := claude.WriteLog(t, sameInstant, resultOne, resultTwo)

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.DurationMs != 500000 {
		t.Errorf("DurationMs: got %d, want 500000 (floor to longest session's own duration_ms, not a collapsed 0ms span)", u.DurationMs)
	}
}

// A timestamp span far narrower than any individual session's own duration_ms
// (bracketing timestamps that don't cover a session's full wall time) must not
// report a wall time shorter than the longest session actually ran.
func TestExtractUsage_Totals_SpanFlooredToLongestSessionDuration(t *testing.T) {
	start := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	resultOne := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":600000,"usage":{"input_tokens":10,"output_tokens":5}}`
	end := `{"type":"assistant","timestamp":"2026-08-11T19:00:01.000Z"}`
	resultTwo := `{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":600000,"usage":{"input_tokens":20,"output_tokens":10}}`
	path := claude.WriteLog(t, start, resultOne, end, resultTwo)

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.DurationMs != 600000 {
		t.Errorf("DurationMs: got %d, want 600000 (floored to longest session's own duration_ms; the 1s span alone would be impossible against 10min sessions)", u.DurationMs)
	}
}

func TestExtractUsage_Totals_DurationMsFromTimestampSpanWhenPresent(t *testing.T) {
	// The two rules disagree on these values: summing the per-session
	// duration_ms gives 20 minutes, the timestamp span gives 45, so the test
	// discriminates between them.
	assistantStart := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	resultOne := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":600000,"usage":{"input_tokens":10,"output_tokens":5}}`
	userMid := `{"type":"user","timestamp":"2026-08-11T19:20:00.000Z"}`
	resultTwo := `{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":600000,"usage":{"input_tokens":20,"output_tokens":10}}`
	assistantEnd := `{"type":"assistant","timestamp":"2026-08-11T19:45:00.000Z"}`
	path := claude.WriteLog(t, assistantStart, resultOne, userMid, resultTwo, assistantEnd)

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	wantMs := int64(45 * 60 * 1000)
	if u.DurationMs != wantMs {
		t.Errorf("DurationMs: got %d, want %d (timestamp span, not sum of duration_ms)", u.DurationMs, wantMs)
	}
}

// A real single-session claude-code log still carries a top-level "timestamp"
// on its user/assistant lines (see classify_test.go's captured-log fixtures),
// but the span between them is not the session's wall time: a real process has
// startup, network and render time they don't bracket. A single-session log
// reports the result event's own duration_ms unchanged (AC-4).
func TestExtractUsage_Totals_SingleSessionDurationMsUnaffectedByTimestamps(t *testing.T) {
	assistantStart := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	assistantEnd := `{"type":"assistant","timestamp":"2026-08-11T19:00:05.000Z"}`
	result := `{"type":"result","num_turns":4,"total_cost_usd":0.1,"duration_ms":300000,"usage":{"input_tokens":10,"output_tokens":5}}`
	path := claude.WriteLog(t, assistantStart, assistantEnd, result)

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.DurationMs != 300000 {
		t.Errorf("DurationMs: got %d, want 300000 (single session's own duration_ms, not the 5s timestamp span)", u.DurationMs)
	}
}

// A single-session log with exactly one timestamped line (a plausible
// single-turn session) must not take earliest == latest as the span and report
// 0s wall time.
func TestExtractUsage_Totals_SingleTimestampLineDoesNotCollapseDuration(t *testing.T) {
	assistantOnly := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	result := `{"type":"result","num_turns":1,"total_cost_usd":0.02,"duration_ms":300000,"usage":{"input_tokens":10,"output_tokens":5}}`
	path := claude.WriteLog(t, assistantOnly, result)

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.DurationMs != 300000 {
		t.Errorf("DurationMs: got %d, want 300000 (single session's own duration_ms, not a collapsed 0s span)", u.DurationMs)
	}
}

// A bare line carrying only a top-level "timestamp" and no "type" is not a
// driver session event (it can be content dumped into a tool_result), so it
// must not widen the earliest/latest span used for multi-session wall time.
func TestExtractUsage_Totals_StrayTimestampLineIgnoredInSpan(t *testing.T) {
	stray := `{"timestamp":"1970-01-01T00:00:00.000Z"}`
	assistantStart := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	resultOne := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":600000,"usage":{"input_tokens":10,"output_tokens":5}}`
	assistantEnd := `{"type":"assistant","timestamp":"2026-08-11T19:45:00.000Z"}`
	resultTwo := `{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":600000,"usage":{"input_tokens":20,"output_tokens":10}}`
	path := claude.WriteLog(t, stray, assistantStart, resultOne, assistantEnd, resultTwo)

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	wantMs := int64(45 * 60 * 1000)
	if u.DurationMs != wantMs {
		t.Errorf("DurationMs: got %d, want %d (span across typed events only, stray untyped timestamp ignored)", u.DurationMs, wantMs)
	}
}

// A "timestamp" nested inside a tool_result dump, on a line that does carry a
// top-level "type" (unlike the stray-line case above), must still not widen the
// span, because the decoder's topLevelTimestamp only ever reads the
// top-level "timestamp" key, never one buried inside message.content.
func TestExtractUsage_Totals_NestedTimestampInToolResultIgnoredInSpan(t *testing.T) {
	assistantStart := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	resultOne := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":600000,"usage":{"input_tokens":10,"output_tokens":5}}`
	toolResultDump := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"log line: {\"timestamp\":\"1970-01-01T00:00:00.000Z\"}"}]}}`
	assistantEnd := `{"type":"assistant","timestamp":"2026-08-11T19:45:00.000Z"}`
	resultTwo := `{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":600000,"usage":{"input_tokens":20,"output_tokens":10}}`
	path := claude.WriteLog(t, assistantStart, resultOne, toolResultDump, assistantEnd, resultTwo)

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	wantMs := int64(45 * 60 * 1000)
	if u.DurationMs != wantMs {
		t.Errorf("DurationMs: got %d, want %d (nested tool_result timestamp ignored)", u.DurationMs, wantMs)
	}
}

func TestExtractUsage_Totals_NoCacheFields(t *testing.T) {
	line := `{"type":"result","num_turns":3,"total_cost_usd":0.05,"duration_ms":2000,"usage":{"input_tokens":100,"output_tokens":40}}`
	path := claude.WriteLog(t, line)

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.CacheReadInputTokens != 0 {
		t.Errorf("CacheReadInputTokens: got %d, want 0 (absent field)", u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != 0 {
		t.Errorf("CacheCreationInputTokens: got %d, want 0 (absent field)", u.CacheCreationInputTokens)
	}
}

func TestExtractUsage_Totals_NotFound(t *testing.T) {
	path := claude.WriteLog(t, "some output", "no result event here")
	_, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestExtractUsage_Totals_FileNotFound(t *testing.T) {
	_, _, found, err := totals(t, "/nonexistent/path/test.log")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing file")
	}
}

func TestExtractUsage_Totals_MalformedJSON(t *testing.T) {
	path := claude.WriteLog(t, `{"type":"result","num_turns":INVALID}`)
	_, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error for malformed JSON: %v", err)
	}
	if found {
		t.Fatal("expected found=false for malformed JSON")
	}
}

func TestExtractUsage_Totals_OversizedLine(t *testing.T) {
	const fiveMiB = 5 * 1024 * 1024
	path := filepath.Join(t.TempDir(), "big.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, fiveMiB)
	for i := range big {
		big[i] = 'x'
	}
	f.Write(big)
	f.WriteString("\n")
	f.WriteString(`{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":200,"usage":{"input_tokens":20,"output_tokens":10}}` + "\n")
	f.Close()

	u, _, found, err := totals(t, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after oversized line")
	}
	if u.NumTurns != 2 {
		t.Errorf("NumTurns: got %d, want 2", u.NumTurns)
	}
}

// The claude driver reports the issue #3213 semantics as OutputIsMainLoopOnly,
// the flag every downstream consumer (pass_usage payload, heartbeat caveat)
// keys off.
func TestExtractUsage_OutputIsMainLoopOnly(t *testing.T) {
	path := claude.WriteLog(t,
		`{"type":"assistant","message":{"id":"msg_main","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":1000,"usage":{"input_tokens":10,"output_tokens":500}}`,
	)

	report, err := newDriver(t).ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.OutputIsMainLoopOnly {
		t.Errorf("report.OutputIsMainLoopOnly = false, want true: %+v", report)
	}
}

func TestExtractUsage_Totals_MistypedTotalsResultIsSkipped(t *testing.T) {
	bad := `{"type":"result","result":"x","num_turns":"3","total_cost_usd":9,"duration_ms":999,"usage":{"input_tokens":50}}`
	good := `{"type":"result","num_turns":2,"total_cost_usd":0.5,"duration_ms":100,"usage":{"input_tokens":7}}`
	u, _, found, err := totals(t, claude.WriteLog(t, bad, good))
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if u.NumTurns != 2 || u.InputTokens != 7 || u.TotalCostUSD != 0.5 || u.DurationMs != 100 {
		t.Errorf("totals = %+v, want only the well-typed result", u)
	}
	if _, _, found, _ := totals(t, claude.WriteLog(t, bad)); found {
		t.Error("a log with only a mistyped-totals result must be found=false")
	}
}
