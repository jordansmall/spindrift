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

// A missing log is not an error: ExtractUsage reports Found: false rather than
// propagating os.ErrNotExist.
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
	if got, want := report.Totals.NumTurns, 2; got != want {
		t.Errorf("NumTurns: got %d, want %d", got, want)
	}
	if got, want := report.Totals.InputTokens, 8; got != want {
		t.Errorf("InputTokens: got %d, want %d", got, want)
	}
	if got, want := report.Totals.OutputTokens, 240; got != want {
		t.Errorf("OutputTokens: got %d, want %d", got, want)
	}
}

// Reasoning tokens fold into OutputTokens, and DurationMs spans the first
// step_start to the last step_finish, which is why the wanted numbers here do
// not match any single field of the log lines.
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
	if got, want := report.Totals.InputTokens, 8; got != want {
		t.Errorf("InputTokens: got %d, want %d", got, want)
	}
	if got, want := report.Totals.OutputTokens, 240; got != want {
		t.Errorf("OutputTokens: got %d, want %d", got, want)
	}
	if got, want := report.Totals.CacheReadInputTokens, 8000; got != want {
		t.Errorf("CacheReadInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.Totals.CacheCreationInputTokens, 1000; got != want {
		t.Errorf("CacheCreationInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.Totals.TotalCostUSD, 0.02; math.Abs(got-want) > costEpsilon {
		t.Errorf("TotalCostUSD: got %v, want %v", got, want)
	}
	if got, want := report.Totals.DurationMs, int64(1500); got != want {
		t.Errorf("DurationMs: got %d, want %d", got, want)
	}
	if got, want := report.Totals.NumTurns, 2; got != want {
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

// The event span comes from the same timestamps DurationMs is derived from, so
// the last assertion ties the two together rather than repeating the numbers.
func TestExtractUsage_EventSpan(t *testing.T) {
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
	if !report.HasEventSpan {
		t.Fatalf("HasEventSpan: got false, want true")
	}
	if got, want := report.EarliestEventMs, int64(1000); got != want {
		t.Errorf("EarliestEventMs: got %d, want %d", got, want)
	}
	if got, want := report.LatestEventMs, int64(2500); got != want {
		t.Errorf("LatestEventMs: got %d, want %d", got, want)
	}
	if got, want := report.LatestEventMs-report.EarliestEventMs, report.Totals.DurationMs; got != want {
		t.Errorf("LatestEventMs - EarliestEventMs = %d, want DurationMs %d", got, want)
	}
}

// A step_finish with no preceding step_start leaves the window unanchored, the
// same condition DurationMs's own haveStart check guards against.
func TestExtractUsage_EventSpan_NoStepStart(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"step_finish","timestamp":1500,"part":{"messageID":"msg_1","modelID":"gpt-5","tokens":{"input":3,"output":120,"reasoning":0,"cache":{"write":800,"read":6400}},"cost":0.012}}`,
	)

	report, err := opencode.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if !report.Found {
		t.Fatalf("Found: got false, want true")
	}
	if report.HasEventSpan {
		t.Errorf("HasEventSpan: got true, want false")
	}
	if got, want := report.EarliestEventMs, int64(0); got != want {
		t.Errorf("EarliestEventMs: got %d, want %d", got, want)
	}
	if got, want := report.LatestEventMs, int64(0); got != want {
		t.Errorf("LatestEventMs: got %d, want %d", got, want)
	}
	if got, want := report.Totals.DurationMs, int64(0); got != want {
		t.Errorf("DurationMs: got %d, want %d", got, want)
	}
}

func TestExtractUsage_EventSpan_NoStepFinish(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"step_start","timestamp":1000,"part":{"messageID":"msg_1"}}`,
	)

	report, err := opencode.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if report.Found {
		t.Errorf("Found: got true, want false")
	}
	if report.HasEventSpan {
		t.Errorf("HasEventSpan: got true, want false")
	}
	if got, want := report.EarliestEventMs, int64(0); got != want {
		t.Errorf("EarliestEventMs: got %d, want %d", got, want)
	}
	if got, want := report.LatestEventMs, int64(0); got != want {
		t.Errorf("LatestEventMs: got %d, want %d", got, want)
	}
}

// testdata/run-usage-sample.jsonl is hand-authored, not a captured run: this
// box has no opencode CLI or network to record one. Round-number tokens keep
// the wanted totals hand-computable, an unrelated "text" event proves non-step
// lines are ignored, and the two model ids pin ascending-raw-id order. opencode
// reports no cache TTL split, so every cache.write lands in the 5-minute bucket.
func TestExtractUsage_Fixture(t *testing.T) {
	report, err := opencode.ExtractUsage("testdata/run-usage-sample.jsonl")
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if !report.Found {
		t.Fatalf("Found: got false, want true")
	}
	if got, want := report.Totals.InputTokens, 600; got != want {
		t.Errorf("InputTokens: got %d, want %d", got, want)
	}
	if got, want := report.Totals.OutputTokens, 315; got != want {
		t.Errorf("OutputTokens: got %d, want %d", got, want)
	}
	if got, want := report.Totals.CacheReadInputTokens, 1000; got != want {
		t.Errorf("CacheReadInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.Totals.CacheCreationInputTokens, 100; got != want {
		t.Errorf("CacheCreationInputTokens: got %d, want %d", got, want)
	}
	if got, want := report.Totals.TotalCostUSD, 0.06; math.Abs(got-want) > costEpsilon {
		t.Errorf("TotalCostUSD: got %v, want %v", got, want)
	}
	if got, want := report.Totals.DurationMs, int64(3000); got != want {
		t.Errorf("DurationMs: got %d, want %d", got, want)
	}
	if got, want := report.Totals.NumTurns, 3; got != want {
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

// opencode's step_finish tallies are real whole-pass output tokens, so it
// leaves the issue #3213 flag false and nothing downstream may qualify them as
// main-loop-only.
func TestExtractUsage_OutputIsNotMainLoopOnly(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"step_start","timestamp":1000,"part":{"messageID":"msg_1"}}`,
		`{"type":"step_finish","timestamp":1500,"part":{"messageID":"msg_1","modelID":"gpt-5","tokens":{"input":3,"output":120,"reasoning":0,"cache":{"write":0,"read":0}},"cost":0.01}}`,
	)

	report, err := opencode.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if report.OutputIsMainLoopOnly {
		t.Errorf("report.OutputIsMainLoopOnly = true, want false: %+v", report)
	}
}
