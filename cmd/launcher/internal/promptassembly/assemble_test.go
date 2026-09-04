package promptassembly

import (
	"encoding/json"
	"errors"
	"fmt"
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

// markerGrammarSpindriftCommentExcerpt is a contiguous, verbatim excerpt of
// caveman-default-research.md's marker-grammar exemption paragraph, copied
// from the fragment itself, spanning from "machine-parsed marker grammar"
// through its SPINDRIFT_COMMENT mention. Asserting this single literal
// (rather than locating the first "machine-parsed marker grammar" substring
// anywhere in the whole rendered prompt and slicing out the paragraph that
// follows it) ties the phrase directly to SPINDRIFT_COMMENT in one string,
// so the assertion can't silently mis-scope itself if that phrase ever
// starts appearing in more than one fragment within the same rendered
// prompt.
const markerGrammarSpindriftCommentExcerpt = "The machine-parsed marker grammar is exempt too: the `SPINDRIFT_OUTCOME`\nline and its `note=` field, and any host-relay signal line such as\n`SPINDRIFT_COMMENT`"

// coveredEnv returns a fixture Env sitting exactly in Assemble's covered
// cell (see checkCoveredCell): github tracker, github forge, a read-write
// box, dispatch kind "work", a fresh box (FixPass == 0), the orchestrator
// off, and every skill baked. TrackerAxisRead/TrackerAxisWrite/
// TrackerAxisFiler and ForgeBackend are set to nix's own precomputed
// resolution of the github tracker/forge (issue #2533) -- since Gates no
// longer re-derives them in-box, a fixture claiming to sit in the github
// tracker/forge cell must carry their already-resolved axis values
// directly. ReviewLoopInline mirrors !OrchestratorEnabled for the same
// reason. Tests mutate a copy to move a single axis off the covered cell.
func coveredEnv() Env {
	return Env{
		IssueTracker:         "github",
		TrackerAxisRead:      "GITHUB",
		TrackerAxisWrite:     "GITHUB",
		TrackerAxisFiler:     "GH",
		CodeForge:            "github",
		ForgeBackend:         "GH",
		BoxWriteEnabled:      true,
		DispatchKind:         "work",
		FixPass:              0,
		OrchestratorEnabled:  false,
		ReviewLoopInline:     true,
		SkillsFound:          "caveman, tdd, commit, code-review",
		CavemanSkillBaked:    true,
		TDDSkillBaked:        true,
		CommitSkillBaked:     true,
		CodeReviewSkillBaked: true,
		AutoFormatSkillBaked: true,
		AutoLintSkillBaked:   true,
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

// localTrackerEnv returns a copy of coveredEnv with IssueTracker (and its
// nix-precomputed axis fields, issue #2533) set to "local" -- otherwise
// identical, still a read-write box with every other axis at its
// covered-cell value.
func localTrackerEnv() Env {
	env := coveredEnv()
	env.IssueTracker = "local"
	env.TrackerAxisRead = "LOCAL"
	env.TrackerAxisWrite = ""
	env.TrackerAxisFiler = "GH"
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

// fragmentText reads the named file out of promptsDir's fragments directory
// and returns its trimmed content, failing the test on error. Its one caller
// asserts a whole fragment's body is absent from a gate-off prompt; an
// earlier review round found a hand-copied excerpt there had silently
// stopped matching once the fragment's wording changed.
func fragmentText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(promptsDir, "fragments", name))
	if err != nil {
		t.Fatalf("read fragment %s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

// agentPromptFromJSON decodes agentsJSON (an Assemble result's AgentsJSON)
// and returns the rendered prompt for the named agent, failing the test if
// the JSON doesn't parse or the agent is missing -- the decode-and-lookup
// shape TestAssembleScoutPromptCavemanAndSkillPreamble and
// TestAssembleWorkerPromptCavemanAndSkillPreamble's subtests all repeat.
func agentPromptFromJSON(t *testing.T, agentsJSON, agent string) string {
	t.Helper()
	var parsed map[string]struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(agentsJSON), &parsed); err != nil {
		t.Fatalf("unmarshal AgentsJSON: %v\n%s", err, agentsJSON)
	}
	entry, ok := parsed[agent]
	if !ok {
		t.Fatalf("AgentsJSON missing %s entry", agent)
	}
	return entry.Prompt
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

// TestRenderText verifies the exported RenderText helper (issue #2060
// review finding: the orchestrator's own cherry-pick conflict-resolve
// guidance reuses this exact ${NAME} substitution mechanism at runtime,
// rather than hand-rolling a bespoke strings.ReplaceAll pass) substitutes
// every ${NAME} token present in vars, leaves an unlisted ${OTHER} token
// untouched, and trims trailing newlines the same way renderFile does for
// an on-disk file's contents.
func TestRenderText(t *testing.T) {
	got := RenderText("A ${FOO} and a ${BAR}, but not ${BAZ}.\n\n", map[string]string{
		"FOO": "one",
		"BAR": "two",
	})
	want := "A one and a two, but not ${BAZ}."
	if got != want {
		t.Errorf("RenderText() = %q, want %q", got, want)
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
// ResumeAfterHold, and Handoff.ReviewPromptFile/ReviewModel/ReviewEffort
// plus Result.ReviewPromptText all stay empty.
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
			if result.ReviewPromptText != "" {
				t.Errorf("ReviewPromptText = %q, want empty", result.ReviewPromptText)
			}
			if result.Handoff.ReviewModel != "" {
				t.Errorf("Handoff.ReviewModel = %q, want empty", result.Handoff.ReviewModel)
			}
			if result.Handoff.ReviewEffort != "" {
				t.Errorf("Handoff.ReviewEffort = %q, want empty", result.Handoff.ReviewEffort)
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
		env.AgentsPromptFiles = `{"scout":"fragments/tdd-baked.md"}`

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
			t.Errorf("scout.prompt missing substituted tdd-baked.md content: %q", scout.Prompt)
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

// TestAssembleScoutPromptCavemanAndSkillPreamble covers issue #2706:
// scout-prompt.md, rendered through renderAgentsJSON's per-agent prompt
// lookup (Env.AgentsPromptFiles["scout"] -> "scout-prompt.md"), must carry
// the caveman-default narration directive and the skill-advertisement
// preamble when the caveman skill is baked and skills are present, and must
// carry neither -- no empty placeholder residue, no dangling literal
// ${CAVEMAN_STEP}/${SKILL_PREAMBLE} token -- when skills are absent.
func TestAssembleScoutPromptCavemanAndSkillPreamble(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("skills present", func(t *testing.T) {
		env := coveredEnv()
		env.AgentsJSONTemplate = `{"scout":{"model":"x"}}`
		env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		prompt := agentPromptFromJSON(t, result.AgentsJSON, "scout")
		if !strings.Contains(prompt, "Default to the `/caveman` skill") {
			t.Errorf("scout.prompt missing caveman-default.md fragment text: %q", prompt)
		}
		if !strings.Contains(prompt, "Skills available:") {
			t.Errorf("scout.prompt missing skill-preamble.md fragment text: %q", prompt)
		}
	})

	t.Run("skills absent", func(t *testing.T) {
		env := coveredEnv()
		env.SkillsFound = ""
		env.CavemanSkillBaked = false
		env.TDDSkillBaked = false
		env.CommitSkillBaked = false
		env.CodeReviewSkillBaked = false
		env.AgentsJSONTemplate = `{"scout":{"model":"x"}}`
		env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		prompt := agentPromptFromJSON(t, result.AgentsJSON, "scout")
		if strings.Contains(prompt, "/caveman") {
			t.Errorf("scout.prompt contains /caveman text, want absent (CAVEMAN_BAKED gate off): %q", prompt)
		}
		if strings.Contains(prompt, "Skills available:") {
			t.Errorf("scout.prompt contains skill-preamble.md fragment text, want absent (SKILLS_FOUND gate off): %q", prompt)
		}
		if strings.Contains(prompt, "${") {
			t.Errorf("scout.prompt still contains an unsubstituted ${...} token: %q", prompt)
		}
	})
}

// TestAssembleScoutPromptCitedExcerpts covers issue #3216: the scout's
// brief must require a cited verbatim excerpt -- quoted lines under a
// path:line anchor -- for each load-bearing Map / Invariants & gotchas /
// Suggested-approach claim, reusing #3158's "verbatim... not a paraphrase"
// excerpt format rather than forking a second one, so a coordinator can
// verify a claim by reading the cited lines instead of re-exploring the
// tree.
func TestAssembleScoutPromptCitedExcerpts(t *testing.T) {
	reg := loadTestRegistry(t)

	env := coveredEnv()
	env.AgentsJSONTemplate = `{"scout":{"model":"x"}}`
	env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	prompt := agentPromptFromJSON(t, result.AgentsJSON, "scout")
	if !strings.Contains(prompt, "cited verbatim excerpt") {
		t.Errorf("scout.prompt missing cited-verbatim-excerpt requirement (issue #3216): %q", prompt)
	}
	if !strings.Contains(prompt, "load-bearing claim") {
		t.Errorf("scout.prompt missing load-bearing-claim scoping (issue #3216): %q", prompt)
	}
	if !strings.Contains(prompt, "path:line anchor") {
		t.Errorf("scout.prompt missing path:line anchor requirement (issue #3216): %q", prompt)
	}
}

// TestAssembleWorkerPromptCavemanAndSkillPreamble covers issue #2706:
// worker-prompt.md, rendered through renderAgentsJSON's per-agent prompt
// lookup (Env.AgentsPromptFiles["worker"] -> "worker-prompt.md"), must carry
// the caveman-default narration directive and the skill-advertisement
// preamble when the caveman skill is baked and skills are present, and must
// carry neither -- no empty placeholder residue, no dangling literal
// ${CAVEMAN_STEP}/${SKILL_PREAMBLE} token -- when skills are absent.
func TestAssembleWorkerPromptCavemanAndSkillPreamble(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("skills present", func(t *testing.T) {
		env := coveredEnv()
		env.AgentsJSONTemplate = `{"worker":{"model":"x"}}`
		env.AgentsPromptFiles = `{"worker":"worker-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		prompt := agentPromptFromJSON(t, result.AgentsJSON, "worker")
		if !strings.Contains(prompt, "Default to the `/caveman` skill") {
			t.Errorf("worker.prompt missing caveman-default.md fragment text: %q", prompt)
		}
		if !strings.Contains(prompt, "Skills available:") {
			t.Errorf("worker.prompt missing skill-preamble.md fragment text: %q", prompt)
		}
		for _, marker := range []string{"SPINDRIFT_OUTCOME", "VERDICT: APPROVE", "VERDICT: BLOCK"} {
			if strings.Contains(prompt, marker) {
				t.Errorf("worker.prompt contains forbidden marker %q (issue #2059/#2491 quarantine), want absent: %q", marker, prompt)
			}
		}
	})

	t.Run("skills absent", func(t *testing.T) {
		env := coveredEnv()
		env.SkillsFound = ""
		env.CavemanSkillBaked = false
		env.TDDSkillBaked = false
		env.CommitSkillBaked = false
		env.CodeReviewSkillBaked = false
		env.AgentsJSONTemplate = `{"worker":{"model":"x"}}`
		env.AgentsPromptFiles = `{"worker":"worker-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		prompt := agentPromptFromJSON(t, result.AgentsJSON, "worker")
		if strings.Contains(prompt, "/caveman") {
			t.Errorf("worker.prompt contains /caveman text, want absent (CAVEMAN_BAKED gate off): %q", prompt)
		}
		if strings.Contains(prompt, "Skills available:") {
			t.Errorf("worker.prompt contains skill-preamble.md fragment text, want absent (SKILLS_FOUND gate off): %q", prompt)
		}
		if strings.Contains(prompt, "${") {
			t.Errorf("worker.prompt still contains an unsubstituted ${...} token: %q", prompt)
		}
	})
}

// TestAssembleWorkerPromptScoutBrief covers issue #3157: worker-prompt.md
// must direct the worker to read the scout's persisted brief at
// /tmp/brief.md before exploring the repo itself when a scout is
// provisioned (SCOUT_PROVISIONED, a plain passthrough of
// Env.ScoutProvisioned), and must carry neither the brief reference nor any
// dangling ${WORKER_SCOUT_BRIEF_STEP} residue when no scout is provisioned --
// the no-scout-in-the-roster case this gate exists to degrade gracefully
// for.
func TestAssembleWorkerPromptScoutBrief(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("scout provisioned", func(t *testing.T) {
		env := coveredEnv()
		env.ScoutProvisioned = true
		env.AgentsJSONTemplate = `{"worker":{"model":"x"}}`
		env.AgentsPromptFiles = `{"worker":"worker-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		prompt := agentPromptFromJSON(t, result.AgentsJSON, "worker")
		if !strings.Contains(prompt, "/tmp/brief.md") {
			t.Errorf("worker.prompt missing /tmp/brief.md reference: %q", prompt)
		}
		if !strings.Contains(prompt, "Read `/tmp/brief.md` before") {
			t.Errorf("worker.prompt missing worker-scout-brief.md fragment text: %q", prompt)
		}
	})

	t.Run("scout not provisioned", func(t *testing.T) {
		env := coveredEnv()
		env.ScoutProvisioned = false
		env.AgentsJSONTemplate = `{"worker":{"model":"x"}}`
		env.AgentsPromptFiles = `{"worker":"worker-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		prompt := agentPromptFromJSON(t, result.AgentsJSON, "worker")
		if strings.Contains(prompt, "/tmp/brief.md") {
			t.Errorf("worker.prompt contains /tmp/brief.md reference, want absent (SCOUT_PROVISIONED gate off): %q", prompt)
		}
		if strings.Contains(prompt, "${WORKER_SCOUT_BRIEF_STEP}") {
			t.Errorf("worker.prompt still contains an unsubstituted ${WORKER_SCOUT_BRIEF_STEP} token: %q", prompt)
		}
	})
}

// TestAssembleWorkerPromptBudgetCheckpointAndBatchedEdits covers issue
// #3159's worker-side counterpart to the coordinator's budget-sizing and
// checkpoint-handoff guidance (coordinator.md): worker-prompt.md must direct
// a worker nearing its stated turn budget to stop cleanly and return a
// remaining-work checkpoint a fresh worker can resume from, and must direct
// it to batch related edits behind one combined verification per group
// rather than an edit-then-check loop per line. Both directives are
// scout-independent, so this asserts against coveredEnv() directly rather
// than forking on ScoutProvisioned the way TestAssembleWorkerPromptScoutBrief
// does.
func TestAssembleWorkerPromptBudgetCheckpointAndBatchedEdits(t *testing.T) {
	reg := loadTestRegistry(t)

	env := coveredEnv()
	env.AgentsJSONTemplate = `{"worker":{"model":"x"}}`
	env.AgentsPromptFiles = `{"worker":"worker-prompt.md"}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	prompt := agentPromptFromJSON(t, result.AgentsJSON, "worker")
	for _, want := range []string{
		"turn budget",
		"stop cleanly",
		"remaining-work checkpoint",
		"fresh worker",
		"one combined verification per group",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("worker.prompt missing budget/checkpoint/batched-edits text %q (issue #3159):\n%s", want, prompt)
		}
	}
}

// TestAssembleIssuePromptScoutSection covers issue #3157's SCOUT_PROVISIONED/
// SCOUT_ABSENT paired fork of the `# SCOUT` section (scout-delegate.md/
// scout-absent.md, same exactly-one-on shape as REVIEW_LOOP_INLINE/
// REVIEW_LOOP_ORCHESTRATOR): a scout-provisioned run must carry the
// delegate-and-persist-the-brief instructions and the /tmp/brief.md path,
// never the scout-absent arm's text; a scout-absent run must carry the
// scout-absent arm and no /tmp/brief.md reference at all, and neither run
// may leave an unsubstituted ${SCOUT_DELEGATE_STEP}/${SCOUT_ABSENT_STEP} token
// in the rendered prompt.
func TestAssembleIssuePromptScoutSection(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("scout provisioned", func(t *testing.T) {
		env := coveredEnv()
		env.ScoutProvisioned = true

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if !strings.Contains(result.Prompt, "Delegate exploration to the `scout` subagent") {
			t.Errorf("Prompt missing scout-delegate.md fragment text:\n%s", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "/tmp/brief.md") {
			t.Errorf("Prompt missing /tmp/brief.md reference:\n%s", result.Prompt)
		}
		if strings.Contains(result.Prompt, "No `scout` subagent is provisioned") {
			t.Errorf("Prompt contains scout-absent.md fragment text, want absent (SCOUT_ABSENT gate off):\n%s", result.Prompt)
		}
	})

	t.Run("scout absent", func(t *testing.T) {
		env := coveredEnv()
		env.ScoutProvisioned = false

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if !strings.Contains(result.Prompt, "No `scout` subagent is provisioned") {
			t.Errorf("Prompt missing scout-absent.md fragment text:\n%s", result.Prompt)
		}
		if strings.Contains(result.Prompt, "/tmp/brief.md") {
			t.Errorf("Prompt contains /tmp/brief.md reference, want absent (no scout, no brief written):\n%s", result.Prompt)
		}
		if strings.Contains(result.Prompt, "${SCOUT_DELEGATE_STEP}") || strings.Contains(result.Prompt, "${SCOUT_ABSENT_STEP}") {
			t.Errorf("Prompt still contains an unsubstituted SCOUT step token:\n%s", result.Prompt)
		}
		if strings.Contains(result.Prompt, "${COORDINATOR_SCOUT_BRIEF_STEP}") {
			t.Errorf("Prompt still contains an unsubstituted ${COORDINATOR_SCOUT_BRIEF_STEP} token:\n%s", result.Prompt)
		}
	})
}

// TestAssembleScoutDelegateCitedExcerpts covers issue #3216's addition to
// scout-delegate.md: the delegation ask to the scout subagent now requires a
// cited verbatim excerpt per load-bearing claim, not just paths and line
// refs, and the coordinator's trust sentence is reframed around that
// evidence -- re-search only when a citation itself is wrong or missing,
// not on any wrong/missing pointer. A scout-absent run must carry neither
// phrase and no dangling ${SCOUT_DELEGATE_STEP}/${SCOUT_ABSENT_STEP} token.
func TestAssembleScoutDelegateCitedExcerpts(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("scout provisioned", func(t *testing.T) {
		env := coveredEnv()
		env.ScoutProvisioned = true

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		for _, want := range []string{
			"cited with a verbatim excerpt",
			"path:line anchor",
			"Re-search only when a citation is wrong",
		} {
			if !strings.Contains(result.Prompt, want) {
				t.Errorf("Prompt missing scout-delegate.md cited-excerpt text %q (issue #3216):\n%s", want, result.Prompt)
			}
		}
	})

	t.Run("scout absent", func(t *testing.T) {
		env := coveredEnv()
		env.ScoutProvisioned = false

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		for _, unwanted := range []string{
			"cited with a verbatim excerpt",
			"Re-search only when a citation is wrong",
		} {
			if strings.Contains(result.Prompt, unwanted) {
				t.Errorf("Prompt contains scout-delegate.md cited-excerpt text %q, want absent (SCOUT_DELEGATE gate off):\n%s", unwanted, result.Prompt)
			}
		}
		if strings.Contains(result.Prompt, "${SCOUT_DELEGATE_STEP}") || strings.Contains(result.Prompt, "${SCOUT_ABSENT_STEP}") {
			t.Errorf("Prompt still contains an unsubstituted SCOUT step token:\n%s", result.Prompt)
		}
	})
}

// TestAssembleCoordinatorScoutBriefGate covers issue #3157's
// COORDINATOR_SCOUT_BRIEF-gated coordinator-scout-brief.md: a worker
// provisioned without a scout must render coordinator.md's own IMPLEMENT
// delegation with no reference to a brief that was never written
// (coordinator.md itself is scout-neutral prose, unconditionally), and must
// not render coordinator-scout-brief.md's own guidance at all. A worker
// provisioned *with* a scout must render coordinator-scout-brief.md's own
// guidance verbatim, including the slice-from-the-brief instruction that
// coordinator.md dropped when it went scout-neutral -- otherwise a
// scout-present run loses the "break the issue into slices from the
// brief's map" instruction entirely. Issue #3158 sharpens the delegation
// itself: each one must quote the brief's Map entries, Invariants &
// gotchas, and Suggested-approach step for its slice verbatim, scoped to
// the slice rather than pasting the whole brief. Issue #3216 adds the
// verification direction alongside it: the coordinator verifies those
// claims from the brief's own cited excerpts, reading the tree only to
// spot-check a citation that looks wrong or missing, not as a standing
// sweep over ground the brief already covers.
func TestAssembleCoordinatorScoutBriefGate(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("scout absent", func(t *testing.T) {
		env := coveredEnv()
		env.WorkerProvisioned = true
		env.ScoutProvisioned = false
		env.AgentsJSONTemplate = `{"worker":{"model":"x"}}`
		env.AgentsPromptFiles = `{"worker":"worker-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if !strings.Contains(result.Prompt, "run IMPLEMENT as its\n**coordinator**") {
			t.Errorf("Prompt missing coordinator.md fragment text:\n%s", result.Prompt)
		}
		if strings.Contains(result.Prompt, "the brief's relevant pointers") {
			t.Errorf("Prompt still contains coordinator.md's old brief reference:\n%s", result.Prompt)
		}
		if strings.Contains(result.Prompt, "Use the scout brief") {
			t.Errorf("Prompt still contains coordinator.md's old brief reference:\n%s", result.Prompt)
		}
		if strings.Contains(result.Prompt, fragmentText(t, "coordinator-scout-brief.md")) {
			t.Errorf("Prompt contains coordinator-scout-brief.md text, want absent (COORDINATOR_SCOUT_BRIEF gate off with no scout):\n%s", result.Prompt)
		}
		if strings.Contains(result.Prompt, "${COORDINATOR_SCOUT_BRIEF_STEP}") {
			t.Errorf("Prompt still contains an unsubstituted ${COORDINATOR_SCOUT_BRIEF_STEP} token:\n%s", result.Prompt)
		}
		// Issue #3159: budget-sizing and checkpoint-handoff guidance lives in
		// coordinator.md itself (scout-neutral), not the scout-brief fragment,
		// so it must render on a scout-less run too.
		if !strings.Contains(result.Prompt, "stays bounded") {
			t.Errorf("Prompt missing coordinator.md's bounded-worker-run guidance (issue #3159):\n%s", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "Turn budget for this slice: about <N> turns.") {
			t.Errorf("Prompt missing coordinator.md's stated-budget delegation template with the corrected turns unit label (issue #3159):\n%s", result.Prompt)
		}
		// "fresh" alone would match unrelated prose elsewhere in the prompt
		// (a fresh base, a fresh reviewer, a fresh clone), so pin the phrase.
		if !strings.Contains(result.Prompt, "**fresh** worker seeded from that checkpoint") {
			t.Errorf("Prompt missing coordinator.md's fresh-worker checkpoint handoff guidance (issue #3159):\n%s", result.Prompt)
		}
	})

	t.Run("scout provisioned", func(t *testing.T) {
		env := coveredEnv()
		env.WorkerProvisioned = true
		env.ScoutProvisioned = true
		env.AgentsJSONTemplate = `{"worker":{"model":"x"}}`
		env.AgentsPromptFiles = `{"worker":"worker-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if !strings.Contains(result.Prompt, fragmentText(t, "coordinator-scout-brief.md")) {
			t.Errorf("Prompt missing coordinator-scout-brief.md fragment text:\n%s", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "break the issue into the ordered set of slices") {
			t.Errorf("Prompt missing coordinator-scout-brief.md's slice-from-the-brief instruction:\n%s", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "verbatim from the brief") {
			t.Errorf("Prompt missing coordinator-scout-brief.md's verbatim-excerpt instruction (issue #3158):\n%s", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "never paste the whole brief into a delegation") {
			t.Errorf("Prompt missing coordinator-scout-brief.md's slice-scoped excerpt instruction (issue #3158):\n%s", result.Prompt)
		}
		// The whole-fragment pin above already fails if any of this text is
		// missing; these narrow the failure message to the clause that moved.
		// Each phrase is one only coordinator-scout-brief.md uses -- not the
		// "path:line anchor" wording it shares with scout-delegate.md, which
		// renders into this same prompt.
		for _, want := range []string{
			"spot-check a citation",
			"standing sweep",
			"the same evidence your\ndelegations carry",
		} {
			if !strings.Contains(result.Prompt, want) {
				t.Errorf("Prompt missing coordinator-scout-brief.md's verify-from-citations text %q (issue #3216):\n%s", want, result.Prompt)
			}
		}
		if strings.Contains(result.Prompt, "${COORDINATOR_SCOUT_BRIEF_STEP}") {
			t.Errorf("Prompt still contains an unsubstituted ${COORDINATOR_SCOUT_BRIEF_STEP} token:\n%s", result.Prompt)
		}
	})
}

// TestAssembleReviewPromptCaveman covers issue #2707:
// review-prompt.md, rendered into Result.ReviewPromptText (only when the
// orchestrator is on, kind == "work", FixPass == 0 -- see
// TestAssembleOrchestratorReviewerDrop), must carry the caveman-default
// narration directive when the caveman skill is baked, plus explicit
// exemption wording naming the VERDICT line and Non-blocking finding text
// (the text the Filer turns into an issue body) as staying full prose, and
// must carry no /caveman mention and no dangling ${...} token when skills
// are absent. A third subtest flips only CavemanSkillBaked off, leaving
// SkillsFound and the other three skill booleans at their coveredEnv
// defaults, to prove the fragment is gated on CAVEMAN_BAKED specifically
// rather than riding along on TDD_BAKED or the general SKILLS_FOUND signal.
func TestAssembleReviewPromptCaveman(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("skills present", func(t *testing.T) {
		env := coveredEnv()
		env.OrchestratorEnabled = true
		env.ReviewLoopInline = false
		env.ReviewLoopOrchestrator = true

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		prompt := result.ReviewPromptText
		if !strings.Contains(prompt, "Default to the `/caveman` skill") {
			t.Errorf("ReviewPromptText missing caveman-default-review.md fragment text: %q", prompt)
		}
		if !strings.Contains(prompt, "the `VERDICT: APPROVE` / `VERDICT: BLOCK` line") {
			t.Errorf("ReviewPromptText missing verdict-line exemption wording: %q", prompt)
		}
		if !strings.Contains(prompt, "Non-blocking finding") {
			t.Errorf("ReviewPromptText missing Non-blocking-finding exemption wording: %q", prompt)
		}
	})

	t.Run("skills absent", func(t *testing.T) {
		env := coveredEnv()
		env.OrchestratorEnabled = true
		env.ReviewLoopInline = false
		env.ReviewLoopOrchestrator = true
		env.SkillsFound = ""
		env.CavemanSkillBaked = false
		env.TDDSkillBaked = false
		env.CommitSkillBaked = false
		env.CodeReviewSkillBaked = false

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		prompt := result.ReviewPromptText
		if strings.Contains(prompt, "/caveman") {
			t.Errorf("ReviewPromptText contains /caveman text, want absent (CAVEMAN_BAKED gate off): %q", prompt)
		}
		if strings.Contains(prompt, "${") {
			t.Errorf("ReviewPromptText still contains an unsubstituted ${...} token: %q", prompt)
		}
	})

	t.Run("only caveman skill absent", func(t *testing.T) {
		env := coveredEnv()
		env.OrchestratorEnabled = true
		env.ReviewLoopInline = false
		env.ReviewLoopOrchestrator = true
		env.CavemanSkillBaked = false

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		prompt := result.ReviewPromptText
		if strings.Contains(prompt, "Default to the `/caveman` skill") {
			t.Errorf("ReviewPromptText contains caveman-default-review.md fragment text, want absent (CAVEMAN_BAKED gate off, other skills still baked): %q", prompt)
		}
		if strings.Contains(prompt, "${") {
			t.Errorf("ReviewPromptText still contains an unsubstituted ${...} token: %q", prompt)
		}
	})
}

// TestAssembleUnsupportedCell covers the one axis checkCoveredCell still
// validates (DispatchKind, issue #2540 -- IssueTracker and CodeForge are
// covered upstream by lib/mkHarness.nix's choicesCheckOk assert and
// cmd/launcher/main.go's validate(), so they no longer have cases here):
// an unrecognized value must return an error satisfying errors.Is(err,
// ErrUnsupportedCell).
func TestAssembleUnsupportedCell(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name   string
		mutate func(*Env)
	}{
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

// TestAssembleUnknownTrackerOrForgeNoLongerRejected pins the tolerance
// deleting checkCoveredCell's IssueTracker/CodeForge arms left behind
// (issue #2540): a bogus value for either field renders without error since
// Assemble/Gates never branch on IssueTracker/CodeForge to reject an
// unrecognized value -- their axis/backend resolution now lives entirely
// upstream in nix (TrackerAxisRead/TrackerAxisWrite/TrackerAxisFiler/
// ForgeBackend, issue #2533), so IssueTracker/CodeForge themselves are
// inert here beyond the other purposes documented on Env. Upstream
// validation (lib/mkHarness.nix's choicesCheckOk assert, cmd/launcher/
// main.go's validate()) is the only thing that rejects a bogus value now.
// Exists so a future change re-adding allowlist validation here shows up as
// a deliberate test change, not a silent behavior shift.
func TestAssembleUnknownTrackerOrForgeNoLongerRejected(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name   string
		mutate func(*Env)
	}{
		{name: "bogus issue tracker", mutate: func(e *Env) { e.IssueTracker = "bogus-tracker" }},
		{name: "bogus code forge", mutate: func(e *Env) { e.CodeForge = "bogus-forge" }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			tc.mutate(&env)

			if _, err := Assemble(env, reg); err != nil {
				t.Fatalf("Assemble: %v, want no error (checkCoveredCell no longer validates this field)", err)
			}
		})
	}
}

// TestAssembleAccessForgeCellsCovered covers the CodeForge x
// BoxWriteEnabled cells this issue adds to Assemble's covered set
// (github+read-write was already covered): github+read-only,
// forgejo+read-write, forgejo+read-only, plus (issue #2354) the "git" and
// "local" CodeForge values -- both schema-documented (lib/env-schema.nix)
// and already handled identically to "github" by Gates()
// (gates_access_forge.go: "only forgejo diverges from the shared gh-flavored
// path"). checkCoveredCell no longer re-validates CodeForge itself (issue
// #2540 -- that's covered upstream, see its doc comment), but Assemble's
// rendering logic must still accept all of these values. Each must render
// without error.
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

// TestAssembleLandGitStopStepNumbering covers the LAND THE CHANGE
// CODE_FORGE=git block's own step numbering: the "Print exactly one
// line..." step (LAND_GIT_STOP_READ_WRITE_STEP/LAND_GIT_STOP_READ_ONLY_STEP)
// must carry a leading number consistent with whatever step, if any,
// precedes it. Read-write follows the git-push step
// (LAND_GIT_PUSH_READ_WRITE_STEP, "1. `git push` ..."), so it must be "2.";
// read-only has no preceding step in this block at all (issue #2526's
// eval-time assert makes BOX_FORGE_AND_ISSUE_ACCESS=read-only paired with
// CODE_FORGE=git unbuildable, so LAND_GIT_PUSH_READ_ONLY_STEP no longer
// exists to supply one), so it must be "1." -- never an orphaned "2." with
// nothing numbered before it.
func TestAssembleLandGitStopStepNumbering(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name        string
		boxWrite    bool
		wantNumber  string
		wantMissing string
	}{
		{name: "read-write", boxWrite: true, wantNumber: "2. Print exactly one line", wantMissing: "1. Print exactly one line"},
		{name: "read-only", boxWrite: false, wantNumber: "1. Print exactly one line", wantMissing: "2. Print exactly one line"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.BoxWriteEnabled = tc.boxWrite

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			section := landGitForgeSection(t, result.Prompt)
			if !strings.Contains(section, tc.wantNumber) {
				t.Errorf("CODE_FORGE=git section missing %q:\n%s", tc.wantNumber, section)
			}
			if strings.Contains(section, tc.wantMissing) {
				t.Errorf("CODE_FORGE=git section unexpectedly contains %q:\n%s", tc.wantMissing, section)
			}
		})
	}
}

// landGitForgeSection extracts the LAND THE CHANGE section's
// "**`CODE_FORGE=git`**" block -- from that header up to (not including) the
// next "**`CODE_FORGE=" header -- the same slice-out-a-named-block pattern
// TestAssembleInjectsSharedBlocks/TestAssembleSharedBlockAlreadyPresentIsNoOp
// use for other named prompt regions, so a numbering assertion below can't
// accidentally match a "Print exactly one line" step from a different
// CODE_FORGE arm.
func landGitForgeSection(t *testing.T, prompt string) string {
	t.Helper()
	start := strings.Index(prompt, "**`CODE_FORGE=git`**")
	if start == -1 {
		t.Fatalf("prompt missing CODE_FORGE=git header:\n%s", prompt)
	}
	rest := prompt[start+len("**`CODE_FORGE=git`**"):]
	end := strings.Index(rest, "**`CODE_FORGE=")
	if end == -1 {
		t.Fatalf("prompt missing a CODE_FORGE header after CODE_FORGE=git:\n%s", prompt)
	}
	return rest[:end]
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

// TestAssembleResearchFileFindingsRelay covers issue #2593 (ADR 0041): the
// FILE FINDINGS delegate-to-filer section, gated on FILER_FILE_RELAY,
// renders in both research-prompt.md (SelfContained == false) and
// research-self-contained-prompt.md (SelfContained == true) whenever a
// research dispatch has the Filer provisioned -- unconditionally, with no
// orchestrator or BoxWriteEnabled condition (gates_tracker.go's
// researchForceRelay) -- and never renders (the "renders exactly as today"
// pin) when the Filer isn't provisioned at all.
func TestAssembleResearchFileFindingsRelay(t *testing.T) {
	reg := loadTestRegistry(t)

	for _, selfContained := range []bool{false, true} {
		selfContained := selfContained
		name := "research-prompt"
		if selfContained {
			name = "research-self-contained-prompt"
		}

		t.Run(name+"/filer enabled read-write", func(t *testing.T) {
			env := coveredEnv()
			env.DispatchKind = "research"
			env.SelfContained = selfContained
			env.ResearchStatusEnum = "recommend|reject|unclear"
			env.FilerEnabled = true
			env.BoxWriteEnabled = true
			env.OrchestratorEnabled = false

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			if !strings.Contains(result.Prompt, "**File findings.**") {
				t.Errorf("Prompt missing the FILE FINDINGS section: %q", result.Prompt)
			}
			if !strings.Contains(result.Prompt, "SPINDRIFT_ISSUE_INTENT") {
				t.Errorf("Prompt missing SPINDRIFT_ISSUE_INTENT from research-file-issues-relay.md: %q", result.Prompt)
			}
			if !strings.Contains(result.Prompt, "agent-research-finding") {
				t.Errorf("Prompt missing the agent-research-finding label mention: %q", result.Prompt)
			}
			if strings.Contains(result.Prompt, "${RESEARCH_FILE_ISSUES_RELAY_STEP}") {
				t.Errorf("Prompt contains an unsubstituted RESEARCH_FILE_ISSUES_RELAY_STEP token: %q", result.Prompt)
			}
		})

		t.Run(name+"/filer not enabled", func(t *testing.T) {
			env := coveredEnv()
			env.DispatchKind = "research"
			env.SelfContained = selfContained
			env.ResearchStatusEnum = "recommend|reject|unclear"
			env.FilerEnabled = false

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			if strings.Contains(result.Prompt, "**File findings.**") {
				t.Errorf("Prompt contains the FILE FINDINGS section, want absent (Filer not provisioned): %q", result.Prompt)
			}
			if strings.Contains(result.Prompt, "SPINDRIFT_ISSUE_INTENT") {
				t.Errorf("Prompt contains SPINDRIFT_ISSUE_INTENT, want absent (Filer not provisioned): %q", result.Prompt)
			}
		})

		for _, boxWriteEnabled := range []bool{true, false} {
			for _, orchestratorEnabled := range []bool{true, false} {
				boxWriteEnabled, orchestratorEnabled := boxWriteEnabled, orchestratorEnabled
				t.Run(fmt.Sprintf("%s/never direct-file boxWrite=%v orchestrator=%v", name, boxWriteEnabled, orchestratorEnabled), func(t *testing.T) {
					env := coveredEnv()
					env.DispatchKind = "research"
					env.SelfContained = selfContained
					env.ResearchStatusEnum = "recommend|reject|unclear"
					env.FilerEnabled = true
					env.BoxWriteEnabled = boxWriteEnabled
					env.OrchestratorEnabled = orchestratorEnabled

					result, err := Assemble(env, reg)
					if err != nil {
						t.Fatalf("Assemble: %v", err)
					}

					if strings.Contains(result.Prompt, "gh issue create --title") {
						t.Errorf("Prompt contains filer-file-direct.md's direct-file literal, want never in a research prompt: %q", result.Prompt)
					}
				})
			}
		}
	}
}

// TestAssembleFilerLabelRelayStepByKind covers a review finding on issue
// #2593: filer-label-relay.md's write-mechanism gate used to be the
// kind-agnostic FILER_FILE_RELAY, so a research dispatch with the Filer
// provisioned rendered the work-worded sentence naming
// `agent-review-finding` -- the label the launcher's work path
// (settle/gate.go) applies -- even though the launcher's research path
// (settle/research.go:97, fileIssueIntentsDetailed) actually applies
// `agent-research-finding`. gates_tracker.go now splits FILER_FILE_RELAY
// into FILER_FILE_RELAY_RESEARCH/FILER_FILE_RELAY_WORK so
// filer-label-relay-research.md (naming the correct label) renders instead
// for research, while filer-label-relay.md keeps rendering unchanged for
// work. Checked on the filer's own rendered prompt, extracted from
// AgentsJSON via agentPromptFromJSON -- not result.Prompt, which is the
// delegating orchestrator/research prompt, not the filer's.
func TestAssembleFilerLabelRelayStepByKind(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("research + Filer: research label, never the work label", func(t *testing.T) {
		env := coveredEnv()
		env.DispatchKind = "research"
		env.ResearchStatusEnum = "recommend|reject|unclear"
		env.FilerEnabled = true
		env.BoxWriteEnabled = true
		env.OrchestratorEnabled = false
		env.AgentsJSONTemplate = `{"filer":{"model":"m"}}`
		env.AgentsPromptFiles = `{"filer":"filer-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		filerPrompt := agentPromptFromJSON(t, result.AgentsJSON, "filer")
		if !strings.Contains(filerPrompt, "applies the `agent-research-finding` label itself") {
			t.Errorf("filer prompt missing the agent-research-finding label-relay sentence: %q", filerPrompt)
		}
		if strings.Contains(filerPrompt, "applies the `agent-review-finding` label itself") {
			t.Errorf("filer prompt contains the work-worded agent-review-finding label-relay sentence, want absent for research: %q", filerPrompt)
		}
		if strings.Contains(filerPrompt, "${FILER_LABEL_RELAY_RESEARCH_STEP}") {
			t.Errorf("filer prompt contains an unsubstituted FILER_LABEL_RELAY_RESEARCH_STEP token: %q", filerPrompt)
		}
	})

	t.Run("work relay (read-only + orchestrator): work label, never the research label", func(t *testing.T) {
		env := coveredEnv()
		env.FilerEnabled = true
		env.BoxWriteEnabled = false
		env.OrchestratorEnabled = true
		env.AgentsJSONTemplate = `{"filer":{"model":"m"}}`
		env.AgentsPromptFiles = `{"filer":"filer-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		filerPrompt := agentPromptFromJSON(t, result.AgentsJSON, "filer")
		if !strings.Contains(filerPrompt, "applies the `agent-review-finding` label itself") {
			t.Errorf("filer prompt missing the agent-review-finding label-relay sentence: %q", filerPrompt)
		}
		if strings.Contains(filerPrompt, "applies the `agent-research-finding` label itself") {
			t.Errorf("filer prompt contains the research-worded agent-research-finding label-relay sentence, want absent for work: %q", filerPrompt)
		}
		if strings.Contains(filerPrompt, "${FILER_LABEL_RELAY_STEP}") {
			t.Errorf("filer prompt contains an unsubstituted FILER_LABEL_RELAY_STEP token: %q", filerPrompt)
		}
	})
}

// TestAssembleResearchPromptCaveman covers issue #2708: research-prompt.md
// (DispatchKind == "research", SelfContained == false), rendered as
// result.Prompt (a single-document render, not AgentsJSON -- research
// prompts are not agent-roster prompts like scout/worker), must carry the
// caveman-default-research narration directive -- including its
// research-specific exemption for the posted verdict comment -- when the
// caveman skill is baked, and must carry neither it nor a dangling literal
// ${CAVEMAN_STEP_RESEARCH} token when it isn't. research-prompt.md wires
// only ${CAVEMAN_STEP_RESEARCH}, not ${SKILL_PREAMBLE} (unlike
// scout-prompt.md/worker-prompt.md): skill-preamble.md's own "fallback when
// a skill is absent" prose only makes sense next to the TDD/commit/
// code-review guidance those two roster prompts also wire, none of which
// research renders.
func TestAssembleResearchPromptCaveman(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("caveman baked", func(t *testing.T) {
		env := coveredEnv()
		env.DispatchKind = "research"
		env.SelfContained = false
		env.ResearchStatusEnum = "recommend|reject|unclear"

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if !strings.Contains(result.Prompt, "Default to the `/caveman` skill") {
			t.Errorf("Prompt missing caveman-default-research.md fragment text: %q", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "context-for-a-worker section") {
			t.Errorf("Prompt missing caveman-default-research.md's research-specific exemption text: %q", result.Prompt)
		}
		if strings.Contains(result.Prompt, "${") {
			t.Errorf("Prompt still contains an unsubstituted ${...} token: %q", result.Prompt)
		}
	})

	t.Run("caveman not baked", func(t *testing.T) {
		env := coveredEnv()
		env.DispatchKind = "research"
		env.SelfContained = false
		env.ResearchStatusEnum = "recommend|reject|unclear"
		env.CavemanSkillBaked = false

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if strings.Contains(result.Prompt, "/caveman") {
			t.Errorf("Prompt contains /caveman text, want absent (CAVEMAN_BAKED gate off): %q", result.Prompt)
		}
		if strings.Contains(result.Prompt, "${") {
			t.Errorf("Prompt still contains an unsubstituted ${...} token: %q", result.Prompt)
		}
	})
}

// TestAssembleResearchPromptCavemanReadOnly covers issue #2708: on a
// read-only research dispatch (BoxWriteEnabled == false), the box never
// posts the verdict comment itself -- it emits a single stdout
// `SPINDRIFT_COMMENT ${RUN_NONCE} <base64>` line
// (research-verdict-github-readonly.md) that outcome.LastCommentLineInLog
// parses host-side. caveman-default-research.md's marker-grammar exemption
// must name SPINDRIFT_COMMENT explicitly, not just SPINDRIFT_OUTCOME, or a
// caveman-narrating box could reflow/reword the sole carrier of the
// verdict and silently drop it.
func TestAssembleResearchPromptCavemanReadOnly(t *testing.T) {
	reg := loadTestRegistry(t)

	env := coveredEnv()
	env.DispatchKind = "research"
	env.SelfContained = false
	env.ResearchStatusEnum = "recommend|reject|unclear"
	env.BoxWriteEnabled = false

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "SPINDRIFT_COMMENT run-nonce-abc123") {
		t.Errorf("Prompt missing research-verdict-github-readonly.md's substituted SPINDRIFT_COMMENT relay line: %q", result.Prompt)
	}
	if !strings.Contains(result.Prompt, markerGrammarSpindriftCommentExcerpt) {
		t.Errorf("Prompt's marker-grammar exemption paragraph doesn't name SPINDRIFT_COMMENT: %q", result.Prompt)
	}
}

// TestAssembleResearchSelfContainedPromptCavemanReadOnly is the same
// coverage as TestAssembleResearchPromptCavemanReadOnly, but for
// research-self-contained-prompt.md (SelfContained == true): the
// ISSUE_TRACKER_GITHUB_READONLY gate (lib/fragments.nix) forks on itWrite
// and BoxWriteEnabled alone, independent of SelfContained, so the
// self-contained cell relays the verdict through the identical
// research-verdict-github-readonly.md fragment the repo-backed cell does.
func TestAssembleResearchSelfContainedPromptCavemanReadOnly(t *testing.T) {
	reg := loadTestRegistry(t)

	env := coveredEnv()
	env.DispatchKind = "research"
	env.SelfContained = true
	env.ResearchStatusEnum = "recommend|reject|unclear"
	env.BoxWriteEnabled = false

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "SPINDRIFT_COMMENT run-nonce-abc123") {
		t.Errorf("Prompt missing research-verdict-github-readonly.md's substituted SPINDRIFT_COMMENT relay line: %q", result.Prompt)
	}
	if !strings.Contains(result.Prompt, markerGrammarSpindriftCommentExcerpt) {
		t.Errorf("Prompt's marker-grammar exemption paragraph doesn't name SPINDRIFT_COMMENT: %q", result.Prompt)
	}
}

// TestAssembleResearchSelfContainedPromptCaveman is the same coverage as
// TestAssembleResearchPromptCaveman, but for
// research-self-contained-prompt.md (SelfContained == true).
func TestAssembleResearchSelfContainedPromptCaveman(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("caveman baked", func(t *testing.T) {
		env := coveredEnv()
		env.DispatchKind = "research"
		env.SelfContained = true
		env.ResearchStatusEnum = "recommend|reject|unclear"

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if !strings.Contains(result.Prompt, "Default to the `/caveman` skill") {
			t.Errorf("Prompt missing caveman-default-research.md fragment text: %q", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "context-for-a-worker section") {
			t.Errorf("Prompt missing caveman-default-research.md's research-specific exemption text: %q", result.Prompt)
		}
		if strings.Contains(result.Prompt, "${") {
			t.Errorf("Prompt still contains an unsubstituted ${...} token: %q", result.Prompt)
		}
	})

	t.Run("caveman not baked", func(t *testing.T) {
		env := coveredEnv()
		env.DispatchKind = "research"
		env.SelfContained = true
		env.ResearchStatusEnum = "recommend|reject|unclear"
		env.CavemanSkillBaked = false

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if strings.Contains(result.Prompt, "/caveman") {
			t.Errorf("Prompt contains /caveman text, want absent (CAVEMAN_BAKED gate off): %q", result.Prompt)
		}
		if strings.Contains(result.Prompt, "${") {
			t.Errorf("Prompt still contains an unsubstituted ${...} token: %q", result.Prompt)
		}
	})
}

// TestAssembleResearchPromptCheckHygiene covers issue #3227: research-prompt.md
// (DispatchKind == "research", SelfContained == false) wires the harness-owned
// CHECK_HYGIENE_STEP anchor in its EXPLORE section, phase-positioned next to
// the repro guidance it governs -- present when the skill is baked, absent
// (with no dangling literal token) when it isn't.
func TestAssembleResearchPromptCheckHygiene(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("check-hygiene baked", func(t *testing.T) {
		env := coveredEnv()
		env.DispatchKind = "research"
		env.SelfContained = false
		env.ResearchStatusEnum = "recommend|reject|unclear"
		env.CheckHygieneSkillBaked = true

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if !strings.Contains(result.Prompt, "Before running the first gate, invoke the `/check-hygiene` skill.") {
			t.Errorf("Prompt missing check-hygiene-default.md fragment text: %q", result.Prompt)
		}
		if strings.Contains(result.Prompt, "${") {
			t.Errorf("Prompt still contains an unsubstituted ${...} token: %q", result.Prompt)
		}
	})

	t.Run("check-hygiene not baked", func(t *testing.T) {
		env := coveredEnv()
		env.DispatchKind = "research"
		env.SelfContained = false
		env.ResearchStatusEnum = "recommend|reject|unclear"
		env.CheckHygieneSkillBaked = false

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if strings.Contains(result.Prompt, "/check-hygiene") {
			t.Errorf("Prompt contains /check-hygiene text, want absent (CHECK_HYGIENE_BAKED gate off): %q", result.Prompt)
		}
		if strings.Contains(result.Prompt, "${") {
			t.Errorf("Prompt still contains an unsubstituted ${...} token: %q", result.Prompt)
		}
	})
}

// TestAssembleResearchSelfContainedPromptCheckHygiene covers issue #3227: the
// self-contained research prompt has no repo and nothing to run, so it never
// wires CHECK_HYGIENE_STEP -- the anchor would be dead text pointing at a
// gate with no suite to run. This holds regardless of the skill's baked
// state, unlike research-prompt.md.
func TestAssembleResearchSelfContainedPromptCheckHygiene(t *testing.T) {
	reg := loadTestRegistry(t)

	env := coveredEnv()
	env.DispatchKind = "research"
	env.SelfContained = true
	env.ResearchStatusEnum = "recommend|reject|unclear"
	env.CheckHygieneSkillBaked = true

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if strings.Contains(result.Prompt, "/check-hygiene") {
		t.Errorf("Prompt contains /check-hygiene text, want absent (research-self-contained-prompt.md never wires CHECK_HYGIENE_STEP): %q", result.Prompt)
	}
	if strings.Contains(result.Prompt, "${") {
		t.Errorf("Prompt still contains an unsubstituted ${...} token: %q", result.Prompt)
	}
}

// TestAssembleResearchPromptCavemanLocalTracker covers issue #2708's third
// research-verdict relay cell: a local issue tracker (env.IssueTracker ==
// "local") fires ISSUE_TRACKER_LOCAL, wiring research-verdict-local.md
// (lib/fragments.nix, cmd/launcher/internal/promptassembly/gates_tracker.go)
// -- unlike the github split, this fragment's SPINDRIFT_COMMENT relay line
// renders regardless of BoxWriteEnabled, since a local tracker has no
// in-box tracker client to post a comment with either way. BoxWriteEnabled
// is set false here anyway, to mirror TestAssembleResearchPromptCavemanReadOnly's
// fixture and keep the read-only-box story consistent across both relay
// cells' tests.
func TestAssembleResearchPromptCavemanLocalTracker(t *testing.T) {
	reg := loadTestRegistry(t)

	env := localTrackerEnv()
	env.DispatchKind = "research"
	env.SelfContained = false
	env.ResearchStatusEnum = "recommend|reject|unclear"
	env.BoxWriteEnabled = false

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "SPINDRIFT_COMMENT run-nonce-abc123") {
		t.Errorf("Prompt missing research-verdict-local.md's substituted SPINDRIFT_COMMENT relay line: %q", result.Prompt)
	}
	if !strings.Contains(result.Prompt, markerGrammarSpindriftCommentExcerpt) {
		t.Errorf("Prompt's marker-grammar exemption paragraph doesn't name SPINDRIFT_COMMENT: %q", result.Prompt)
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
// what precedes it by a blank line. CODE COMMENTS is no longer one of these
// blocks (issue #3221): it's the ${CODE_COMMENTS_STEP} anchor fix-prompt.md
// now carries directly in its own FIX section.
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
	env.TrackerAxisRead = "FORGEJO"
	env.TrackerAxisWrite = "FORGEJO"
	env.TrackerAxisFiler = "FORGEJO"

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
// shares github's arm end to end (nix's precomputed TrackerAxisRead/
// TrackerAxisWrite/TrackerAxisFiler resolution, issue #2533 -- coveredEnv's
// axis fields stay at their github values here since only IssueTracker
// itself is mutated), so Assemble accepts it and does not return
// ErrUnsupportedCell.
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
	env.AgentsPromptFiles = `{"scout":"fragments/tdd-baked.md"}`

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
	if result.ReviewPromptText == "" {
		t.Fatal("ReviewPromptText is empty, want non-empty")
	}
	// "issue #2349" (SPEC dimension) only renders on the CODE_REVIEW_UNBAKED
	// arm (issue #3222); coveredEnv's CodeReviewSkillBaked default is true,
	// so check the tracker-read fragment's ISSUE_NUMBER substitution
	// instead -- unconditional regardless of the code-review pair's arm.
	if !strings.Contains(result.ReviewPromptText, "gh issue view 2349") {
		t.Errorf("ReviewPromptText missing substituted ISSUE_NUMBER:\n%s", result.ReviewPromptText)
	}
	if result.Handoff.ReviewPromptFile != "" {
		t.Errorf("Handoff.ReviewPromptFile = %q, want empty (Assemble no longer writes rendered text there, issue #2975)", result.Handoff.ReviewPromptFile)
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
		t.Errorf("scout.prompt missing substituted tdd-baked.md content: %q", scout.Prompt)
	}
}

// TestAssembleOrchestratorCommitReworkFragment covers issue #2698's
// commit-rework-orchestrator.md wiring: it shares the REVIEW_LOOP_ORCHESTRATOR
// gate (lib/fragments.nix), so it renders only when the orchestrator is
// enabled and stays empty on the inline (orchestrator-off) path. This test
// only asserts the marker's presence/absence; byte-identity of the inline
// prompt itself is what the untouched inline golden fixtures in
// tests/testdata/prompt-assembly-golden/ pin.
func TestAssembleOrchestratorCommitReworkFragment(t *testing.T) {
	reg := loadTestRegistry(t)
	const marker = "fold each fix into the commit it logically belongs to"

	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.ReviewLoopInline = false
	env.ReviewLoopOrchestrator = true

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(result.Prompt, marker) {
		t.Errorf("Prompt missing commit-rework-orchestrator.md fragment text (orchestrator on):\n%s", result.Prompt)
	}

	offEnv := coveredEnv()
	offResult, err := Assemble(offEnv, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if strings.Contains(offResult.Prompt, marker) {
		t.Errorf("Prompt contains commit-rework-orchestrator.md fragment text with orchestrator off, want absent:\n%s", offResult.Prompt)
	}
}

// TestAssembleLandPassOrderOrchestratorFragment covers issue #3214's
// land-pass-order-orchestrator.md wiring: it shares the
// REVIEW_LOOP_ORCHESTRATOR gate (lib/fragments.nix) with
// review-loop-orchestrator.md and commit-rework-orchestrator.md, so it
// renders only when the orchestrator is enabled and stays empty on the
// inline (orchestrator-off) path. It also pins the registry row itself
// (gate/fragment/var), since a marker-presence assertion alone wouldn't
// catch a row registered under the wrong gate or var name.
func TestAssembleLandPassOrderOrchestratorFragment(t *testing.T) {
	reg := loadTestRegistry(t)

	var row *FragmentRow
	for i := range reg.Rows {
		if reg.Rows[i].Fragment == "land-pass-order-orchestrator.md" {
			row = &reg.Rows[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("registry missing a row for land-pass-order-orchestrator.md")
	}
	if row.Gate != "REVIEW_LOOP_ORCHESTRATOR" {
		t.Errorf("land-pass-order-orchestrator.md row gate = %q, want REVIEW_LOOP_ORCHESTRATOR", row.Gate)
	}
	if row.Var != "LAND_PASS_ORDER_ORCHESTRATOR_STEP" {
		t.Errorf("land-pass-order-orchestrator.md row var = %q, want LAND_PASS_ORDER_ORCHESTRATOR_STEP", row.Var)
	}

	const marker = "This ordering supersedes the COMMIT section's"

	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.ReviewLoopInline = false
	env.ReviewLoopOrchestrator = true

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(result.Prompt, marker) {
		t.Errorf("Prompt missing land-pass-order-orchestrator.md fragment text (orchestrator on):\n%s", result.Prompt)
	}

	offEnv := coveredEnv()
	offResult, err := Assemble(offEnv, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if strings.Contains(offResult.Prompt, marker) {
		t.Errorf("Prompt contains land-pass-order-orchestrator.md fragment text with orchestrator off, want absent:\n%s", offResult.Prompt)
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
	if result.ReviewPromptText == "" {
		t.Error("ReviewPromptText is empty, want non-empty even with no reviewer configured")
	}
	if result.Handoff.ReviewPromptFile != "" {
		t.Errorf("Handoff.ReviewPromptFile = %q, want empty (Assemble no longer writes rendered text there, issue #2975)", result.Handoff.ReviewPromptFile)
	}
}

// TestAssembleOrchestratorEmptyAgentsTemplate covers the orchestrator-on
// cell with no AgentsJSONTemplate at all: AgentsJSON stays empty (no
// --agents flag), ReviewModel and ReviewEffort both stay empty, and
// ReviewPromptText is still rendered (it doesn't depend on
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
	if result.ReviewPromptText == "" {
		t.Error("ReviewPromptText is empty, want non-empty")
	}
	if result.Handoff.ReviewPromptFile != "" {
		t.Errorf("Handoff.ReviewPromptFile = %q, want empty (Assemble no longer writes rendered text there, issue #2975)", result.Handoff.ReviewPromptFile)
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
// legitimately bake any subset of the four. Only TDD_BAKED_STEP's fragment
// text must render; CAVEMAN_STEP/COMMIT_BAKED_STEP/CODE_REVIEW_BAKED_STEP must not.
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

	if !strings.Contains(result.Prompt, tddAnchorClause) {
		t.Errorf("Prompt missing tdd-baked.md fragment text (TDD_BAKED gate on):\n%s", result.Prompt)
	}
	for _, unwanted := range []string{
		tddInlineClause,
		"Default to the `/caveman` skill",
		"Use the `/commit` skill to write every commit message",
		"Run the `/code-review` skill and fold its two-axis",
	} {
		if strings.Contains(result.Prompt, unwanted) {
			t.Errorf("Prompt contains %q, want only TDD_BAKED_STEP to render (partial skill-baked combination)", unwanted)
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

	if !strings.Contains(result.Prompt, tddAnchorClause) {
		t.Errorf("Prompt missing tdd-baked.md fragment text (TDD_BAKED gate on):\n%s", result.Prompt)
	}
	for _, unwanted := range []string{
		tddInlineClause,
		"Default to the `/caveman` skill",
		"Use the `/commit` skill to write every commit message",
		"Run the `/code-review` skill and fold its two-axis",
	} {
		if strings.Contains(result.Prompt, unwanted) {
			t.Errorf("Prompt contains %q, want only TDD_BAKED_STEP to render (partial skill-baked combination)", unwanted)
		}
	}
}

// The two arms of the TDD_BAKED/TDD_UNBAKED fragment pair, each a clause
// unique to its own fragment so a Contains check on one can never be
// satisfied by the other.
const (
	tddAnchorClause = "Work test-first: run `/tdd` for each slice."
	tddInlineClause = "RED: write ONE failing test"
)

// TestAssembleTDDPairRendersExactlyOneArm covers issue #3219's tracer: the
// IMPLEMENT section's test-first prose is an exactly-one-on fragment pair,
// not a deferral note stacked on top of always-rendered inline steps.
// Baking the tdd skill SUBTRACTS the red/green/refactor fallback in favour
// of the anchor line; not baking it renders the fallback exactly as before,
// with no dangling reference to a skill that isn't there.
func TestAssembleTDDPairRendersExactlyOneArm(t *testing.T) {
	reg := loadTestRegistry(t)
	cases := []struct {
		name    string
		baked   bool
		want    string
		notWant string
	}{
		{name: "tdd skill baked", baked: true, want: tddAnchorClause, notWant: tddInlineClause},
		{name: "tdd skill not baked", baked: false, want: tddInlineClause, notWant: tddAnchorClause},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.TDDSkillBaked = tc.baked
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v, want nil error", err)
			}
			if !strings.Contains(result.Prompt, tc.want) {
				t.Errorf("Prompt missing %q (TDDSkillBaked=%v):\n%s", tc.want, tc.baked, result.Prompt)
			}
			if strings.Contains(result.Prompt, tc.notWant) {
				t.Errorf("Prompt contains %q, want only the other arm of the pair (TDDSkillBaked=%v)", tc.notWant, tc.baked)
			}
			if strings.Contains(result.Prompt, "red-green-refactor discipline is authoritative") {
				t.Errorf("Prompt still carries the retired /tdd deferral fragment (TDDSkillBaked=%v):\n%s", tc.baked, result.Prompt)
			}
		})
	}
}

// Vocabulary that only tdd-unbaked.md's step prose introduces. Any of it
// surviving into a baked cell means some fragment is naming prose the pair
// subtracted from that cell.
var tddUnbakedOnlyMarkers = []string{"RED:", "GREEN:", "REFACTOR"}

// TestAssembleOrchestratorCoordinatorTDDBakedHasNoDanglingReference guards
// the cross-fragment half of issue #3219's pair: coordinator.md pointed at
// the Hard rule ("the one-slice, test-first Hard rule below") that baking
// the skill removes. Asserted over the whole prompt rather than against
// coordinator.md, so any future fragment that grows the same dangling
// reference is caught too; the unbaked arm is asserted alongside it so a
// rename of the markers cannot make this pass vacuously.
func TestAssembleOrchestratorCoordinatorTDDBakedHasNoDanglingReference(t *testing.T) {
	reg := loadTestRegistry(t)
	cases := []struct {
		name  string
		baked bool
	}{
		{name: "tdd skill baked", baked: true},
		{name: "tdd skill not baked", baked: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.OrchestratorEnabled = true
			env.WorkerProvisioned = true
			env.AgentsJSONTemplate = `{"worker":{"model":"x"}}`
			env.AgentsPromptFiles = `{"worker":"worker-prompt.md"}`
			env.TDDSkillBaked = tc.baked

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v, want nil error", err)
			}
			// Shout-cased, and matched that way on purpose: lowercase
			// "red-green-refactor" is ordinary prose both arms may use.
			for _, marker := range tddUnbakedOnlyMarkers {
				if got := strings.Contains(result.Prompt, marker); got == tc.baked {
					t.Errorf("Prompt contains %q = %v, want %v (TDDSkillBaked=%v):\n%s", marker, got, !tc.baked, tc.baked, result.Prompt)
				}
			}
			// Case-folded instead: a cross-reference is free to name the
			// rule in sentence case, the way coordinator.md's did.
			if got := strings.Contains(strings.ToLower(result.Prompt), "hard rule"); got == tc.baked {
				t.Errorf("Prompt contains \"Hard rule\" = %v, want %v (TDDSkillBaked=%v):\n%s", got, !tc.baked, tc.baked, result.Prompt)
			}
		})
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
	if result.ReviewPromptText != "" {
		t.Errorf("ReviewPromptText = %q, want empty (fix pass, not the default fresh-work-dispatch path)", result.ReviewPromptText)
	}
	if result.Handoff.ReviewModel != "review-model-x" {
		t.Errorf("Handoff.ReviewModel = %q, want %q (extraction is unconditional whenever the orchestrator is on)", result.Handoff.ReviewModel, "review-model-x")
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
	if result.ReviewPromptText != "" {
		t.Errorf("ReviewPromptText = %q, want empty (research dispatch, not the default fresh-work-dispatch path)", result.ReviewPromptText)
	}
	if result.Handoff.ReviewModel != "review-model-x" {
		t.Errorf("Handoff.ReviewModel = %q, want %q (extraction is unconditional whenever the orchestrator is on)", result.Handoff.ReviewModel, "review-model-x")
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
	env.AgentsPromptFiles = `{"reviewer":"fragments/tdd-baked.md"}`

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
		t.Errorf("reviewer.prompt missing substituted tdd-baked.md content: %q", reviewer.Prompt)
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
	if result.ReviewPromptText != "" {
		t.Errorf("ReviewPromptText = %q, want empty (orchestrator off)", result.ReviewPromptText)
	}
}

// TestAssembleOrchestratorOffReviewerGetsCavemanFragment covers issue
// #2707: with the orchestrator off, an inline reviewer entry's prompt still
// flows through the same gated-fragment substitution as any other roster
// entry (TestAssembleOrchestratorOffReviewerFlowsThroughGenericLoop's
// tdd-baked.md case), so mapping "reviewer" to
// fragments/caveman-default-review.md with CAVEMAN_BAKED on (coveredEnv's
// default) must substitute the caveman narration directive into the
// reviewer's inline AgentsJSON prompt. Previously this was pinned only by
// the golden fixture covered-cell-populated-roster.agents.json, not by any
// Go unit test.
func TestAssembleOrchestratorOffReviewerGetsCavemanFragment(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x"}}`
	env.AgentsPromptFiles = `{"reviewer":"fragments/caveman-default-review.md"}`

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
	if !strings.Contains(reviewer.Prompt, "Default to the `/caveman` skill") {
		t.Errorf("reviewer.prompt missing substituted caveman-default-review.md content: %q", reviewer.Prompt)
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
	env.AgentsPromptFiles = `{"scout":"fragments/tdd-baked.md"}`

	if _, err := Assemble(env, reg); err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	body := agentFileBody(t, filepath.Join(dir, "scout.md"))
	if body == "placeholder body for scout\n" || strings.TrimSpace(body) == "" {
		t.Errorf("scout.md body not rewritten: %q", body)
	}
	if !strings.Contains(body, "/tdd") {
		t.Errorf("scout.md body missing substituted tdd-baked.md content: %q", body)
	}
	if agentFileFrontmatter(t, filepath.Join(dir, "scout.md")) != frontmatterBefore {
		t.Errorf("scout.md frontmatter changed, want unchanged")
	}
}

// TestAssembleDriverAgentFilesWorkerCavemanAndSkillPreamble covers issue
// #2706's second, independent render path: rewriteAgentFiles' on-disk
// worker.md rewrite (entrypoint.sh: 1128-1187) must also carry the
// caveman-default narration directive and the skill-advertisement preamble
// when the caveman skill is baked and skills are present, and must carry
// neither -- no dangling literal ${CAVEMAN_STEP}/${SKILL_PREAMBLE} token --
// when skills are absent, mirroring TestAssembleWorkerPromptCavemanAndSkillPreamble's
// renderAgentsJSON-path coverage of the same worker-prompt.md template.
func TestAssembleDriverAgentFilesWorkerCavemanAndSkillPreamble(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("skills present", func(t *testing.T) {
		dir := t.TempDir()
		writeAgentFile(t, filepath.Join(dir, "worker.md"), "worker")

		env := coveredEnv()
		env.CodeCommentsSkillBaked = true
		env.DriverAgentFilesDir = dir
		env.AgentsPromptFiles = `{"worker":"worker-prompt.md"}`

		if _, err := Assemble(env, reg); err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		body := agentFileBody(t, filepath.Join(dir, "worker.md"))
		if !strings.Contains(body, "Default to the `/caveman` skill") {
			t.Errorf("worker.md body missing caveman-default.md fragment text: %q", body)
		}
		if !strings.Contains(body, "Skills available:") {
			t.Errorf("worker.md body missing skill-preamble.md fragment text: %q", body)
		}
		if !strings.Contains(body, "invoke the `/code-comments` skill") {
			t.Errorf("worker.md body missing code-comments-default.md fragment text (CODE_COMMENTS_BAKED gate on): %q", body)
		}
		for _, marker := range []string{"SPINDRIFT_OUTCOME", "VERDICT: APPROVE", "VERDICT: BLOCK"} {
			if strings.Contains(body, marker) {
				t.Errorf("worker.md body contains forbidden marker %q (issue #2059/#2491 quarantine), want absent: %q", marker, body)
			}
		}
	})

	t.Run("skills absent", func(t *testing.T) {
		dir := t.TempDir()
		writeAgentFile(t, filepath.Join(dir, "worker.md"), "worker")

		env := coveredEnv()
		env.SkillsFound = ""
		env.CavemanSkillBaked = false
		env.TDDSkillBaked = false
		env.CommitSkillBaked = false
		env.CodeReviewSkillBaked = false
		env.CodeCommentsSkillBaked = false
		env.DriverAgentFilesDir = dir
		env.AgentsPromptFiles = `{"worker":"worker-prompt.md"}`

		if _, err := Assemble(env, reg); err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		body := agentFileBody(t, filepath.Join(dir, "worker.md"))
		if strings.Contains(body, "/caveman") {
			t.Errorf("worker.md body contains /caveman text, want absent (CAVEMAN_BAKED gate off): %q", body)
		}
		if strings.Contains(body, "Skills available:") {
			t.Errorf("worker.md body contains skill-preamble.md fragment text, want absent (SKILLS_FOUND gate off): %q", body)
		}
		if strings.Contains(body, "/code-comments") {
			t.Errorf("worker.md body contains code-comments-default.md fragment text, want absent (CODE_COMMENTS_BAKED gate off): %q", body)
		}
		if strings.Contains(body, "${") {
			t.Errorf("worker.md body still contains an unsubstituted ${...} token: %q", body)
		}
	})
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
	env.AgentsPromptFiles = `{"scout":"fragments/tdd-baked.md","reviewer":"fragments/tdd-baked.md"}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "reviewer.md")); !os.IsNotExist(err) {
		t.Errorf("reviewer.md still exists (or unexpected stat error %v), want removed", err)
	}
	body := agentFileBody(t, filepath.Join(dir, "scout.md"))
	if !strings.Contains(body, "/tdd") {
		t.Errorf("scout.md body missing substituted tdd-baked.md content: %q", body)
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

// TestAssembleReviewOverrides covers the dispatch-time
// REVIEW_MODEL/REVIEW_EFFORT override channel (issue #3171):
// Env.ReviewModelOverride/ReviewEffortOverride, carried into the Box as
// BOX_REVIEW_MODEL_OVERRIDE/BOX_REVIEW_EFFORT_OVERRIDE only when the
// operator explicitly set them at dispatch time, bind into
// Handoff.ReviewModel/ReviewEffort last -- over the AgentsJSONTemplate
// extraction, over the reviewer.md agent-file rewrite, and even when the
// roster opted the reviewer out entirely. The empty-override passthrough
// half of the contract is pinned by every pre-existing reviewer-extraction
// test in this file, all of which run with both override fields at their
// zero value.
func TestAssembleReviewOverrides(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("override wins over the baked reviewer entry", func(t *testing.T) {
		env := coveredEnv()
		env.OrchestratorEnabled = true
		env.AgentsJSONTemplate = `{"reviewer":{"model":"baked-model","effort":"baked-effort"}}`
		env.ReviewModelOverride = "env-model"
		env.ReviewEffortOverride = "env-effort"

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if result.Handoff.ReviewModel != "env-model" {
			t.Errorf("Handoff.ReviewModel = %q, want %q", result.Handoff.ReviewModel, "env-model")
		}
		if result.Handoff.ReviewEffort != "env-effort" {
			t.Errorf("Handoff.ReviewEffort = %q, want %q", result.Handoff.ReviewEffort, "env-effort")
		}
	})

	t.Run("partial override leaves the other half on the baked value", func(t *testing.T) {
		env := coveredEnv()
		env.OrchestratorEnabled = true
		env.AgentsJSONTemplate = `{"reviewer":{"model":"baked-model","effort":"baked-effort"}}`
		env.ReviewModelOverride = "env-model"

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if result.Handoff.ReviewModel != "env-model" {
			t.Errorf("Handoff.ReviewModel = %q, want %q", result.Handoff.ReviewModel, "env-model")
		}
		if result.Handoff.ReviewEffort != "baked-effort" {
			t.Errorf("Handoff.ReviewEffort = %q, want %q (unset effort override follows the roster)", result.Handoff.ReviewEffort, "baked-effort")
		}
	})

	t.Run("override applies with the reviewer opted out of the roster", func(t *testing.T) {
		env := coveredEnv()
		env.OrchestratorEnabled = true
		env.AgentsJSONTemplate = `{"scout":{"model":"scout-model-y"}}`
		env.ReviewModelOverride = "env-model"
		env.ReviewEffortOverride = "env-effort"

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if result.Handoff.ReviewModel != "env-model" {
			t.Errorf("Handoff.ReviewModel = %q, want %q (env applies instead of the coordinator-model fallback)", result.Handoff.ReviewModel, "env-model")
		}
		if result.Handoff.ReviewEffort != "env-effort" {
			t.Errorf("Handoff.ReviewEffort = %q, want %q", result.Handoff.ReviewEffort, "env-effort")
		}
	})

	t.Run("override wins over the reviewer.md agent-file rewrite", func(t *testing.T) {
		dir := t.TempDir()
		writeAgentFile(t, filepath.Join(dir, "reviewer.md"), "reviewer")

		env := coveredEnv()
		env.OrchestratorEnabled = true
		env.DriverAgentFilesDir = dir
		env.AgentsJSONTemplate = `{"reviewer":{"model":"haiku"}}`
		env.ReviewModelOverride = "env-model"

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if result.Handoff.ReviewModel != "env-model" {
			t.Errorf("Handoff.ReviewModel = %q, want %q (dispatch env wins over the file path's frontmatter model)", result.Handoff.ReviewModel, "env-model")
		}
	})

	t.Run("orchestrator off ignores the override entirely", func(t *testing.T) {
		env := coveredEnv()
		env.AgentsJSONTemplate = `{"reviewer":{"model":"baked-model","effort":"baked-effort"}}`
		env.ReviewModelOverride = "env-model"
		env.ReviewEffortOverride = "env-effort"

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if result.Handoff.ReviewModel != "" || result.Handoff.ReviewEffort != "" {
			t.Errorf("Handoff.ReviewModel/ReviewEffort = (%q,%q), want both empty under driver-exec invoker", result.Handoff.ReviewModel, result.Handoff.ReviewEffort)
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
	env.AgentsPromptFiles = `{"worker":"fragments/tdd-baked.md"}`

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
	env.AgentsPromptFiles = `{"scout":"fragments/tdd-baked.md"}`

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
	env.AgentsPromptFiles = `{"reviewer":"fragments/tdd-baked.md"}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if result.Handoff.ReviewModel != "" {
		t.Errorf("Handoff.ReviewModel = %q, want empty (no model: line in reviewer.md frontmatter)", result.Handoff.ReviewModel)
	}
}
