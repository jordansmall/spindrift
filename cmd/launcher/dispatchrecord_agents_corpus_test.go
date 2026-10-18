package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/usage"
)

// These live in the root package, not dispatchrecord: only bundle binaries
// may reach Driver packages in their test closure (bundle_guard_test.go).

// corpusRecord ingests a real Driver corpus pass log under a stamp naming that
// Driver, after the given lines (an explicit pass_start where the corpus has none).
func corpusRecord(t *testing.T, drv, corpus string, before ...string) dispatchrecord.Record {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("internal", "driver", corpus, "pass.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	claim := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	start := hostop.DispatchStart{
		RecordID:    dispatchrecord.RecordID("work", "42", claim),
		Kind:        "work",
		DispatchKey: "42",
		ClaimTime:   claim,
		Started:     claim,
		Driver:      drv,
		RoleModels:  map[string]string{"main": "provider/model-x"},
	}
	log := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: "dispatch_start", Start: &start})
	for _, l := range before {
		log += l
	}
	p := filepath.Join(t.TempDir(), "issue-42.log")
	if err := os.WriteFile(p, []byte(log+string(b)), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, _, err := dispatchrecord.ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestParseLogRealClaudeSubagentsBreakDownThePass(t *testing.T) {
	const dir = "claude/testdata/corpus/real-fix-pass-subagents"
	rec := corpusRecord(t, "claude", dir)
	b, err := os.ReadFile(filepath.Join("internal", "driver", dir, "usage.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct{ SummedByAgent []usage.AgentUsage }
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	// The log holds several passes; the golden sums the whole file. Output is
	// not compared: it is not additive across subagents.
	sum := map[string]dispatchrecord.PassAgent{}
	for _, p := range rec.Passes {
		for _, a := range p.Agents {
			s := sum[a.Agent]
			s.APICalls += a.APICalls
			s.InputTokens += a.InputTokens
			s.CacheReadInputTokens += a.CacheReadInputTokens
			s.CacheCreationInputTokens += a.CacheCreationInputTokens
			sum[a.Agent] = s
		}
		if len(p.Agents) == 0 || p.Agents[0].Agent != "main" {
			t.Fatalf("pass %d agents = %+v, want the main loop first", p.Ordinal, p.Agents)
		}
		// The pass's own input and cache totals are the main row's, subagent
		// output is 0 (issue #3213), and API calls sum across rows.
		m := p.Agents[0]
		if m.InputTokens != p.InputTokens || m.CacheReadInputTokens != p.CacheReadInputTokens ||
			m.CacheCreationInputTokens != p.CacheCreationInputTokens {
			t.Errorf("pass %d main row = %+v, want input/cache totals %d/%d/%d", p.Ordinal, m,
				p.InputTokens, p.CacheReadInputTokens, p.CacheCreationInputTokens)
		}
		if m.OutputTokens != p.OutputTokens {
			t.Errorf("pass %d main row output = %d, want the pass's %d", p.Ordinal, m.OutputTokens, p.OutputTokens)
		}
		calls := m.APICalls
		for _, a := range p.Agents[1:] {
			if a.OutputTokens != 0 {
				t.Errorf("pass %d subagent %q output = %d, want 0", p.Ordinal, a.Agent, a.OutputTokens)
			}
			calls += a.APICalls
		}
		if calls != p.APICalls {
			t.Errorf("pass %d agent rows sum to %d API calls; pass has %d", p.Ordinal, calls, p.APICalls)
		}
	}
	if len(sum) != len(golden.SummedByAgent) {
		t.Fatalf("summed agents = %+v, want %d of them", sum, len(golden.SummedByAgent))
	}
	for _, g := range golden.SummedByAgent {
		want := dispatchrecord.PassAgent{APICalls: g.APICalls, InputTokens: g.UncachedInputTokens,
			CacheReadInputTokens: g.CacheReadInputTokens, CacheCreationInputTokens: g.CacheCreationInputTokens}
		if got, ok := sum[g.Agent]; !ok || got != want {
			t.Errorf("agent %q summed = %+v (present %v), want %+v", g.Agent, got, ok, want)
		}
	}
	for _, name := range []string{"main", "review-axis", "worker"} {
		if _, ok := sum[name]; !ok {
			t.Errorf("agent %q missing from %+v", name, sum)
		}
	}
}

func TestParseLogRealOpencodeSubagentTaskIsMainOnly(t *testing.T) {
	rec := corpusRecord(t, "opencode", "opencode/testdata/corpus/subagent-task-fixture",
		hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}))
	if len(rec.Passes) != 1 {
		t.Fatalf("passes = %+v", rec.Passes)
	}
	want := []dispatchrecord.PassAgent{{Agent: "main", APICalls: 1, InputTokens: 60, OutputTokens: 30}}
	if got := rec.Passes[0].Agents; !reflect.DeepEqual(got, want) {
		t.Fatalf("agents = %+v, want %+v", got, want)
	}
}
