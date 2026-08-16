package promptassembly

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// promptsDir is the real templates/default/prompts tree, resolved relative
// to this package directory (cmd/launcher/internal/promptassembly), the
// same convention testdata/registry.json's tests use for their own
// package-relative testdata path.
const promptsDir = "../../../../templates/default/prompts"

// coveredEnv returns a fixture Env sitting exactly in Assemble's covered
// cell (see checkCoveredCell): github tracker, github forge, a read-write
// box, dispatch kind "work", a fresh box (FixPass == 0), the orchestrator
// off, and every skill baked. Tests mutate a copy to move a single axis off
// the covered cell.
func coveredEnv() Env {
	return Env{
		IssueTracker:         "github",
		CodeForge:            "github",
		BoxWriteEnabled:      true,
		DispatchKind:         "work",
		FixPass:              0,
		OrchestratorEnabled:  false,
		SkillsFound:          "caveman, tdd, commit, code-review",
		CavemanSkillBaked:    true,
		TDDSkillBaked:        true,
		CommitSkillBaked:     true,
		CodeReviewSkillBaked: true,
		PromptsDir:           promptsDir,
		IssueNumber:          "2349",
		IssueTitle:           "Add promptassembly.Assemble",
		Branch:               "agent/issue-2349",
		BaseBranch:           "main",
		InProgressLabel:      "agent-in-progress",
		CompleteLabel:        "agent-complete",
		RunNonce:             "run-nonce-abc123",
	}
}

// localTrackerEnv returns a copy of coveredEnv with IssueTracker set to
// "local" -- otherwise identical, still a read-write box with every other
// axis at its covered-cell value.
func localTrackerEnv() Env {
	env := coveredEnv()
	env.IssueTracker = "local"
	return env
}

func loadTestRegistry(t *testing.T) Registry {
	t.Helper()
	reg, err := LoadRegistryFile("testdata/registry.json")
	if err != nil {
		t.Fatalf("LoadRegistryFile: %v", err)
	}
	return reg
}

// TestAssembleCoveredCellRendersPrompt covers the covered cell's happy
// path: a non-empty prompt with the fixed allowlist names substituted, a
// gate-on fragment's text present, and a gate-off fragment's text absent.
func TestAssembleCoveredCellRendersPrompt(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if result.Prompt == "" {
		t.Fatal("Prompt is empty")
	}

	if !strings.Contains(result.Prompt, "Implement GitHub issue #2349: Add promptassembly.Assemble") {
		t.Errorf("Prompt missing substituted ISSUE_NUMBER/ISSUE_TITLE:\n%s", result.Prompt)
	}
	if !strings.Contains(result.Prompt, "new branch `agent/issue-2349` cut from `main`") {
		t.Errorf("Prompt missing substituted BRANCH/BASE_BRANCH:\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "${") {
		t.Errorf("Prompt still contains an unsubstituted ${...} allowlisted token:\n%s", result.Prompt)
	}

	// REVIEW_LOOP_INLINE is on (orchestrator off) for the covered cell —
	// its fragment text must appear.
	if !strings.Contains(result.Prompt, "Before the PR, spawn a fresh `reviewer` subagent") {
		t.Errorf("Prompt missing REVIEW_LOOP_INLINE fragment text")
	}
	// REVIEW_LOOP_ORCHESTRATOR's gate is off — its fragment text (from
	// review-loop-orchestrator.md) must not appear.
	if strings.Contains(result.Prompt, "REVIEW_LOOP_ORCHESTRATOR_STEP") {
		t.Errorf("Prompt contains a literal unsubstituted REVIEW_LOOP_ORCHESTRATOR_STEP token")
	}

	// ISSUE_TRACKER_GITHUB's fragment text must appear, and its off-gate
	// siblings' assigned vars must render empty (never a literal
	// unsubstituted token).
	if !strings.Contains(result.Prompt, "gh issue view") {
		t.Errorf("Prompt missing ISSUE_TRACKER_GITHUB fragment text (issue-read-github.md)")
	}
}

// TestAssembleAutoFormatGate covers issue #2354's AUTO_FORMAT wiring
// (lib/fragments.nix: gate = "AUTO_FORMAT", var = "AUTO_FORMAT_STEP"): the
// gate is a plain passthrough of Env.AutoFormat (entrypoint.sh's old
// `[ -n "${AUTO_FORMAT:-}" ]` presence check, ported verbatim as a bool field
// rather than restated as a second string-presence field), so auto-format.md
// renders into the prompt when it's true and stays absent when it's false.
func TestAssembleAutoFormatGate(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name       string
		autoFormat bool
	}{
		{name: "unset", autoFormat: false},
		{name: "set", autoFormat: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.AutoFormat = tc.autoFormat

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			const marker = "invoke the `/auto-format` skill"
			got := strings.Contains(result.Prompt, marker)
			if got != tc.autoFormat {
				t.Errorf("Prompt contains auto-format.md text = %v, want %v:\n%s", got, tc.autoFormat, result.Prompt)
			}
		})
	}
}

// TestAssembleAutoLintGate covers issue #2354's AUTO_LINT wiring
// (lib/fragments.nix: gate = "AUTO_LINT", var = "AUTO_LINT_STEP"): same
// plain-passthrough presence semantics as AUTO_FORMAT above.
func TestAssembleAutoLintGate(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name     string
		autoLint bool
	}{
		{name: "unset", autoLint: false},
		{name: "set", autoLint: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.AutoLint = tc.autoLint

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			const marker = "invoke the `/auto-lint` skill"
			got := strings.Contains(result.Prompt, marker)
			if got != tc.autoLint {
				t.Errorf("Prompt contains auto-lint.md text = %v, want %v:\n%s", got, tc.autoLint, result.Prompt)
			}
		})
	}
}

// TestAssembleCIFailureSummaryGate covers issue #2354's CI_FAILURE_SUMMARY
// wiring (lib/fragments.nix: gate = "CI_FAILURE_SUMMARY", var =
// "CI_FAILURE_STEP", extraSubstVars = [ "CI_FAILURE_SUMMARY" ]) on a fix-pass
// Env (the only cell that renders fix-prompt.md, which is the only base
// template referencing ${CI_FAILURE_STEP}): the gate mirrors entrypoint.sh's
// old `[ -n "${CI_FAILURE_SUMMARY:-}" ]` presence check on the *value*, not a
// separate bool field, so a non-empty CIFailureSummary both turns the gate on
// and substitutes its own text into ci-failure.md's ${CI_FAILURE_SUMMARY}
// token; an empty CIFailureSummary (the default, non-fix-pass, or a fix pass
// where CI didn't fail) leaves ci-failure.md's marker text out of the prompt
// entirely.
func TestAssembleCIFailureSummaryGate(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name             string
		ciFailureSummary string
		wantRendered     bool
	}{
		{name: "unset on a fix pass", ciFailureSummary: "", wantRendered: false},
		{name: "set on a fix pass", ciFailureSummary: "go test ./... failed: TestFoo", wantRendered: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.FixPass = 1
			env.CIFailureSummary = tc.ciFailureSummary

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			const marker = "The launcher captured this from the failing PR checks"
			got := strings.Contains(result.Prompt, marker)
			if got != tc.wantRendered {
				t.Errorf("Prompt contains ci-failure.md text = %v, want %v:\n%s", got, tc.wantRendered, result.Prompt)
			}
			if tc.wantRendered && !strings.Contains(result.Prompt, tc.ciFailureSummary) {
				t.Errorf("Prompt missing substituted CI_FAILURE_SUMMARY value %q:\n%s", tc.ciFailureSummary, result.Prompt)
			}
		})
	}
}

// promptsDirMissingFragment symlinks a fixture PromptsDir alongside the real
// templates/default/prompts tree (base templates plus every fragments/*
// file) EXCEPT the named fragment, which it omits entirely -- the exact
// on-disk shape TestAssembleMissingGatedFragmentFileIsSwallowed needs to
// observe the fragment loop's missing-file handling in isolation, without
// hand-building a whole prompts fixture of its own.
func promptsDirMissingFragment(t *testing.T, omit string) string {
	t.Helper()
	dir := t.TempDir()
	realDir, err := filepath.Abs(promptsDir)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}

	entries, err := os.ReadDir(realDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", realDir, err)
	}
	for _, entry := range entries {
		if entry.Name() == "fragments" {
			continue
		}
		if err := os.Symlink(filepath.Join(realDir, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
			t.Fatalf("Symlink(%s): %v", entry.Name(), err)
		}
	}

	realFragments := filepath.Join(realDir, "fragments")
	fragmentsDir := filepath.Join(dir, "fragments")
	if err := os.Mkdir(fragmentsDir, 0o755); err != nil {
		t.Fatalf("Mkdir(%s): %v", fragmentsDir, err)
	}
	fragEntries, err := os.ReadDir(realFragments)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", realFragments, err)
	}
	for _, entry := range fragEntries {
		if entry.Name() == omit {
			continue
		}
		if err := os.Symlink(filepath.Join(realFragments, entry.Name()), filepath.Join(fragmentsDir, entry.Name())); err != nil {
			t.Fatalf("Symlink(%s): %v", entry.Name(), err)
		}
	}

	return dir
}

