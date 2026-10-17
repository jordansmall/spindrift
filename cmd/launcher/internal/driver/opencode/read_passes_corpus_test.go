package opencode_test

import (
	"path/filepath"
	"testing"
)

func TestOpencodeDriverReadPassesCorpus(t *testing.T) {
	d := newDriver(t)
	got, err := d.ReadPasses(filepath.Join("testdata", "corpus", "real-normal", "pass.jsonl"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("tallies = %d, want 4 (3 pass_starts + preamble)", len(got))
	}
	var calls, tokens int
	for _, p := range got {
		calls += p.APICalls
		tokens += p.Usage.TotalTokens()
	}
	if calls == 0 || tokens == 0 {
		t.Errorf("calls=%d tokens=%d, want non-zero", calls, tokens)
	}
	if got[0].APICalls != 0 {
		t.Errorf("preamble segment APICalls = %d, want 0", got[0].APICalls)
	}
	last := got[len(got)-1]
	if len(last.Results) != 1 {
		t.Fatalf("last pass results = %v", last.Results)
	}
}

func TestOpencodeDriverReadPassesLastResultMatchesResultText(t *testing.T) {
	d := newDriver(t)
	path := filepath.Join("testdata", "corpus", "real-normal", "pass.jsonl")
	got, err := d.ReadPasses(path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := d.ResultText(path)
	if err != nil {
		t.Fatal(err)
	}
	last := got[len(got)-1]
	if len(last.Results) != 1 || last.Results[0] != want {
		t.Errorf("last pass results = %q, want [%q]", last.Results, want)
	}
}
