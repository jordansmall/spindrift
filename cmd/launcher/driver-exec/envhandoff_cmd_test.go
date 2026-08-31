package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
)

// fullEnvHandoffArgs returns a flag set that populates every env-handoff
// flag with a distinct, recognizable value, mirroring assembleprompt_cmd_test.go's
// coveredCellArgs/PopulatesPassthroughHandoffFields pattern.
func fullEnvHandoffArgs(handoffOutput string) []string {
	return []string{
		"--driver", "claude",
		"--driver-bin", "/usr/bin/claude",
		"--driver-flags", "--foo --bar",
		"--model", "gpt-codex",
		"--effort", "high",
		"--devshell",
		"--devshell-name", "myshell",
		"--issue", "2975",
		"--heartbeat-log", "/tmp/hb.log",
		"--argv-prompt-style", "positional",
		"--argv-prompt-flag", "-p",
		"--argv-model-flag", "--model",
		"--argv-model-omit-empty",
		"--argv-agents-flag", "--agents",
		"--argv-effort-flag", "--effort",
		"--argv-order", "prompt model agents session driverFlags effort",
		"--max-budget-tokens", "50000",
		"--max-budget-usd", "12.5",
		"--handoff-output", handoffOutput,
	}
}

// TestRunEnvHandoff_PopulatesAllFields verifies a full-flags invocation
// writes a Handoff JSON with every field populated correctly (issue #2975
// slice 2, mirroring TestRunAssemblePrompt_PopulatesPassthroughHandoffFields).
func TestRunEnvHandoff_PopulatesAllFields(t *testing.T) {
	dir := t.TempDir()
	handoffOutput := filepath.Join(dir, "handoff.json")

	var stdout bytes.Buffer
	rc := runEnvHandoff(fullEnvHandoffArgs(handoffOutput), &stdout)
	if rc != 0 {
		t.Fatalf("runEnvHandoff exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	handoffBytes, err := os.ReadFile(handoffOutput)
	if err != nil {
		t.Fatalf("read handoff output: %v", err)
	}
	var handoff promptassembly.Handoff
	if err := json.Unmarshal(handoffBytes, &handoff); err != nil {
		t.Fatalf("unmarshal handoff output: %v\n%s", err, handoffBytes)
	}

	if handoff.Driver != "claude" {
		t.Errorf("Handoff.Driver = %q, want claude", handoff.Driver)
	}
	if handoff.DriverBin != "/usr/bin/claude" {
		t.Errorf("Handoff.DriverBin = %q, want /usr/bin/claude", handoff.DriverBin)
	}
	if handoff.DriverFlags != "--foo --bar" {
		t.Errorf("Handoff.DriverFlags = %q, want %q", handoff.DriverFlags, "--foo --bar")
	}
	if handoff.Model != "gpt-codex" {
		t.Errorf("Handoff.Model = %q, want gpt-codex", handoff.Model)
	}
	if handoff.Effort != "high" {
		t.Errorf("Handoff.Effort = %q, want high", handoff.Effort)
	}
	if !handoff.Devshell {
		t.Error("Handoff.Devshell = false, want true")
	}
	if handoff.DevshellName != "myshell" {
		t.Errorf("Handoff.DevshellName = %q, want myshell", handoff.DevshellName)
	}
	if handoff.Issue != "2975" {
		t.Errorf("Handoff.Issue = %q, want 2975", handoff.Issue)
	}
	if handoff.HeartbeatLog != "/tmp/hb.log" {
		t.Errorf("Handoff.HeartbeatLog = %q, want /tmp/hb.log", handoff.HeartbeatLog)
	}

	wantArgvShape := promptassembly.ArgvShape{
		PromptStyle:    "positional",
		PromptFlag:     "-p",
		ModelFlag:      "--model",
		ModelOmitEmpty: true,
		AgentsFlag:     "--agents",
		EffortFlag:     "--effort",
		Order:          []string{"prompt", "model", "agents", "session", "driverFlags", "effort"},
	}
	if handoff.ArgvShape.PromptStyle != wantArgvShape.PromptStyle ||
		handoff.ArgvShape.PromptFlag != wantArgvShape.PromptFlag ||
		handoff.ArgvShape.ModelFlag != wantArgvShape.ModelFlag ||
		handoff.ArgvShape.ModelOmitEmpty != wantArgvShape.ModelOmitEmpty ||
		handoff.ArgvShape.AgentsFlag != wantArgvShape.AgentsFlag ||
		handoff.ArgvShape.EffortFlag != wantArgvShape.EffortFlag ||
		!stringSlicesEqual(handoff.ArgvShape.Order, wantArgvShape.Order) {
		t.Errorf("Handoff.ArgvShape = %+v, want %+v", handoff.ArgvShape, wantArgvShape)
	}

	wantCaps := promptassembly.Caps{
		MaxSlices:       promptassembly.DefaultMaxSlices,
		MaxReviewRounds: promptassembly.DefaultMaxReviewRounds,
		MaxBudgetTokens: 50000,
		MaxBudgetUSD:    12.5,
	}
	if handoff.Caps != wantCaps {
		t.Errorf("Handoff.Caps = %+v, want %+v", handoff.Caps, wantCaps)
	}

	// Fields the conflict-resolve pass never needs must stay at their zero
	// values -- env-handoff has no flags for them (see entrypoint.sh's
	// _write_env_handoff doc comment).
	if handoff.PromptFile != "" {
		t.Errorf("Handoff.PromptFile = %q, want empty (no flag for it)", handoff.PromptFile)
	}
	if handoff.AgentsFile != "" {
		t.Errorf("Handoff.AgentsFile = %q, want empty (no flag for it)", handoff.AgentsFile)
	}
	if handoff.ReviewPromptFile != "" {
		t.Errorf("Handoff.ReviewPromptFile = %q, want empty (no flag for it)", handoff.ReviewPromptFile)
	}
	if handoff.ReviewModel != "" {
		t.Errorf("Handoff.ReviewModel = %q, want empty (no flag for it)", handoff.ReviewModel)
	}
	if handoff.ReviewEffort != "" {
		t.Errorf("Handoff.ReviewEffort = %q, want empty (no flag for it)", handoff.ReviewEffort)
	}
}

// TestRunEnvHandoff_DefaultCaps verifies Caps.MaxSlices/MaxReviewRounds
// always come out as promptassembly.DefaultMaxSlices/DefaultMaxReviewRounds
// -- there is no flag to override either, so this must hold on every
// invocation (issue #2975 slice 2). It also verifies the default ArgvShape
// (every argv-* flag omitted) matches assembleprompt_cmd.go's own defaults
// exactly and is a usable shape, not just present -- a caller of env-handoff
// that omits the argv-* flags must get a working invocation, not a
// buildDriverArgs "invalid promptStyle" failure (issue #2975 review finding
// #2: this file used to declare its own, different defaults).
func TestRunEnvHandoff_DefaultCaps(t *testing.T) {
	dir := t.TempDir()
	handoffOutput := filepath.Join(dir, "handoff.json")

	var stdout bytes.Buffer
	rc := runEnvHandoff([]string{"--handoff-output", handoffOutput}, &stdout)
	if rc != 0 {
		t.Fatalf("runEnvHandoff exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	handoffBytes, err := os.ReadFile(handoffOutput)
	if err != nil {
		t.Fatalf("read handoff output: %v", err)
	}
	var handoff promptassembly.Handoff
	if err := json.Unmarshal(handoffBytes, &handoff); err != nil {
		t.Fatalf("unmarshal handoff output: %v\n%s", err, handoffBytes)
	}

	if handoff.Caps.MaxSlices != promptassembly.DefaultMaxSlices {
		t.Errorf("Handoff.Caps.MaxSlices = %d, want %d", handoff.Caps.MaxSlices, promptassembly.DefaultMaxSlices)
	}
	if handoff.Caps.MaxReviewRounds != promptassembly.DefaultMaxReviewRounds {
		t.Errorf("Handoff.Caps.MaxReviewRounds = %d, want %d", handoff.Caps.MaxReviewRounds, promptassembly.DefaultMaxReviewRounds)
	}

	wantArgvShape := promptassembly.ArgvShape{
		PromptStyle:    "flag",
		PromptFlag:     "",
		ModelFlag:      "--model",
		ModelOmitEmpty: false,
		AgentsFlag:     "",
		EffortFlag:     "--effort",
		Order:          []string{"prompt", "model", "agents", "session", "driverFlags", "effort"},
	}
	if handoff.ArgvShape.PromptStyle != wantArgvShape.PromptStyle ||
		handoff.ArgvShape.PromptFlag != wantArgvShape.PromptFlag ||
		handoff.ArgvShape.ModelFlag != wantArgvShape.ModelFlag ||
		handoff.ArgvShape.ModelOmitEmpty != wantArgvShape.ModelOmitEmpty ||
		handoff.ArgvShape.AgentsFlag != wantArgvShape.AgentsFlag ||
		handoff.ArgvShape.EffortFlag != wantArgvShape.EffortFlag ||
		!stringSlicesEqual(handoff.ArgvShape.Order, wantArgvShape.Order) {
		t.Errorf("Handoff.ArgvShape = %+v, want %+v (assembleprompt_cmd.go's own defaults)", handoff.ArgvShape, wantArgvShape)
	}

	// Prove "usable", not just "matches the wanted struct": a prompt file
	// with content and no agents/session/driverFlags/model/effort must build
	// a Driver argv without error under this exact shape.
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}
	if _, err := buildDriverArgs(driverInput{
		shape: argvShape{
			promptStyle:    handoff.ArgvShape.PromptStyle,
			promptFlag:     handoff.ArgvShape.PromptFlag,
			modelFlag:      handoff.ArgvShape.ModelFlag,
			modelOmitEmpty: handoff.ArgvShape.ModelOmitEmpty,
			agentsFlag:     handoff.ArgvShape.AgentsFlag,
			effortFlag:     handoff.ArgvShape.EffortFlag,
			order:          handoff.ArgvShape.Order,
		},
		promptFile: promptFile,
	}); err != nil {
		t.Errorf("buildDriverArgs with the default ArgvShape returned an error, want a usable shape: %v", err)
	}
}

// TestRunEnvHandoff_MalformedBudgetCapsDegradeToZero pins the same
// graceful-degrade contract assembleprompt_cmd.go's runAssemblePrompt has
// (issue #2975 review finding #1): a malformed or negative
// --max-budget-tokens/--max-budget-usd value must degrade to 0, never make
// runEnvHandoff return non-zero -- entrypoint.sh forwards
// MAX_BUDGET_TOKENS/MAX_BUDGET_USD verbatim under set -euo pipefail.
func TestRunEnvHandoff_MalformedBudgetCapsDegradeToZero(t *testing.T) {
	tests := []struct {
		name            string
		maxBudgetTokens string
		maxBudgetUSD    string
	}{
		{"malformed strings", "not-a-number", "not-a-number"},
		{"negative values", "-1", "-0.01"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			handoffOutput := filepath.Join(dir, "handoff.json")

			args := []string{
				"--max-budget-tokens", tt.maxBudgetTokens,
				"--max-budget-usd", tt.maxBudgetUSD,
				"--handoff-output", handoffOutput,
			}

			var stdout bytes.Buffer
			rc := runEnvHandoff(args, &stdout)
			if rc != 0 {
				t.Fatalf("runEnvHandoff exit = %d, want 0 (malformed/negative budget caps must degrade to 0, not fail the run) (stdout=%q)", rc, stdout.String())
			}

			handoffBytes, err := os.ReadFile(handoffOutput)
			if err != nil {
				t.Fatalf("read handoff output: %v", err)
			}
			var handoff promptassembly.Handoff
			if err := json.Unmarshal(handoffBytes, &handoff); err != nil {
				t.Fatalf("unmarshal handoff output: %v\n%s", err, handoffBytes)
			}

			if handoff.Caps.MaxBudgetTokens != 0 {
				t.Errorf("Handoff.Caps.MaxBudgetTokens = %d, want 0", handoff.Caps.MaxBudgetTokens)
			}
			if handoff.Caps.MaxBudgetUSD != 0 {
				t.Errorf("Handoff.Caps.MaxBudgetUSD = %v, want 0", handoff.Caps.MaxBudgetUSD)
			}
		})
	}
}

// TestRunEnvHandoff_MissingHandoffOutputReturnsNonZero verifies a missing
// --handoff-output fails loudly (exit 1) instead of silently writing
// nowhere (issue #2975 slice 2, mirrors
// TestRunAssemblePrompt_MissingRequiredFlagReturnsNonZero).
func TestRunEnvHandoff_MissingHandoffOutputReturnsNonZero(t *testing.T) {
	var stdout bytes.Buffer
	rc := runEnvHandoff([]string{"--driver", "claude"}, &stdout)
	if rc == 0 {
		t.Fatal("runEnvHandoff exit = 0, want non-zero for a missing --handoff-output")
	}
}

// TestIsEnvHandoffInvocation verifies the env-handoff subcommand's dispatch
// guard: a bare "env-handoff" first arg selects it, while every other
// invocation shape falls through to the default Driver-invocation path (or,
// for the other subcommands, to those) -- mirrors
// TestIsAssemblePromptInvocation.
func TestIsEnvHandoffInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"env-handoff first arg", []string{"env-handoff", "--driver", "claude"}, true},
		{"no args", nil, false},
		{"ordinary flag invocation", []string{"--driver", "claude"}, false},
		{"assemble-prompt", []string{"assemble-prompt"}, false},
		{"bundle-out", []string{"bundle-out"}, false},
	}
	for _, c := range cases {
		if got := isEnvHandoffInvocation(c.args); got != c.want {
			t.Errorf("%s: isEnvHandoffInvocation(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}

// stringSlicesEqual compares two string slices element-by-element, mirroring
// assembleprompt_cmd_test.go's reflectStringSlicesEqual (kept as a separate
// copy since the two test files must each stand on their own without
// cross-file helper coupling).
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
