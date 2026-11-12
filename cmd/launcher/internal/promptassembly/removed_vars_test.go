package promptassembly

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// issueReadFamily mirrors the issue-read row shape in lib/fragments.nix for
// one slot: the shared tracker-axis slot row and its unreadable arm, then the
// deprecation-window fills (GITHUB, its unreadable arm, optionally LOCAL, and
// FORGEJO) with their pre-move legacy fragments. pre is the var stem
// ("ISSUE_READ"), stem the fragment stem ("issue-read"); bodies are markers
// keyed by pre so each family's output is distinguishable.
func issueReadFamily(pre, stem, slotGate string, local bool) ([]FragmentRow, map[string]string) {
	rows := []FragmentRow{
		{Axis: "tracker", Shared: true, Gate: slotGate, Fragment: stem + ".md", Var: pre + "_STEP"},
		{Gate: "ISSUE_TRACKER_LINKS_UNREADABLE", Fragment: stem + "-unreadable.md", Var: pre + "_STEP"},
		{Gate: "ISSUE_TRACKER_GITHUB_LINKS_READABLE", Fragment: stem + ".md", LegacyFragment: stem + "-github.md", Var: pre + "_GITHUB_STEP"},
		{Gate: "ISSUE_TRACKER_GITHUB_LINKS_UNREADABLE", Fragment: stem + "-unreadable.md", Var: pre + "_GITHUB_STEP"},
	}
	files := map[string]string{
		"fragments/" + stem + ".md":            "TRACKER-ARM[" + pre + "]\n",
		"fragments/" + stem + "-unreadable.md": "NO-CLIENT-ARM[" + pre + "]\n",
	}
	if local {
		rows = append(rows, FragmentRow{Gate: "ISSUE_TRACKER_LOCAL", Fragment: "local/" + stem + ".md", LegacyFragment: stem + "-local.md", Var: pre + "_LOCAL_STEP"})
		files["fragments/local/"+stem+".md"] = "LOCAL-ARM[" + pre + "]\n"
	}
	rows = append(rows,
		FragmentRow{Gate: "ISSUE_TRACKER_FORGEJO_LINKS_READABLE", Fragment: stem + ".md", LegacyFragment: stem + "-forgejo.md", Var: pre + "_FORGEJO_STEP"},
		FragmentRow{Gate: "ISSUE_TRACKER_FORGEJO_LINKS_UNREADABLE", Fragment: stem + "-unreadable.md", Var: pre + "_FORGEJO_STEP"})
	return rows, files
}

// removedVarFixture is a synthetic registry whose issue prompt splices one
// plain fragment (fragments/x.md) and the work issue-read family, with stub
// templates for every prompt file the tests below append a stale token to.
func removedVarFixture(t *testing.T) (Registry, Env) {
	t.Helper()
	rows, files := issueReadFamily("ISSUE_READ", "issue-read", "ISSUE_TRACKER_LINKS_READABLE", true)
	rows = append(rows, FragmentRow{Fragment: "x.md", Var: "X_STEP"})
	files["fragments/x.md"] = "x-body\n"
	files["issue-prompt.md"] = "issue ${X_STEP} ${ISSUE_READ_STEP}\n"
	for _, name := range []string{"worker-prompt.md", "scout-prompt.md", "fix-prompt.md"} {
		files[name] = "stub\n"
	}
	files["butler-prompt.md"] = "butler ${CHORE_PROMPT}\n"
	files["chores/bugs.md"] = "chore\n"
	return syntheticRegistry(t, rows, files)
}

