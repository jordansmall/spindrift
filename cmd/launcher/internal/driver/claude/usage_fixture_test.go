package claude

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestExtractUsage_MultiSessionFixture pins ExtractUsage's aggregation over a
// fixture modeled on the nine-session orchestrator log from issue #2058. That
// log is not in this repo, so the per-session figures are hand-authored to land
// on the issue's published aggregates. The wall-time target is the turn-scoped
// span AC#3 asks for, not the issue's wider raw span, which sumInLog excludes.
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

// TestExtractUsage_OutputTokenPlaceholderFixture pins issue #3213's
// placeholder-vs-ground-truth behavior. The fixture's envelopes are trimmed
// from a real claude-code 2.1.204 capture, with token magnitudes hand-authored
// from #3213's own #3183 pass-1 evidence. It also carries a subagent spawn and
// a duplicate message.id re-emit, so agent-split and dedup stay exercised.
func TestExtractUsage_OutputTokenPlaceholderFixture(t *testing.T) {
	path := filepath.Join("testdata", "output-placeholder-3183.jsonl")

	resultOutputTokens := resultEventOutputTokens(t, path)

	naiveSum := naivePerMessageOutputSum(t, path)
	if naiveSum == 0 {
		t.Fatalf("naivePerMessageOutputSum(%s) = 0, fixture parsing broken", path)
	}
	// A generous 100x threshold: real placeholder magnitudes land closer to
	// 900x, but this assertion exists to fail loudly, not precisely, the moment
	// a future claude-code version starts emitting real usage on stdout
	// assistant events. Both sides are read out of the fixture, so regenerating
	// it is the only edit needed for that drift to speak here.
	if resultOutputTokens < naiveSum*100 {
		t.Errorf("stdout assistant events now appear to carry final usage — revisit issue #3213 "+
			"(naive per-message output sum %d is no longer wildly smaller than the result event's %d)",
			naiveSum, resultOutputTokens)
	}

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("ExtractUsage(%s): %v", path, err)
	}
	if !report.Found {
		t.Fatalf("ExtractUsage(%s).Found = false, want true", path)
	}
	if len(report.SummedByAgent) != 2 {
		t.Fatalf("len(report.SummedByAgent) = %d, want 2: %+v", len(report.SummedByAgent), report.SummedByAgent)
	}

	main, scout := agentRows(t, report.SummedByAgent, "scout")

	if main.OutputTokens != resultOutputTokens {
		t.Errorf("main.OutputTokens = %d, want %d (the result event's figure)", main.OutputTokens, resultOutputTokens)
	}
	if scout.OutputTokens != 0 {
		t.Errorf("scout.OutputTokens = %d, want 0 (no ground truth for subagent output)", scout.OutputTokens)
	}

	// This issue leaves the other four columns alone: each is the row's own
	// per-message deduped sum, so msg_main_1's re-emit collapses to one call.
	// Main's cache-read sum matches the result event's exactly, which is what
	// makes that event main-loop ground truth rather than an unrelated snapshot.
	if main.APICalls != 2 {
		t.Errorf("main.APICalls = %d, want 2", main.APICalls)
	}
	if main.UncachedInputTokens != 80 {
		t.Errorf("main.UncachedInputTokens = %d, want 80 (50 + 30)", main.UncachedInputTokens)
	}
	if main.CacheReadInputTokens != 3000 {
		t.Errorf("main.CacheReadInputTokens = %d, want 3000 (1000 + 2000)", main.CacheReadInputTokens)
	}
	if main.CacheCreationInputTokens != 150 {
		t.Errorf("main.CacheCreationInputTokens = %d, want 150 (100 + 50)", main.CacheCreationInputTokens)
	}

	if scout.APICalls != 2 {
		t.Errorf("scout.APICalls = %d, want 2", scout.APICalls)
	}
	if scout.UncachedInputTokens != 25 {
		t.Errorf("scout.UncachedInputTokens = %d, want 25 (10 + 15)", scout.UncachedInputTokens)
	}
	if scout.CacheReadInputTokens != 500 {
		t.Errorf("scout.CacheReadInputTokens = %d, want 500 (200 + 300)", scout.CacheReadInputTokens)
	}
	if scout.CacheCreationInputTokens != 30 {
		t.Errorf("scout.CacheCreationInputTokens = %d, want 30 (10 + 20)", scout.CacheCreationInputTokens)
	}
}

// forEachLine calls fn with each raw line of path. Callers keep their own
// unmarshal target inside fn, so this helper never depends on a production
// event type.
func forEachLine(t *testing.T, path string, fn func(line []byte)) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fn(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
}

// resultEventOutputTokens parses path independently of sumInLog and returns the
// output_tokens summed across its result events, the log's own ground-truth
// figure for main-loop output.
func resultEventOutputTokens(t *testing.T, path string) int {
	t.Helper()
	sum := 0
	found := false
	forEachLine(t, path, func(line []byte) {
		var ev resultEvent
		if err := json.Unmarshal(line, &ev); err != nil || ev.Type != "result" {
			return
		}
		sum += ev.UsageData.OutputTokens
		found = true
	})
	if !found {
		t.Fatalf("no result event in %s", path)
	}
	return sum
}

// naivePerMessageOutputSum parses path independently of
// breakdownByAgentFile/sumInLog and sums output_tokens across every assistant
// line's message.usage, deduplicated by message.id the way the production scan
// does (the first non-empty id wins). This is the naive, wrong-for-issue-#3213
// figure the assertions compare against the result event's ground truth.
func naivePerMessageOutputSum(t *testing.T, path string) int {
	t.Helper()
	seenIDs := make(map[string]bool)
	sum := 0
	forEachLine(t, path, func(line []byte) {
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil || ev.Type != "assistant" || ev.Message == nil {
			return
		}
		if id := ev.Message.ID; id != "" {
			if seenIDs[id] {
				return
			}
			seenIDs[id] = true
		}
		sum += ev.Message.Usage.OutputTokens
	})
	return sum
}
