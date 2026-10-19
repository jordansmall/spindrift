package promptassembly

import (
	"errors"
	"fmt"
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

// replaceInFile rewrites path with every old replaced by new, failing the test
// if the file does not contain old.
func replaceInFile(t *testing.T, path, old, new string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s does not contain %s", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), old, new)), 0o644); err != nil {
		t.Fatal(err)
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

// assembleValidateWarnings is assembleValidate for a run expected to pass,
// returning the Validate warnings.
func assembleValidateWarnings(t *testing.T, env Env) []string {
	t.Helper()
	result, err := Assemble(env, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	warnings, err := Validate(env, result, nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return warnings
}

// A deprecated variable is not removed yet, so it never errors; it warns once,
// naming the replacement, however many times or files reference it.
func TestAssembleValidateWarnsOnceOnDeprecatedFragmentVar(t *testing.T) {
	env := coveredEnv()
	env.PromptsDir = stagePromptsCopy(t)
	appendToFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "\n${ISSUE_READ_GITHUB_STEP}\n${ISSUE_READ_GITHUB_STEP}\n")
	appendToFile(t, filepath.Join(env.PromptsDir, "review-prompt.md"), "\n${ISSUE_READ_GITHUB_STEP}\n")

	warnings := assembleValidateWarnings(t, env)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly one", warnings)
	}
	for _, want := range []string{"${ISSUE_READ_GITHUB_STEP}", "${ISSUE_READ_STEP}", "#5646", "MIGRATING.md"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning %q missing %q", warnings[0], want)
		}
	}
}

func TestAssembleValidateDeprecatedAndRemovedVarStillErrors(t *testing.T) {
	env := coveredEnv()
	env.PromptsDir = stagePromptsCopy(t)
	appendToFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "\n${ISSUE_READ_GITHUB_STEP}\n${TDD_STEP}\n")

	err := assembleValidate(t, env)
	if err == nil {
		t.Fatal("Validate() error = nil, want removed-var reject")
	}
	requireErrContains(t, err, "${TDD_STEP}")
}

func TestAssembleNoDeprecationWarningFromShippedPrompts(t *testing.T) {
	if warnings := assembleValidateWarnings(t, coveredEnv()); len(warnings) != 0 {
		t.Errorf("warnings = %q, want none for the shipped prompts", warnings)
	}
}

// An override still naming a per-backend issue-read var renders exactly what
// the shipped ${ISSUE_READ_STEP} prompt does for that backend.
func TestAssembleDeprecatedIssueReadVarRendersAsBefore(t *testing.T) {
	forgejo := coveredEnv()
	forgejo.IssueTracker = "forgejo"
	forgejo.TrackerAxisRead = "FORGEJO"
	forgejo.TrackerAxisWrite = "FORGEJO"
	forgejo.TrackerAxisFiler = "FORGEJO"

	cases := []struct {
		name string
		env  Env
		v    string
	}{
		{"github", coveredEnv(), "${ISSUE_READ_GITHUB_STEP}"},
		{"forgejo", forgejo, "${ISSUE_READ_FORGEJO_STEP}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := Assemble(tc.env, loadTestRegistry(t))
			if err != nil {
				t.Fatalf("Assemble default: %v", err)
			}
			env := tc.env
			env.PromptsDir = stagePromptsCopy(t)
			replaceInFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "${ISSUE_READ_STEP}", tc.v)
			got, err := Assemble(env, loadTestRegistry(t))
			if err != nil {
				t.Fatalf("Assemble override: %v", err)
			}
			if got.Prompt != want.Prompt {
				t.Errorf("override prompt differs from the ${ISSUE_READ_STEP} prompt")
			}
		})
	}
}

// A per-backend var for a backend that is not active still expands empty.
func TestAssembleDeprecatedIssueReadVarOfInactiveBackendIsEmpty(t *testing.T) {
	env := coveredEnv()
	env.PromptsDir = stagePromptsCopy(t)
	replaceInFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "${ISSUE_READ_STEP}", "${ISSUE_READ_FORGEJO_STEP}")
	got, err := Assemble(env, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if strings.Contains(got.Prompt, "via Forgejo") || strings.Contains(got.Prompt, "${ISSUE_READ") {
		t.Errorf("prompt carries a forgejo issue-read or a literal token under the github tracker")
	}
}

