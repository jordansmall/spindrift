package claude

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

// TestBreakdownByAgent_MainLoopVsSubagent covers the base attribution split:
// a main-loop message that spawns a "scout" subagent must land under
// usage.MainLoopAgent, while the spawned subagent's own message lands under
// its subagent_type, each with its own APICalls and token counts.
// breakdownByAgent itself never sums OutputTokens (issue #3213) -- both rows
// carry 0 here; only ExtractUsage patches the MainLoopAgent row from the
// result event.
func TestBreakdownByAgent_MainLoopVsSubagent(t *testing.T) {
	mainLine := `{"type":"assistant","message":{"id":"msg_main","model":"claude-opus-4-8","content":[{"type":"tool_use","id":"toolu_1","name":"Task","input":{"subagent_type":"scout"}}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`
	scoutLine := `{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"id":"msg_scout","model":"claude-haiku-4-5","content":[],"usage":{"input_tokens":20,"output_tokens":10,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}`
	path := WriteLog(t, mainLine, scoutLine)

	got, err := breakdownByAgent(path)
	if err != nil {
		t.Fatalf("breakdownByAgent: %v", err)
	}
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
	if main.UncachedInputTokens != 100 || main.OutputTokens != 0 || main.CacheReadInputTokens != 10 || main.CacheCreationInputTokens != 5 {
		t.Errorf("main = %+v, want {100 0 10 5}", main)
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

// TestBreakdownByAgent_FileNotFound confirms a missing log file degrades to
// (nil, nil), matching breakdownByModelFile's contract -- the "pass that
// crashes or produces no usage events" acceptance criterion.
func TestBreakdownByAgent_FileNotFound(t *testing.T) {
	got, err := breakdownByAgent("/nonexistent/x.log")
	if err != nil {
		t.Fatalf("breakdownByAgent: %v", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil", got)
	}
}

// TestBreakdownByAgent_EmptyLog confirms a log with no assistant events at
// all (e.g. a Box that crashed before producing any) also degrades to
// (nil, nil), not a zero-valued but non-nil slice.
func TestBreakdownByAgent_EmptyLog(t *testing.T) {
	path := WriteLog(t, "some output", "no assistant event here")

	got, err := breakdownByAgent(path)
	if err != nil {
		t.Fatalf("breakdownByAgent: %v", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil", got)
	}
}

// TestBreakdownByAgent_OrderingCostliestSubagentFirst confirms
// usage.MainLoopAgent always sorts first (even though it is not the
// costliest agent here), and among subagents the costliest by TotalTokens
// sorts first -- the issue's own goal that "a single expensive worker is
// identifiable".
func TestBreakdownByAgent_OrderingCostliestSubagentFirst(t *testing.T) {
	spawnScout := `{"type":"tool_use","id":"toolu_scout","name":"Task","input":{"subagent_type":"scout"}}`
	spawnWorker := `{"type":"tool_use","id":"toolu_worker","name":"Task","input":{"subagent_type":"worker"}}`
	mainLine := `{"type":"assistant","message":{"id":"msg_main","content":[` + spawnScout + `,` + spawnWorker + `],"usage":{"input_tokens":1,"output_tokens":1}}}`
	// Output is never summed here, so totals are input only: scout 5,
	// worker 500 -- the expensive one.
	scoutLine := `{"type":"assistant","parent_tool_use_id":"toolu_scout","message":{"id":"msg_scout","content":[],"usage":{"input_tokens":5,"output_tokens":5}}}`
	workerLine := `{"type":"assistant","parent_tool_use_id":"toolu_worker","message":{"id":"msg_worker","content":[],"usage":{"input_tokens":500,"output_tokens":500}}}`
	path := WriteLog(t, mainLine, scoutLine, workerLine)

	got, err := breakdownByAgent(path)
	if err != nil {
		t.Fatalf("breakdownByAgent: %v", err)
	}
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

// TestBreakdownByAgent_DedupByMessageID confirms two lines sharing the same
// message.id (claude-code's one-line-per-content-block re-emit) count once,
// both APICalls and every summed token field -- not once per line.
// OutputTokens is not among the summed fields (issue #3213): breakdownByAgent
// never sums it, dedup or not.
func TestBreakdownByAgent_DedupByMessageID(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"id":"msg_a","model":"claude-opus-4-8","content":[{"type":"text","text":"block 1"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`,
		`{"type":"assistant","message":{"id":"msg_a","model":"claude-opus-4-8","content":[{"type":"text","text":"block 2"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`,
	}
	path := WriteLog(t, lines...)

	got, err := breakdownByAgent(path)
	if err != nil {
		t.Fatalf("breakdownByAgent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	main := got[0]
	if main.APICalls != 1 {
		t.Errorf("main.APICalls = %d, want 1 (deduped by message.id)", main.APICalls)
	}
	if main.UncachedInputTokens != 100 || main.OutputTokens != 0 || main.CacheReadInputTokens != 10 || main.CacheCreationInputTokens != 5 {
		t.Errorf("main = %+v, want {100 0 10 5} (deduped, not doubled)", main)
	}
}

func TestSumInLog_FullResultEvent(t *testing.T) {
	line := `{"type":"result","num_turns":7,"total_cost_usd":0.1234,"duration_ms":5000,"duration_api_ms":3000,"usage":{"input_tokens":800,"output_tokens":200,"cache_read_input_tokens":150,"cache_creation_input_tokens":50}}`
	path := WriteLog(t, "some output", line)

	u, _, found, err := sumInLog(path)
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

func TestSumInLog_SumsAcrossMultipleResultEvents(t *testing.T) {
	first := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":100,"usage":{"input_tokens":10,"output_tokens":5}}`
	last := `{"type":"result","num_turns":9,"total_cost_usd":0.99,"duration_ms":9000,"usage":{"input_tokens":900,"output_tokens":90}}`
	path := WriteLog(t, first, "some other output", last)

	u, _, found, err := sumInLog(path)
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

func TestSumInLog_AdditiveFieldsSumAcrossResultEvents(t *testing.T) {
	first := `{"type":"result","num_turns":2,"total_cost_usd":0.5,"duration_ms":1000,"duration_api_ms":500,"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}}`
	second := `{"type":"result","num_turns":3,"total_cost_usd":0.25,"duration_ms":2000,"duration_api_ms":700,"usage":{"input_tokens":200,"output_tokens":30,"cache_read_input_tokens":6,"cache_creation_input_tokens":4}}`
	path := WriteLog(t, first, second)

	u, _, found, err := sumInLog(path)
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

// TestSumInLog_IdenticalTimestampsDoesNotCollapseMultiSessionDuration covers
// a multi-session log whose timestamped lines all land on the exact same
// instant (earliest == latest): the span is 0, but the run's wall time is
// not, so it must floor to the longest individual session's own duration_ms
// rather than reporting a collapsed 0ms.
func TestSumInLog_IdenticalTimestampsDoesNotCollapseMultiSessionDuration(t *testing.T) {
	sameInstant := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	resultOne := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":300000,"usage":{"input_tokens":10,"output_tokens":5}}`
	resultTwo := `{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":500000,"usage":{"input_tokens":20,"output_tokens":10}}`
	path := WriteLog(t, sameInstant, resultOne, resultTwo)

	u, _, found, err := sumInLog(path)
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

// TestSumInLog_SpanFlooredToLongestSessionDuration covers the sanity-relation
// finding: a timestamp span far narrower than any individual session's own
// duration_ms (bracketing timestamps that don't cover a session's full wall
// time) must not report a wall time shorter than the longest session
// actually ran.
func TestSumInLog_SpanFlooredToLongestSessionDuration(t *testing.T) {
	start := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	resultOne := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":600000,"usage":{"input_tokens":10,"output_tokens":5}}`
	end := `{"type":"assistant","timestamp":"2026-08-11T19:00:01.000Z"}`
	resultTwo := `{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":600000,"usage":{"input_tokens":20,"output_tokens":10}}`
	path := WriteLog(t, start, resultOne, end, resultTwo)

	u, _, found, err := sumInLog(path)
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

func TestSumInLog_DurationMsFromTimestampSpanWhenPresent(t *testing.T) {
	// Timestamps 45 minutes apart bracketing two result events, each
	// claiming its own duration_ms of 10 minutes. Sum-based answer would be
	// 20 minutes; span-based answer is 45 minutes. Pick values that
	// disagree so the test discriminates between the two rules.
	assistantStart := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	resultOne := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":600000,"usage":{"input_tokens":10,"output_tokens":5}}`
	userMid := `{"type":"user","timestamp":"2026-08-11T19:20:00.000Z"}`
	resultTwo := `{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":600000,"usage":{"input_tokens":20,"output_tokens":10}}`
	assistantEnd := `{"type":"assistant","timestamp":"2026-08-11T19:45:00.000Z"}`
	path := WriteLog(t, assistantStart, resultOne, userMid, resultTwo, assistantEnd)

	u, _, found, err := sumInLog(path)
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

// TestSumInLog_SingleSessionDurationMsUnaffectedByTimestamps covers AC-4: a
// real single-session claude-code log carries a top-level "timestamp" on its
// user/assistant lines (see classify_test.go's captured-log fixtures) even
// though it is only one session. The span between those timestamps (5s here)
// is not the session's actual wall time (300s, the result event's own
// duration_ms) — a real process has startup/network/render time the
// assistant-to-assistant timestamps don't bracket. A single-session log must
// report the result event's own duration_ms unchanged, never the span.
func TestSumInLog_SingleSessionDurationMsUnaffectedByTimestamps(t *testing.T) {
	assistantStart := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	assistantEnd := `{"type":"assistant","timestamp":"2026-08-11T19:00:05.000Z"}`
	result := `{"type":"result","num_turns":4,"total_cost_usd":0.1,"duration_ms":300000,"usage":{"input_tokens":10,"output_tokens":5}}`
	path := WriteLog(t, assistantStart, assistantEnd, result)

	u, _, found, err := sumInLog(path)
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

// TestSumInLog_SingleTimestampLineDoesNotCollapseDuration covers the
// zero-span case named in review: a single-session log with exactly one
// timestamped line (a plausible single-turn session) must not report 0s wall
// time by taking earliest==latest as the span.
func TestSumInLog_SingleTimestampLineDoesNotCollapseDuration(t *testing.T) {
	assistantOnly := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	result := `{"type":"result","num_turns":1,"total_cost_usd":0.02,"duration_ms":300000,"usage":{"input_tokens":10,"output_tokens":5}}`
	path := WriteLog(t, assistantOnly, result)

	u, _, found, err := sumInLog(path)
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

// TestSumInLog_StrayTimestampLineIgnoredInSpan covers the sanity-bound
// finding: a bare line carrying only a top-level "timestamp" field (no
// "type", so it is not a driver session event — e.g. content dumped into a
// tool_result) must not widen the earliest/latest span used for multi-session
// wall time.
func TestSumInLog_StrayTimestampLineIgnoredInSpan(t *testing.T) {
	stray := `{"timestamp":"1970-01-01T00:00:00.000Z"}`
	assistantStart := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	resultOne := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":600000,"usage":{"input_tokens":10,"output_tokens":5}}`
	assistantEnd := `{"type":"assistant","timestamp":"2026-08-11T19:45:00.000Z"}`
	resultTwo := `{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":600000,"usage":{"input_tokens":20,"output_tokens":10}}`
	path := WriteLog(t, stray, assistantStart, resultOne, assistantEnd, resultTwo)

	u, _, found, err := sumInLog(path)
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

// TestSumInLog_NestedTimestampInToolResultIgnoredInSpan covers the doc
// comment's central safety claim: a "timestamp" string nested inside a
// tool_result dump, on a line that DOES carry a top-level "type" (unlike
// the stray-line case above), must still not widen the span -- because
// json.Unmarshal into timestampedEvent only ever reads the top-level
// "timestamp" key, never one buried inside message.content.
func TestSumInLog_NestedTimestampInToolResultIgnoredInSpan(t *testing.T) {
	assistantStart := `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`
	resultOne := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":600000,"usage":{"input_tokens":10,"output_tokens":5}}`
	toolResultDump := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"log line: {\"timestamp\":\"1970-01-01T00:00:00.000Z\"}"}]}}`
	assistantEnd := `{"type":"assistant","timestamp":"2026-08-11T19:45:00.000Z"}`
	resultTwo := `{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":600000,"usage":{"input_tokens":20,"output_tokens":10}}`
	path := WriteLog(t, assistantStart, resultOne, toolResultDump, assistantEnd, resultTwo)

	u, _, found, err := sumInLog(path)
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

func TestSumInLog_NoCacheFields(t *testing.T) {
	line := `{"type":"result","num_turns":3,"total_cost_usd":0.05,"duration_ms":2000,"usage":{"input_tokens":100,"output_tokens":40}}`
	path := WriteLog(t, line)

	u, _, found, err := sumInLog(path)
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

func TestSumInLog_NotFound(t *testing.T) {
	path := WriteLog(t, "some output", "no result event here")
	_, _, found, err := sumInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestSumInLog_FileNotFound(t *testing.T) {
	_, _, found, err := sumInLog("/nonexistent/path/test.log")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing file")
	}
}

func TestSumInLog_MalformedJSON(t *testing.T) {
	path := WriteLog(t, `{"type":"result","num_turns":INVALID}`)
	_, _, found, err := sumInLog(path)
	if err != nil {
		t.Fatalf("unexpected error for malformed JSON: %v", err)
	}
	if found {
		t.Fatal("expected found=false for malformed JSON")
	}
}

func TestSumInLog_OversizedLine(t *testing.T) {
	const fiveMiB = 5 * 1024 * 1024
	// Write an oversized line then a valid result event
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

	u, _, found, err := sumInLog(path)
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

// TestExtractUsage_OutputIsMainLoopOnly pins that the claude driver asserts
// the issue #3213 semantics on its report -- the flag every downstream
// consumer (pass_usage payload, heartbeat caveat) keys off.
func TestExtractUsage_OutputIsMainLoopOnly(t *testing.T) {
	path := WriteLog(t,
		`{"type":"assistant","message":{"id":"msg_main","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":1000,"usage":{"input_tokens":10,"output_tokens":500}}`,
	)

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.OutputIsMainLoopOnly {
		t.Errorf("report.OutputIsMainLoopOnly = false, want true: %+v", report)
	}
}
