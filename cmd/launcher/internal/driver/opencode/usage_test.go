package opencode_test

import (
	"math"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/usage"
)

// costEpsilon bounds float rounding when comparing summed TotalCostUSD, so a
// fixture edit that changes the accumulation order can't flake an exact ==.
const costEpsilon = 1e-9

// TestExtractUsage_MissingLog verifies that ExtractUsage on a log path that
// does not exist returns Found: false with no error, rather than propagating
// os.ErrNotExist or panicking.
func TestExtractUsage_MissingLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "nope.log")

	report, err := opencode.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if report.Found {
		t.Errorf("Found: got true, want false")
	}
}

// TestExtractUsage_EmptyLog verifies that ExtractUsage on a log file that
// exists but contains no lines returns Found: false with no error.
func TestExtractUsage_EmptyLog(t *testing.T) {
	logPath := opencode.WriteLog(t)

	report, err := opencode.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if report.Found {
		t.Errorf("Found: got true, want false")
	}
}

// TestExtractUsage_NoStepFinishEvents verifies that ExtractUsage returns
// Found: false when a log contains events but no step_finish — e.g. only a
// step_start and unrelated event types.
func TestExtractUsage_NoStepFinishEvents(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"step_start","timestamp":1000,"part":{"messageID":"msg_1"}}`,
		`{"type":"text","part":{"text":"hi"}}`,
	)

	report, err := opencode.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if report.Found {
		t.Errorf("Found: got true, want false")
	}
}

// TestExtractUsage_SkipsMalformedLines verifies that ExtractUsage skips
// non-JSON and blank lines interleaved among valid step_start/step_finish
// pairs, aggregating only the valid step_finish events.
func TestExtractUsage_SkipsMalformedLines(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"step_start","timestamp":1000,"part":{"messageID":"msg_1"}}`,
		`not json`,
		`{"type":"step_finish","timestamp":1500,"part":{"messageID":"msg_1","tokens":{"input":3,"output":120,"reasoning":0,"cache":{"write":800,"read":6400}},"cost":0.012}}`,
		``,
		`{"type":"step_start","timestamp":2000,"part":{"messageID":"msg_2"}}`,
		`{"type":"step_finish","timestamp":2500,"part":{"messageID":"msg_2","tokens":{"input":5,"output":80,"reasoning":40,"cache":{"write":200,"read":1600}},"cost":0.008}}`,
	)

	report, err := opencode.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if !report.Found {
		t.Fatalf("Found: got false, want true")
	}
	if got, want := report.FinalSnapshot.NumTurns, 2; got != want {
		t.Errorf("NumTurns: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.InputTokens, 8; got != want {
		t.Errorf("InputTokens: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.OutputTokens, 240; got != want {
		t.Errorf("OutputTokens: got %d, want %d", got, want)
	}
}

// TestExtractUsage_AggregatesStepFinishEvents verifies that ExtractUsage sums
// tokens, cost, and turn count across every step_finish event in an opencode
// NDJSON run log, folding reasoning tokens into OutputTokens, and computes
// wall-clock DurationMs from the first step_start to the last step_finish
// timestamp. Per-model breakdown is out of scope for this slice (Models is
// left empty).
func TestExtractUsage_AggregatesStepFinishEvents(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"step_start","timestamp":1000,"part":{"messageID":"msg_1"}}`,
		`{"type":"step_finish","timestamp":1500,"part":{"messageID":"msg_1","modelID":"gpt-5","tokens":{"input":3,"output":120,"reasoning":0,"cache":{"write":800,"read":6400}},"cost":0.012}}`,
		`{"type":"step_start","timestamp":2000,"part":{"messageID":"msg_2"}}`,
		`{"type":"step_finish","timestamp":2500,"part":{"messageID":"msg_2","modelID":"claude-sonnet-4","tokens":{"input":5,"output":80,"reasoning":40,"cache":{"write":200,"read":1600}},"cost":0.008}}`,
	)

	report, err := opencode.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if !report.Found {
		t.Fatalf("Found: got false, want true")
	}
	if got, want := report.FinalSnapshot.InputTokens, 8; got != want {
		t.Errorf("InputTokens: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.OutputTokens, 240; got != want {
		t.Errorf("OutputTokens: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.CacheReadInputTokens, 8000; got != want {
		t.Errorf("CacheReadInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.CacheCreationInputTokens, 1000; got != want {
		t.Errorf("CacheCreationInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.TotalCostUSD, 0.02; math.Abs(got-want) > costEpsilon {
		t.Errorf("TotalCostUSD: got %v, want %v", got, want)
	}
	if got, want := report.FinalSnapshot.DurationMs, int64(1500); got != want {
		t.Errorf("DurationMs: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.NumTurns, 2; got != want {
		t.Errorf("NumTurns: got %d, want %d", got, want)
	}
	if got, want := len(report.SummedByModel), 2; got != want {
		t.Fatalf("len(SummedByModel) = %d, want %d: %+v", got, want, report.SummedByModel)
	}
	wantOrder := []string{"claude-sonnet-4", "gpt-5"}
	for i, m := range report.SummedByModel {
		if m.Model != wantOrder[i] {
			t.Errorf("SummedByModel[%d].Model = %q, want %q (order: %v)", i, m.Model, wantOrder[i], report.SummedByModel)
		}
	}
	sonnet := report.SummedByModel[0]
	if sonnet.UncachedInputTokens != 5 {
		t.Errorf("sonnet.UncachedInputTokens = %d, want 5", sonnet.UncachedInputTokens)
	}
	if sonnet.OutputTokens != 120 {
		t.Errorf("sonnet.OutputTokens = %d, want 120", sonnet.OutputTokens)
	}
	gpt5 := report.SummedByModel[1]
	if gpt5.UncachedInputTokens != 3 {
		t.Errorf("gpt5.UncachedInputTokens = %d, want 3", gpt5.UncachedInputTokens)
	}
	if gpt5.OutputTokens != 120 {
		t.Errorf("gpt5.OutputTokens = %d, want 120", gpt5.OutputTokens)
	}
}

// TestExtractUsage_Fixture locks the aggregate totals against a committed
// synthetic opencode NDJSON run log: testdata/run-usage-sample.jsonl.
//
// The fixture is hand-authored (not a captured real run — this box has no
// opencode CLI or network to record one) with 3 completed turns, each a
// step_start/step_finish pair, plus an interspersed unrelated "text" event
// between turn 1's finish and turn 2's start to prove non-step lines are
// ignored. Turns 1 and 3 carry modelID "gpt-5"; turn 2 carries
// "claude-sonnet-4", exercising both the multi-model sum and the
// ascending-raw-id ordering ("claude-sonnet-4" sorts before "gpt-5").
// Hand-computed totals from the fixture's round-number tokens:
//
//	turn 1 (gpt-5):            input=100 output=50 reasoning=10 cache{write=20 read=200} cost=0.01
//	turn 2 (claude-sonnet-4):  input=200 output=100 reasoning=0  cache{write=30 read=300} cost=0.02
//	turn 3 (gpt-5):            input=300 output=150 reasoning=5  cache{write=50 read=500} cost=0.03
//
// InputTokens sums to 600; OutputTokens folds reasoning into output per turn
// (60+100+155) to 315; CacheReadInputTokens sums to 1000;
// CacheCreationInputTokens (cache.write) sums to 100; TotalCostUSD sums to
// 0.06; DurationMs is the last step_finish timestamp (4000) minus the first
// step_start timestamp (1000), i.e. 3000; NumTurns is 3. SummedByModel is
// now populated by breakdownByModel, summing each turn's per-call usage by
// distinct messageID and keyed by exact modelID, with cache.write mapped to
// the 5-minute bucket (opencode reports no TTL split): gpt-5 sums turns 1
// and 3 (input 400, output 60+155=215, cache.read 700, cache.write5m 70);
// claude-sonnet-4 is turn 2 alone (input 200, output 100, cache.read 300,
// cache.write5m 30).
func TestExtractUsage_Fixture(t *testing.T) {
	report, err := opencode.ExtractUsage("testdata/run-usage-sample.jsonl")
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if !report.Found {
		t.Fatalf("Found: got false, want true")
	}
	if got, want := report.FinalSnapshot.InputTokens, 600; got != want {
		t.Errorf("InputTokens: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.OutputTokens, 315; got != want {
		t.Errorf("OutputTokens: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.CacheReadInputTokens, 1000; got != want {
		t.Errorf("CacheReadInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.CacheCreationInputTokens, 100; got != want {
		t.Errorf("CacheCreationInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.TotalCostUSD, 0.06; math.Abs(got-want) > costEpsilon {
		t.Errorf("TotalCostUSD: got %v, want %v", got, want)
	}
	if got, want := report.FinalSnapshot.DurationMs, int64(3000); got != want {
		t.Errorf("DurationMs: got %d, want %d", got, want)
	}
	if got, want := report.FinalSnapshot.NumTurns, 3; got != want {
		t.Errorf("NumTurns: got %d, want %d", got, want)
	}

	if got, want := len(report.SummedByModel), 2; got != want {
		t.Fatalf("len(SummedByModel) = %d, want %d: %+v", got, want, report.SummedByModel)
	}
	wantOrder := []string{"claude-sonnet-4", "gpt-5"}
	for i, m := range report.SummedByModel {
		if m.Model != wantOrder[i] {
			t.Errorf("SummedByModel[%d].Model = %q, want %q (order: %v)", i, m.Model, wantOrder[i], report.SummedByModel)
		}
	}
	byModel := make(map[string]usage.ModelUsage)
	for _, m := range report.SummedByModel {
		byModel[m.Model] = m
	}

	gpt5 := byModel["gpt-5"]
	if gpt5.UncachedInputTokens != 400 {
		t.Errorf("gpt5.UncachedInputTokens = %d, want 400", gpt5.UncachedInputTokens)
	}
	if gpt5.OutputTokens != 215 {
		t.Errorf("gpt5.OutputTokens = %d, want 215", gpt5.OutputTokens)
	}
	if gpt5.CacheReadInputTokens != 700 {
		t.Errorf("gpt5.CacheReadInputTokens = %d, want 700", gpt5.CacheReadInputTokens)
	}
	if gpt5.CacheWrite5mTokens != 70 {
		t.Errorf("gpt5.CacheWrite5mTokens = %d, want 70", gpt5.CacheWrite5mTokens)
	}
	if gpt5.CacheWrite1hTokens != 0 {
		t.Errorf("gpt5.CacheWrite1hTokens = %d, want 0", gpt5.CacheWrite1hTokens)
	}

	sonnet := byModel["claude-sonnet-4"]
	if sonnet.UncachedInputTokens != 200 {
		t.Errorf("sonnet.UncachedInputTokens = %d, want 200", sonnet.UncachedInputTokens)
	}
	if sonnet.OutputTokens != 100 {
		t.Errorf("sonnet.OutputTokens = %d, want 100", sonnet.OutputTokens)
	}
	if sonnet.CacheReadInputTokens != 300 {
		t.Errorf("sonnet.CacheReadInputTokens = %d, want 300", sonnet.CacheReadInputTokens)
	}
	if sonnet.CacheWrite5mTokens != 30 {
		t.Errorf("sonnet.CacheWrite5mTokens = %d, want 30", sonnet.CacheWrite5mTokens)
	}
	if sonnet.CacheWrite1hTokens != 0 {
		t.Errorf("sonnet.CacheWrite1hTokens = %d, want 0", sonnet.CacheWrite1hTokens)
	}
}
