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

// TestParseNonnegBudgetTokens guards -max-budget-tokens' graceful-degrade
// parsing (issue #2694 review finding, moved here from
// orchestrator/caps_test.go by issue #2975 review finding #1 alongside
// ParseNonnegBudgetTokens itself): a negative or malformed value collapses
// to 0 (disabled) instead of erroring, mirroring the host launcher's own
// atoiNonneg tolerance for the identical MAX_BUDGET_TOKENS env var -- the Box
// must not be stricter than the host about the same knob now that it's
// forwarded there unconditionally (boxEnv=true).
func TestParseNonnegBudgetTokens(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   int
		wantOK bool
	}{
		{"zero", "0", 0, true},
		{"positive", "100", 100, true},
		{"negative collapses to 0", "-1", 0, false},
		{"malformed collapses to 0", "not-a-number", 0, false},
		{"empty collapses to 0", "", 0, false},
		{"fractional collapses to 0 (Atoi rejects it, not a valid int)", "4.44", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseNonnegBudgetTokens(tt.in)
			if got != tt.want {
				t.Errorf("ParseNonnegBudgetTokens(%q) = %d, want %d", tt.in, got, tt.want)
			}
			if ok != tt.wantOK {
				t.Errorf("ParseNonnegBudgetTokens(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
		})
	}
}

// TestParseNonnegBudgetUSD is TestParseNonnegBudgetTokens' -max-budget-usd
// counterpart, mirroring the host launcher's own floatNonnegSchema tolerance
// the same way -- including that a fractional value, unlike the tokens case,
// parses normally here.
func TestParseNonnegBudgetUSD(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   float64
		wantOK bool
	}{
		{"zero", "0", 0, true},
		{"positive", "4.44", 4.44, true},
		{"negative collapses to 0", "-0.01", 0, false},
		{"malformed collapses to 0", "not-a-number", 0, false},
		{"empty collapses to 0", "", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseNonnegBudgetUSD(tt.in)
			if got != tt.want {
				t.Errorf("ParseNonnegBudgetUSD(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if ok != tt.wantOK {
				t.Errorf("ParseNonnegBudgetUSD(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
		})
	}
}
