package opencode_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

func TestOpencodeDriverReadPassesAgentsAreMainLoopOnly(t *testing.T) {
	d := newDriver()
	got, err := d.ReadPasses(filepath.Join("testdata", "corpus", "subagent-task-fixture", "pass.jsonl"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	for _, p := range got {
		for _, a := range p.Agents {
			if a.Agent != usage.MainLoopAgent {
				t.Errorf("agent row %q, want only %q (opencode never sets InSubagent)", a.Agent, usage.MainLoopAgent)
			}
			calls += a.APICalls
		}
	}
	if calls == 0 {
		t.Error("no agent rows tallied")
	}
}