// TestAssembleMissingGatedFragmentFileIsSwallowed covers old bash's
// documented quirk (entrypoint.sh: 1001-1009's fragment loop, e.g.
// `printf -v "$_fvar" '%s' "$(_subst "${PROMPTS_DIR}/fragments/${_ffile}")"`):
// because the failed command substitution sits as a printf argument rather
// than a bare assignment, `set -e` never sees a non-zero exit and the
// missing/unreadable fragment silently resolves to an empty string instead
// of aborting the script. Assemble's fragment loop must reproduce that
// specific swallow -- CAVEMAN_BAKED is on in coveredEnv, but its backing
// fragment file is absent here, so Assemble must still succeed, with
// CAVEMAN_STEP resolving to empty rather than the file's real text or a
// literal unsubstituted token.
func TestAssembleMissingGatedFragmentFileIsSwallowed(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.PromptsDir = promptsDirMissingFragment(t, "caveman-default.md")

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v, want nil (a missing gated fragment file must be swallowed, not hard-fail)", err)
	}

	if strings.Contains(result.Prompt, "Default to the `/caveman` skill") {
		t.Errorf("Prompt contains caveman-default.md's fragment text despite the file being absent from PromptsDir/fragments:\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "${CAVEMAN_STEP}") {
		t.Errorf("Prompt still contains a literal unsubstituted ${CAVEMAN_STEP} token, want empty-string substitution:\n%s", result.Prompt)
	}
}

// TestAssembleSkillPreambleSelfSubstitution covers a fragment substituting
// its own extraSubstVars entry: skill-preamble.md's ${SKILLS_FOUND} must
// resolve to Env.SkillsFound's actual value, not stay literal or empty.
func TestAssembleSkillPreambleSelfSubstitution(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.SkillsFound = "caveman, tdd"

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "Skills available: caveman, tdd.") {
		t.Errorf("Prompt missing skill-preamble.md's substituted SKILLS_FOUND:\n%s", result.Prompt)
	}
}

// TestAssemblePromptHasNoTrailingNewline covers the bash-parity trim
// (issue #2349, prompt-assembly-parity.bats): agent/entrypoint.sh's
// `prompt="$(_subst "${PROMPTS_DIR}/issue-prompt.md")"` sits inside a
// $(...) command substitution, which strips ALL trailing newlines from its
// captured output -- and nothing downstream re-adds one before the prompt
// reaches disk (entrypoint.sh: 1244's `printf '%s' "$prompt" >
// "$_prompt_file"` writes it raw). issue-prompt.md itself ends with a
// substitution token immediately followed by a single on-disk newline, and
// that token's covered-cell value is a fragment-loop var whose own
// assignment already appends "\n\n" -- so an unstripped Result.Prompt would
// end in multiple trailing newlines, not zero.
func TestAssemblePromptHasNoTrailingNewline(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if strings.HasSuffix(result.Prompt, "\n") {
		t.Errorf("Prompt ends with a trailing newline, want none (bash $(...) strips all of them): %q", result.Prompt[len(result.Prompt)-10:])
	}
}

// TestAssembleFragmentSeparatorIsExactlyTwoNewlines covers the fragment
// loop's "\n\n" separator (entrypoint.sh: 1001-1009): a fragment file that
// itself ends with a blank line on disk (e.g. skill-preamble.md, which ends
// "\n\n") must not leak that blank line into the rendered prompt as extra
// newlines beyond the two the assignment site appends -- entrypoint.sh's
// `"$(_subst "$f")"$'\n\n'` strips the fragment's own trailing newlines
// (all of them, via $(...)) before appending exactly "\n\n".
func TestAssembleFragmentSeparatorIsExactlyTwoNewlines(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.SkillsFound = "caveman, tdd"

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	const marker = "the inline guidance below is the fallback when a skill is absent."
	idx := strings.Index(result.Prompt, marker)
	if idx == -1 {
		t.Fatalf("Prompt missing skill-preamble.md's trailing sentence:\n%s", result.Prompt)
	}
	after := result.Prompt[idx+len(marker):]
	if !strings.HasPrefix(after, "\n\n") || strings.HasPrefix(after, "\n\n\n") {
		t.Errorf("text after skill-preamble.md's marker = %q, want exactly two newlines then non-newline content", after[:min(6, len(after))])
	}
}

// TestAssembleHandoff covers Result.Handoff for the covered cell: Invoker
// is always "driver-exec" (orchestrator off), SessionMode follows
// ResumeAfterHold, and ReviewPromptFile/ReviewModel/ReviewEffort all stay
// empty.
func TestAssembleHandoff(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name            string
		resumeAfterHold bool
		wantMode        string
	}{
		{name: "fresh dispatch", resumeAfterHold: false, wantMode: "initial"},
		{name: "resumed after hold", resumeAfterHold: true, wantMode: "resume"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.ResumeAfterHold = tc.resumeAfterHold

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			if result.Handoff.Invoker != "driver-exec" {
				t.Errorf("Handoff.Invoker = %q, want driver-exec", result.Handoff.Invoker)
			}
			if result.Handoff.SessionMode != tc.wantMode {
				t.Errorf("Handoff.SessionMode = %q, want %q", result.Handoff.SessionMode, tc.wantMode)
			}
			if result.Handoff.ReviewPromptFile != "" {
				t.Errorf("Handoff.ReviewPromptFile = %q, want empty", result.Handoff.ReviewPromptFile)
			}
			if result.Handoff.ReviewModel != "" {
				t.Errorf("Handoff.ReviewModel = %q, want empty", result.Handoff.ReviewModel)
			}
			if result.Handoff.ReviewEffort != "" {
				t.Errorf("Handoff.ReviewEffort = %q, want empty", result.Handoff.ReviewEffort)
			}
			if result.Handoff.WorkerPromptFile != "" {
				t.Errorf("Handoff.WorkerPromptFile = %q, want empty (issue #2059)", result.Handoff.WorkerPromptFile)
			}
		})
	}
}

