package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestBuildDriverArgsMinimal verifies the prompt file's content is spliced in
// as -p's value and --model is always present, even with no agents/session
// file, matching the Driver invocation's pre-driver-exec shape.
func TestBuildDriverArgsMinimal(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("implement the thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		promptFile: promptFile,
		model:      "claude-opus-4-8",
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	want := []string{"-p", "implement the thing", "--model", "claude-opus-4-8"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDriverArgs = %q, want %q", got, want)
	}
}

// TestBuildDriverArgsWithAgents verifies a non-empty agents file's content is
// spliced in as --agents' value.
func TestBuildDriverArgsWithAgents(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsFile := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(agentsFile, []byte(`{"scout":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		promptFile: promptFile,
		model:      "claude-opus-4-8",
		agentsFile: agentsFile,
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	want := []string{"-p", "do it", "--model", "claude-opus-4-8", "--agents", `{"scout":{}}`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDriverArgs = %q, want %q", got, want)
	}
}

// TestBuildDriverArgsEmptyAgentsFileOmitsFlag verifies an empty (or unset)
// agents file omits --agents entirely, matching the pre-driver-exec pipeline
// which only set agents_args when agents_json was non-empty.
func TestBuildDriverArgsEmptyAgentsFileOmitsFlag(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsFile := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(agentsFile, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		promptFile: promptFile,
		model:      "claude-opus-4-8",
		agentsFile: agentsFile,
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	want := []string{"-p", "do it", "--model", "claude-opus-4-8"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDriverArgs = %q, want %q", got, want)
	}
}

// TestBuildDriverArgsSessionAndFlagsAreWordSplit verifies the session file's
// content is word-split into separate argv elements (matching the shell's
// prior `read -ra` behaviour) and driverFlags (a space-separated common-flags
// string) is spliced in the same way, appended after the session args.
func TestBuildDriverArgsSessionAndFlagsAreWordSplit(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(sessionFile, []byte("--session-id abc-123"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		promptFile:  promptFile,
		model:       "claude-opus-4-8",
		sessionFile: sessionFile,
		driverFlags: "--verbose --dangerously-skip-permissions",
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	want := []string{
		"-p", "do it", "--model", "claude-opus-4-8",
		"--session-id", "abc-123",
		"--verbose", "--dangerously-skip-permissions",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDriverArgs = %q, want %q", got, want)
	}
}
