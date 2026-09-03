package claude

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
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

// TestExtractUsage_OutputTokenPlaceholderFixture pins issue #3213's
// placeholder-vs-ground-truth behavior against
// testdata/output-placeholder-3183.jsonl -- a fixture whose envelopes are
// trimmed from a real `claude -p --output-format stream-json --verbose`
// capture taken with claude-code 2.1.204; only the token magnitudes are
// hand-authored. A live run in this Box is unauthenticated -- the
// credential-scrub PreToolUse hook empties ANTHROPIC_API_KEY for every Bash
// invocation -- so it returns is_error with every usage figure zero, giving
// no ground truth to capture. #3183 itself carries only the aggregate
// run-usage comment and no attachment, and its three workflow runs
// (33793564048/33793564177/33793564266) report zero artifacts.
// Magnitudes are taken from #3213's own #3183 pass-1 evidence: per-message
// output_tokens are message_start placeholders, mostly single digits and
// never above 21, while the pass's result event reports 33,815 output
// tokens -- a ~900x gap that stands in for the issue's own "wildly smaller"
// figure. As in that evidence, the result event's cache_read_input_tokens
// (3000) matches the main loop's own per-message cache-read sum exactly
// (1000 + 2000, the two distinct msg_main_1/msg_main_2 messages) -- the
// property that makes the result event main-loop ground truth rather than
// an unrelated snapshot. The fixture also carries a subagent spawn (a
// "Task" tool_use block naming subagent_type "scout", followed by two
// parent_tool_use_id-carrying scout messages) and a duplicate message.id
// re-emit (msg_main_1 appears on two lines, a second content block of the
// same call) so both the agent-split and the dedup-by-message.id rule stay
// exercised.
func TestExtractUsage_OutputTokenPlaceholderFixture(t *testing.T) {
	path := filepath.Join("testdata", "output-placeholder-3183.jsonl")

	resultOutputTokens := resultEventOutputTokens(t, path)

	naiveSum := naivePerMessageOutputSum(t, path)
	if naiveSum == 0 {
		t.Fatalf("naivePerMessageOutputSum(%s) = 0, fixture parsing broken", path)
	}
	// A generous 100x threshold: real placeholder magnitudes land closer to
	// 900x, but the point of this assertion is to FAIL loudly, not
	// precisely, the moment a future claude-code version starts emitting
	// real usage on stdout assistant events. Both sides are read out of the
	// fixture, so regenerating testdata/output-placeholder-3183.jsonl from a
	// fresh `claude --output-format stream-json` capture is the only edit
	// needed for that drift to speak here -- there is no companion constant
	// to hand-update in lockstep.
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

	// The other four columns are unaffected by this issue: each row's own
	// per-message deduped sum (msg_main_1's re-emit collapses to one call).
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

// forEachLine opens path, scans it line by line, and calls fn with each raw
// line -- the open/defer-close/bufio.Scanner/scanner.Err() scaffold shared
// by resultEventOutputTokens and naivePerMessageOutputSum below. Each caller
// keeps its own unmarshal target inside fn, so this helper never depends on
// either production event type.
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

// resultEventOutputTokens parses path itself -- independent of sumInLog --
// and returns the output_tokens summed across its result events, the log's
// own ground-truth figure for main-loop output.
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

// naivePerMessageOutputSum parses path itself -- independent of
// breakdownByAgentFile/sumInLog -- and returns the sum of output_tokens
// across every "assistant" line's message.usage, deduplicated by
// message.id the same way the production scan is (first occurrence of a
// non-empty id wins; a later line sharing that id is skipped). This is the
// naive, wrong-for-issue-#3213 figure the fixture's assertions compare
// against the result event's ground truth.
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
