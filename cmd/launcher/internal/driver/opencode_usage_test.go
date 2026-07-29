package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpencodeDriverExtractUsage verifies the opencode Driver's ExtractUsage
// method surfaces a per-model breakdown (usage.Report.SummedByModel) through
// the driver.Driver seam — the same seam dispatch's UsageReport calls —
// mirroring TestClaudeDriverExtractUsage above.
//
// The log carries three step_finish events across two distinct modelIDs
// (gpt-5, claude-sonnet-4), plus one re-emit of msg_1's messageID under
// gpt-5 to prove SummedByModel's dedup-by-messageID rule flows through the
// Driver seam: the re-emit is folded into FinalSnapshot's plain sum (no
// dedup there) but excluded from SummedByModel's per-model sum (issue
// #2085's documented FinalSnapshot vs. SummedByModel divergence).
//
// Hand-computed expectations:
//
//	msg_1 (gpt-5):            input=100 output=50 reasoning=10 cache.write=20 cache.read=200 cost=0.01
//	msg_1 re-emit (gpt-5):    input=5   output=5  reasoning=0  cache.write=5  cache.read=5   cost=0.001
//	msg_2 (claude-sonnet-4):  input=200 output=100 reasoning=0 cache.write=30 cache.read=300 cost=0.02
//	msg_3 (gpt-5):            input=300 output=150 reasoning=5 cache.write=50 cache.read=500 cost=0.03
//
//	FinalSnapshot (plain sum over all 4 step_finish events, no dedup):
//	  NumTurns=4
//	  InputTokens = 100+5+200+300 = 605
//	  OutputTokens = (50+10)+(5+0)+(100+0)+(150+5) = 320
//	  CacheReadInputTokens = 200+5+300+500 = 1005
//	  CacheCreationInputTokens = 20+5+30+50 = 105
//	  TotalCostUSD = 0.01+0.001+0.02+0.03 = 0.061
//
//	SummedByModel (deduped by messageID, sorted ascending by raw model id —
//	the re-emit's messageID "msg_1" was already seen, so it is skipped):
//	  claude-sonnet-4: UncachedInputTokens=200 OutputTokens=100 CacheReadInputTokens=300 CacheWrite5mTokens=30
//	  gpt-5:           UncachedInputTokens=100+300=400 OutputTokens=(50+10)+(150+5)=215 CacheReadInputTokens=200+500=700 CacheWrite5mTokens=20+50=70
func TestOpencodeDriverExtractUsage(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue-1.log")
	lines := []string{
		`{"type":"step_start","timestamp":1000,"part":{"messageID":"msg_1"}}`,
		`{"type":"step_finish","timestamp":1500,"part":{"messageID":"msg_1","modelID":"gpt-5","tokens":{"input":100,"output":50,"reasoning":10,"cache":{"write":20,"read":200}},"cost":0.01}}`,
		`{"type":"step_finish","timestamp":1600,"part":{"messageID":"msg_1","modelID":"gpt-5","tokens":{"input":5,"output":5,"reasoning":0,"cache":{"write":5,"read":5}},"cost":0.001}}`,
		`{"type":"step_start","timestamp":2000,"part":{"messageID":"msg_2"}}`,
		`{"type":"step_finish","timestamp":2500,"part":{"messageID":"msg_2","modelID":"claude-sonnet-4","tokens":{"input":200,"output":100,"reasoning":0,"cache":{"write":30,"read":300}},"cost":0.02}}`,
		`{"type":"step_start","timestamp":3000,"part":{"messageID":"msg_3"}}`,
		`{"type":"step_finish","timestamp":4000,"part":{"messageID":"msg_3","modelID":"gpt-5","tokens":{"input":300,"output":150,"reasoning":5,"cache":{"write":50,"read":500}},"cost":0.03}}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := d.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if !report.Found {
		t.Fatal("Found: got false, want true")
	}
	if report.FinalSnapshot.NumTurns != 4 {
		t.Errorf("NumTurns: got %d, want 4", report.FinalSnapshot.NumTurns)
	}

	if len(report.SummedByModel) != 2 {
		t.Fatalf("len(SummedByModel) = %d, want 2: %+v", len(report.SummedByModel), report.SummedByModel)
	}

	claude := report.SummedByModel[0]
	if claude.Model != "claude-sonnet-4" {
		t.Errorf("SummedByModel[0].Model: got %q, want %q", claude.Model, "claude-sonnet-4")
	}
	if claude.UncachedInputTokens != 200 {
		t.Errorf("claude-sonnet-4 UncachedInputTokens: got %d, want 200", claude.UncachedInputTokens)
	}
	if claude.OutputTokens != 100 {
		t.Errorf("claude-sonnet-4 OutputTokens: got %d, want 100", claude.OutputTokens)
	}
	if claude.CacheReadInputTokens != 300 {
		t.Errorf("claude-sonnet-4 CacheReadInputTokens: got %d, want 300", claude.CacheReadInputTokens)
	}
	if claude.CacheWrite5mTokens != 30 {
		t.Errorf("claude-sonnet-4 CacheWrite5mTokens: got %d, want 30", claude.CacheWrite5mTokens)
	}

	gpt := report.SummedByModel[1]
	if gpt.Model != "gpt-5" {
		t.Errorf("SummedByModel[1].Model: got %q, want %q", gpt.Model, "gpt-5")
	}
	if gpt.UncachedInputTokens != 400 {
		t.Errorf("gpt-5 UncachedInputTokens: got %d, want 400 (dedup must exclude the msg_1 re-emit)", gpt.UncachedInputTokens)
	}
	if gpt.OutputTokens != 215 {
		t.Errorf("gpt-5 OutputTokens: got %d, want 215", gpt.OutputTokens)
	}
	if gpt.CacheReadInputTokens != 700 {
		t.Errorf("gpt-5 CacheReadInputTokens: got %d, want 700", gpt.CacheReadInputTokens)
	}
	if gpt.CacheWrite5mTokens != 70 {
		t.Errorf("gpt-5 CacheWrite5mTokens: got %d, want 70", gpt.CacheWrite5mTokens)
	}
}
