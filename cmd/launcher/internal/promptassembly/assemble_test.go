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

// The real templates/default/prompts tree, resolved relative to this
// package directory, the same convention the testdata paths here use.
const promptsDir = "../../../../templates/default/prompts"

// A verbatim excerpt of caveman-default-research.md's marker-grammar
// exemption paragraph. One contiguous literal ties "machine-parsed marker
// grammar" to SPINDRIFT_COMMENT, so the assertion cannot mis-scope itself
// if a second fragment in the same prompt ever uses that phrase.
const markerGrammarSpindriftCommentExcerpt = "The machine-parsed marker grammar is exempt too: the `SPINDRIFT_OUTCOME`\nline and its `note=` field, and any host-relay signal line such as\n`SPINDRIFT_COMMENT`"

// A fixture Env sitting exactly in Assemble's covered cell (see
// checkCoveredCell). Gates no longer re-derives the tracker/forge axes
// in-box (issue #2533), so the axis and backend fields must carry nix's
// already-resolved values, and ReviewLoopInline must mirror
// !OrchestratorEnabled. Tests mutate a copy to move one axis off the cell.
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

// coveredEnv with the tracker and its nix-precomputed axis fields (issue
// #2533) moved to "local", every other axis left on the covered cell.
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

// Callers assert a whole fragment body against the prompt. Reading the
// fragment beats hand-copying an excerpt: a review round found a copied one
// had silently stopped matching after the fragment's wording changed.
func fragmentText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(promptsDir, "fragments", name))
	if err != nil {
		t.Fatalf("read fragment %s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

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

	if !strings.Contains(result.Prompt, "Before the PR, spawn a fresh `reviewer` subagent") {
		t.Errorf("Prompt missing REVIEW_LOOP_INLINE fragment text")
	}
	if strings.Contains(result.Prompt, "REVIEW_LOOP_ORCHESTRATOR_STEP") {
		t.Errorf("Prompt contains a literal unsubstituted REVIEW_LOOP_ORCHESTRATOR_STEP token")
	}

	if !strings.Contains(result.Prompt, "via GitHub") {
		t.Errorf("Prompt missing ISSUE_TRACKER_GITHUB fragment text (issue-read-github.md)")
	}
}

// Issue #2354's AUTO_FORMAT wiring. The gate is a plain passthrough of
// Env.AutoFormat, entrypoint.sh's old `[ -n "${AUTO_FORMAT:-}" ]` presence
// check ported as a bool field rather than a second string-presence field.
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

// Issue #2354's AUTO_LINT wiring: the same plain-passthrough presence
// semantics as AUTO_FORMAT above.
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

// Issue #2354's CI_FAILURE_SUMMARY wiring, run on a fix-pass Env because
// fix-prompt.md is the only base template referencing ${CI_FAILURE_STEP}.
// The gate is a presence check on the value itself, not a separate bool
// field, so a non-empty CIFailureSummary both opens the gate and
// substitutes its own text into ci-failure.md.
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

// Builds a PromptsDir that symlinks the real tree except for one omitted
// fragment. That on-disk shape lets a caller observe the fragment loop's
// missing-file handling without hand-building a whole prompts fixture.
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

// Assemble's fragment loop must reproduce old bash's swallow
// (entrypoint.sh: 1001-1009): the failed command substitution sat as a
// printf argument, so `set -e` never saw a non-zero exit and a missing
// fragment resolved to an empty string. CAVEMAN_BAKED is on in coveredEnv
// while its fragment file is absent here, so the swallow is observable.
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

// Pins a fragment substituting its own extraSubstVars entry:
// skill-preamble.md's ${SKILLS_FOUND} must resolve to Env.SkillsFound's
// value, not stay literal or empty.
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

// The bash-parity trim (issue #2349, prompt-assembly-parity.bats). bash
// captured the prompt through $(...), which strips every trailing newline,
// and nothing downstream re-added one. issue-prompt.md ends with a
// fragment-loop token whose assignment already appends "\n\n", so an
// unstripped Result.Prompt would end in several newlines, not zero.
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

// RenderText is exported for an issue #2060 review finding: the
// orchestrator's cherry-pick conflict-resolve guidance reuses this ${NAME}
// substitution at runtime instead of its own strings.ReplaceAll pass.
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

// The fragment loop's "\n\n" separator (entrypoint.sh: 1001-1009). A
// fragment ending with a blank line on disk (skill-preamble.md does) must
// not leak it into the prompt: bash stripped the fragment's own trailing
// newlines through $(...) before appending exactly two.
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

// The --agents JSON injection loop (entrypoint.sh: 1105-1116).
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

// Issue #2706: scout-prompt.md, rendered through renderAgentsJSON's
// per-agent prompt lookup, carries the caveman narration directive and the
// skill preamble when those skills are baked, and neither of them (nor a
// dangling ${CAVEMAN_STEP}/${SKILL_PREAMBLE}) when skills are absent.
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

// Issue #3216: the scout's brief must cite a verbatim excerpt under a
// path:line anchor for each load-bearing claim, reusing #3158's excerpt
// format, so a coordinator can verify a claim by reading the cited lines
// instead of re-exploring the tree.
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

// Issue #3449 moved the scout's brief off the return message and onto
// disk, so the rendered prompt must carry the /tmp/brief.md path, the
// write-it-yourself and never-retype rules, and the verification step.
func TestAssembleScoutPromptWritesBriefToDisk(t *testing.T) {
	reg := loadTestRegistry(t)

	env := coveredEnv()
	env.AgentsJSONTemplate = `{"scout":{"model":"x"}}`
	env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	prompt := agentPromptFromJSON(t, result.AgentsJSON, "scout")
	if !strings.Contains(prompt, "/tmp/brief.md") {
		t.Errorf("scout.prompt missing /tmp/brief.md, the scout brief path (issue #3449): %q", prompt)
	}
	if !strings.Contains(prompt, "write a structured brief to") {
		t.Errorf("scout.prompt missing the write-it-yourself contract (issue #3449): %q", prompt)
	}
	if !strings.Contains(prompt, "Never retype a cited excerpt") {
		t.Errorf("scout.prompt missing the never-retype/append-from-source rule (issue #3449): %q", prompt)
	}
	if !strings.Contains(prompt, "verify every") || !strings.Contains(prompt, "`diff` it against the excerpt block the brief") {
		t.Errorf("scout.prompt missing the pre-return excerpt verification step (issue #3449): %q", prompt)
	}
}

// Issue #2706, the worker-prompt.md half of
// TestAssembleScoutPromptCavemanAndSkillPreamble's coverage.
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

// Issue #3157: with a scout provisioned, worker-prompt.md directs the
// worker to the delegation's quoted excerpt first and to /tmp/brief.md only
// when that excerpt is missing, wrong, or silent (issue #3419). With no
// scout, the gate exists to degrade gracefully: no brief reference and no
// dangling ${WORKER_SCOUT_BRIEF_STEP}.
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
		if !strings.Contains(prompt, "work from that excerpt\nfirst") {
			t.Errorf("worker.prompt missing worker-scout-brief.md fragment text: %q", prompt)
		}
		if !strings.Contains(prompt, "Open the full brief at `/tmp/brief.md` only when the excerpt is") {
			t.Errorf("worker.prompt missing worker-scout-brief.md's conditional-read clause (issue #3419): %q", prompt)
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

// Issue #3159's worker-side counterpart to coordinator.md's budget and
// checkpoint guidance, plus issue #3420's batched-edit directive. Both are
// scout-independent, so this asserts against coveredEnv() rather than
// forking on ScoutProvisioned the way TestAssembleWorkerPromptScoutBrief
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
		"one patch file",
		"apply it in a single command",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("worker.prompt missing budget/checkpoint/batched-edits text %q (issues #3159, #3420):\n%s", want, prompt)
		}
	}
}