// TestAssembleAgentsJSON covers the --agents JSON injection loop
// (entrypoint.sh: 1105-1116): a fixture template's scout entry gets its
// .scout.prompt set to the substituted prompt file text; an empty template
// leaves Result.AgentsJSON empty.
func TestAssembleAgentsJSON(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("template present", func(t *testing.T) {
		env := coveredEnv()
		env.AgentsJSONTemplate = `{"scout":{"model":"x"}}`
		env.AgentsPromptFiles = `{"scout":"fragments/tdd-default.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if result.AgentsJSON == "" {
			t.Fatal("AgentsJSON is empty, want non-empty")
		}

		var parsed map[string]struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal([]byte(result.AgentsJSON), &parsed); err != nil {
			t.Fatalf("unmarshal AgentsJSON: %v\n%s", err, result.AgentsJSON)
		}
		scout, ok := parsed["scout"]
		if !ok {
			t.Fatal("AgentsJSON missing scout entry")
		}
		if scout.Model != "x" {
			t.Errorf("scout.model = %q, want %q", scout.Model, "x")
		}
		if !strings.Contains(scout.Prompt, "/tdd") {
			t.Errorf("scout.prompt missing substituted tdd-default.md content: %q", scout.Prompt)
		}
	})

	t.Run("empty template", func(t *testing.T) {
		env := coveredEnv()

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if result.AgentsJSON != "" {
			t.Errorf("AgentsJSON = %q, want empty", result.AgentsJSON)
		}
	})
}

// TestAssembleUnsupportedCell covers every axis individually flipped away
// from the covered cell: each must return an error satisfying
// errors.Is(err, ErrUnsupportedCell).
func TestAssembleUnsupportedCell(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name   string
		mutate func(*Env)
	}{
		{name: "wrong issue tracker", mutate: func(e *Env) { e.IssueTracker = "bitbucket" }},
		{name: "unrecognized code forge", mutate: func(e *Env) { e.CodeForge = "bogus" }},
		{name: "unrecognized dispatch kind", mutate: func(e *Env) { e.DispatchKind = "bogus" }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			tc.mutate(&env)

			_, err := Assemble(env, reg)
			if err == nil {
				t.Fatal("Assemble: got nil error, want ErrUnsupportedCell")
			}
			if !errors.Is(err, ErrUnsupportedCell) {
				t.Errorf("Assemble error = %v, want it to wrap ErrUnsupportedCell", err)
			}
		})
	}
}

// TestAssembleAccessForgeCellsCovered covers the CodeForge x
// BoxWriteEnabled cells this issue adds to checkCoveredCell's covered set
// (github+read-write was already covered): github+read-only,
// forgejo+read-write, forgejo+read-only, plus (issue #2354) the "git" and
// "local" CodeForge values -- both schema-documented (lib/env-schema.nix)
// and already handled identically to "github" by Gates()
// (gates_access_forge.go: "only forgejo diverges from the shared gh-flavored
// path"), so checkCoveredCell's allowlist must accept them too. Each must
// render without error.
func TestAssembleAccessForgeCellsCovered(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name   string
		mutate func(*Env)
	}{
		{name: "forgejo read-write", mutate: func(e *Env) { e.CodeForge = "forgejo" }},
		{name: "forgejo read-only", mutate: func(e *Env) { e.CodeForge = "forgejo"; e.BoxWriteEnabled = false }},
		{name: "github read-only", mutate: func(e *Env) { e.BoxWriteEnabled = false }},
		{name: "git forge", mutate: func(e *Env) { e.CodeForge = "git" }},
		{name: "local forge", mutate: func(e *Env) { e.CodeForge = "local" }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			tc.mutate(&env)

			if _, err := Assemble(env, reg); err != nil {
				t.Fatalf("Assemble: %v, want nil (cell should now be covered)", err)
			}
		})
	}
}

// TestAssembleResearchKindRendersResearchPrompt covers the research cell
// (DispatchKind == "research", SelfContained == false): Assemble no longer
// rejects it, renders research-prompt.md (not issue-prompt.md), and always
// sets SessionMode to "initial" -- even when ResumeAfterHold is also set,
// per entrypoint.sh's precedence (entrypoint.sh: 1031-1063: research's
// branch never even inspects RESUME_AFTER_HOLD).
func TestAssembleResearchKindRendersResearchPrompt(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.DispatchKind = "research"
	env.SelfContained = false
	env.ResumeAfterHold = true
	env.ResearchStatusEnum = "recommend|reject|unclear"

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "Research GitHub issue #2349: Add promptassembly.Assemble") {
		t.Errorf("Prompt missing research-prompt.md's substituted ISSUE_NUMBER/ISSUE_TITLE:\n%s", result.Prompt)
	}
	if !strings.Contains(result.Prompt, "This is a research\ndispatch (ADR 0022)") {
		t.Errorf("Prompt missing research-prompt.md's distinguishing text:\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "self-contained research dispatch") {
		t.Errorf("Prompt contains research-self-contained-prompt.md's text, want research-prompt.md:\n%s", result.Prompt)
	}
	// The OUTCOME grammar line's verdict enumeration renders from
	// Env.ResearchStatusEnum via the RESEARCH_STATUS_ENUM allowlist entry
	// (issue #2504), not a hand-typed literal in the template.
	if !strings.Contains(result.Prompt, "status=<recommend|reject|unclear>") {
		t.Errorf("Prompt missing substituted RESEARCH_STATUS_ENUM in the OUTCOME grammar line:\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "${RESEARCH_STATUS_ENUM}") {
		t.Errorf("Prompt contains an unsubstituted RESEARCH_STATUS_ENUM token:\n%s", result.Prompt)
	}
	if result.Handoff.SessionMode != "initial" {
		t.Errorf("Handoff.SessionMode = %q, want %q even with ResumeAfterHold set", result.Handoff.SessionMode, "initial")
	}
}

// TestAssembleResearchSelfContainedRendersSelfContainedPrompt covers the
// self-contained research cell (DispatchKind == "research", SelfContained ==
// true): Assemble renders research-self-contained-prompt.md, not
// research-prompt.md.
func TestAssembleResearchSelfContainedRendersSelfContainedPrompt(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.DispatchKind = "research"
	env.SelfContained = true
	env.ResearchStatusEnum = "recommend|reject|unclear"

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "self-contained research dispatch (ADR 0022, issue #2202)") {
		t.Errorf("Prompt missing research-self-contained-prompt.md's distinguishing text:\n%s", result.Prompt)
	}
	// Same RESEARCH_STATUS_ENUM substitution as research-prompt.md's OUTCOME
	// section (issue #2504).
	if !strings.Contains(result.Prompt, "status=<recommend|reject|unclear>") {
		t.Errorf("Prompt missing substituted RESEARCH_STATUS_ENUM in the OUTCOME grammar line:\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "${RESEARCH_STATUS_ENUM}") {
		t.Errorf("Prompt contains an unsubstituted RESEARCH_STATUS_ENUM token:\n%s", result.Prompt)
	}
	if result.Handoff.SessionMode != "initial" {
		t.Errorf("Handoff.SessionMode = %q, want %q", result.Handoff.SessionMode, "initial")
	}
}

// TestAssembleFixPassRendersFixPrompt covers the fix-pass cell (DispatchKind
// left at default "work", FixPass > 0): Assemble renders fix-prompt.md and
// always sets SessionMode to "resume" -- regardless of ResumeAfterHold, per
// entrypoint.sh's precedence (entrypoint.sh: 1031-1063: the fix-pass branch
// never inspects RESUME_AFTER_HOLD either).
func TestAssembleFixPassRendersFixPrompt(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name            string
		resumeAfterHold bool
	}{
		{name: "resume after hold unset", resumeAfterHold: false},
		{name: "resume after hold set", resumeAfterHold: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.FixPass = 1
			env.ResumeAfterHold = tc.resumeAfterHold

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			if !strings.Contains(result.Prompt, "Fix box for GitHub issue #2349: Add promptassembly.Assemble") {
				t.Errorf("Prompt missing fix-prompt.md's substituted ISSUE_NUMBER/ISSUE_TITLE:\n%s", result.Prompt)
			}
			if !strings.Contains(result.Prompt, "This is a warm fix pass, not a fresh implementation") {
				t.Errorf("Prompt missing fix-prompt.md's distinguishing text:\n%s", result.Prompt)
			}
			if result.Handoff.SessionMode != "resume" {
				t.Errorf("Handoff.SessionMode = %q, want %q", result.Handoff.SessionMode, "resume")
			}
		})
	}
}

// TestAssembleResearchTakesPrecedenceOverFixPass covers entrypoint.sh's
// if/elif precedence (entrypoint.sh: 1031-1063): DispatchKind == "research"
// is checked first, so a research Env with FixPass > 0 still renders the
// research prompt, never fix-prompt.md.
func TestAssembleResearchTakesPrecedenceOverFixPass(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.DispatchKind = "research"
	env.FixPass = 1

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if strings.Contains(result.Prompt, "This is a warm fix pass") {
		t.Errorf("Prompt contains fix-prompt.md's text, want research-prompt.md:\n%s", result.Prompt)
	}
	if !strings.Contains(result.Prompt, "This is a research\ndispatch (ADR 0022)") {
		t.Errorf("Prompt missing research-prompt.md's distinguishing text:\n%s", result.Prompt)
	}
	if result.Handoff.SessionMode != "initial" {
		t.Errorf("Handoff.SessionMode = %q, want %q", result.Handoff.SessionMode, "initial")
	}
}

// TestAssembleUnsupportedCellDefaultsCovered covers that an empty
// IssueTracker/CodeForge/DispatchKind (each defaulting to github/github/work
// per entrypoint.sh) is itself still the covered cell, not an error.
// writeContractFile writes content to dir/name and returns the path, for
// building temp shared-block contract-file fixtures (see
// TestAssembleInjectsSharedBlocks and friends): unlike the real nix-baked
// contract files (lib/mkHarness.nix: 622-631), these are plain files this
// package's tests fully control the content of.
func writeContractFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

// TestAssembleInjectsSharedBlocks covers shared-block injection
// (_inject_shared_block, entrypoint.sh: 632-643, 1064-1074) for the
// fix-pass cell: unlike issue-prompt.md (whose own COMMS/CHECK/OUTCOME
// sections already contain each marker -- see the comment above the
// base-template switch in Assemble), fix-prompt.md does not, so all three
// blocks get appended, in comms/check/outcome order, each separated from
// what precedes it by a blank line.
func TestAssembleInjectsSharedBlocks(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	env := coveredEnv()
	env.FixPass = 1
	env.CommsContractFile = writeContractFile(t, dir, "comms-contract.md", "# COMMS\n\ncomms body text\n")
	env.CheckContractFile = writeContractFile(t, dir, "check-contract.md", "# CHECK\n\ncheck body text\n")
	env.OutcomeContractFile = writeContractFile(t, dir, "outcome-contract.md", "# LAND THE CHANGE\n\noutcome body text\n")

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	commsIdx := strings.Index(result.Prompt, "comms body text")
	checkIdx := strings.Index(result.Prompt, "check body text")
	outcomeIdx := strings.Index(result.Prompt, "outcome body text")
	if commsIdx == -1 || checkIdx == -1 || outcomeIdx == -1 {
		t.Fatalf("Prompt missing an injected block: comms=%d check=%d outcome=%d\n%s", commsIdx, checkIdx, outcomeIdx, result.Prompt)
	}
	if !(commsIdx < checkIdx && checkIdx < outcomeIdx) {
		t.Errorf("blocks out of order: comms=%d check=%d outcome=%d, want comms < check < outcome", commsIdx, checkIdx, outcomeIdx)
	}
	if !strings.Contains(result.Prompt, "\n\n# COMMS") {
		t.Errorf("comms block not separated from prior content by a blank line:\n%s", result.Prompt)
	}
	if !strings.Contains(result.Prompt, "\n\n# CHECK") {
		t.Errorf("check block not separated from prior content by a blank line:\n%s", result.Prompt)
	}
	if !strings.Contains(result.Prompt, "\n\n# LAND THE CHANGE") {
		t.Errorf("outcome block not separated from prior content by a blank line:\n%s", result.Prompt)
	}
}

// TestAssembleSharedBlockAlreadyPresentIsNoOp covers
// injectSharedBlock/_inject_shared_block's idempotent guard (entrypoint.sh:
// 632-643): a base template whose content already contains a block's marker
// (here, a PromptsDir fixture whose issue-prompt.md already has a "# COMMS"
// line) does not get that block appended a second time -- the distinguishing
// body text from the contract-file fixture must appear zero times, not one
// or two.
func TestAssembleSharedBlockAlreadyPresentIsNoOp(t *testing.T) {
	reg := loadTestRegistry(t)
	promptsFixtureDir := t.TempDir()
	// The fragment loop reads PromptsDir/fragments/* regardless of which
	// base template is selected, so this fixture symlinks the real
	// fragments dir in alongside its own issue-prompt.md rather than
	// standing up a full fragments fixture of its own.
	fragmentsDir, err := filepath.Abs(filepath.Join(promptsDir, "fragments"))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if err := os.Symlink(fragmentsDir, filepath.Join(promptsFixtureDir, "fragments")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(promptsFixtureDir, "issue-prompt.md"),
		[]byte("# TASK\n\nImplement GitHub issue #${ISSUE_NUMBER}.\n\n# COMMS\n\nalready here\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	contractDir := t.TempDir()
	env := coveredEnv()
	env.PromptsDir = promptsFixtureDir
	env.CommsContractFile = writeContractFile(t, contractDir, "comms-contract.md", "# COMMS\n\ncomms body text\n")

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if strings.Count(result.Prompt, "comms body text") != 0 {
		t.Errorf("Prompt contains injected comms block's body text, want zero occurrences (marker already present):\n%s", result.Prompt)
	}
	if strings.Count(result.Prompt, "# COMMS") != 1 {
		t.Errorf("Prompt contains %d occurrences of \"# COMMS\", want exactly 1", strings.Count(result.Prompt, "# COMMS"))
	}
}

// TestAssembleResearchCellOnlyInjectsResearchVerdict covers that the
// research branch of Assemble's injection step (entrypoint.sh: 1064-1074)
// only ever attempts research-verdict injection, never comms/check/outcome,
// even when every contract-file Env field is populated. Unlike the real
// research-prompt.md (whose own "# POST THE VERDICT" section already
// contains that marker, same as issue-prompt.md's COMMS/CHECK/OUTCOME
// sections -- see the comment above the base-template switch in Assemble),
// this test's PromptsDir fixture omits it so the injected block is
// observable.
func TestAssembleResearchCellOnlyInjectsResearchVerdict(t *testing.T) {
	reg := loadTestRegistry(t)
	promptsFixtureDir := t.TempDir()
	fragmentsDir, err := filepath.Abs(filepath.Join(promptsDir, "fragments"))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if err := os.Symlink(fragmentsDir, filepath.Join(promptsFixtureDir, "fragments")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(promptsFixtureDir, "research-prompt.md"),
		[]byte("# TASK\n\nResearch GitHub issue #${ISSUE_NUMBER}.\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dir := t.TempDir()
	env := coveredEnv()
	env.PromptsDir = promptsFixtureDir
	env.DispatchKind = "research"
	env.CommsContractFile = writeContractFile(t, dir, "comms-contract.md", "# COMMS\n\ncomms body text\n")
	env.CheckContractFile = writeContractFile(t, dir, "check-contract.md", "# CHECK\n\ncheck body text\n")
	env.OutcomeContractFile = writeContractFile(t, dir, "outcome-contract.md", "# LAND THE CHANGE\n\noutcome body text\n")
	env.ResearchOutcomeContractFile = writeContractFile(t, dir, "research-outcome-contract.md", "# POST THE VERDICT\n\nverdict body text\n")

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "verdict body text") {
		t.Errorf("Prompt missing injected research-verdict block:\n%s", result.Prompt)
	}
	for _, unwanted := range []string{"comms body text", "check body text", "outcome body text"} {
		if strings.Contains(result.Prompt, unwanted) {
			t.Errorf("Prompt contains %q, want research cell to never inject comms/check/outcome", unwanted)
		}
	}
}

// TestAssembleInjectedBlockSubstitutesTokens covers that a contract file's
// own ${...} substitution tokens are resolved through the same allowlist as
// every other file Assemble renders (entrypoint.sh's _subst call inside
// _inject_shared_block, entrypoint.sh: 638).
func TestAssembleInjectedBlockSubstitutesTokens(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	env := coveredEnv()
	env.FixPass = 1
	env.OutcomeContractFile = writeContractFile(t, dir, "outcome-contract.md", "# LAND THE CHANGE\n\nissue #${ISSUE_NUMBER}\n")

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "issue #2349") {
		t.Errorf("Prompt missing substituted ISSUE_NUMBER in injected outcome block:\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "${ISSUE_NUMBER}") {
		t.Errorf("Prompt contains unsubstituted ${ISSUE_NUMBER} in injected outcome block:\n%s", result.Prompt)
	}
}

// TestAssembleLocalTracker covers the local-tracker cell (issue #2352):
// Assemble accepts IssueTracker == "local" and renders issue-read-local.md's
// fragment text, never issue-read-github.md's "gh issue view".
func TestAssembleLocalTracker(t *testing.T) {
	reg := loadTestRegistry(t)
	env := localTrackerEnv()

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "This is a local issue with no GitHub-side counterpart") {
		t.Errorf("Prompt missing ISSUE_TRACKER_LOCAL fragment text (issue-read-local.md):\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "gh issue view") {
		t.Errorf("Prompt contains ISSUE_TRACKER_GITHUB fragment text (issue-read-github.md), want local tracker's fragment only")
	}
}

// TestAssembleLocalTrackerWithLocalIssueReference covers the local-tracker
// cell with LocalIssueReference == true: the PR body must carry the
// "Local-issue:" breadcrumb (pr-body-local-ref.md), never the local-noref
// cell's marker text (pr-body-local-noref.md).
func TestAssembleLocalTrackerWithLocalIssueReference(t *testing.T) {
	reg := loadTestRegistry(t)
	env := localTrackerEnv()
	env.LocalIssueReference = true

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "Local-issue: 2349") {
		t.Errorf("Prompt missing PR_BODY_LOCAL_REF fragment text (pr-body-local-ref.md):\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "Body must NOT reference the local ticket by slug or number") {
		t.Errorf("Prompt contains PR_BODY_LOCAL_NOREF fragment text (pr-body-local-noref.md), want local-ref only")
	}
}

// TestAssembleLocalTrackerWithoutLocalIssueReference covers the
// local-tracker cell with LocalIssueReference left at its default (false):
// the PR body must carry PR_BODY_LOCAL_NOREF's marker text
// (pr-body-local-noref.md), never the "Local-issue:" breadcrumb
// (pr-body-local-ref.md).
func TestAssembleLocalTrackerWithoutLocalIssueReference(t *testing.T) {
	reg := loadTestRegistry(t)
	env := localTrackerEnv()

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "Body must NOT reference the local ticket by slug or number") {
		t.Errorf("Prompt missing PR_BODY_LOCAL_NOREF fragment text (pr-body-local-noref.md):\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "Local-issue:") {
		t.Errorf("Prompt contains PR_BODY_LOCAL_REF fragment text (pr-body-local-ref.md), want local-noref only")
	}
}

// TestAssembleForgejoTrackerReadWrite covers the forgejo-tracker cell,
// read-write box (issue #2352, ADR 0022's read-write-only acceptance
// criterion -- read-only tracker cells are out of scope): Assemble accepts
// IssueTracker == "forgejo" and renders issue-read-forgejo.md's
// distinguishing "fj issue view" text, never issue-read-github.md's "gh
// issue view".
func TestAssembleForgejoTrackerReadWrite(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.IssueTracker = "forgejo"

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "fj issue view") {
		t.Errorf("Prompt missing ISSUE_TRACKER_FORGEJO fragment text (issue-read-forgejo.md):\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "gh issue view") {
		t.Errorf("Prompt contains ISSUE_TRACKER_GITHUB fragment text (issue-read-github.md), want forgejo tracker's fragment only")
	}
}

// TestAssembleJiraTracker covers the jira-tracker cell (issue #2352): jira
// shares github's arm end to end (gates_tracker.go's issueTrackerAxis), so
// Assemble accepts it and does not return ErrUnsupportedCell.
func TestAssembleJiraTracker(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.IssueTracker = "jira"

	if _, err := Assemble(env, reg); err != nil {
		t.Errorf("Assemble with jira tracker: %v, want nil error", err)
	}
}

// TestAssembleJiraRidesGithubArms pins this issue's explicit acceptance
// criterion: jira really does render through github's arm end-to-end at the
// Assemble level (not just at Gates()'s gate-map level, already covered by
// gates_tracker_test.go). Two Envs, identical except for IssueTracker
// ("jira" vs "github"), must produce byte-identical Result.Prompt.
func TestAssembleJiraRidesGithubArms(t *testing.T) {
	reg := loadTestRegistry(t)

	jiraEnv := coveredEnv()
	jiraEnv.IssueTracker = "jira"
	githubEnv := coveredEnv()
	githubEnv.IssueTracker = "github"

	jiraResult, err := Assemble(jiraEnv, reg)
	if err != nil {
		t.Fatalf("Assemble(jira): %v", err)
	}
	githubResult, err := Assemble(githubEnv, reg)
	if err != nil {
		t.Fatalf("Assemble(github): %v", err)
	}

	if jiraResult.Prompt != githubResult.Prompt {
		t.Errorf("jira Prompt != github Prompt, want byte-identical (jira rides github's arm end-to-end):\njira:\n%s\n\ngithub:\n%s", jiraResult.Prompt, githubResult.Prompt)
	}
}

func TestAssembleUnsupportedCellDefaultsCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.IssueTracker = ""
	env.CodeForge = ""
	env.DispatchKind = ""

	if _, err := Assemble(env, reg); err != nil {
		t.Errorf("Assemble with defaulted tracker/forge/kind: %v, want nil error", err)
	}
}

// TestAssembleOrchestratorReviewerDrop covers entrypoint.sh's
// orchestrator-on reviewer-drop / review-model-extraction / review-prompt
// rendering (entrypoint.sh: 1029-1062, 1086-1107): review_prompt_rendered is
// populated with review-prompt.md's substituted text, review_model_rendered
// is extracted from .reviewer.model (and, mirroring that same extraction,
// Handoff.ReviewEffort is extracted from .reviewer.effort) before the
// reviewer key is deleted from the agents JSON template, and the generic
// per-agent injection loop still runs for every other agent.
func TestAssembleOrchestratorReviewerDrop(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x","effort":"review-effort-x"},"scout":{"model":"scout-model-y"}}`
	env.AgentsPromptFiles = `{"scout":"fragments/tdd-default.md"}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if result.Handoff.Invoker != "orchestrator" {
		t.Errorf("Handoff.Invoker = %q, want orchestrator", result.Handoff.Invoker)
	}
	if result.Handoff.ReviewModel != "review-model-x" {
		t.Errorf("Handoff.ReviewModel = %q, want %q", result.Handoff.ReviewModel, "review-model-x")
	}
	if result.Handoff.ReviewEffort != "review-effort-x" {
		t.Errorf("Handoff.ReviewEffort = %q, want %q", result.Handoff.ReviewEffort, "review-effort-x")
	}
	if result.Handoff.ReviewPromptFile == "" {
		t.Fatal("Handoff.ReviewPromptFile is empty, want non-empty")
	}
	if !strings.Contains(result.Handoff.ReviewPromptFile, "#2349") {
		t.Errorf("Handoff.ReviewPromptFile missing substituted ISSUE_NUMBER:\n%s", result.Handoff.ReviewPromptFile)
	}
	if result.Handoff.WorkerPromptFile == "" {
		t.Fatal("Handoff.WorkerPromptFile is empty, want non-empty (issue #2059)")
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.AgentsJSON), &parsed); err != nil {
		t.Fatalf("unmarshal AgentsJSON: %v\n%s", err, result.AgentsJSON)
	}
	if _, ok := parsed["reviewer"]; ok {
		t.Errorf("AgentsJSON still contains reviewer key, want it dropped: %s", result.AgentsJSON)
	}

	var scout struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(parsed["scout"], &scout); err != nil {
		t.Fatalf("unmarshal scout entry: %v", err)
	}
	if !strings.Contains(scout.Prompt, "/tdd") {
		t.Errorf("scout.prompt missing substituted tdd-default.md content: %q", scout.Prompt)
	}
}

// TestAssembleOrchestratorNoReviewerKey covers that ReviewModel and
// ReviewEffort both stay empty (mirroring jq's `.reviewer.model // empty`
// and `.reviewer.effort // empty`) when the template carries no reviewer
// key at all, while review-prompt.md rendering is unaffected --
// it's independent of whether a reviewer is configured.
func TestAssembleOrchestratorNoReviewerKey(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.AgentsJSONTemplate = `{"scout":{"model":"scout-model-y"}}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if result.Handoff.ReviewModel != "" {
		t.Errorf("Handoff.ReviewModel = %q, want empty", result.Handoff.ReviewModel)
	}
	if result.Handoff.ReviewEffort != "" {
		t.Errorf("Handoff.ReviewEffort = %q, want empty", result.Handoff.ReviewEffort)
	}
	if result.Handoff.ReviewPromptFile == "" {
		t.Error("Handoff.ReviewPromptFile is empty, want non-empty even with no reviewer configured")
	}
	if result.Handoff.WorkerPromptFile == "" {
		t.Error("Handoff.WorkerPromptFile is empty, want non-empty even with no reviewer configured (issue #2059)")
	}
}

// TestAssembleOrchestratorEmptyAgentsTemplate covers the orchestrator-on
// cell with no AgentsJSONTemplate at all: AgentsJSON stays empty (no
// --agents flag), ReviewModel and ReviewEffort both stay empty, and
// ReviewPromptFile is still rendered (it doesn't depend on
// AgentsJSONTemplate at all).
func TestAssembleOrchestratorEmptyAgentsTemplate(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if result.AgentsJSON != "" {
		t.Errorf("AgentsJSON = %q, want empty", result.AgentsJSON)
	}
	if result.Handoff.ReviewModel != "" {
		t.Errorf("Handoff.ReviewModel = %q, want empty", result.Handoff.ReviewModel)
	}
	if result.Handoff.ReviewEffort != "" {
		t.Errorf("Handoff.ReviewEffort = %q, want empty", result.Handoff.ReviewEffort)
	}
	if result.Handoff.ReviewPromptFile == "" {
		t.Error("Handoff.ReviewPromptFile is empty, want non-empty")
	}
	if result.Handoff.WorkerPromptFile == "" {
		t.Error("Handoff.WorkerPromptFile is empty, want non-empty (issue #2059)")
	}
}

// TestAssembleOrchestratorWorkerPromptForbidsStoreBuild covers issue #2496:
// worker-prompt.md must forbid a worker from invoking any Nix store build
// (workers run fully concurrently -- issue #2059 -- so a store build in one
// worker is a store build in K workers at once) and must instead prescribe
// only fast per-file gates (nil diagnostics, shellcheck, scoped go vet/go
// test) that stay on PATH without a store round-trip.
func TestAssembleOrchestratorWorkerPromptForbidsStoreBuild(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	prompt := result.Handoff.WorkerPromptFile
	if prompt == "" {
		t.Fatal("Handoff.WorkerPromptFile is empty, want non-empty")
	}

	// Presence alone isn't enough: a CHECK section that *prescribes*
	// `nix build .#checks-inbox` still contains the phrases "nix build"
	// and "checks-inbox", and the doc's own unrelated "Do not expand
	// scope" line (line 6) already satisfies a bare "do not" search. Tie
	// the forbidding word to the same sentence as the store-build phrase
	// so a prescribing prompt actually fails this test. That alone isn't
	// sufficient either: the doc also names checks-inbox a second time,
	// descriptively, where the coordinator's own authoritative run
	// happens -- see TestSentenceForbidsCatchesWorkerPromptRegression,
	// which pins that this second, unrelated mention can't stand in for
	// the real forbidding one if it's ever deleted.
	forbidding := []string{"nix build", "checks-inbox"}
	for _, phrase := range forbidding {
		if !sentenceForbids(prompt, phrase) {
			t.Errorf("WorkerPromptFile doesn't forbid %q in the same sentence as a do-not/never/must-not phrase, want it named and forbidden together:\n%s", phrase, prompt)
		}
	}

	prescriptive := []string{"nil diagnostics", "shellcheck", "go vet", "go test"}
	for _, phrase := range prescriptive {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("WorkerPromptFile missing sanctioned per-file gate %q, want it prescribed:\n%s", phrase, prompt)
		}
	}
}

// TestAssembleOrchestratorWorkerPromptGateFailureSurfacesInResult pins
// issue #2496 AC4: a worker slice whose per-file gate fails must surface
// that failure in its result file, not just its final chat report, so the
// coordinator can scope the fix without re-running anything. A byte-level
// golden diff doesn't pin this -- it would keep "passing" even if a future
// edit dropped the result-file destination and left only the final-report
// path. Isolate the paragraph naming "result file" and assert it also ties
// together the gate-failure content (failing command + output/exit status)
// that must land there.
func TestAssembleOrchestratorWorkerPromptGateFailureSurfacesInResult(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	prompt := result.Handoff.WorkerPromptFile
	if prompt == "" {
		t.Fatal("Handoff.WorkerPromptFile is empty, want non-empty")
	}

	var resultFileParagraph string
	for _, paragraph := range strings.Split(prompt, "\n\n") {
		if strings.Contains(paragraph, "result file") {
			resultFileParagraph = paragraph
			break
		}
	}
	if resultFileParagraph == "" {
		t.Fatalf("no paragraph in WorkerPromptFile mentions %q, want the gate-failure report tied to a result file destination:\n%s", "result file", prompt)
	}

	for _, phrase := range []string{"failing command", "exit status"} {
		if !strings.Contains(resultFileParagraph, phrase) {
			t.Errorf("paragraph naming \"result file\" doesn't also name %q, want the gate-failure content and its result-file destination tied together in one paragraph, got paragraph:\n%s", phrase, resultFileParagraph)
		}
	}
}

// TestAssembleOrchestratorCoordinatorChecksInboxOnce pins issue #2496 AC2:
// fragments/coordinator.md must tell the coordinator that `checks-inbox`
// runs exactly once, on the fully-integrated tree, in CHECK -- and that
// this once-only rule **overrides** CHECK's own "before each commit, run
// the repo's own checks green" instruction for the per-slice integration
// commits the coordinator authors while cherry-picking each worker's
// branch. A byte-level golden diff alone doesn't catch a regression here:
// it's reflow-fragile (any unrelated wording tweak anywhere in the
// document trips it, whether or not the tweak is semantically related) and
// asserts nothing about intent -- a golden fixture updated to match a
// broken doc goes on "passing" forever. Unlike
// TestAssembleOrchestratorWorkerPromptForbidsStoreBuild's sentenceForbids
// check, this rule isn't just a forbidding sentence around a single
// phrase; it's an explicit override tying two rules together, so this
// test isolates the paragraph carrying both "checks-inbox" and
// "overrides" and asserts that same paragraph also names the CHECK rule
// it overrides.
func TestAssembleOrchestratorCoordinatorChecksInboxOnce(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.AgentsJSONTemplate = `{"worker":{"model":"m"}}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// coordinator.md is spliced directly into the main assembled prompt via
	// the WORKER_PROVISIONED gate -> COORDINATOR_STEP var (testdata/registry.json),
	// not into a separate Handoff field.
	prompt := result.Prompt

	if !sentenceForbids(prompt, "checks-inbox") {
		t.Errorf("Prompt doesn't forbid %q in the same sentence as a do-not/never/must-not phrase, want the per-slice checks-inbox run forbidden:\n%s", "checks-inbox", prompt)
	}

	var overrideParagraph string
	for _, paragraph := range strings.Split(prompt, "\n\n") {
		if strings.Contains(paragraph, "checks-inbox") && strings.Contains(paragraph, "overrides") {
			overrideParagraph = paragraph
			break
		}
	}
	if overrideParagraph == "" {
		t.Fatalf("no paragraph in Prompt contains both %q and %q, want the once-only checks-inbox rule to name its own override:\n%s", "checks-inbox", "overrides", prompt)
	}
	if !strings.Contains(overrideParagraph, "before each commit") {
		t.Errorf("paragraph carrying checks-inbox/overrides doesn't name CHECK's \"before each commit\" rule, want it tied to that rule explicitly, got paragraph:\n%s", overrideParagraph)
	}
}

// sentenceForbids reports whether prompt contains phrase inside a sentence
// (a hard-wrapped line, further split on ". ") that also carries an
// unambiguous forbidding word (do not / never / must not) -- catches a
// prompt that merely *mentions* phrase elsewhere (prescriptively, or in an
// unrelated sentence) without actually forbidding it. Splitting on line
// breaks first, then ". ", keeps a markdown doc's own hard-wrapped
// paragraphs (where a sentence's mid-point line break carries no trailing
// space) from being silently merged with the next paragraph.
func sentenceForbids(prompt, phrase string) bool {
	for _, line := range strings.Split(prompt, "\n") {
		for _, sentence := range strings.Split(line, ". ") {
			if !strings.Contains(sentence, phrase) {
				continue
			}
			lower := strings.ToLower(sentence)
			if strings.Contains(lower, "do not") || strings.Contains(lower, "never") || strings.Contains(lower, "must not") {
				return true
			}
		}
	}
	return false
}

// TestSentenceForbidsRejectsPrescriptiveMention pins the exact failure mode
// TestAssembleOrchestratorWorkerPromptForbidsStoreBuild must not reproduce:
// a CHECK section that *prescribes* running checks-inbox mentions both
// "nix build" and "checks-inbox" (so a bare substring-presence check would
// pass it) and an unrelated sentence elsewhere may say "do not" about
// something else entirely (so a whole-document "do not" search would also
// pass it). sentenceForbids must reject both.
func TestSentenceForbidsRejectsPrescriptiveMention(t *testing.T) {
	prescriptive := "Do not expand scope beyond your slice.\n\n" +
		"## CHECK\n\n" +
		"Run `nix build .#checks-inbox` to check your slice before committing."
	if sentenceForbids(prescriptive, "nix build") {
		t.Error("sentenceForbids(prescriptive, \"nix build\") = true, want false: the prompt prescribes nix build, it never forbids it")
	}
	if sentenceForbids(prescriptive, "checks-inbox") {
		t.Error("sentenceForbids(prescriptive, \"checks-inbox\") = true, want false: the prompt prescribes checks-inbox, it never forbids it")
	}

	forbidding := "Do not run `nix build` (any target, including `checks-inbox`), " +
		"or anything else that triggers a Nix store round-trip."
	if !sentenceForbids(forbidding, "nix build") {
		t.Error("sentenceForbids(forbidding, \"nix build\") = false, want true")
	}
	if !sentenceForbids(forbidding, "checks-inbox") {
		t.Error("sentenceForbids(forbidding, \"checks-inbox\") = false, want true")
	}
}

// TestSentenceForbidsCatchesWorkerPromptRegression pins the exact false-pass
// the synthetic literals above can't reproduce: worker-prompt.md names
// "checks-inbox" twice -- once forbidding it to the worker, once
// descriptively, where it says the coordinator owns the authoritative run.
// A synthetic "prescriptive" string never puts a forbidding word near that
// second, unrelated mention, so it can't prove the real doc is safe if the
// first, true forbidding mention is ever deleted. Mutate the real assembled
// prompt -- not a hand-written literal -- to remove only that one true
// forbidding mention, and confirm no other real mention of "checks-inbox"
// left in the doc still trips sentenceForbids.
func TestSentenceForbidsCatchesWorkerPromptRegression(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	prompt := result.Handoff.WorkerPromptFile
	if !sentenceForbids(prompt, "checks-inbox") {
		t.Fatal("WorkerPromptFile doesn't forbid \"checks-inbox\" today, want a starting point that passes before mutation")
	}

	const trueForbiddingMention = "(any target, including `checks-inbox`)"
	if !strings.Contains(prompt, trueForbiddingMention) {
		t.Fatalf("WorkerPromptFile doesn't contain %q, want the real forbidding mention this test deletes to still be there:\n%s", trueForbiddingMention, prompt)
	}
	mutated := strings.Replace(prompt, trueForbiddingMention, "(any target)", 1)
	if !strings.Contains(mutated, "checks-inbox") {
		t.Fatal("mutation removed every mention of checks-inbox, want at least one other real mention left over to prove this test isn't vacuous")
	}

	if sentenceForbids(mutated, "checks-inbox") {
		t.Error("sentenceForbids(mutated, \"checks-inbox\") = true, want false: deleting the one true forbidding mention must not leave a different, unrelated real mention that still passes")
	}
}

// TestAssembleOrchestratorBoxReadOnlyCovered covers that
// OrchestratorEnabled + BoxWriteEnabled == false is a covered cell now
// (the "filer relay" precondition axis, issue #2353), not rejected by
// checkCoveredCell.
func TestAssembleOrchestratorBoxReadOnlyCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.BoxWriteEnabled = false

	if _, err := Assemble(env, reg); err != nil {
		t.Errorf("Assemble: %v, want nil error (orchestrator on + box read-only is covered)", err)
	}
}

// TestAssembleOrchestratorSkillsAbsentCovered covers that
// OrchestratorEnabled with SkillsFound == "" and every *SkillBaked flag
// false ("skills-absent" cell, issue #2353) is covered, and that the
// rendered prompt omits the skill-preamble fragment text -- mirroring
// TestAssembleCoveredCellRendersPrompt's fragment-gate-off assertions.
func TestAssembleOrchestratorSkillsAbsentCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.SkillsFound = ""
	env.CavemanSkillBaked = false
	env.TDDSkillBaked = false
	env.CommitSkillBaked = false
	env.CodeReviewSkillBaked = false

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if strings.Contains(result.Prompt, "Skills available:") {
		t.Errorf("Prompt contains skill-preamble.md fragment text, want it absent (SKILLS_FOUND gate off):\n%s", result.Prompt)
	}
}