func TestWriteAssemblyPrintsDeprecatedVarWarning(t *testing.T) {
	env := coveredEnv()
	env.PromptsDir = stagePromptsCopy(t)
	appendToFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "\n${ISSUE_READ_LOCAL_STEP}\n")
	w := new(strings.Builder)

	if _, err := WriteAssembly(env, loadTestRegistry(t), nil, testPassthrough(), writeTestOutputs(t), w); err != nil {
		t.Fatalf("WriteAssembly: %v", err)
	}
	if !strings.Contains(w.String(), "use ${ISSUE_READ_STEP} instead") {
		t.Errorf("output = %q, want the deprecation warning", w.String())
	}
}

// An override directory still on the 0.28 layout (root-level
// fragments/issue-read-<backend>.md, no per-backend folders) keeps rendering
// the step through the deprecated variable, with one warning.
func TestAssembleDeprecatedIssueReadVarFallsBackToLegacyFragment(t *testing.T) {
	cases := []struct {
		name    string
		tracker string
		axis    string
		v       string
		legacy  string
	}{
		{"github", "github", "GITHUB", "ISSUE_READ_GITHUB_STEP", "issue-read-github.md"},
		{"local", "local", "LOCAL", "ISSUE_READ_LOCAL_STEP", "issue-read-local.md"},
		{"forgejo", "forgejo", "FORGEJO", "ISSUE_READ_FORGEJO_STEP", "issue-read-forgejo.md"},
		{"jira reads via the github gate", "jira", "GITHUB", "ISSUE_READ_GITHUB_STEP", "issue-read-github.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.IssueTracker = tc.tracker
			env.TrackerAxisRead, env.TrackerAxisWrite, env.TrackerAxisFiler = tc.axis, tc.axis, tc.axis
			env.PromptsDir = stagePromptsCopy(t)
			for _, p := range []string{"issue-read.md", "local/issue-read.md"} {
				if err := os.Remove(filepath.Join(env.PromptsDir, "fragments", p)); err != nil {
					t.Fatal(err)
				}
			}
			const legacyText = "LEGACY-ISSUE-READ-STEP-SENTINEL"
			if err := os.WriteFile(filepath.Join(env.PromptsDir, "fragments", tc.legacy), []byte(legacyText+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			replaceInFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "${ISSUE_READ_STEP}", "${"+tc.v+"}")

			result, err := Assemble(env, loadTestRegistry(t))
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if !strings.Contains(result.Prompt, legacyText) {
				t.Errorf("prompt does not render the legacy fragment %s", tc.legacy)
			}
			warnings, err := Validate(env, result, nil)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], "${"+tc.v+"}") {
				t.Errorf("warnings = %q, want one naming ${%s}", warnings, tc.v)
			}
		})
	}
}

