package claude_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

func TestClaudeDriverReadPassesAgentsSumToUsageBreakdown(t *testing.T) {
	dir := filepath.Join("testdata", "corpus", "real-fix-pass-subagents")
	got, err := newDriver().ReadPasses(filepath.Join(dir, "pass.jsonl"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "usage.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct{ SummedByAgent []usage.AgentUsage }
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	sum := map[string]usage.AgentUsage{}
	for _, p := range got {
		for _, a := range p.Agents {
			s := sum[a.Agent]
			s.Agent = a.Agent
			s.APICalls += a.APICalls
			s.UncachedInputTokens += a.UncachedInputTokens
			s.OutputTokens += a.OutputTokens
			s.CacheReadInputTokens += a.CacheReadInputTokens
			s.CacheCreationInputTokens += a.CacheCreationInputTokens
			sum[a.Agent] = s
		}
	}
	if len(golden.SummedByAgent) < 2 {
		t.Fatalf("golden has %d agents, want main plus subagents", len(golden.SummedByAgent))
	}
	if len(sum) != len(golden.SummedByAgent) {
		t.Errorf("segments name %d agents, golden %d", len(sum), len(golden.SummedByAgent))
	}
	for _, w := range golden.SummedByAgent {
		if sum[w.Agent] != w {
			t.Errorf("agent %q summed over segments = %+v, want %+v", w.Agent, sum[w.Agent], w)
		}
	}
}
