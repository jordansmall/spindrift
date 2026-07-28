package opencode_test

import (
	"math"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/opencode"
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
	if got, want := report.NumTurns, 2; got != want {
		t.Errorf("NumTurns: got %d, want %d", got, want)
	}
	if got, want := report.InputTokens, 8; got != want {
		t.Errorf("InputTokens: got %d, want %d", got, want)
	}
	if got, want := report.OutputTokens, 240; got != want {
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
		`{"type":"step_finish","timestamp":1500,"part":{"messageID":"msg_1","tokens":{"input":3,"output":120,"reasoning":0,"cache":{"write":800,"read":6400}},"cost":0.012}}`,
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
	if got, want := report.InputTokens, 8; got != want {
		t.Errorf("InputTokens: got %d, want %d", got, want)
	}
	if got, want := report.OutputTokens, 240; got != want {
		t.Errorf("OutputTokens: got %d, want %d", got, want)
	}
	if got, want := report.CacheReadInputTokens, 8000; got != want {
		t.Errorf("CacheReadInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.CacheCreationInputTokens, 1000; got != want {
		t.Errorf("CacheCreationInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.TotalCostUSD, 0.02; math.Abs(got-want) > costEpsilon {
		t.Errorf("TotalCostUSD: got %v, want %v", got, want)
	}
	if got, want := report.DurationMs, int64(1500); got != want {
		t.Errorf("DurationMs: got %d, want %d", got, want)
	}
	if got, want := report.NumTurns, 2; got != want {
		t.Errorf("NumTurns: got %d, want %d", got, want)
	}
	if len(report.Models) != 0 {
		t.Errorf("Models: got %v, want empty", report.Models)
	}
}

// TestExtractUsage_Fixture locks the aggregate totals against a committed
// synthetic opencode NDJSON run log: testdata/run-usage-sample.jsonl.
//
// The fixture is hand-authored (not a captured real run — this box has no
// opencode CLI or network to record one) with 3 completed turns, each a
// step_start/step_finish pair, plus an interspersed unrelated "text" event
// between turn 1's finish and turn 2's start to prove non-step lines are
// ignored. Hand-computed totals from the fixture's round-number tokens:
//
//	turn 1: input=100 output=50 reasoning=10 cache{write=20 read=200} cost=0.01
//	turn 2: input=200 output=100 reasoning=0  cache{write=30 read=300} cost=0.02
//	turn 3: input=300 output=150 reasoning=5  cache{write=50 read=500} cost=0.03
//
// InputTokens sums to 600; OutputTokens folds reasoning into output per turn
// (60+100+155) to 315; CacheReadInputTokens sums to 1000;
// CacheCreationInputTokens (cache.write) sums to 100; TotalCostUSD sums to
// 0.06; DurationMs is the last step_finish timestamp (4000) minus the first
// step_start timestamp (1000), i.e. 3000; NumTurns is 3; Models stays empty
// (per-model breakdown is out of scope for this slice, issue #262).
func TestExtractUsage_Fixture(t *testing.T) {
	report, err := opencode.ExtractUsage("testdata/run-usage-sample.jsonl")
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if !report.Found {
		t.Fatalf("Found: got false, want true")
	}
	if got, want := report.InputTokens, 600; got != want {
		t.Errorf("InputTokens: got %d, want %d", got, want)
	}
	if got, want := report.OutputTokens, 315; got != want {
		t.Errorf("OutputTokens: got %d, want %d", got, want)
	}
	if got, want := report.CacheReadInputTokens, 1000; got != want {
		t.Errorf("CacheReadInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.CacheCreationInputTokens, 100; got != want {
		t.Errorf("CacheCreationInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.TotalCostUSD, 0.06; math.Abs(got-want) > costEpsilon {
		t.Errorf("TotalCostUSD: got %v, want %v", got, want)
	}
	if got, want := report.DurationMs, int64(3000); got != want {
		t.Errorf("DurationMs: got %d, want %d", got, want)
	}
	if got, want := report.NumTurns, 3; got != want {
		t.Errorf("NumTurns: got %d, want %d", got, want)
	}
	if len(report.Models) != 0 {
		t.Errorf("Models: got %v, want empty", report.Models)
	}
}