// An override still naming the per-backend vars the slot-collapse (issue
// #5647) deprecated renders exactly what the shipped slot prompt does, on
// every tracker.
func TestAssembleDeprecatedSlotVarsRenderAsBefore(t *testing.T) {
	envFor := func(tracker, axis string) Env {
		e := coveredEnv()
		e.IssueTracker = tracker
		e.TrackerAxisRead, e.TrackerAxisWrite, e.TrackerAxisFiler = axis, axis, axis
		e.FilerEnabled = true
		e.AgentsJSONTemplate = `{"scout":{"model":"x"},"filer":{"model":"x"}}`
		e.AgentsPromptFiles = `{"scout":"scout-prompt.md","filer":"filer-prompt.md"}`
		return e
	}
	slots := []struct {
		name, kind, file, slot, old string
	}{
		{"research", "research", "research-prompt.md", "${RESEARCH_ISSUE_READ_STEP}",
			"${RESEARCH_ISSUE_READ_GITHUB_STEP}${RESEARCH_ISSUE_READ_LOCAL_STEP}${RESEARCH_ISSUE_READ_FORGEJO_STEP}"},
		{"scout", "work", "scout-prompt.md", "${SCOUT_ISSUE_READ_STEP}",
			"${SCOUT_ISSUE_READ_GITHUB_STEP}${SCOUT_ISSUE_READ_FORGEJO_STEP}"},
		{"filer", "work", "filer-prompt.md", "${FILER_DEDUP_STEP}",
			"${FILER_DEDUP_SEARCH_STEP}${FILER_DEDUP_SEARCH_FORGEJO_STEP}${FILER_DEDUP_SEARCH_LOCAL_STEP}"},
	}
	trackers := []struct{ tracker, axis string }{{"github", "GITHUB"}, {"forgejo", "FORGEJO"}, {"local", "LOCAL"}, {"jira", "GITHUB"}}
	rendered := func(t *testing.T, env Env, kind string) string {
		t.Helper()
		env.DispatchKind = kind
		res, err := Assemble(env, loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		return res.Prompt + "\n" + res.AgentsJSON
	}
	for _, s := range slots {
		for _, tr := range trackers {
			t.Run(s.name+"/"+tr.tracker, func(t *testing.T) {
				env := envFor(tr.tracker, tr.axis)
				want := rendered(t, env, s.kind)
				env.PromptsDir = stagePromptsCopy(t)
				replaceInFile(t, filepath.Join(env.PromptsDir, s.file), s.slot, s.old)
				if got := rendered(t, env, s.kind); got != want {
					t.Errorf("override with the deprecated %s vars renders differently from %s", s.name, s.slot)
				}
			})
		}
	}
}

// The same guarantee for the verdict, landing and blocked-comment slots
// (issue #5647), which fork on write access and signal carrier as well as on
// the tracker: every combination must render as the per-backend vars did.
func TestAssembleDeprecatedAccessSlotVarsRenderAsBefore(t *testing.T) {
	slots := []struct {
		name, kind, file, slot, old string
	}{
		{"verdict", "research", "research-prompt.md",
			"${RESEARCH_VERDICT_STEP}${RESEARCH_VERDICT_RELAY_STEP}${RESEARCH_VERDICT_RELAY_SOCKET_STEP}",
			"${RESEARCH_VERDICT_GITHUB_STEP}${RESEARCH_VERDICT_GITHUB_READONLY_STEP}${RESEARCH_VERDICT_GITHUB_READONLY_SOCKET_STEP}" +
				"${RESEARCH_VERDICT_LOCAL_STEP}${RESEARCH_VERDICT_LOCAL_SOCKET_STEP}${RESEARCH_VERDICT_FORGEJO_STEP}" +
				"${RESEARCH_VERDICT_FORGEJO_READONLY_STEP}${RESEARCH_VERDICT_FORGEJO_READONLY_SOCKET_STEP}"},
		{"landing", "research", "research-prompt.md", "${RESEARCH_OUTCOME_LANDING_STEP}",
			"${RESEARCH_OUTCOME_LANDING_GITHUB_STEP}${RESEARCH_OUTCOME_LANDING_FORGEJO_STEP}"},
		{"blocked comment", "work", "issue-prompt.md",
			"${ISSUE_BLOCKED_COMMENT_STEP}${ISSUE_BLOCKED_COMMENT_RELAY_STEP}",
			"${ISSUE_BLOCKED_COMMENT_GITHUB_STEP}${ISSUE_BLOCKED_COMMENT_GITHUB_READONLY_STEP}${ISSUE_BLOCKED_COMMENT_LOCAL_STEP}" +
				"${ISSUE_BLOCKED_COMMENT_FORGEJO_STEP}${ISSUE_BLOCKED_COMMENT_FORGEJO_READONLY_STEP}"},
	}
	trackers := []struct{ tracker, axis string }{{"github", "GITHUB"}, {"forgejo", "FORGEJO"}, {"local", "LOCAL"}, {"jira", "GITHUB"}}
	for _, s := range slots {
		for _, tr := range trackers {
			for _, boxWrite := range []bool{true, false} {
				for _, carrier := range []string{"log", "socket"} {
					name := fmt.Sprintf("%s/%s/write=%v/%s", s.name, tr.tracker, boxWrite, carrier)
					t.Run(name, func(t *testing.T) {
						env := coveredEnv()
						env.DispatchKind = s.kind
						env.IssueTracker = tr.tracker
						env.TrackerAxisRead, env.TrackerAxisWrite, env.TrackerAxisFiler = tr.axis, tr.axis, tr.axis
						if tr.tracker == "local" {
							env.TrackerAxisWrite = ""
						}
						env.BoxWriteEnabled = boxWrite
						env.SignalCarrier = carrier
						res, err := Assemble(env, loadTestRegistry(t))
						if err != nil {
							t.Fatalf("Assemble: %v", err)
						}
						env.PromptsDir = stagePromptsCopy(t)
						replaceInFile(t, filepath.Join(env.PromptsDir, s.file), s.slot, s.old)
						got, err := Assemble(env, loadTestRegistry(t))
						if err != nil {
							t.Fatalf("Assemble (override): %v", err)
						}
						if got.Prompt != res.Prompt {
							t.Errorf("override with the deprecated %s vars renders differently from the slot", s.name)
						}
					})
				}
			}
		}
	}
}
