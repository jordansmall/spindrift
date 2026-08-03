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

// TestBuildDriverArgsOpencodeShapeIsRunLeadingPromptTrailing verifies
// driverInput{driver: "opencode"} builds opencode's own argv shape (issue
// #262 slice 4): the `run` subcommand from driverFlags leads, followed by
// -m <model> when model is non-empty, then any session-file content, with
// the prompt spliced in as one positional argument last -- never -p, never
// --agents.
func TestBuildDriverArgsOpencodeShapeIsRunLeadingPromptTrailing(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("implement the thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		driver:      "opencode",
		promptFile:  promptFile,
		model:       "opencode/gpt-5",
		driverFlags: "run --format json --auto",
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	want := []string{"run", "--format", "json", "--auto", "-m", "opencode/gpt-5", "implement the thing"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDriverArgs = %q, want %q", got, want)
	}
}

// TestBuildDriverArgsOpencodeEmptyModelOmitsDashM verifies an empty model
// omits -m entirely for opencode -- unlike claude's --model, which is always
// present even empty.
func TestBuildDriverArgsOpencodeEmptyModelOmitsDashM(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		driver:      "opencode",
		promptFile:  promptFile,
		model:       "",
		driverFlags: "run --format json --auto",
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	want := []string{"run", "--format", "json", "--auto", "do it"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDriverArgs = %q, want %q", got, want)
	}
}

// TestBuildDriverArgsClaudeEffort verifies a non-empty effort is spliced in
// as a trailing --effort <value> pair, after model/agents/session/
// driverFlags, for the claude shape.
func TestBuildDriverArgsClaudeEffort(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		promptFile: promptFile,
		model:      "claude-opus-4-8",
		effort:     "high",
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	want := []string{"-p", "do it", "--model", "claude-opus-4-8", "--effort", "high"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDriverArgs = %q, want %q", got, want)
	}
}

// TestBuildDriverArgsClaudeEffortOmittedWhenEmpty verifies an empty effort
// omits --effort entirely for the claude shape -- unlike --model, which is
// always present even empty.
func TestBuildDriverArgsClaudeEffortOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		promptFile: promptFile,
		model:      "claude-opus-4-8",
		effort:     "",
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	for _, a := range got {
		if a == "--effort" {
			t.Errorf("buildDriverArgs = %q, want no --effort flag", got)
		}
	}
	want := []string{"-p", "do it", "--model", "claude-opus-4-8"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDriverArgs = %q, want %q", got, want)
	}
}

// TestBuildDriverArgsOpencodeEffort verifies a non-empty effort is spliced
// in as --variant <value>, in the same position as -m <model>, before the
// trailing prompt positional argument, for the opencode shape.
func TestBuildDriverArgsOpencodeEffort(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		driver:      "opencode",
		promptFile:  promptFile,
		model:       "opencode/gpt-5",
		effort:      "high",
		driverFlags: "run --format json --auto",
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	want := []string{"run", "--format", "json", "--auto", "-m", "opencode/gpt-5", "--variant", "high", "do it"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDriverArgs = %q, want %q", got, want)
	}
}

// TestBuildDriverArgsOpencodeEffortOmittedWhenEmpty verifies an empty effort
// omits --variant entirely for the opencode shape.
func TestBuildDriverArgsOpencodeEffortOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		driver:      "opencode",
		promptFile:  promptFile,
		model:       "opencode/gpt-5",
		effort:      "",
		driverFlags: "run --format json --auto",
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	for _, a := range got {
		if a == "--variant" {
			t.Errorf("buildDriverArgs = %q, want no --variant flag", got)
		}
	}
	want := []string{"run", "--format", "json", "--auto", "-m", "opencode/gpt-5", "do it"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDriverArgs = %q, want %q", got, want)
	}
}

// TestBuildDriverArgsClaudeEffortWithSessionAndFlags verifies the claude
// shape's full relative ordering from the buildDriverArgs doc comment when
// session, driverFlags, and effort are all set together: session-file
// content word-split first, then driverFlags word-split, then --effort
// <value> last.
func TestBuildDriverArgsClaudeEffortWithSessionAndFlags(t *testing.T) {
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
		effort:      "high",
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	want := []string{
		"-p", "do it", "--model", "claude-opus-4-8",
		"--session-id", "abc-123",
		"--verbose", "--dangerously-skip-permissions",
		"--effort", "high",
	}
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
