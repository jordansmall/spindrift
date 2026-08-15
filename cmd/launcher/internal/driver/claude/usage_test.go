package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSumInLog_FullResultEvent(t *testing.T) {
	line := `{"type":"result","num_turns":7,"total_cost_usd":0.1234,"duration_ms":5000,"duration_api_ms":3000,"usage":{"input_tokens":800,"output_tokens":200,"cache_read_input_tokens":150,"cache_creation_input_tokens":50}}`
	path := WriteLog(t, "some output", line)

	u, found, err := sumInLog(path)
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

	u, found, err := sumInLog(path)
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

	u, found, err := sumInLog(path)
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

	u, found, err := sumInLog(path)
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

	u, found, err := sumInLog(path)
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

	u, found, err := sumInLog(path)
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

	u, found, err := sumInLog(path)
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

	u, found, err := sumInLog(path)
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

	u, found, err := sumInLog(path)
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

	u, found, err := sumInLog(path)
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

	u, found, err := sumInLog(path)
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
	_, found, err := sumInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestSumInLog_FileNotFound(t *testing.T) {
	_, found, err := sumInLog("/nonexistent/path/test.log")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing file")
	}
}

func TestSumInLog_MalformedJSON(t *testing.T) {
	path := WriteLog(t, `{"type":"result","num_turns":INVALID}`)
	_, found, err := sumInLog(path)
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

	u, found, err := sumInLog(path)
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
