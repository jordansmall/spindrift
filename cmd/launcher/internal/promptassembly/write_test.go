package promptassembly

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestOutputs(t *testing.T) OutputPaths {
	t.Helper()
	dir := t.TempDir()
	return OutputPaths{
		Prompt:       filepath.Join(dir, "prompt.md"),
		AgentsJSON:   filepath.Join(dir, "agents.json"),
		Handoff:      filepath.Join(dir, "handoff.json"),
		ReviewPrompt: filepath.Join(dir, "review-prompt.md"),
		Fragments:    filepath.Join(dir, "fragments.txt"),
	}
}

func testPassthrough() Passthrough {
	return Passthrough{
		Model:        "opus",
		Effort:       "high",
		Driver:       "claude",
		DriverBin:    "/bin/claude",
		DriverFlags:  "--verbose",
		Devshell:     true,
		DevshellName: "ci",
		HeartbeatLog: "/tmp/hb.log",
		ArgvShape:    ArgvShape{PromptStyle: "flag", ModelFlag: "--model", Order: []string{"prompt", "model"}},
		Caps:         Caps{MaxSlices: 9, MaxReviewRounds: 3, MaxBudgetTokens: 5, MaxBudgetUSD: 1.5},
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestWriteAssemblyWritesFilesAndLayersHandoff(t *testing.T) {
	env := coveredEnv()
	env.AgentsJSONTemplate = `{"scout":{"model":"x"}}`
	env.AgentsPromptFiles = `{"scout":"fragments/tdd-baked.md"}`
	out := writeTestOutputs(t)
	var warn strings.Builder

	result, err := WriteAssembly(env, loadTestRegistry(t), testValidateMarkerRows(), testPassthrough(), out, &warn)
	if err != nil {
		t.Fatalf("WriteAssembly: %v", err)
	}

	if got := mustRead(t, out.Prompt); got != result.Prompt {
		t.Errorf("prompt file != result.Prompt")
	}
	if got := mustRead(t, out.AgentsJSON); got != result.AgentsJSON || got == "" {
		t.Errorf("agents file = %q, want non-empty result.AgentsJSON", got)
	}
	if got := mustRead(t, out.ReviewPrompt); got != result.ReviewPromptText || got == "" {
		t.Errorf("review prompt file = %q, want non-empty ReviewPromptText", got)
	}
	if got := mustRead(t, out.Fragments); got != strings.Join(result.Fragments, "\n")+"\n" {
		t.Errorf("fragments file = %q", got)
	}

	wantJSON, err := json.Marshal(result.Handoff)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, out.Handoff); got != string(wantJSON) {
		t.Errorf("handoff file = %s, want %s", got, wantJSON)
	}

	h, err := LoadHandoffFile(out.Handoff)
	if err != nil {
		t.Fatal(err)
	}
	p := testPassthrough()
	if h.PromptFile != out.Prompt || h.AgentsFile != out.AgentsJSON || h.ReviewPromptFile != out.ReviewPrompt {
		t.Errorf("handoff paths = %q %q %q", h.PromptFile, h.AgentsFile, h.ReviewPromptFile)
	}
	if h.Model != p.Model || h.Effort != p.Effort || h.Driver != p.Driver || h.DriverBin != p.DriverBin ||
		h.DriverFlags != p.DriverFlags || !h.Devshell || h.DevshellName != p.DevshellName || h.HeartbeatLog != p.HeartbeatLog {
		t.Errorf("handoff passthrough not layered: %+v", h)
	}
	if h.Issue != env.IssueNumber {
		t.Errorf("Handoff.Issue = %q, want %q", h.Issue, env.IssueNumber)
	}
	if h.Caps != p.Caps || h.ArgvShape.PromptStyle != "flag" || strings.Join(h.ArgvShape.Order, " ") != "prompt model" {
		t.Errorf("handoff caps/argv = %+v %+v", h.Caps, h.ArgvShape)
	}
	if result.Handoff.PromptFile != out.Prompt {
		t.Errorf("returned Handoff not layered: %+v", result.Handoff)
	}
}

func TestWriteAssemblyNoAgentsJSONLeavesAgentsFileEmpty(t *testing.T) {
	out := writeTestOutputs(t)

	result, err := WriteAssembly(coveredEnv(), loadTestRegistry(t), testValidateMarkerRows(), testPassthrough(), out, &strings.Builder{})
	if err != nil {
		t.Fatalf("WriteAssembly: %v", err)
	}
	if result.Handoff.AgentsFile != "" {
		t.Errorf("Handoff.AgentsFile = %q, want empty", result.Handoff.AgentsFile)
	}
	if got := mustRead(t, out.AgentsJSON); got != "" {
		t.Errorf("agents file = %q, want empty file", got)
	}
}

func TestWriteAssemblyReviewPromptOnlyWhenRendered(t *testing.T) {
	t.Run("no path given", func(t *testing.T) {
		out := writeTestOutputs(t)
		out.ReviewPrompt = ""
		result, err := WriteAssembly(coveredEnv(), loadTestRegistry(t), testValidateMarkerRows(), testPassthrough(), out, &strings.Builder{})
		if err != nil {
			t.Fatalf("WriteAssembly: %v", err)
		}
		if result.ReviewPromptText == "" {
			t.Fatal("fixture no longer renders a review prompt")
		}
		if result.Handoff.ReviewPromptFile != "" {
			t.Errorf("Handoff.ReviewPromptFile = %q, want empty", result.Handoff.ReviewPromptFile)
		}
	})

	t.Run("not rendered", func(t *testing.T) {
		out := writeTestOutputs(t)
		env := coveredEnv()
		env.DispatchKind = "research"
		env.BoxWriteEnabled = true
		result, err := WriteAssembly(env, loadTestRegistry(t), nil, testPassthrough(), out, &strings.Builder{})
		if err != nil {
			t.Fatalf("WriteAssembly: %v", err)
		}
		if result.ReviewPromptText != "" {
			t.Fatal("fixture unexpectedly renders a review prompt")
		}
		if _, err := os.Stat(out.ReviewPrompt); !os.IsNotExist(err) {
			t.Errorf("review prompt file exists (stat err = %v), want absent", err)
		}
		if result.Handoff.ReviewPromptFile != "" {
			t.Errorf("Handoff.ReviewPromptFile = %q, want empty", result.Handoff.ReviewPromptFile)
		}
	})
}

func TestWriteAssemblyValidateRejectWritesNoHandoff(t *testing.T) {
	env := coveredEnv()
	env.DispatchKind = "research"
	env.BoxWriteEnabled = false
	out := writeTestOutputs(t)
	rows := []ValidateMarkerRow{{
		ID: "always-missing", Marker: "NO-SUCH-MARKER-ANYWHERE", Severity: "reject",
		When: "readOnlyResearch", Message: "reject-message",
	}}

	_, err := WriteAssembly(env, loadTestRegistry(t), rows, testPassthrough(), out, &strings.Builder{})
	var ve *ValidateError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidateError", err)
	}
	if err.Error() != "reject-message" {
		t.Errorf("err = %q, want bare marker message", err.Error())
	}
	if _, statErr := os.Stat(out.Handoff); !os.IsNotExist(statErr) {
		t.Errorf("handoff file exists (stat err = %v), want absent", statErr)
	}
	if _, statErr := os.Stat(out.Prompt); !os.IsNotExist(statErr) {
		t.Errorf("prompt file exists (stat err = %v), want absent", statErr)
	}
}