// assembleValidate runs Assemble then Validate with no marker rows, so the
// only possible Validate error is the removed-var reject.
func assembleValidate(t *testing.T, reg Registry, env Env) error {
	t.Helper()
	result, err := Assemble(env, reg)
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
		{"fragment", "fragments/x.md", false, "prompt", "${COMMIT_STEP}", "#3222"},
		{"agent prompt", "scout-prompt.md", true, `agent "scout" prompt`, "${CODE_COMMENTS_STEP}", "#3505"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, env := removedVarFixture(t)
			if tc.agents {
				agent := strings.TrimSuffix(tc.file, "-prompt.md")
				env.AgentsJSONTemplate = `{"` + agent + `":{"model":"x"}}`
				env.AgentsPromptFiles = `{"` + agent + `":"` + tc.file + `"}`
			}
			appendToFile(t, filepath.Join(env.PromptsDir, tc.file), "\nstale "+tc.token+"\n")

			err := assembleValidate(t, reg, env)
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
	reg, env := removedVarFixture(t)
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`
	appendToFile(t, filepath.Join(env.PromptsDir, "scout-prompt.md"), "\nstale ${CODE_COMMENTS_STEP}\n")

	err := assembleValidate(t, reg, env)
	if err == nil {
		t.Fatal("Validate() error = nil, want removed-var reject")
	}
	requireErrContains(t, err, `agent "scout" prompt`, "${CODE_COMMENTS_STEP}", "#3505", "MIGRATING.md")
}

// The chore prompt is a Consumer-overridable file embedded through the
// CHORE_PROMPT var, so it is operator-authored despite arriving as a var.
func TestAssembleValidateRejectsRemovedFragmentVarInChoreFile(t *testing.T) {
	reg, env := removedVarFixture(t)
	butler := butlerEnv()
	butler.PromptsDir = env.PromptsDir
	appendToFile(t, filepath.Join(butler.PromptsDir, "chores", butler.ChoreName+".md"), "\nstale ${TDD_STEP}\n")

	err := assembleValidate(t, reg, butler)
	if err == nil {
		t.Fatal("Validate() error = nil, want removed-var reject")
	}
	requireErrContains(t, err, "${TDD_STEP}", "#3219")
}

func TestWriteAssemblyRemovedVarInOverrideIsValidateError(t *testing.T) {
	reg, env := removedVarFixture(t)
	appendToFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "\n${TDD_STEP}\n")
	out := writeTestOutputs(t)

	_, err := WriteAssembly(env, reg, nil, testPassthrough(), out, &strings.Builder{})
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
			reg, env := removedVarFixture(t)
			tc.set(&env)
			if err := assembleValidate(t, reg, env); err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestAssembleValidateAllowsOtherPassThroughTokens(t *testing.T) {
	reg, env := removedVarFixture(t)
	appendToFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"),
		"\ntoken ${FORGEJO_TOKEN} home ${HOME} partial ${TDD_STEP_X} $TDD_STEP\n")
	if err := assembleValidate(t, reg, env); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// assembleValidateWarnings is assembleValidate for a run expected to pass,
// returning the Validate warnings.
func assembleValidateWarnings(t *testing.T, reg Registry, env Env) []string {
	t.Helper()
	result, err := Assemble(env, reg)
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
	reg, env := removedVarFixture(t)
	appendToFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "\n${ISSUE_READ_GITHUB_STEP}\n${ISSUE_READ_GITHUB_STEP}\n")
	appendToFile(t, filepath.Join(env.PromptsDir, "review-prompt.md"), "\n${ISSUE_READ_GITHUB_STEP}\n")

	warnings := assembleValidateWarnings(t, reg, env)
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
	reg, env := removedVarFixture(t)
	appendToFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "\n${ISSUE_READ_GITHUB_STEP}\n${TDD_STEP}\n")

	err := assembleValidate(t, reg, env)
	if err == nil {
		t.Fatal("Validate() error = nil, want removed-var reject")
	}
	requireErrContains(t, err, "${TDD_STEP}")
}

// issueReadEnv moves coveredEnv onto the tracker whose axis signal is axis,
// on the socket carrier whose issue verb the readable arm names.
func issueReadEnv(env Env, tracker, axis string) Env {
	env.IssueTracker = tracker
	env.SignalCarrier = "socket"
	env.TrackerAxisRead, env.TrackerAxisWrite, env.TrackerAxisFiler = axis, axis, axis
	return env
}

// An override still naming a per-backend issue-read var renders exactly what
// the ${ISSUE_READ_STEP} slot does for that backend.
func TestAssembleDeprecatedIssueReadVarRendersAsBefore(t *testing.T) {
	cases := []struct {
		name, tracker, axis, v string
	}{
		{"github", "github", "GITHUB", "${ISSUE_READ_GITHUB_STEP}"},
		{"forgejo", "forgejo", "FORGEJO", "${ISSUE_READ_FORGEJO_STEP}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			render := func(token string) string {
				rows, files := issueReadFamily("ISSUE_READ", "issue-read", "ISSUE_TRACKER_LINKS_READABLE", true)
				files["issue-prompt.md"] = "issue " + token + "\n"
				reg, env := syntheticRegistry(t, rows, files)
				res, err := Assemble(issueReadEnv(env, tc.tracker, tc.axis), reg)
				if err != nil {
					t.Fatalf("Assemble %s: %v", token, err)
				}
				return res.Prompt
			}
			want, got := render("${ISSUE_READ_STEP}"), render(tc.v)
			if !strings.Contains(want, "TRACKER-ARM[ISSUE_READ]") {
				t.Fatalf("slot prompt renders no issue-read body: %q", want)
			}
			if got != want {
				t.Errorf("override prompt = %q, want the ${ISSUE_READ_STEP} prompt %q", got, want)
			}
		})
	}
}

// A per-backend var for a backend that is not active still expands empty.
func TestAssembleDeprecatedIssueReadVarOfInactiveBackendIsEmpty(t *testing.T) {
	rows, files := issueReadFamily("ISSUE_READ", "issue-read", "ISSUE_TRACKER_LINKS_READABLE", true)
	files["issue-prompt.md"] = "issue ${ISSUE_READ_FORGEJO_STEP}\n"
	reg, env := syntheticRegistry(t, rows, files)
	got, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if strings.Contains(got.Prompt, "-ARM[") || strings.Contains(got.Prompt, "${ISSUE_READ") {
		t.Errorf("prompt = %q, want no issue-read body and no literal token under the github tracker", got.Prompt)
	}
}

func TestWriteAssemblyPrintsDeprecatedVarWarning(t *testing.T) {
	reg, env := removedVarFixture(t)
	appendToFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "\n${ISSUE_READ_LOCAL_STEP}\n")
	w := new(strings.Builder)

	if _, err := WriteAssembly(env, reg, nil, testPassthrough(), writeTestOutputs(t), w); err != nil {
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const legacyText = "LEGACY-ISSUE-READ-STEP-SENTINEL"
			rows, files := issueReadFamily("ISSUE_READ", "issue-read", "ISSUE_TRACKER_LINKS_READABLE", true)
			delete(files, "fragments/issue-read.md")
			delete(files, "fragments/local/issue-read.md")
			files["fragments/"+tc.legacy] = legacyText + "\n"
			files["issue-prompt.md"] = "issue ${" + tc.v + "}\n"
			reg, env := syntheticRegistry(t, rows, files)
			env = issueReadEnv(env, tc.tracker, tc.axis)

			result, err := Assemble(env, reg)
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

// Without a socket, jira (which rides github's arm) has no way to read a linked
// issue, so an override still naming the deprecated GITHUB issue-read var must
// render the unreadable arm, not nothing and not the readable arm (issues #6344,
// #6894).
func TestAssembleDeprecatedIssueReadVarOnLogCarrierRendersUnreadable(t *testing.T) {
	slots := []struct {
		name, kind, file, pre, stem, gate string
		agent                             string // subagent whose prompt carries the slot; "" means the main prompt
	}{
		{name: "work", kind: "work", file: "issue-prompt.md", pre: "ISSUE_READ", stem: "issue-read",
			gate: "ISSUE_TRACKER_LINKS_READABLE"},
		{name: "research", kind: "research", file: "research-prompt.md", pre: "RESEARCH_ISSUE_READ", stem: "research-issue-read",
			gate: "ISSUE_TRACKER_LINKS_READABLE"},
		{name: "scout", kind: "work", file: "scout-prompt.md", pre: "SCOUT_ISSUE_READ", stem: "scout-issue-read",
			gate: "ISSUE_TRACKER_LINKS_REACHABLE", agent: "scout"},
	}
	for _, s := range slots {
		for _, v := range []string{"${" + s.pre + "_STEP}", "${" + s.pre + "_GITHUB_STEP}"} {
			t.Run(s.name+"/"+v, func(t *testing.T) {
				rows, files := issueReadFamily(s.pre, s.stem, s.gate, false)
				files[s.file] = "prompt " + v + "\n"
				reg, env := syntheticRegistry(t, rows, files)
				env = issueReadEnv(env, "jira", "GITHUB")
				env.SignalCarrier = "log"
				env.DispatchKind = s.kind
				if s.agent != "" {
					env.AgentsJSONTemplate = `{"` + s.agent + `":{"model":"x"}}`
					env.AgentsPromptFiles = `{"` + s.agent + `":"` + s.file + `"}`
				}
				res, err := Assemble(env, reg)
				if err != nil {
					t.Fatalf("Assemble: %v", err)
				}
				prompt := res.Prompt
				if s.agent != "" {
					prompt = agentPromptFromJSON(t, res.AgentsJSON, s.agent)
				}
				if want := "NO-CLIENT-ARM[" + s.pre + "]"; !strings.Contains(prompt, want) {
					t.Errorf("prompt lacks the unreadable arm %q:\n%s", want, prompt)
				}
				if strings.Contains(prompt, "TRACKER-ARM[") {
					t.Errorf("jira prompt renders the readable arm:\n%s", prompt)
				}
			})
		}
	}
}

// Every deprecated row warns at runtime and names the slot var replacing it.
func TestAssembleValidateDeprecatedWarningNamesSlot(t *testing.T) {
	for _, r := range removedFragmentVars {
		if r.severity != severityDeprecated {
			continue
		}
		t.Run(r.name, func(t *testing.T) {
			if r.replacement == "" {
				t.Fatalf("deprecated %s names no replacement slot", r.name)
			}
			reg, env := removedVarFixture(t)
			appendToFile(t, filepath.Join(env.PromptsDir, "issue-prompt.md"), "\n${"+r.name+"}\n")

			warnings := assembleValidateWarnings(t, reg, env)
			want := "use ${" + r.replacement + "} instead"
			found := false
			for _, w := range warnings {
				found = found || (strings.Contains(w, "${"+r.name+"}") && strings.Contains(w, want))
			}
			if !found {
				t.Errorf("warnings = %q, want one naming ${%s} and %q", warnings, r.name, want)
			}
		})
	}
}