// TestAssembleOrchestratorOffSkillsAbsentCovered mirrors
// TestAssembleOrchestratorSkillsAbsentCovered for the orchestrator-off
// branch (issue #2354): SkillsFound == "" and every *SkillBaked flag false
// ("skills-absent") is covered when the orchestrator is off too, not just
// when it's on -- most real bats fixtures and many real Consumers bake zero
// skills, and there's no reason skills-absent should only be safe when the
// orchestrator happens to be on.
func TestAssembleOrchestratorOffSkillsAbsentCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.SkillsFound = ""
	env.CavemanSkillBaked = false
	env.TDDSkillBaked = false
	env.CommitSkillBaked = false
	env.CodeReviewSkillBaked = false

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v, want nil error (orchestrator off + skills fully absent is covered)", err)
	}

	if strings.Contains(result.Prompt, "Skills available:") {
		t.Errorf("Prompt contains skill-preamble.md fragment text, want it absent (SKILLS_FOUND gate off):\n%s", result.Prompt)
	}
}

// TestAssemblePartialSkillsCovered covers that a PARTIAL skill-baked
// combination -- here, only the tdd skill baked, the other three not -- is a
// covered cell for the orchestrator-off branch, not rejected by
// checkCoveredCell (issue #2354): each of the four per-skill gates
// (CAVEMAN_BAKED, TDD_BAKED, COMMIT_BAKED, CODE_REVIEW_BAKED) is a fully
// independent boolean with no cross-dependency, matching Gates()'s own
// implementation and lib/image.nix's per-skill baking -- a real Consumer can
// legitimately bake any subset of the four. Only TDD_STEP's fragment text
// must render; CAVEMAN_STEP/COMMIT_STEP/CODE_REVIEW_STEP must not.
func TestAssemblePartialSkillsCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.SkillsFound = "tdd"
	env.CavemanSkillBaked = false
	env.TDDSkillBaked = true
	env.CommitSkillBaked = false
	env.CodeReviewSkillBaked = false

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v, want nil error (partial skill-baked combination is covered)", err)
	}

	if !strings.Contains(result.Prompt, "Use the `/tdd` skill to run the test-first loop") {
		t.Errorf("Prompt missing tdd-default.md fragment text (TDD_BAKED gate on):\n%s", result.Prompt)
	}
	for _, unwanted := range []string{
		"Default to the `/caveman` skill",
		"Use the `/commit` skill to write every commit message",
		"Run the `/code-review` skill FIRST",
	} {
		if strings.Contains(result.Prompt, unwanted) {
			t.Errorf("Prompt contains %q, want only TDD_STEP to render (partial skill-baked combination)", unwanted)
		}
	}
}