func TestWriteAssemblyWarningsGoToWriter(t *testing.T) {
	env := coveredEnv()
	env.DispatchKind = "research"
	env.BoxWriteEnabled = false
	rows := []ValidateMarkerRow{{
		ID: "warn-row", Marker: "NO-SUCH-MARKER-ANYWHERE", Severity: "warn",
		When: "readOnlyResearch", Message: "warn-message",
	}}
	var warn strings.Builder

	if _, err := WriteAssembly(env, loadTestRegistry(t), rows, testPassthrough(), writeTestOutputs(t), &warn); err != nil {
		t.Fatalf("WriteAssembly: %v", err)
	}
	if warn.String() != "warn-message\n" {
		t.Errorf("warnings = %q, want %q", warn.String(), "warn-message\n")
	}
}

func TestWriteAssemblyWriteErrorIsWrappedNotValidate(t *testing.T) {
	out := writeTestOutputs(t)
	out.Prompt = filepath.Join(t.TempDir(), "missing-dir", "prompt.md")

	_, err := WriteAssembly(coveredEnv(), loadTestRegistry(t), nil, testPassthrough(), out, &strings.Builder{})
	if err == nil || !strings.HasPrefix(err.Error(), "write prompt output: ") {
		t.Fatalf("err = %v, want 'write prompt output:' prefix", err)
	}
	var ve *ValidateError
	if errors.As(err, &ve) {
		t.Error("write error classified as ValidateError")
	}
}

func TestScanSkillsFound(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("tdd/SKILL.md")
	mk("caveman/SKILL.md")
	mk("Zeta/SKILL.md")
	mk("flat.md")
	mk(".hidden/SKILL.md")
	mk("nofile/README.md")
	if err := os.MkdirAll(filepath.Join(dir, "skilldir", "SKILL.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got, want := ScanSkillsFound(dir), "Zeta, caveman, tdd"; got != want {
		t.Errorf("ScanSkillsFound = %q, want %q", got, want)
	}
	if got := ScanSkillsFound(filepath.Join(dir, "absent")); got != "" {
		t.Errorf("ScanSkillsFound(missing) = %q, want empty", got)
	}
	if got := ScanSkillsFound(t.TempDir()); got != "" {
		t.Errorf("ScanSkillsFound(empty) = %q, want empty", got)
	}
}
