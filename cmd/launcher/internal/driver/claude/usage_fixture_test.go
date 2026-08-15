package claude

import (
	"math"
	"path/filepath"
	"testing"
)

// TestExtractUsage_MultiSessionFixture pins ExtractUsage's aggregation
// across testdata/multi-session-2058.jsonl -- a fixture modeled on the real
// nine-session orchestrator log from issue #2058 -- to the exact totals
// reported in that issue: 565 turns, $45.34, 2h7m16s of API time
// (7,636,000 ms), and 3h14m52s of wall time (11,692,000 ms) derived from
// the earliest/latest top-level "timestamp" span in the log.
func TestExtractUsage_MultiSessionFixture(t *testing.T) {
	path := filepath.Join("testdata", "multi-session-2058.jsonl")

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("ExtractUsage(%s): %v", path, err)
	}
	if !report.Found {
		t.Fatalf("ExtractUsage(%s).Found = false, want true", path)
	}

	u := report.Totals
	if u.NumTurns != 565 {
		t.Errorf("NumTurns: got %d, want 565", u.NumTurns)
	}
	const wantCost = 45.34
	if math.Abs(u.TotalCostUSD-wantCost) > 0.0001 {
		t.Errorf("TotalCostUSD: got %f, want %f", u.TotalCostUSD, wantCost)
	}
	if u.DurationApiMs != 7636000 {
		t.Errorf("DurationApiMs: got %d, want 7636000", u.DurationApiMs)
	}
	if u.DurationMs != 11692000 {
		t.Errorf("DurationMs: got %d, want 11692000", u.DurationMs)
	}
	if u.InputTokens != 9000 {
		t.Errorf("InputTokens: got %d, want 9000", u.InputTokens)
	}
	if u.OutputTokens != 1800 {
		t.Errorf("OutputTokens: got %d, want 1800", u.OutputTokens)
	}
	if u.CacheReadInputTokens != 45000 {
		t.Errorf("CacheReadInputTokens: got %d, want 45000", u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != 900 {
		t.Errorf("CacheCreationInputTokens: got %d, want 900", u.CacheCreationInputTokens)
	}
}
