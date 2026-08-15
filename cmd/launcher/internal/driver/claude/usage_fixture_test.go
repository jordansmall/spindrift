package claude

import (
	"math"
	"path/filepath"
	"testing"
)

// TestExtractUsage_MultiSessionFixture pins ExtractUsage's aggregation
// across testdata/multi-session-2058.jsonl -- a fixture modeled on the real
// nine-session orchestrator log from issue #2058: nine sessions, each with
// its own system/init boundary and a pair of user/assistant lines carrying
// RFC3339 timestamps, alternating worker and reviewer roles as the issue
// describes. The real #2058 log is not available in this repo, so
// per-session turns, cost, API time, and cache-read tokens are
// hand-authored -- varied and scaled, not a uniform block replayed nine
// times -- to land on the issue's own published aggregate figures: 565
// turns, $45.34 total ($15.38 in the final session alone), 2h7m16s of API
// time (7,636,000ms), and 52.8M cache-read tokens (29.3M in the final
// session alone).
//
// The wall-time target, 3h14m52s (11,692,000ms), is the span between the
// earliest and latest of those user/assistant timestamps. The issue also
// reports a wider raw span for the same log, 3h15m26s -- this fixture
// deliberately does not reproduce that wider figure, since it covers
// additional non-turn timestamps sumInLog's span excludes (see its doc
// comment); AC#3 asks for the narrower, turn-scoped figure.
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
	if u.InputTokens != 3792 {
		t.Errorf("InputTokens: got %d, want 3792", u.InputTokens)
	}
	if u.OutputTokens != 877 {
		t.Errorf("OutputTokens: got %d, want 877", u.OutputTokens)
	}
	if u.CacheReadInputTokens != 52800000 {
		t.Errorf("CacheReadInputTokens: got %d, want 52800000", u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != 669000 {
		t.Errorf("CacheCreationInputTokens: got %d, want 669000", u.CacheCreationInputTokens)
	}
}
