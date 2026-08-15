package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLastInLog_FullResultEvent(t *testing.T) {
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
	// No timestamps present anywhere in the log: fall back to summing each
	// result event's own duration_ms.
	if u.DurationMs != 3000 {
		t.Errorf("DurationMs: got %d, want 3000 (sum of both events' duration_ms, no timestamps present)", u.DurationMs)
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

func TestLastInLog_NoCacheFields(t *testing.T) {
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

func TestLastInLog_NotFound(t *testing.T) {
	path := WriteLog(t, "some output", "no result event here")
	_, found, err := sumInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestLastInLog_FileNotFound(t *testing.T) {
	_, found, err := sumInLog("/nonexistent/path/test.log")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing file")
	}
}

func TestLastInLog_MalformedJSON(t *testing.T) {
	path := WriteLog(t, `{"type":"result","num_turns":INVALID}`)
	_, found, err := sumInLog(path)
	if err != nil {
		t.Fatalf("unexpected error for malformed JSON: %v", err)
	}
	if found {
		t.Fatal("expected found=false for malformed JSON")
	}
}

func TestLastInLog_OversizedLine(t *testing.T) {
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
