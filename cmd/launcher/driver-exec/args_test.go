package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/promptassembly"
)

// claudeShape is the claude Driver's argv shape from issue #262 slice 4.
// --model is always present, even empty; --agents and --effort drop out
// when empty.
var claudeShape = promptassembly.ArgvShape{
	PromptStyle:    "flag",
	PromptFlag:     "-p",
	ModelFlag:      "--model",
	ModelOmitEmpty: false,
	AgentsFlag:     "--agents",
	EffortFlag:     "--effort",
	Order:          []string{"prompt", "model", "agents", "session", "driverFlags", "effort"},
}

// opencodeShape is the opencode Driver's argv shape from issue #262 slice 4.
// driverFlags leads because opencode's own `run` subcommand arrives that way,
// and the prompt is one trailing positional argument.
var opencodeShape = promptassembly.ArgvShape{
	PromptStyle:    "positional",
	ModelFlag:      "-m",
	ModelOmitEmpty: true,
	AgentsFlag:     "",
	EffortFlag:     "--variant",
	Order:          []string{"driverFlags", "model", "effort", "session", "prompt"},
}

func TestBuildDriverArgsMinimal(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("implement the thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		shape:      claudeShape,
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
		shape:      claudeShape,
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

// An empty agents file must drop --agents, as the shell pipeline
// buildDriverArgs replaced did.
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
		shape:      claudeShape,
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

// Opencode never takes -p and never takes --agents (issue #262 slice 4).
func TestBuildDriverArgsOpencodeShapeIsRunLeadingPromptTrailing(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("implement the thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		shape:       opencodeShape,
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

// Opencode drops -m on an empty model, unlike claude's --model, which stays
// in argv even when empty.
func TestBuildDriverArgsOpencodeEmptyModelOmitsDashM(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		shape:       opencodeShape,
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

func TestBuildDriverArgsClaudeEffort(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		shape:      claudeShape,
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

// An empty effort drops --effort, unlike --model, which stays in argv even
// when empty.
func TestBuildDriverArgsClaudeEffortOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		shape:      claudeShape,
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

func TestBuildDriverArgsOpencodeEffort(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		shape:       opencodeShape,
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

func TestBuildDriverArgsOpencodeEffortOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		shape:       opencodeShape,
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

// Session, driverFlags, and effort are set together here to pin their
// relative ordering, which no single-slot test can catch.
func TestBuildDriverArgsClaudeEffortWithSessionAndFlags(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(sessionFile, []byte(`["--session-id","abc-123"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		shape:       claudeShape,
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

// driverFlags holds a space-separated string that must become separate argv
// elements, matching the shell's prior `read -ra`.
func TestBuildDriverArgsFlagsAreWordSplit(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(sessionFile, []byte(`["--session-id","abc-123"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	in := driverInput{
		shape:       claudeShape,
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

// The session file is a JSON array of argv words, so a value with spaces stays
// one element; driverFlags is still split on whitespace.
func TestBuildDriverArgsSessionWordsStayWhole(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(sessionFile, []byte(`["--title","spindrift a b"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := buildDriverArgs(driverInput{shape: claudeShape, promptFile: promptFile, model: "m", sessionFile: sessionFile})
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	if !slices.Contains(got, "spindrift a b") || slices.Index(got, "--title")+1 != slices.Index(got, "spindrift a b") {
		t.Errorf("buildDriverArgs = %q, want --title and \"spindrift a b\" as two adjacent elements", got)
	}
}

func TestBuildDriverArgsSessionFileEmptyOrMalformed(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		content string
		bad     bool
	}{{"", false}, {"--session-id abc", true}} {
		sessionFile := filepath.Join(dir, "session.txt")
		if err := os.WriteFile(sessionFile, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := buildDriverArgs(driverInput{shape: claudeShape, promptFile: promptFile, model: "m", sessionFile: sessionFile})
		if (err != nil) != c.bad {
			t.Errorf("session %q: err = %v, want error %v", c.content, err, c.bad)
		}
		if !c.bad && !slices.Equal(got, []string{"-p", "do it", "--model", "m"}) {
			t.Errorf("empty session file: args = %q, want no session words", got)
		}
	}
}

// Without this check an unrecognised slot name in shape.order, for example
// from a mis-set DRIVER_ARGV_ORDER, drops out of argv silently (issue #2534
// follow-up).
func TestBuildDriverArgsUnknownSlotErrors(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	shape := claudeShape
	shape.Order = []string{"prompt", "bogusSlot"}
	in := driverInput{
		shape:      shape,
		promptFile: promptFile,
		model:      "claude-opus-4-8",
	}
	if _, err := buildDriverArgs(in); err == nil {
		t.Fatal("buildDriverArgs error = nil, want a non-nil error naming the unrecognised slot \"bogusSlot\"")
	}
}

// Without this check a typo'd promptStyle falls through to the positional
// branch and builds the wrong argv shape (issue #2534 follow-up).
func TestBuildDriverArgsInvalidPromptStyleErrors(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	shape := claudeShape
	shape.PromptStyle = "flagg"
	in := driverInput{
		shape:      shape,
		promptFile: promptFile,
		model:      "claude-opus-4-8",
	}
	if _, err := buildDriverArgs(in); err == nil {
		t.Fatal("buildDriverArgs error = nil, want a non-nil error naming the invalid promptStyle \"flagg\"")
	}
}

// synthShape shares no order or flag value with claudeShape or opencodeShape,
// so a flag name hardcoded in args.go cannot pass.
func TestBuildDriverArgsSyntheticShapeIsDataDriven(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("synthesize it"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsFile := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(agentsFile, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(sessionFile, []byte(`["--resume","xyz"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	synthShape := promptassembly.ArgvShape{
		PromptStyle:    "flag",
		PromptFlag:     "--input",
		ModelFlag:      "--llm",
		ModelOmitEmpty: true,
		AgentsFlag:     "--personas",
		EffortFlag:     "--thinking",
		Order:          []string{"agents", "model", "driverFlags", "session", "effort", "prompt"},
	}
	in := driverInput{
		shape:       synthShape,
		promptFile:  promptFile,
		model:       "synth-model",
		effort:      "max",
		agentsFile:  agentsFile,
		sessionFile: sessionFile,
		driverFlags: "--extra flag",
	}
	got, err := buildDriverArgs(in)
	if err != nil {
		t.Fatalf("buildDriverArgs: %v", err)
	}
	want := []string{
		"--personas", `{"a":1}`,
		"--llm", "synth-model",
		"--extra", "flag",
		"--resume", "xyz",
		"--thinking", "max",
		"--input", "synthesize it",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDriverArgs = %q, want %q", got, want)
	}
}

// piShape mirrors the argv shape lib/drivers/pi.nix declares; unlike claude's
// and opencode's it is not otherwise pinned in this package.
var piShape = promptassembly.ArgvShape{
	PromptStyle:    "positional",
	ModelFlag:      "--model",
	ModelOmitEmpty: true,
	EffortFlag:     "--thinking",
	Order:          []string{"driverFlags", "model", "effort", "session", "prompt"},
}

// A Driver's structured session answer crosses the Box's gate and its JSON
// session file into the agent argv at the session slot, one word per element.
func TestSessionAnswerReachesAgentArgv(t *testing.T) {
	uuid := driverkit.SessionUUID("o/r", "7")
	title := "spindrift-" + driverkit.SessionKey("o/r", "7")
	for _, c := range []struct {
		name, driver string
		shape        promptassembly.ArgvShape
		answer       driverkit.SessionFlag
		want         []string
	}{
		{"claude initial", "claude", claudeShape, driverkit.SessionFlag{Flag: "--session-id", Value: uuid},
			[]string{"-p", "do it", "--model", "m", "--session-id", uuid}},
		{"claude resume", "claude", claudeShape, driverkit.SessionFlag{},
			[]string{"-p", "do it", "--model", "m"}},
		{"opencode initial", "opencode", opencodeShape, driverkit.SessionFlag{Flag: "--title", Value: title},
			[]string{"-m", "m", "--title", title, "do it"}},
		{"opencode resume", "opencode", opencodeShape, driverkit.SessionFlag{Flag: "--session", Value: "ses_x"},
			[]string{"-m", "m", "--session", "ses_x", "do it"}},
		{"opencode value with spaces", "opencode", opencodeShape, driverkit.SessionFlag{Flag: "--title", Value: "spindrift-o/r 7 two words"},
			[]string{"-m", "m", "--title", "spindrift-o/r 7 two words", "do it"}},
		{"pi initial", "pi", piShape, driverkit.SessionFlag{Flag: "--session-id", Value: uuid},
			[]string{"--model", "m", "--session-id", uuid, "do it"}},
		{"pi resume", "pi", piShape, driverkit.SessionFlag{Flag: "--session-id", Value: uuid},
			[]string{"--model", "m", "--session-id", uuid, "do it"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			argv, err := driver.SessionArgs(c.driver, c.answer)
			if err != nil {
				t.Fatalf("SessionArgs: %v", err)
			}
			session := ""
			if len(argv) > 0 {
				b, err := json.Marshal(argv)
				if err != nil {
					t.Fatal(err)
				}
				session = string(b)
			}
			dir := t.TempDir()
			promptFile := filepath.Join(dir, "prompt.txt")
			sessionFile := filepath.Join(dir, "session.json")
			for path, body := range map[string]string{promptFile: "do it", sessionFile: session} {
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := buildDriverArgs(driverInput{shape: c.shape, promptFile: promptFile, model: "m", sessionFile: sessionFile})
			if err != nil {
				t.Fatalf("buildDriverArgs: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("argv = %q, want %q", got, c.want)
			}
		})
	}
}