// TestAssembleOrchestratorPartialSkillsCovered mirrors
// TestAssemblePartialSkillsCovered for the orchestrator-on branch (issue
// #2354): a partial skill-baked combination is covered there too, not just
// when the orchestrator is off.
func TestAssembleOrchestratorPartialSkillsCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.SkillsFound = "tdd"
	env.CavemanSkillBaked = false
	env.TDDSkillBaked = true
	env.CommitSkillBaked = false
	env.CodeReviewSkillBaked = false

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v, want nil error (orchestrator on + partial skill-baked combination is covered)", err)
	}

	if !strings.Contains(result.Prompt, "Use the `/tdd` skill to run the test-first loop") {
		t.Errorf("Prompt missing tdd-default.md fragment text (TDD_BAKED gate on):\n%s", result.Prompt)
	}
	for _, unwanted := range []string{
		"Default to the `/caveman` skill",
		"Use the `/commit` skill to write every commit message",
		"Run the `/code-review` skill FIRST",
	} {
		if strings.Contains(result.Prompt, unwanted) {
			t.Errorf("Prompt contains %q, want only TDD_STEP to render (partial skill-baked combination)", unwanted)
		}
	}
}

// TestAssembleOrchestratorFixPassCovered covers that OrchestratorEnabled ==
// true combined with FixPass > 0 is a covered cell (issue #2354): Gates()'s
// own implementation and Assemble's base-template switch already handle
// this combination correctly regardless of the orchestrator flag, and it is
// reachable in real production (ORCHESTRATOR_ENABLED is a static
// per-Consumer knob forwarded unchanged to fix-pass Boxes). Assemble must
// succeed and render fix-prompt.md, and Handoff.ReviewPromptFile must stay
// empty -- review_prompt_rendered only ever populates on the default
// fresh-work-dispatch path (kind "work", FixPass == 0), never a warm fix
// pass. Handoff.ReviewModel, by contrast, is a separate, unconditional
// extraction from AgentsJSONTemplate's "reviewer" key whenever the
// orchestrator is on (entrypoint.sh: 1086-1101, see Handoff's doc comment)
// -- it is NOT gated to the fresh-work-dispatch path, so with a reviewer
// configured it still populates here.
func TestAssembleOrchestratorFixPassCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.FixPass = 1
	env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x"}}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v, want nil error (orchestrator on + fix pass is covered)", err)
	}

	if !strings.Contains(result.Prompt, "This is a warm fix pass, not a fresh implementation") {
		t.Errorf("Prompt missing fix-prompt.md's distinguishing text:\n%s", result.Prompt)
	}
	if result.Handoff.ReviewPromptFile != "" {
		t.Errorf("Handoff.ReviewPromptFile = %q, want empty (fix pass, not the default fresh-work-dispatch path)", result.Handoff.ReviewPromptFile)
	}
	if result.Handoff.ReviewModel != "review-model-x" {
		t.Errorf("Handoff.ReviewModel = %q, want %q (extraction is unconditional whenever the orchestrator is on)", result.Handoff.ReviewModel, "review-model-x")
	}
	if result.Handoff.WorkerPromptFile != "" {
		t.Errorf("Handoff.WorkerPromptFile = %q, want empty (fix pass, not the default fresh-work-dispatch path, issue #2059)", result.Handoff.WorkerPromptFile)
	}
}

