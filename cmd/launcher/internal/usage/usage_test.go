package usage_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestLastInLog_FullResultEvent(t *testing.T) {
	line := `{"type":"result","num_turns":7,"total_cost_usd":0.1234,"duration_ms":5000,"duration_api_ms":3000,"usage":{"input_tokens":800,"output_tokens":200,"cache_read_input_tokens":150,"cache_creation_input_tokens":50}}`
	path := writeLog(t, "some output", line)

	u, found, err := usage.LastInLog(path)
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

func TestLastInLog_TakesLast(t *testing.T) {
	first := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":100,"usage":{"input_tokens":10,"output_tokens":5}}`
	last := `{"type":"result","num_turns":9,"total_cost_usd":0.99,"duration_ms":9000,"usage":{"input_tokens":900,"output_tokens":90}}`
	path := writeLog(t, first, "some other output", last)

	u, found, err := usage.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.NumTurns != 9 {
		t.Errorf("NumTurns: got %d, want 9 (should take last)", u.NumTurns)
	}
}

func TestLastInLog_NoCacheFields(t *testing.T) {
	line := `{"type":"result","num_turns":3,"total_cost_usd":0.05,"duration_ms":2000,"usage":{"input_tokens":100,"output_tokens":40}}`
	path := writeLog(t, line)

	u, found, err := usage.LastInLog(path)
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
	path := writeLog(t, "some output", "no result event here")
	_, found, err := usage.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestLastInLog_FileNotFound(t *testing.T) {
	_, found, err := usage.LastInLog("/nonexistent/path/test.log")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing file")
	}
}

func TestLastInLog_MalformedJSON(t *testing.T) {
	path := writeLog(t, `{"type":"result","num_turns":INVALID}`)
	_, found, err := usage.LastInLog(path)
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

	u, found, err := usage.LastInLog(path)
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