// Issue #3157's SCOUT_PROVISIONED/SCOUT_ABSENT fork of the `# SCOUT`
// section, an exactly-one-on pair like REVIEW_LOOP_INLINE and
// REVIEW_LOOP_ORCHESTRATOR. Each arm must carry only its own text, with no
// unsubstituted ${SCOUT_DELEGATE_STEP}/${SCOUT_ABSENT_STEP} left behind.
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
		// Issue #3449: the scout writes the brief itself; the session
		// reads it back from disk and never persists it.
		if !strings.Contains(result.Prompt, "The scout writes that brief itself, to `/tmp/brief.md`") {
			t.Errorf("Prompt missing scout-delegate.md's scout-writes-it wording (issue #3449):\n%s", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "read it back from disk before you start work") {
			t.Errorf("Prompt missing scout-delegate.md's reads-from-disk wording (issue #3163):\n%s", result.Prompt)
		}
		if strings.Contains(result.Prompt, "Persist what it returns") {
			t.Errorf("Prompt still contains scout-delegate.md's dropped persist-what-it-returns wording (issue #3449):\n%s", result.Prompt)
		}
		if strings.Contains(result.Prompt, "Scout the issue yourself before implementing") {
			t.Errorf("Prompt contains scout-absent.md's lead directive, want absent (SCOUT_ABSENT gate off):\n%s", result.Prompt)
		}
		if strings.Contains(result.Prompt, "there is no brief waiting on disk") {
			t.Errorf("Prompt contains scout-absent.md fragment text, want absent (SCOUT_ABSENT gate off):\n%s", result.Prompt)
		}
		// Issue #3449: COORDINATOR_SCOUT_BRIEF needs WorkerProvisioned too,
		// so in this cell scout-delegate.md is the only fragment that can
		// carry the brief-absent degradation clause.
		if !strings.Contains(result.Prompt, "If `/tmp/brief.md` isn't there, explore the repo yourself as usual") {
			t.Errorf("Prompt missing scout-delegate.md's brief-missing degradation clause (issue #3449):\n%s", result.Prompt)
		}
		// Issue #3163: scout-delegate.md renders on SCOUT_PROVISIONED alone,
		// independent of WorkerProvisioned (which coveredEnv() leaves false),
		// so its wording must never assert a delegation-to-worker step that
		// this scout-only roster never runs.
		scoutStart := strings.Index(result.Prompt, "# SCOUT")
		implementStart := strings.Index(result.Prompt, "# IMPLEMENT")
		if scoutStart == -1 || implementStart == -1 || implementStart < scoutStart {
			t.Fatalf("could not locate # SCOUT..# IMPLEMENT span in prompt:\n%s", result.Prompt)
		}
		scoutSection := result.Prompt[scoutStart:implementStart]
		if strings.Contains(scoutSection, "before you delegate any slice") {
			t.Errorf("scout-only # SCOUT section still contains the dropped delegation wording (issue #3163):\n%s", scoutSection)
		}
		if strings.Contains(strings.ToLower(scoutSection), "worker") {
			t.Errorf("scout-only # SCOUT section references a worker, but WorkerProvisioned is false in this cell (issue #3163):\n%s", scoutSection)
		}
	})

	t.Run("scout absent", func(t *testing.T) {
		env := coveredEnv()
		env.ScoutProvisioned = false

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		// The section leads with the work, so pin that lead directive and
		// not just the trailing absence clause.
		if !strings.Contains(result.Prompt, "Scout the issue yourself before implementing") {
			t.Errorf("Prompt missing scout-absent.md's lead directive (issue #3168):\n%s", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "there is no brief waiting on disk") {
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

// Issue #3216's addition to scout-delegate.md: the delegation asks for a
// cited verbatim excerpt per load-bearing claim, not just paths and line
// refs, and the coordinator re-searches only when a citation itself is
// wrong or missing, not on any wrong pointer.
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

// Issue #3157's COORDINATOR_SCOUT_BRIEF gate. coordinator.md went
// scout-neutral and dropped the slice-from-the-brief instruction, so a
// scout-present run loses it entirely unless coordinator-scout-brief.md
// renders. Later issues layer on: #3158 slice-scoped verbatim excerpts,
// #3216 verify from citations, #3449 the scout writes /tmp/brief.md itself.
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
		// Issue #3159: the budget and checkpoint guidance lives in
		// coordinator.md, not the scout-brief fragment, so it must render on
		// a scout-less run too.
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
		// Issue #3419: the coordinator no longer tells a worker to read the
		// whole brief file before its own quoted excerpt. Only the worker's
		// own fragment, worker-scout-brief.md, makes that read conditional.
		if strings.Contains(result.Prompt, "read `/tmp/brief.md` first") {
			t.Errorf("Prompt still contains coordinator-scout-brief.md's dropped read-brief-first instruction (issue #3419):\n%s", result.Prompt)
		}
		// The whole-fragment pin above already fails if any of this text is
		// missing; these narrow the failure message to the clause that moved.
		// Each phrase is one only coordinator-scout-brief.md uses, not the
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
		// Issue #3449: the scout writes the brief itself; the coordinator no
		// longer claims to have persisted it, only that it reads it back.
		if strings.Contains(result.Prompt, "You already persisted") {
			t.Errorf("Prompt still contains coordinator-scout-brief.md's dropped already-persisted wording (issue #3449):\n%s", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "The scout wrote its brief to `/tmp/brief.md` itself") {
			t.Errorf("Prompt missing coordinator-scout-brief.md's scout-wrote-it wording (issue #3449):\n%s", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "If `/tmp/brief.md` isn't there, explore the repo yourself as usual") {
			t.Errorf("Prompt missing scout-delegate.md's brief-missing degradation clause (issue #3449):\n%s", result.Prompt)
		}
	})
}

// Issue #2707: review-prompt.md must exempt the VERDICT line and the
// Non-blocking finding text, which the Filer turns into an issue body, from
// caveman narration. The third subtest flips only CavemanSkillBaked, to
// prove the fragment is gated on CAVEMAN_BAKED rather than riding along on
// TDD_BAKED or the general SKILLS_FOUND signal.
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

// DispatchKind is the one axis checkCoveredCell still validates (issue
// #2540). IssueTracker and CodeForge moved upstream to lib/mkHarness.nix's
// choicesCheckOk assert and main.go's validate(), so they have no case here.
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

// Pins the tolerance that deleting checkCoveredCell's IssueTracker and
// CodeForge arms left behind (issue #2540). Axis resolution lives in nix
// now (issue #2533), so a bogus value renders without error and only
// upstream validation rejects it. This test exists so a future change
// re-adding allowlist validation here shows up as a deliberate edit.
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

// The CodeForge x BoxWriteEnabled cells beyond github+read-write, plus
// issue #2354's "git" and "local" values, which Gates() already handles
// identically to "github" (gates_access_forge.go: only forgejo diverges).
// checkCoveredCell no longer re-validates CodeForge (issue #2540), but
// Assemble's rendering must still accept every one of these values.
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

// The CODE_FORGE=git block's step numbering must follow whatever step
// precedes it. Read-write follows the git-push step, so it numbers "2.".
// Read-only has no preceding step, because issue #2526's eval-time assert
// makes read-only plus CODE_FORGE=git unbuildable and
// LAND_GIT_PUSH_READ_ONLY_STEP is gone, so it must number "1.".
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

// Slices out the CODE_FORGE=git block so a numbering assertion cannot
// match a "Print exactly one line" step from a different CODE_FORGE arm.
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

// The research cell always sets SessionMode to "initial", even with
// ResumeAfterHold set: entrypoint.sh's research branch (1031-1063) never
// inspected RESUME_AFTER_HOLD, so this fixture sets it to pin that.
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
	// Env.ResearchStatusEnum through the RESEARCH_STATUS_ENUM allowlist
	// entry (issue #2504), not a literal typed into the template.
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

// The self-contained research cell renders
// research-self-contained-prompt.md, not research-prompt.md.
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

// Issue #2593 (ADR 0041): with the Filer provisioned, the FILE FINDINGS
// section renders in both research prompts unconditionally, with no
// orchestrator or BoxWriteEnabled condition (gates_tracker.go's
// researchForceRelay), and never renders without the Filer.
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
				for _, signalCarrier := range []string{"", "log", "socket"} {
					boxWriteEnabled, orchestratorEnabled, signalCarrier := boxWriteEnabled, orchestratorEnabled, signalCarrier
					t.Run(fmt.Sprintf("%s/never direct-file boxWrite=%v orchestrator=%v carrier=%q", name, boxWriteEnabled, orchestratorEnabled, signalCarrier), func(t *testing.T) {
						env := coveredEnv()
						env.DispatchKind = "research"
						env.SelfContained = selfContained
						env.ResearchStatusEnum = "recommend|reject|unclear"
						env.FilerEnabled = true
						env.BoxWriteEnabled = boxWriteEnabled
						env.OrchestratorEnabled = orchestratorEnabled
						env.SignalCarrier = signalCarrier

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
}

// A review finding on issue #2593: the kind-agnostic FILER_FILE_RELAY gate
// made a research dispatch render the work-worded sentence naming
// `agent-review-finding`, though the launcher's research path applies
// `agent-research-finding`. Assert on the filer's own prompt from
// AgentsJSON, not result.Prompt, which is the delegating prompt.
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

// Issue #2708: research-prompt.md carries the caveman-default-research
// directive, including its exemption for the posted verdict comment. It
// wires only ${CAVEMAN_STEP_RESEARCH}, never ${SKILL_PREAMBLE}, because
// skill-preamble.md's fallback prose only makes sense next to the
// tdd/commit/code-review guidance research does not render.
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

// Issue #2708: a read-only research box never posts the verdict itself. It
// emits one stdout SPINDRIFT_COMMENT line the host parses, so
// caveman-default-research.md's exemption must name SPINDRIFT_COMMENT and
// not just SPINDRIFT_OUTCOME. Otherwise a narrating box could reword the
// sole carrier of the verdict and silently drop it.
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

// TestAssembleResearchPromptCavemanReadOnly's coverage for the
// self-contained cell. The ISSUE_TRACKER_GITHUB_READONLY gate forks on
// itWrite and BoxWriteEnabled alone, independent of SelfContained, so both
// cells relay the verdict through the same fragment.
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

// TestAssembleResearchPromptCaveman's coverage for the self-contained cell.
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

// Issue #3227: research-prompt.md wires the harness-owned
// CHECK_HYGIENE_STEP anchor in its EXPLORE section, positioned next to the
// repro guidance it governs.
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

// Issue #3227: the self-contained research prompt has no repo and nothing
// to run, so it never wires CHECK_HYGIENE_STEP. That holds regardless of
// the skill's baked state, unlike research-prompt.md, which is why this
// test bakes the skill and still expects the anchor to be absent.
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

// Issue #2708's third research-verdict relay cell. Unlike the github
// split, research-verdict-local.md's SPINDRIFT_COMMENT line renders
// whatever BoxWriteEnabled says, since a local tracker has no in-box
// client to post with. The fixture still clears BoxWriteEnabled, to mirror
// TestAssembleResearchPromptCavemanReadOnly across both relay cells.
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

// Issue #3726: the research-verdict relay forks a second time on
// BOX_SIGNAL_CARRIER, independent of the tracker-backend split above. A
// read-only github research cell must carry exactly one of the log
// fragment's SPINDRIFT_COMMENT relay line or the socket fragment's
// driver-exec verb text, never both, and SignalCarrier alone must pick
// which.
func TestAssembleResearchPromptCarrierSelectsLogOrSocketFragment(t *testing.T) {
	reg := loadTestRegistry(t)

	base := coveredEnv()
	base.DispatchKind = "research"
	base.SelfContained = false
	base.ResearchStatusEnum = "recommend|reject|unclear"
	base.BoxWriteEnabled = false

	logEnv := base
	logEnv.SignalCarrier = "log"
	logResult, err := Assemble(logEnv, reg)
	if err != nil {
		t.Fatalf("Assemble(log): %v", err)
	}
	if !strings.Contains(logResult.Prompt, "SPINDRIFT_COMMENT run-nonce-abc123") {
		t.Errorf("SignalCarrier=log prompt missing research-verdict-github-readonly.md's SPINDRIFT_COMMENT relay line: %q", logResult.Prompt)
	}
	if strings.Contains(logResult.Prompt, "driver-exec signal comment") {
		t.Errorf("SignalCarrier=log prompt contains the socket fragment's driver-exec verb, want absent: %q", logResult.Prompt)
	}

	socketEnv := base
	socketEnv.SignalCarrier = "socket"
	socketResult, err := Assemble(socketEnv, reg)
	if err != nil {
		t.Fatalf("Assemble(socket): %v", err)
	}
	if !strings.Contains(socketResult.Prompt, "driver-exec signal comment") {
		t.Errorf("SignalCarrier=socket prompt missing research-verdict-github-readonly-socket.md's driver-exec verb: %q", socketResult.Prompt)
	}
	// caveman-default-research.md names SPINDRIFT_COMMENT unconditionally
	// in its marker-grammar exemption paragraph (a later slice's concern),
	// so this asserts against the log fragment's actual relay line, not
	// the bare marker name.
	if strings.Contains(socketResult.Prompt, "SPINDRIFT_COMMENT run-nonce-abc123") {
		t.Errorf("SignalCarrier=socket prompt contains the log fragment's SPINDRIFT_COMMENT relay line, want absent: %q", socketResult.Prompt)
	}
}

// Issue #3726 slice 3: the PR-intent channel forks the same way on the
// carrier. A read-only WORK box never posts the PR itself, so it either
// prints the nonce-guarded SPINDRIFT_PR_INTENT stdout line (log carrier) or
// sends the intent over the Signal socket via `driver-exec signal
// pr-intent` (socket carrier) — for both the OPEN A PULL REQUEST step and
// the IF BLOCKED step, never both fragments at once.
func TestAssembleWorkPromptCarrierSelectsLogOrSocketPRIntentFragment(t *testing.T) {
	reg := loadTestRegistry(t)

	base := coveredEnv()
	base.BoxWriteEnabled = false

	logEnv := base
	logEnv.SignalCarrier = "log"
	logResult, err := Assemble(logEnv, reg)
	if err != nil {
		t.Fatalf("Assemble(log): %v", err)
	}
	// caveman-default-worker.md names SPINDRIFT_PR_INTENT unconditionally in
	// its marker-grammar exemption paragraph, so assert against the log
	// fragments' actual substituted nonce line, not the bare marker name.
	if !strings.Contains(logResult.Prompt, "SPINDRIFT_PR_INTENT run-nonce-abc123") {
		t.Errorf("SignalCarrier=log prompt missing the open-pr-create-outbox.md/if-blocked-pr-outbox.md substituted SPINDRIFT_PR_INTENT line: %q", logResult.Prompt)
	}
	// caveman-default.md also names the bare `driver-exec signal pr-intent`
	// verb unconditionally in its exemption paragraph, so anchor on the
	// socket fragments' own flag-bearing command line, not the bare verb.
	if strings.Contains(logResult.Prompt, `driver-exec signal pr-intent -title "<conventional title>"`) {
		t.Errorf("SignalCarrier=log prompt contains the socket fragments' driver-exec command line, want absent: %q", logResult.Prompt)
	}

	socketEnv := base
	socketEnv.SignalCarrier = "socket"
	socketResult, err := Assemble(socketEnv, reg)
	if err != nil {
		t.Fatalf("Assemble(socket): %v", err)
	}
	if !strings.Contains(socketResult.Prompt, `driver-exec signal pr-intent -title "<conventional title>"`) {
		t.Errorf("SignalCarrier=socket prompt missing the socket fragments' driver-exec command line: %q", socketResult.Prompt)
	}
	if strings.Contains(socketResult.Prompt, "SPINDRIFT_PR_INTENT run-nonce-abc123") {
		t.Errorf("SignalCarrier=socket prompt contains the log fragments' substituted SPINDRIFT_PR_INTENT line, want absent: %q", socketResult.Prompt)
	}
}

// Issue #3726 slice 4: the issue-intent channel forks the same way as the
// comment/pr-intent channels above. A read-only Filer either emits the
// nonce-guarded SPINDRIFT_ISSUE_INTENT stdout line (log carrier) or sends
// each issue over the Signal socket via `driver-exec signal issue-intent`
// (socket carrier) — never both, in the coordinator's own delegation step
// and in the Filer's own prompt, on both the work and research paths.
func TestAssembleWorkPromptCarrierSelectsLogOrSocketIssueIntentFragment(t *testing.T) {
	reg := loadTestRegistry(t)

	base := coveredEnv()
	base.FilerEnabled = true
	base.BoxWriteEnabled = false
	base.OrchestratorEnabled = true
	base.AgentsJSONTemplate = `{"filer":{"model":"m"}}`
	base.AgentsPromptFiles = `{"filer":"filer-prompt.md"}`

	// caveman-default.md names SPINDRIFT_ISSUE_INTENT (and SPINDRIFT_PR_INTENT)
	// unconditionally in its marker-grammar exemption paragraph, so assert
	// against each fragment's own distinguishing sentence, not the bare
	// marker name, which the caveman fragment puts in both prompts either way.
	logEnv := base
	logEnv.SignalCarrier = "log"
	logResult, err := Assemble(logEnv, reg)
	if err != nil {
		t.Fatalf("Assemble(log): %v", err)
	}
	if !strings.Contains(logResult.Prompt, "it emits\n`SPINDRIFT_ISSUE_INTENT` lines instead") {
		t.Errorf("SignalCarrier=log coordinator prompt missing file-issues-relay.md's relay sentence: %q", logResult.Prompt)
	}
	// caveman-default.md also names the bare `driver-exec signal issue-intent`
	// verb unconditionally in its exemption paragraph, so anchor on
	// file-issues-relay-socket.md's own "via ..." sentence, not the bare verb.
	if strings.Contains(logResult.Prompt, "via `driver-exec signal issue-intent`") {
		t.Errorf("SignalCarrier=log coordinator prompt contains file-issues-relay-socket.md's relay sentence, want absent: %q", logResult.Prompt)
	}
	logFilerPrompt := agentPromptFromJSON(t, logResult.AgentsJSON, "filer")
	if !strings.Contains(logFilerPrompt, "print\n   one `SPINDRIFT_ISSUE_INTENT run-nonce-abc123") {
		t.Errorf("SignalCarrier=log filer prompt missing filer-file-relay.md's nonce-guarded marker line: %q", logFilerPrompt)
	}
	// filer-prompt.md's own report-format prose also names the bare verb
	// unconditionally (its QUEUED-report line), so anchor on
	// filer-file-relay-socket.md's own flag-bearing command line.
	if strings.Contains(logFilerPrompt, `driver-exec signal issue-intent -title "<title>" -type bug`) {
		t.Errorf("SignalCarrier=log filer prompt contains filer-file-relay-socket.md's driver-exec command line, want absent: %q", logFilerPrompt)
	}

	socketEnv := base
	socketEnv.SignalCarrier = "socket"
	socketResult, err := Assemble(socketEnv, reg)
	if err != nil {
		t.Fatalf("Assemble(socket): %v", err)
	}
	if !strings.Contains(socketResult.Prompt, "via `driver-exec signal issue-intent`") {
		t.Errorf("SignalCarrier=socket coordinator prompt missing file-issues-relay-socket.md's relay sentence: %q", socketResult.Prompt)
	}
	if strings.Contains(socketResult.Prompt, "it emits\n`SPINDRIFT_ISSUE_INTENT` lines instead") {
		t.Errorf("SignalCarrier=socket coordinator prompt contains the log fragment's relay sentence, want absent: %q", socketResult.Prompt)
	}
	// filer-prompt.md's own report-format prose names SPINDRIFT_ISSUE_INTENT
	// (and the bare driver-exec verb) unconditionally too (its QUEUED-report
	// line), so assert filer-file-relay-socket.md's own flag-bearing command
	// line rather than bare-marker or bare-verb absence.
	socketFilerPrompt := agentPromptFromJSON(t, socketResult.AgentsJSON, "filer")
	if !strings.Contains(socketFilerPrompt, `driver-exec signal issue-intent -title "<title>" -type bug`) {
		t.Errorf("SignalCarrier=socket filer prompt missing filer-file-relay-socket.md's driver-exec command line: %q", socketFilerPrompt)
	}
	if strings.Contains(socketFilerPrompt, "print\n   one `SPINDRIFT_ISSUE_INTENT run-nonce-abc123") {
		t.Errorf("SignalCarrier=socket filer prompt contains filer-file-relay.md's nonce-guarded marker line, want absent: %q", socketFilerPrompt)
	}
	// signal_cmd.go's issue-intent case rejects an empty -title (and an
	// empty -type) with exit 1, so filer-file-relay-socket.md must not
	// claim -title is optional (issue #3726 review finding).
	if strings.Contains(socketFilerPrompt, "Unlike `-title`, `-type` is required") {
		t.Errorf("SignalCarrier=socket filer prompt wrongly claims -title is optional: %q", socketFilerPrompt)
	}
	if !strings.Contains(socketFilerPrompt, "Both `-title` and `-type` are required") {
		t.Errorf("SignalCarrier=socket filer prompt missing filer-file-relay-socket.md's both-required sentence: %q", socketFilerPrompt)
	}
}

// Same fork on the research path (ADR 0041's researchForceRelay), asserted
// against result.Prompt directly since research-file-issues-relay.md/-socket
// render in the coordinator's own research prompt, not a delegated one.
func TestAssembleResearchPromptCarrierSelectsLogOrSocketIssueIntentFragment(t *testing.T) {
	reg := loadTestRegistry(t)

	base := coveredEnv()
	base.DispatchKind = "research"
	base.ResearchStatusEnum = "recommend|reject|unclear"
	base.FilerEnabled = true
	base.BoxWriteEnabled = true
	base.OrchestratorEnabled = false

	logEnv := base
	logEnv.SignalCarrier = "log"
	logResult, err := Assemble(logEnv, reg)
	if err != nil {
		t.Fatalf("Assemble(log): %v", err)
	}
	if !strings.Contains(logResult.Prompt, "SPINDRIFT_ISSUE_INTENT") {
		t.Errorf("SignalCarrier=log research prompt missing research-file-issues-relay.md's SPINDRIFT_ISSUE_INTENT marker: %q", logResult.Prompt)
	}
	if strings.Contains(logResult.Prompt, "driver-exec signal issue-intent") {
		t.Errorf("SignalCarrier=log research prompt contains the socket fragment's driver-exec verb, want absent: %q", logResult.Prompt)
	}

	socketEnv := base
	socketEnv.SignalCarrier = "socket"
	socketResult, err := Assemble(socketEnv, reg)
	if err != nil {
		t.Fatalf("Assemble(socket): %v", err)
	}
	if !strings.Contains(socketResult.Prompt, "driver-exec signal issue-intent") {
		t.Errorf("SignalCarrier=socket research prompt missing research-file-issues-relay-socket.md's driver-exec verb: %q", socketResult.Prompt)
	}
	if strings.Contains(socketResult.Prompt, "SPINDRIFT_ISSUE_INTENT") {
		t.Errorf("SignalCarrier=socket research prompt contains SPINDRIFT_ISSUE_INTENT, want absent: %q", socketResult.Prompt)
	}
}

// The fix-pass cell always sets SessionMode to "resume", whatever
// ResumeAfterHold says: entrypoint.sh's fix-pass branch (1031-1063) never
// inspected it either.
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

// entrypoint.sh's if/elif precedence (1031-1063) checked DispatchKind
// first, so a research Env with FixPass > 0 still renders the research
// prompt, never fix-prompt.md.
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

// Builds a shared-block contract-file fixture. Unlike the real nix-baked
// contract files (lib/mkHarness.nix: 622-631), these are plain files whose
// content the tests here fully control.
func writeContractFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

// Shared-block injection (entrypoint.sh: 632-643, 1064-1074), run on the
// fix-pass cell because issue-prompt.md already contains each marker in its
// own sections while fix-prompt.md does not, so all three blocks append
// here and stay observable. CODE COMMENTS is no longer one of them (issue
// #3221): fix-prompt.md carries that anchor in its own FIX section.
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

// injectSharedBlock's idempotent guard (entrypoint.sh: 632-643): a base
// template that already contains a block's marker does not get that block
// appended again, so the contract file's body text must appear zero times.
func TestAssembleSharedBlockAlreadyPresentIsNoOp(t *testing.T) {
	reg := loadTestRegistry(t)
	promptsFixtureDir := t.TempDir()
	// The fragment loop reads PromptsDir/fragments/* whichever base
	// template is selected, so the fixture symlinks the real fragments dir
	// in rather than standing up a full fragments fixture of its own.
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

// The research branch of the injection step (entrypoint.sh: 1064-1074) only
// ever attempts research-verdict injection, never comms/check/outcome, even
// with every contract-file field populated. The fixture omits the
// "# POST THE VERDICT" marker the real research-prompt.md already carries,
// so the injected block is observable.
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

// A contract file's own ${...} tokens resolve through the same allowlist as
// every other file Assemble renders (entrypoint.sh: 638).
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

// The local-tracker cell (issue #2352). Issue #3469 moved the local link
// chain host-side into # ISSUE TEXT, so issue-read-local.md keeps only the
// trailing `git log` bullet, which is what this asserts on.
func TestAssembleLocalTracker(t *testing.T) {
	reg := loadTestRegistry(t)
	env := localTrackerEnv()

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "git log -n 10 --oneline") {
		t.Errorf("Prompt missing ISSUE_TRACKER_LOCAL fragment text (issue-read-local.md):\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "via GitHub") {
		t.Errorf("Prompt contains ISSUE_TRACKER_GITHUB fragment text (issue-read-github.md), want local tracker's fragment only")
	}
}

// With LocalIssueReference on, the PR body carries the "Local-issue:"
// breadcrumb and never the local-noref arm's text.
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

// With LocalIssueReference left at its default, the PR body carries
// PR_BODY_LOCAL_NOREF's text and never the "Local-issue:" breadcrumb.
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

// The forgejo-tracker cell on a read-write box (issue #2352). ADR 0022's
// acceptance criterion is read-write only, so read-only tracker cells stay
// out of scope here.
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

	if !strings.Contains(result.Prompt, "via Forgejo") {
		t.Errorf("Prompt missing ISSUE_TRACKER_FORGEJO fragment text (issue-read-forgejo.md):\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, "via GitHub") {
		t.Errorf("Prompt contains ISSUE_TRACKER_GITHUB fragment text (issue-read-github.md), want forgejo tracker's fragment only")
	}
}

// The jira-tracker cell (issue #2352). jira shares github's arm end to end
// through nix's precomputed axis resolution (issue #2533), so the fixture
// mutates only IssueTracker and leaves the axis fields on github's values.
func TestAssembleJiraTracker(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.IssueTracker = "jira"

	if _, err := Assemble(env, reg); err != nil {
		t.Errorf("Assemble with jira tracker: %v, want nil error", err)
	}
}

// Pins issue #2352's acceptance criterion at the Assemble level, not just
// the gate-map level gates_tracker_test.go already covers: two Envs
// differing only in IssueTracker must produce byte-identical prompts.
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

// entrypoint.sh's orchestrator-on reviewer drop (1029-1062, 1086-1107): the
// model and effort are extracted from the reviewer entry before the key is
// deleted from the agents JSON, and the generic per-agent injection loop
// still runs for every other agent.
func TestAssembleOrchestratorReviewerDrop(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x","effort":"review-effort-x"},"scout":{"model":"scout-model-y"}}`
	env.AgentsPromptFiles = `{"scout":"fragments/tdd-baked.md"}`
	env.IssueText = "issue body text"

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
	// "issue #2349" only renders on the CODE_REVIEW_UNBAKED arm (issue
	// #3222) and coveredEnv bakes that skill, so assert on the appended
	// # ISSUE TEXT section instead; issue #3445 dropped the issue-read
	// fragment that used to carry this. It renders whenever env.IssueText
	// is set, whichever arm of the code-review pair is on.
	if !strings.Contains(result.ReviewPromptText, "Issue #2349's body") {
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

// Issue #2698's commit-rework-orchestrator.md shares the
// REVIEW_LOOP_ORCHESTRATOR gate, so it renders only with the orchestrator
// on. Only the marker is asserted here; byte-identity of the inline prompt
// is what the golden fixtures in tests/testdata/prompt-assembly-golden pin.
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

// Issue #3214's land-pass-order-orchestrator.md shares the
// REVIEW_LOOP_ORCHESTRATOR gate with the two fragments above. This also
// pins the registry row's gate and var, which a marker-presence assertion
// alone would not catch if the row were registered under the wrong name.
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

// With no reviewer key in the template, ReviewModel and ReviewEffort stay
// empty, mirroring jq's `.reviewer.model // empty`. review-prompt.md still
// renders: it does not depend on a reviewer being configured.
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

// The orchestrator-on cell with no AgentsJSONTemplate: AgentsJSON stays
// empty, so no --agents flag, and ReviewPromptText still renders because it
// does not depend on the template.
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

// The orchestrator on a read-only box is a covered cell now, the filer
// relay precondition axis from issue #2353.
func TestAssembleOrchestratorBoxReadOnlyCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.OrchestratorEnabled = true
	env.BoxWriteEnabled = false

	if _, err := Assemble(env, reg); err != nil {
		t.Errorf("Assemble: %v, want nil error (orchestrator on + box read-only is covered)", err)
	}
}

// The skills-absent cell with the orchestrator on (issue #2353) is covered,
// and the prompt omits the skill-preamble text.
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

// The skills-absent cell for the orchestrator-off branch (issue #2354).
// Most bats fixtures and many real Consumers bake zero skills, so this cell
// must be covered whichever way the orchestrator flag points.
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

// A partial skill-baked combination, only tdd here, is a covered cell
// (issue #2354). The four per-skill gates are independent booleans with no
// cross-dependency, matching Gates() and lib/image.nix's per-skill baking,
// so a real Consumer can bake any subset of the four.
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

// TestAssemblePartialSkillsCovered for the orchestrator-on branch (issue
// #2354).
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

// The two arms of the TDD_BAKED/TDD_UNBAKED fragment pair. Each clause is
// unique to its own fragment, so a Contains check on one can never be
// satisfied by the other.
const (
	tddAnchorClause = "Work test-first: run `/tdd` for each slice."
	tddInlineClause = "RED: write ONE failing test"
)

// Issue #3219's tracer: the IMPLEMENT section's test-first prose is an
// exactly-one-on pair, not a deferral note stacked on always-rendered
// inline steps. Baking the tdd skill subtracts the red/green/refactor
// fallback in favour of the anchor line, and not baking it renders the
// fallback with no dangling reference to a skill that is not there.
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

// The cross-fragment half of issue #3219's pair: coordinator.md pointed at
// the Hard rule that baking the skill removes. Asserted over the whole
// prompt, not against coordinator.md, so a future fragment growing the same
// dangling reference is caught too. The unbaked arm is asserted alongside
// it so renaming a marker cannot make this pass vacuously.
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

// The orchestrator on a fix pass is a covered cell (issue #2354), reachable
// in production because ORCHESTRATOR_ENABLED is a static per-Consumer knob
// forwarded unchanged to fix-pass Boxes. ReviewPromptFile stays empty, since
// only a fresh work dispatch populates it, but ReviewModel still populates:
// its extraction is unconditional whenever the orchestrator is on.
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

// The orchestrator on a research dispatch is a covered cell (issue #2354).
// ReviewPromptFile stays empty because research never reviews (ADR 0022),
// while ReviewModel still populates, as in the fix-pass cell above.
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

// Regression guard for renderAgentsJSON's signature change (issue #2353):
// with the orchestrator off, a reviewer key is not dropped. It flows
// through the generic per-agent injection loop like any other roster entry.
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

// Issue #2707: with the orchestrator off, an inline reviewer entry's prompt
// still flows through the same gated-fragment substitution as any other
// roster entry. The golden fixture
// covered-cell-populated-roster.agents.json used to be the only thing
// pinning this.
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

// A baked opencode agent file fixture: real frontmatter shape, and a body
// distinguishable from any real rendered prompt. The Go-side twin of
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

// The Go-side twin of the bats helper of the same name.
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

// The DRIVER_AGENT_FILES_DIR file-rewrite twin of the --agents JSON
// injection loop (entrypoint.sh: 1128-1187): a baked agent file keeps its
// frontmatter and has its body overwritten with the substituted prompt.
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

// Issue #2706's second, independent render path: rewriteAgentFiles' on-disk
// worker.md rewrite (entrypoint.sh: 1128-1187) must reach the same result
// for worker-prompt.md as
// TestAssembleWorkerPromptCavemanAndSkillPreamble's renderAgentsJSON path.
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
		if !strings.Contains(body, "A comment earns its place only by carrying something the code cannot state") {
			t.Errorf("worker.md body missing the inlined code-comments policy (issue #3419): %q", body)
		}
		if strings.Contains(body, "/code-comments") {
			t.Errorf("worker.md body contains the /code-comments skill anchor, want absent (issue #3419: worker inlines the policy instead): %q", body)
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
		if !strings.Contains(body, "A comment earns its place only by carrying something the code cannot state") {
			t.Errorf("worker.md body missing the inlined code-comments policy, want present regardless of CODE_COMMENTS_BAKED (issue #3419): %q", body)
		}
		if strings.Contains(body, "/code-comments") {
			t.Errorf("worker.md body contains the /code-comments skill anchor, want absent (issue #3419: worker inlines the policy instead): %q", body)
		}
		if strings.Contains(body, "${") {
			t.Errorf("worker.md body still contains an unsubstituted ${...} token: %q", body)
		}
	})
}

// The file-based reviewer drop (entrypoint.sh: 1141-1156): with the
// orchestrator on, reviewer.md's `model:` scalar populates
// Handoff.ReviewModel and the file is removed, while a non-reviewer roster
// file still gets its body rewritten.
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

// Precedence between the two reviewer-model extraction paths
// (entrypoint.sh: 1096 JSON, then 1152-1153 file). The file path runs
// second and overwrites unconditionally, so reviewer.md's frontmatter model
// wins over whatever AgentsJSONTemplate already set.
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

// The dispatch-time review override channel (issue #3171). The overrides
// bind last: over the AgentsJSONTemplate extraction, over the reviewer.md
// rewrite, and even with the reviewer opted out of the roster. The
// empty-override half of the contract is pinned by every other
// reviewer-extraction test here, which all leave both fields at zero.
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

// A roster name with no baked .md file on disk is silently skipped, not an
// error: opencode drops the file when the model is empty, and the reviewer
// drop above removes one mid-run.
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

// A roster entry whose prompt file is missing under PromptsDir leaves the
// on-disk agent file untouched, without error.
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

// frontmatterOf's no-second-fence fallback. bash captured the frontmatter
// through $(awk ...), which strips every trailing newline, so the fallback
// must too. Returning them verbatim makes rewriteAgentFiles' join produce a
// blank line the bash original never would have.
func TestAssembleDriverAgentFilesFrontmatterFallbackTrimsTrailingNewlines(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()

	const markerLine = "no second fence here"
	// One "---" fence line only, so frontmatterOf never reaches a second
	// fence and falls through to the fallback branch. Two trailing newlines
	// exercise that the fallback strips all of them, not just one.
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

// reviewerModelFrontmatter's no-`model:`-line fallback, where
// entrypoint.sh's `sed -n 's/^model: //p'` found no match:
// Handoff.ReviewModel stays empty.
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

// assemblePromptBodies' segment breakdown must stay byte-for-byte faithful
// to what Assemble returns, summed lengths included. That catches an
// attribution refactor that silently drops or double-counts a segment.
func TestAssembleSegmentAttributionMatchesResult(t *testing.T) {
	reg := loadTestRegistry(t)

	assertBodyMatches := func(t *testing.T, b body, want string) {
		t.Helper()
		if got := b.text(); got != want {
			t.Fatalf("body.text() mismatch:\n got: %q\nwant: %q", got, want)
		}
		sum := 0
		for _, seg := range b {
			sum += len(seg.text)
		}
		if sum != len(want) {
			t.Fatalf("sum of segment lengths = %d, want %d", sum, len(want))
		}
	}

	t.Run("covered cell", func(t *testing.T) {
		env := coveredEnv()

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		bodies, err := assemblePromptBodies(env, reg)
		if err != nil {
			t.Fatalf("assemblePromptBodies: %v", err)
		}

		assertBodyMatches(t, bodies.base, result.Prompt)
		if bodies.review != nil {
			t.Fatalf("expected no review body for a non-orchestrator cell, got %+v", bodies.review)
		}
		if result.ReviewPromptText != "" {
			t.Fatalf("ReviewPromptText = %q, want empty", result.ReviewPromptText)
		}
	})

	t.Run("orchestrator on", func(t *testing.T) {
		env := coveredEnv()
		env.OrchestratorEnabled = true
		env.ReviewLoopInline = false
		env.ReviewLoopOrchestrator = true

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		bodies, err := assemblePromptBodies(env, reg)
		if err != nil {
			t.Fatalf("assemblePromptBodies: %v", err)
		}

		assertBodyMatches(t, bodies.base, result.Prompt)
		if bodies.review == nil {
			t.Fatalf("expected a review body for the orchestrator-on/work/FixPass==0 cell")
		}
		assertBodyMatches(t, bodies.review, result.ReviewPromptText)
	})
}