// TestAssembleOrchestratorResearchCovered covers that OrchestratorEnabled ==
// true combined with DispatchKind == "research" is a covered cell (issue
// #2354), for the same reasons as TestAssembleOrchestratorFixPassCovered:
// Assemble must succeed and render research-prompt.md, and
// Handoff.ReviewPromptFile must stay empty -- a research dispatch never
// reviews (ADR 0022). Handoff.ReviewModel still populates from a configured
// reviewer, same as the fix-pass cell (see that test's comment and
// Handoff's doc comment).
func TestAssembleOrchestratorResearchCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.DispatchKind = "research"
	env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x"}}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v, want nil error (orchestrator on + research kind is covered)", err)
	}

	if !strings.Contains(result.Prompt, "This is a research\ndispatch (ADR 0022)") {
		t.Errorf("Prompt missing research-prompt.md's distinguishing text:\n%s", result.Prompt)
	}
	if result.Handoff.ReviewPromptFile != "" {
		t.Errorf("Handoff.ReviewPromptFile = %q, want empty (research dispatch, not the default fresh-work-dispatch path)", result.Handoff.ReviewPromptFile)
	}
	if result.Handoff.ReviewModel != "review-model-x" {
		t.Errorf("Handoff.ReviewModel = %q, want %q (extraction is unconditional whenever the orchestrator is on)", result.Handoff.ReviewModel, "review-model-x")
	}
	if result.Handoff.WorkerPromptFile != "" {
		t.Errorf("Handoff.WorkerPromptFile = %q, want empty (research dispatch, not the default fresh-work-dispatch path, issue #2059)", result.Handoff.WorkerPromptFile)
	}
}

