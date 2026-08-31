package promptassembly

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestLoadHandoffFileRoundTrip covers the write side a later slice's CLI
// wrapper performs (json.Marshal + os.WriteFile) and LoadHandoffFile's read
// side, including the nested ArgvShape/Caps structs -- a value round-tripped
// through both must compare equal field-for-field.
func TestLoadHandoffFileRoundTrip(t *testing.T) {
	want := Handoff{
		SessionMode:      "resume",
		Invoker:          "orchestrator",
		PromptFile:       "/tmp/prompt.txt",
		AgentsFile:       "/tmp/agents.json",
		ReviewPromptFile: "/tmp/review-prompt.txt",
		ReviewModel:      "review-model-x",
		ReviewEffort:     "review-effort-x",
		Model:            "model-x",
		Effort:           "high",
		Driver:           "claude",
		DriverBin:        "/usr/bin/claude",
		DriverFlags:      "--flag-x",
		Devshell:         true,
		DevshellName:     "default",
		Issue:            "2975",
		HeartbeatLog:     "/tmp/heartbeat.log",
		ArgvShape: ArgvShape{
			PromptStyle:    "positional",
			PromptFlag:     "--prompt",
			ModelFlag:      "--model",
			ModelOmitEmpty: true,
			AgentsFlag:     "--agents",
			EffortFlag:     "--effort",
			Order:          []string{"--model", "--agents", "--effort"},
		},
		Caps: Caps{
			MaxSlices:       5,
			MaxReviewRounds: 3,
			MaxBudgetTokens: 100000,
			MaxBudgetUSD:    12.5,
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	got, err := LoadHandoffFile(path)
	if err != nil {
		t.Fatalf("LoadHandoffFile: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadHandoffFile round-trip mismatch:\ngot:  %+v\nwant: %+v", got, want)
	}
}

// TestLoadHandoffFileMissing covers a missing handoff file: LoadHandoffFile
// must return a non-nil, path-mentioning error rather than panicking or
// returning a zero-value Handoff with a nil error.
func TestLoadHandoffFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")

	_, err := LoadHandoffFile(path)
	if err == nil {
		t.Fatal("LoadHandoffFile: got nil error, want non-nil for a missing file")
	}
}
