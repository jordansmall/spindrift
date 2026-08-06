package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// promptsDirForTest is the real templates/default/prompts tree, resolved
// relative to this package directory (cmd/launcher/driver-exec), the same
// convention promptassembly's own assemble_test.go uses for its
// package-relative path.
const promptsDirForTest = "../../../templates/default/prompts"

// registryPathForTest reuses promptassembly's own testdata registry fixture
// rather than duplicating it by hand.
const registryPathForTest = "../internal/promptassembly/testdata/registry.json"

// coveredCellArgs returns the flag set that puts runAssemblePrompt's Env
// squarely in promptassembly.Assemble's one covered cell (see
// promptassembly's checkCoveredCell): github tracker, github forge, a
// read-write box, dispatch kind "work", a fresh box (fix-pass 0), the
// orchestrator off, and every skill baked.
func coveredCellArgs(t *testing.T, promptOutput, agentsJSONOutput, handoffOutput string) []string {
	t.Helper()
	return []string{
		"--caveman-skill-baked=true",
		"--tdd-skill-baked=true",
		"--commit-skill-baked=true",
		"--code-review-skill-baked=true",
		"--orchestrator-enabled=false",
		"--issue-tracker", "github",
		"--box-write-enabled=true",
		"--code-forge", "github",
		"--dispatch-kind", "work",
		"--fix-pass", "0",
		"--prompts-dir", promptsDirForTest,
		"--skills-found", "caveman, tdd, commit, code-review",
		"--issue-number", "2349",
		"--issue-title", "Add assemble-prompt CLI verb",
		"--branch", "agent/issue-2349",
		"--base-branch", "main",
		"--in-progress-label", "agent-in-progress",
		"--complete-label", "agent-complete",
		"--run-nonce", "run-nonce-abc123",
		"--registry", registryPathForTest,
		"--prompt-output", promptOutput,
		"--agents-json-output", agentsJSONOutput,
		"--handoff-output", handoffOutput,
	}
}

// TestRunAssemblePrompt_CoveredCellWritesOutputs verifies the assemble-prompt
// subcommand's flag parsing reaches promptassembly.Assemble with the right
// Env/Registry and writes all three output files (issue #2349).
func TestRunAssemblePrompt_CoveredCellWritesOutputs(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	var stdout bytes.Buffer
	rc := runAssemblePrompt(coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput), &stdout)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	promptBytes, err := os.ReadFile(promptOutput)
	if err != nil {
		t.Fatalf("read prompt output: %v", err)
	}
	if len(promptBytes) == 0 {
		t.Error("prompt output is empty, want non-empty")
	}

	if _, err := os.Stat(agentsJSONOutput); err != nil {
		t.Fatalf("agents json output not written: %v", err)
	}

	handoffBytes, err := os.ReadFile(handoffOutput)
	if err != nil {
		t.Fatalf("read handoff output: %v", err)
	}
	var handoff struct {
		SessionMode      string
		Invoker          string
		ReviewPromptFile string
		ReviewModel      string
	}
	if err := json.Unmarshal(handoffBytes, &handoff); err != nil {
		t.Fatalf("unmarshal handoff output: %v\n%s", err, handoffBytes)
	}
	if handoff.Invoker != "driver-exec" {
		t.Errorf("handoff.Invoker = %q, want driver-exec", handoff.Invoker)
	}
}

// TestRunAssemblePrompt_UnsupportedCellReturnsNonZero verifies an Env
// outside promptassembly.Assemble's covered cell (here, a non-github issue
// tracker) is reported as a CLI failure, not a panic.
func TestRunAssemblePrompt_UnsupportedCellReturnsNonZero(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	for i, a := range args {
		if a == "github" && args[i-1] == "--issue-tracker" {
			args[i] = "forgejo"
		}
	}

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout)
	if rc == 0 {
		t.Fatal("runAssemblePrompt exit = 0, want non-zero for an unsupported cell")
	}
}

// TestRunAssemblePrompt_MissingRequiredFlagReturnsNonZero verifies a missing
// -handoff-output fails loudly (exit 1) instead of running Assemble against
// a zero-value output path.
func TestRunAssemblePrompt_MissingRequiredFlagReturnsNonZero(t *testing.T) {
	dir := t.TempDir()
	var stdout bytes.Buffer
	rc := runAssemblePrompt([]string{
		"--registry", registryPathForTest,
		"--prompt-output", filepath.Join(dir, "prompt.txt"),
		"--agents-json-output", filepath.Join(dir, "agents.json"),
	}, &stdout)
	if rc == 0 {
		t.Fatal("runAssemblePrompt exit = 0, want non-zero for a missing -handoff-output")
	}
}

// TestIsAssemblePromptInvocation verifies the assemble-prompt subcommand's
// dispatch guard: a bare "assemble-prompt" first arg selects it, while every
// other invocation shape falls through to the default Driver-invocation path
// (or, for the other subcommands, to those).
func TestIsAssemblePromptInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"assemble-prompt first arg", []string{"assemble-prompt", "--registry", "x"}, true},
		{"no args", nil, false},
		{"ordinary flag invocation", []string{"--driver", "claude"}, false},
		{"bundle-out", []string{"bundle-out"}, false},
		{"outcome-backstop", []string{"outcome-backstop"}, false},
	}
	for _, c := range cases {
		if got := isAssemblePromptInvocation(c.args); got != c.want {
			t.Errorf("%s: isAssemblePromptInvocation(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}
