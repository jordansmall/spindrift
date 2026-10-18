package opencode_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOpencodeDriverReadPassesModelsAndWrite(t *testing.T) {
	d := newDriver()
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