// TestAssembleOrchestratorOffReviewerFlowsThroughGenericLoop is a
// regression guard for renderAgentsJSON's signature change (issue #2353):
// with the orchestrator off, a reviewer key in AgentsJSONTemplate is NOT
// dropped -- it flows through the generic per-agent injection loop like any
// other roster entry, same as before this slice.
func TestAssembleOrchestratorOffReviewerFlowsThroughGenericLoop(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x"}}`
	env.AgentsPromptFiles = `{"reviewer":"fragments/tdd-default.md"}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.AgentsJSON), &parsed); err != nil {
		t.Fatalf("unmarshal AgentsJSON: %v\n%s", err, result.AgentsJSON)
	}
	reviewerRaw, ok := parsed["reviewer"]
	if !ok {
		t.Fatal("AgentsJSON missing reviewer key, want it present (orchestrator off, no reviewer-drop)")
	}
	var reviewer struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(reviewerRaw, &reviewer); err != nil {
		t.Fatalf("unmarshal reviewer entry: %v", err)
	}
	if !strings.Contains(reviewer.Prompt, "/tdd") {
		t.Errorf("reviewer.prompt missing substituted tdd-default.md content: %q", reviewer.Prompt)
	}
	if result.Handoff.ReviewModel != "" {
		t.Errorf("Handoff.ReviewModel = %q, want empty (orchestrator off)", result.Handoff.ReviewModel)
	}
	if result.Handoff.ReviewEffort != "" {
		t.Errorf("Handoff.ReviewEffort = %q, want empty (orchestrator off)", result.Handoff.ReviewEffort)
	}
	if result.Handoff.ReviewPromptFile != "" {
		t.Errorf("Handoff.ReviewPromptFile = %q, want empty (orchestrator off)", result.Handoff.ReviewPromptFile)
	}
	if result.Handoff.WorkerPromptFile != "" {
		t.Errorf("Handoff.WorkerPromptFile = %q, want empty (orchestrator off, issue #2059)", result.Handoff.WorkerPromptFile)
	}
}

// writeAgentFile writes a baked opencode agent file fixture with real
// frontmatter shape and a placeholder body distinguishable from any real
// rendered prompt -- the Go-side twin of
// tests/entrypoint-opencode-agent-files.bats's write_agent_file.
func writeAgentFile(t *testing.T, path, desc string) {
	t.Helper()
	content := "---\n" +
		"description: \"" + desc + "\"\n" +
		"mode: \"subagent\"\n" +
		"model: \"opus\"\n" +
		"---\n" +
		"placeholder body for " + desc + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// agentFileFrontmatter returns every line up to and including the second
// "---" fence line, the Go-side twin of the bats helper of the same name.
func agentFileFrontmatter(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	lines := strings.Split(string(data), "\n")
	fences := 0
	for i, line := range lines {
		if line == "---" {
			fences++
			if fences == 2 {
				return strings.Join(lines[:i+1], "\n")
			}
		}
	}
	return string(data)
}

// agentFileBody returns everything after the second "---" fence line.
func agentFileBody(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	lines := strings.Split(string(data), "\n")
	fences := 0
	for i, line := range lines {
		if line == "---" {
			fences++
			if fences == 2 {
				return strings.Join(lines[i+1:], "\n")
			}
		}
	}
	return ""
}

// TestAssembleDriverAgentFilesRewrite covers entrypoint.sh's
// DRIVER_AGENT_FILES_DIR-gated file-rewrite twin of the --agents JSON
// injection loop (entrypoint.sh: 1128-1187): with the orchestrator off, a
// baked agent file's frontmatter is preserved and its body is overwritten
// with the substituted prompt file text.
func TestAssembleDriverAgentFilesRewrite(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")
	frontmatterBefore := agentFileFrontmatter(t, filepath.Join(dir, "scout.md"))

	env := coveredEnv()
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"scout":"fragments/tdd-default.md"}`

	if _, err := Assemble(env, reg); err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	body := agentFileBody(t, filepath.Join(dir, "scout.md"))
	if body == "placeholder body for scout\n" || strings.TrimSpace(body) == "" {
		t.Errorf("scout.md body not rewritten: %q", body)
	}
	if !strings.Contains(body, "/tdd") {
		t.Errorf("scout.md body missing substituted tdd-default.md content: %q", body)
	}
	if agentFileFrontmatter(t, filepath.Join(dir, "scout.md")) != frontmatterBefore {
		t.Errorf("scout.md frontmatter changed, want unchanged")
	}
}

