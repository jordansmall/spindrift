package promptassembly

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stagePromptsCopy copies the real prompts dir (fragments included) into a
// temp dir so a test can append a removed token to one file.
func stagePromptsCopy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(promptsDir)); err != nil {
		t.Fatalf("CopyFS: %v", err)
	}
	return dir
}

func appendToFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
}

// assembleValidate runs Assemble then Validate with no marker rows, so the
// only possible Validate error is the removed-var reject.
func assembleValidate(t *testing.T, env Env) error {
	t.Helper()
	result, err := Assemble(env, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	_, err = Validate(env, result, nil)
	return err
}

func requireErrContains(t *testing.T, err error, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestAssembleValidateRejectsRemovedFragmentVar(t *testing.T) {
	// A fragment that reaches the default work prompt, found from a clean run.
	clean, err := Assemble(coveredEnv(), loadTestRegistry(t))
	if err != nil || len(clean.Fragments) == 0 {
		t.Fatalf("clean Assemble: fragments=%v err=%v", clean.Fragments, err)
	}
	fragment := filepath.Join("fragments", clean.Fragments[0])

	cases := []struct {
		name   string
		file   string
		agents bool
		output string
		token  string
		issue  string
	}{
		{"base template", "issue-prompt.md", false, "prompt", "${REVIEW_LOOP_INLINE_STEP}", "#4291"},
		{"worker agent prompt", "worker-prompt.md", true, `agent "worker" prompt`, "${CAVEMAN_STEP_WORKER}", "#4562"},
		{"review prompt", "review-prompt.md", false, "review prompt", "${TDD_STEP}", "#3219"},
		{"fragment", fragment, false, "prompt", "${COMMIT_STEP}", "#3222"},
		{"agent prompt", "scout-prompt.md", true, `agent "scout" prompt`, "${CODE_COMMENTS_STEP}", "#3505"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.PromptsDir = stagePromptsCopy(t)
			if tc.agents {
				agent := strings.TrimSuffix(tc.file, "-prompt.md")
				env.AgentsJSONTemplate = `{"` + agent + `":{"model":"x"}}`
				env.AgentsPromptFiles = `{"` + agent + `":"` + tc.file + `"}`
			}
			appendToFile(t, filepath.Join(env.PromptsDir, tc.file), "\nstale "+tc.token+"\n")

			err := assembleValidate(t, env)
			if err == nil {
				t.Fatal("Validate() error = nil, want removed-var reject")
			}
			requireErrContains(t, err, tc.output, tc.token, tc.issue, "MIGRATING.md")
		})
	}
}

// The on-disk agent-file path (opencode) renders prompts separately from
// renderAgentsJSON and must reject a removed token too.
func TestAssembleValidateRejectsRemovedFragmentVarInAgentFile(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")
	env := coveredEnv()
	env.PromptsDir = stagePromptsCopy(t)
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`
	appendToFile(t, filepath.Join(env.PromptsDir, "scout-prompt.md"), "\nstale ${CODE_COMMENTS_STEP}\n")

	err := assembleValidate(t, env)
	if err == nil {
		t.Fatal("Validate() error = nil, want removed-var reject")
	}
	requireErrContains(t, err, `agent "scout" prompt`, "${CODE_COMMENTS_STEP}", "#3505", "MIGRATING.md")
}

// The chore prompt is a Consumer-overridable file embedded through the
// CHORE_PROMPT var, so it is operator-authored despite arriving as a var.
func TestAssembleValidateRejectsRemovedFragmentVarInChoreFile(t *testing.T) {
	env := butlerEnv()
	env.PromptsDir = stagePromptsCopy(t)
	appendToFile(t, filepath.Join(env.PromptsDir, "chores", env.ChoreName+".md"), "\nstale ${TDD_STEP}\n")

	err := assembleValidate(t, env)
	if err == nil {
		t.Fatal("Validate() error = nil, want removed-var reject")
	}
	requireErrContains(t, err, "${TDD_STEP}", "#3219")
}

func TestWriteAssemblyRemovedVarInOverrideIsValidateError(t *testing.T) {
	env := coveredEnv()
	env.PromptsDir = stagePromptsCopy(t)
	appendToFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "\n${TDD_STEP}\n")
	out := writeTestOutputs(t)

	_, err := WriteAssembly(env, loadTestRegistry(t), nil, testPassthrough(), out, &strings.Builder{})
	var ve *ValidateError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidateError", err)
	}
	if !strings.Contains(err.Error(), "${TDD_STEP}") {
		t.Errorf("err = %q, want it to name ${TDD_STEP}", err)
	}
	if _, statErr := os.Stat(out.Prompt); !os.IsNotExist(statErr) {
		t.Errorf("prompt file exists (stat err = %v), want absent", statErr)
	}
}

// Untrusted issue and CI text that merely quotes a removed token must not
// wedge the dispatch.
func TestAssembleValidateAllowsRemovedVarInUntrustedText(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Env)
	}{
		{"issue text", func(e *Env) { e.IssueText = "try ${TDD_STEP} here" }},
		{"issue title", func(e *Env) { e.IssueTitle = "drop ${REVIEW_LOOP_INLINE_STEP}" }},
		{"ci failure summary", func(e *Env) {
			e.FixPass = 1
			e.CIFailureSummary = "log: ${COMMIT_STEP} unresolved"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			tc.set(&env)
			if err := assembleValidate(t, env); err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestAssembleValidateAllowsOtherPassThroughTokens(t *testing.T) {
	env := coveredEnv()
	env.PromptsDir = stagePromptsCopy(t)
	appendToFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"),
		"\ntoken ${FORGEJO_TOKEN} home ${HOME} partial ${TDD_STEP_X} $TDD_STEP\n")
	if err := assembleValidate(t, env); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}
