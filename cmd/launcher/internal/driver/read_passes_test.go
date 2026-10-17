package driver

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

func TestClaudeDriverReadPasses(t *testing.T) {
	d, err := New("claude")
	if err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":1,"role":"implement"}}`,
		`{"type":"assistant","message":{"id":"m1","model":"claude-sonnet","usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"tool_use","id":"w1","name":"Write","input":{"file_path":"/tmp/verdict.md","content":"APPROVE"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"w1","content":"ok"}]}}`,
		`{"type":"assistant","message":{"id":"m1","model":"claude-sonnet","usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"done"}]}}`,
		`{"type":"result","result":"first","total_cost_usd":0.5,"num_turns":2,"duration_ms":100,"duration_api_ms":60,"usage":{"input_tokens":10,"output_tokens":20},"modelUsage":{"claude-sonnet":{},"claude-haiku":{}}}`,
		`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":2,"role":"fix"}}`,
		`{"type":"result","result":"second","total_cost_usd":0.25,"num_turns":1,"usage":{"input_tokens":3,"output_tokens":4},"modelUsage":{"claude-opus":{}}}`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "pass.jsonl")
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	var lines int
	got, err := d.ReadPasses(path, "/tmp/verdict.md", func(driverkit.PassLine) { lines++ })
	if err != nil {
		t.Fatal(err)
	}
	if lines != 7 {
		t.Errorf("visited %d lines, want 7", lines)
	}
	if len(got) != 3 {
		t.Fatalf("tallies = %d, want 3", len(got))
	}
	if got[0].APICalls != 0 || len(got[0].Results) != 0 {
		t.Errorf("preamble segment = %+v, want empty", got[0])
	}
	p1 := got[1]
	if p1.APICalls != 1 || p1.Usage.InputTokens != 10 || p1.Usage.OutputTokens != 20 ||
		p1.Usage.TotalCostUSD != 0.5 || p1.Usage.NumTurns != 2 || p1.Usage.DurationMs != 100 {
		t.Errorf("pass 1 = %+v", p1)
	}
	if !reflect.DeepEqual(p1.Models, []string{"claude-haiku", "claude-sonnet"}) {
		t.Errorf("pass 1 models = %v", p1.Models)
	}
	if !reflect.DeepEqual(p1.Results, []string{"first"}) {
		t.Errorf("pass 1 results = %v", p1.Results)
	}
	if !p1.Wrote || p1.Written != "APPROVE" {
		t.Errorf("pass 1 write = %q, %v", p1.Written, p1.Wrote)
	}
	p2 := got[2]
	if p2.Usage.TotalCostUSD != 0.25 || !reflect.DeepEqual(p2.Models, []string{"claude-opus"}) ||
		!reflect.DeepEqual(p2.Results, []string{"second"}) || p2.Wrote {
		t.Errorf("pass 2 = %+v", p2)
	}
}

func TestOpencodeDriverReadPassesModelsAndWrite(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":1,"role":"implement"}}`,
		`{"type":"tool_use","timestamp":5,"part":{"callID":"c1","tool":"write","messageID":"msg_1","state":{"status":"completed","input":{"filePath":"/tmp/verdict.md","content":"APPROVE"},"output":"ok"}}}`,
		`{"type":"step_finish","timestamp":9,"part":{"messageID":"msg_1","modelID":"anthropic/claude-sonnet","tokens":{"input":3,"output":4,"reasoning":1,"cache":{"write":0,"read":2}},"cost":0.1}}`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "pass.jsonl")
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := d.ReadPasses(path, "/tmp/verdict.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("tallies = %d, want 2", len(got))
	}
	p := got[1]
	if p.APICalls != 1 || p.Usage.InputTokens != 3 || p.Usage.OutputTokens != 5 || p.Usage.CacheReadInputTokens != 2 ||
		!reflect.DeepEqual(p.Models, []string{"anthropic/claude-sonnet"}) {
		t.Errorf("pass 1 = %+v", p)
	}
	if !p.Wrote || p.Written != "APPROVE" {
		t.Errorf("write = %q, %v", p.Written, p.Wrote)
	}
}