// TestAssembleDriverAgentFilesReviewerDropOrchestratorOn covers
// entrypoint.sh's file-based reviewer-drop/model-extraction twin
// (entrypoint.sh: 1141-1156): with the orchestrator on, reviewer.md's
// `model:` frontmatter scalar populates Handoff.ReviewModel and the file is
// then removed, while a non-reviewer roster file (scout.md) still gets its
// body rewritten.
func TestAssembleDriverAgentFilesReviewerDropOrchestratorOn(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")
	writeAgentFile(t, filepath.Join(dir, "reviewer.md"), "reviewer")

	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"scout":"fragments/tdd-default.md","reviewer":"fragments/tdd-default.md"}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "reviewer.md")); !os.IsNotExist(err) {
		t.Errorf("reviewer.md still exists (or unexpected stat error %v), want removed", err)
	}
	body := agentFileBody(t, filepath.Join(dir, "scout.md"))
	if !strings.Contains(body, "/tdd") {
		t.Errorf("scout.md body missing substituted tdd-default.md content: %q", body)
	}
	if result.Handoff.ReviewModel != "opus" {
		t.Errorf("Handoff.ReviewModel = %q, want %q", result.Handoff.ReviewModel, "opus")
	}
}

// TestAssembleDriverAgentFilesReviewModelPrecedence covers the exact
// overwrite-precedence rule between the two reviewer-model extraction paths
// (entrypoint.sh: 1096 JSON path, then 1152-1153 file path): when
// DriverAgentFilesDir's reviewer.md exists, its frontmatter model wins over
// whatever AgentsJSONTemplate's .reviewer.model already set -- the file path
// runs after the JSON path and unconditionally overwrites.
func TestAssembleDriverAgentFilesReviewModelPrecedence(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("reviewer.md present overwrites the JSON-path value", func(t *testing.T) {
		dir := t.TempDir()
		writeAgentFile(t, filepath.Join(dir, "reviewer.md"), "reviewer")

		env := coveredEnv()
		env.OrchestratorEnabled = true
		env.DriverAgentFilesDir = dir
		env.AgentsJSONTemplate = `{"reviewer":{"model":"haiku"}}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if result.Handoff.ReviewModel != "opus" {
			t.Errorf("Handoff.ReviewModel = %q, want %q (file path wins)", result.Handoff.ReviewModel, "opus")
		}
	})

	t.Run("reviewer.md absent leaves the JSON-path value unchanged", func(t *testing.T) {
		dir := t.TempDir()
		writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")

		env := coveredEnv()
		env.OrchestratorEnabled = true
		env.DriverAgentFilesDir = dir
		env.AgentsJSONTemplate = `{"reviewer":{"model":"haiku"}}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if result.Handoff.ReviewModel != "haiku" {
			t.Errorf("Handoff.ReviewModel = %q, want %q (JSON value survives)", result.Handoff.ReviewModel, "haiku")
		}
	})
}

// TestAssembleDriverAgentFilesSkipsMissingBakedFile covers that a roster
// name with no baked .md file on disk (opencode's empty-model-drops-the-file
// semantics, or a reviewer.md just removed above) is silently skipped, not
// an error.
func TestAssembleDriverAgentFilesSkipsMissingBakedFile(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	// No worker.md on disk at all.

	env := coveredEnv()
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"worker":"fragments/tdd-default.md"}`

	if _, err := Assemble(env, reg); err != nil {
		t.Fatalf("Assemble: %v, want nil error (missing baked file is a silent skip)", err)
	}
}

// TestAssembleDriverAgentFilesSkipsMissingPromptFile covers that a roster
// entry whose looked-up prompt file doesn't exist under PromptsDir leaves
// the on-disk agent file untouched, without error.
func TestAssembleDriverAgentFilesSkipsMissingPromptFile(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")
	before, err := os.ReadFile(filepath.Join(dir, "scout.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	env := coveredEnv()
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"scout":"fragments/does-not-exist.md"}`

	if _, err := Assemble(env, reg); err != nil {
		t.Fatalf("Assemble: %v, want nil error (missing prompt file is a silent skip)", err)
	}

	after, err := os.ReadFile(filepath.Join(dir, "scout.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("scout.md changed, want untouched:\nbefore: %q\nafter:  %q", before, after)
	}
}

// TestAssembleDriverAgentFilesFrontmatterFallbackTrimsTrailingNewlines
// covers frontmatterOf's no-second-fence fallback (a file with only one
// "---" fence line): bash's equivalent captures the frontmatter via
// $(awk ...) command substitution, which strips every trailing newline, so
// the fallback must too, rather than returning the file's raw trailing
// newline(s) verbatim -- otherwise rewriteAgentFiles' `frontmatter + "\n" +
// rendered + "\n"` join produces a spurious blank line the bash original
// never would have.
func TestAssembleDriverAgentFilesFrontmatterFallbackTrimsTrailingNewlines(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()

	const markerLine = "no second fence here"
	// Only one "---" fence line -- frontmatterOf never reaches its second
	// fence, so it falls through to the fallback branch. Two trailing
	// newlines exercise that the fallback strips all of them, not just one.
	fixture := "---\n" +
		"description: \"scout\"\n" +
		"\n" +
		markerLine + "\n" +
		"\n"
	agentFilePath := filepath.Join(dir, "scout.md")
	if err := os.WriteFile(agentFilePath, []byte(fixture), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := coveredEnv()
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"scout":"fragments/tdd-default.md"}`

	if _, err := Assemble(env, reg); err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	after, err := os.ReadFile(agentFilePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(string(after), "\n")
	found := false
	for i, line := range lines {
		if line != markerLine {
			continue
		}
		found = true
		if i+1 >= len(lines) || lines[i+1] == "" {
			t.Errorf("blank line after fallback frontmatter, want the rendered prompt to follow immediately: %q", after)
		}
		break
	}
	if !found {
		t.Fatalf("fallback frontmatter marker line %q not found in rewritten file: %q", markerLine, after)
	}
}

// TestAssembleDriverAgentFilesReviewerModelMissingFallback covers
// reviewerModelFrontmatter's no-`model:`-line fallback (entrypoint.sh's
// `sed -n 's/^model: //p'` finding no match): with the orchestrator on and
// a baked reviewer.md whose frontmatter has no `model:` line,
// Handoff.ReviewModel stays "".
func TestAssembleDriverAgentFilesReviewerModelMissingFallback(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()

	fixture := "---\n" +
		"description: \"reviewer\"\n" +
		"mode: \"subagent\"\n" +
		"---\n" +
		"placeholder body for reviewer\n"
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte(fixture), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"reviewer":"fragments/tdd-default.md"}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if result.Handoff.ReviewModel != "" {
		t.Errorf("Handoff.ReviewModel = %q, want empty (no model: line in reviewer.md frontmatter)", result.Handoff.ReviewModel)
	}
}
