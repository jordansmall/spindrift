package promptassembly

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/signalwire"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

var promptsDir = repopath.PromptsDir()

// A verbatim excerpt of caveman-default-research.md's marker-grammar
// exemption paragraph. One contiguous literal ties "machine-parsed marker
// grammar" to SPINDRIFT_COMMENT, so the assertion cannot mis-scope itself
// if a second fragment in the same prompt ever uses that phrase.
const markerGrammarSpindriftCommentExcerpt = "The machine-parsed marker grammar is exempt too: the `SPINDRIFT_OUTCOME`\nline and its `note=` field, and any host-relay signal line such as\n`SPINDRIFT_COMMENT`"

// A fixture Env sitting exactly in Assemble's covered cell (see
// checkCoveredCell). Gates no longer re-derives the tracker/forge axes
// in-box (issue #2533), so the axis and backend fields must carry nix's
// already-resolved values. Tests mutate a copy to move one axis off the cell.
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

	if !strings.Contains(result.Prompt, "Review is handled by the orchestrator as a separate, code-owned pass") {
		t.Errorf("Prompt missing review-loop-orchestrator.md fragment text")
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

// promptsDirExceptSubdir mirrors the real prompts tree except for the top-level
// entry omit, returning the new dir and the real dir's absolute path.
func promptsDirExceptSubdir(t *testing.T, omit string) (dir, realDir string) {
	t.Helper()
	dir = t.TempDir()
	realDir, err := filepath.Abs(promptsDir)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}

	entries, err := os.ReadDir(realDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", realDir, err)
	}
	for _, entry := range entries {
		if entry.Name() == omit {
			continue
		}
		if err := os.Symlink(filepath.Join(realDir, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
			t.Fatalf("Symlink(%s): %v", entry.Name(), err)
		}
	}
	return dir, realDir
}

// Builds a PromptsDir that symlinks the real tree except for one omitted
// fragment. That on-disk shape lets a caller observe the fragment loop's
// missing-file handling without hand-building a whole prompts fixture.
func promptsDirMissingFragment(t *testing.T, omit string) string {
	t.Helper()
	dir, realDir := promptsDirExceptSubdir(t, "fragments")

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

// The bash-parity trim (issue #2349). bash
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

// Each row runs through both modes: braced-only (the main assembly path) must
// leave every bare $NAME alone, bare mode (conflict-resolve overrides) must
// also take the whole greedy identifier like GNU envsubst.
func TestRenderText(t *testing.T) {
	vars := map[string]string{
		"NAME":  "v",
		"OTHER": "o",
		"LEAK":  "${OTHER} $OTHER",
	}
	cases := []struct {
		name, in, braced, bare string
	}{
		{"braced token", "${NAME}", "v", "v"},
		{"bare token", "$NAME", "$NAME", "v"},
		{"bare takes whole identifier", "$NAMEX", "$NAMEX", "$NAMEX"},
		{"braced delimits identifier", "${NAME}X", "vX", "vX"},
		{"bare then punctuation", "$NAME.x", "$NAME.x", "v.x"},
		{"unlisted braced", "${NOPE}", "${NOPE}", "${NOPE}"},
		{"unlisted bare", "$NOPE", "$NOPE", "$NOPE"},
		{"double dollar", "$$", "$$", "$$"},
		{"double dollar before name", "$$NAME", "$$NAME", "$v"},
		{"empty braces", "${}", "${}", "${}"},
		{"default-value form", "${NAME:-x}", "${NAME:-x}", "${NAME:-x}"},
		{"value not re-expanded braced", "${LEAK}", "${OTHER} $OTHER", "${OTHER} $OTHER"},
		{"value not re-expanded bare", "$LEAK", "$LEAK", "${OTHER} $OTHER"},
		{"trailing newlines trimmed", "A ${NAME} and ${NOPE}.\n\n", "A v and ${NOPE}.", "A v and ${NOPE}."},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := RenderText(tc.in, vars, false); got != tc.braced {
				t.Errorf("RenderText(%q, bare=false) = %q, want %q", tc.in, got, tc.braced)
			}
			if got := RenderText(tc.in, vars, true); got != tc.bare {
				t.Errorf("RenderText(%q, bare=true) = %q, want %q", tc.in, got, tc.bare)
			}
		})
	}
}

// Default prompts carry literal shell text such as `echo $CODE_FORGE` that the
// agent must read verbatim; it has to survive assembly untouched.
func TestAssembleKeepsBareShellVariablesLiteral(t *testing.T) {
	reg := loadTestRegistry(t)

	result, err := Assemble(coveredEnv(), reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(result.Prompt, "echo $CODE_FORGE") {
		t.Errorf("Prompt lost the literal %q", "echo $CODE_FORGE")
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

			if result.Handoff.SessionMode != tc.wantMode {
				t.Errorf("Handoff.SessionMode = %q, want %q", result.Handoff.SessionMode, tc.wantMode)
			}
			// Resuming after a hold changes only the session mode, never the prompt.
			if !strings.Contains(result.Prompt, "Fresh clone, new branch") {
				t.Errorf("Prompt missing issue-prompt.md's distinguishing text:\n%s", result.Prompt)
			}
			if strings.Contains(result.Prompt, "This is a warm fix pass") {
				t.Errorf("Prompt rendered fix-prompt.md:\n%s", result.Prompt)
			}
			if result.Handoff.ReviewPromptFile != "" {
				t.Errorf("Handoff.ReviewPromptFile = %q, want empty", result.Handoff.ReviewPromptFile)
			}
			if result.ReviewPromptText == "" {
				t.Error("ReviewPromptText is empty, want the rendered review-prompt.md")
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

// Issue #4562: scout-prompt.md carries no caveman anchor whether or not
// CAVEMAN_BAKED is set (a roster subagent with no Skill tool under the claude
// Driver), while SKILL_PREAMBLE still follows SKILLS_FOUND.
func TestAssembleScoutPromptNoCavemanWithSkillPreamble(t *testing.T) {
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
		// /caveman, not bare "caveman": SKILL_PREAMBLE still lists the caveman skill by name.
		if strings.Contains(prompt, "/caveman") {
			t.Errorf("scout.prompt mentions caveman with CAVEMAN_BAKED on, want absent (issue #4562): %q", prompt)
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
		if strings.Contains(prompt, "caveman") {
			t.Errorf("scout.prompt mentions caveman, want absent (issue #4562): %q", prompt)
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

// Issue #4562: worker-prompt.md carries no caveman anchor whether or not
// CAVEMAN_BAKED is set (a roster subagent with no Skill tool under the claude
// Driver), while SKILL_PREAMBLE still follows SKILLS_FOUND.
func TestAssembleWorkerPromptNoCavemanWithSkillPreamble(t *testing.T) {
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
		// /caveman, not bare "caveman": SKILL_PREAMBLE still lists the caveman skill by name.
		if strings.Contains(prompt, "/caveman") {
			t.Errorf("worker.prompt mentions caveman with CAVEMAN_BAKED on, want absent (issue #4562): %q", prompt)
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
		if strings.Contains(prompt, "caveman") {
			t.Errorf("worker.prompt mentions caveman, want absent (issue #4562): %q", prompt)
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
// section, an exactly-one-on pair like TDD_BAKED/TDD_UNBAKED. Each arm must
// carry only its own text, with no
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

// A kind with its own Prompts.Base but no SelfContainedBase (no real kind
// today; a synthetic Descriptor stands in for a future one) run with
// SelfContained=true must be rejected up front rather than falling through
// to an empty baseName that then reads PromptsDir itself.
func TestAssembleSelfContainedRequiresSubMode(t *testing.T) {
	synth := &dispatchkind.Descriptor{
		Name:   "synthetic-no-self-contained",
		Verb:   "synthetic",
		Keying: dispatchkind.ByIssue,
		Labels: dispatchkind.LabelsConfigured,
		Prompts: dispatchkind.Prompts{
			Base: "issue-prompt.md",
		},
	}
	dispatchkind.All = append(dispatchkind.All, synth)
	t.Cleanup(func() {
		dispatchkind.All = dispatchkind.All[:len(dispatchkind.All)-1]
	})

	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.DispatchKind = synth.Name
	env.SelfContained = true

	_, err := Assemble(env, reg)
	if err == nil {
		t.Fatal("Assemble: got nil error, want ErrUnsupportedCell")
	}
	if !errors.Is(err, ErrUnsupportedCell) {
		t.Errorf("Assemble error = %v, want it to wrap ErrUnsupportedCell", err)
	}
}

// A descriptor built outside the real kinds with no Contract set must fail
// loudly rather than silently inject no contract block.
func TestAssembleUnknownContractRejected(t *testing.T) {
	synth := &dispatchkind.Descriptor{
		Name:   "synthetic-no-contract",
		Verb:   "synthetic",
		Keying: dispatchkind.ByIssue,
		Labels: dispatchkind.LabelsConfigured,
		Prompts: dispatchkind.Prompts{
			Base: "issue-prompt.md",
		},
	}
	dispatchkind.All = append(dispatchkind.All, synth)
	t.Cleanup(func() {
		dispatchkind.All = dispatchkind.All[:len(dispatchkind.All)-1]
	})

	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.DispatchKind = synth.Name

	_, err := Assemble(env, reg)
	if err == nil || !strings.Contains(err.Error(), "unknown prompt contract") {
		t.Fatalf("Assemble error = %v, want unknown prompt contract", err)
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
	// Env.ResearchVerdicts via forge.VerdictLabels.RenderPrompt
	// (issues #2504, #4159), not a literal typed into the template.
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
// BoxWriteEnabled condition (gates_tracker.go's
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
			env.FilerEnabled = true
			env.BoxWriteEnabled = true

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
			for _, signalCarrier := range []string{"", "log", "socket"} {
				boxWriteEnabled, signalCarrier := boxWriteEnabled, signalCarrier
				t.Run(fmt.Sprintf("%s/never direct-file boxWrite=%v carrier=%q", name, boxWriteEnabled, signalCarrier), func(t *testing.T) {
					env := coveredEnv()
					env.DispatchKind = "research"
					env.SelfContained = selfContained
					env.FilerEnabled = true
					env.BoxWriteEnabled = boxWriteEnabled
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
		env.FilerEnabled = true
		env.BoxWriteEnabled = true
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
	// caveman-default.md names SPINDRIFT_PR_INTENT unconditionally in
	// its marker-grammar exemption paragraph, so assert against the log
	// fragments' actual substituted nonce line, not the bare marker name.
	if !strings.Contains(logResult.Prompt, "SPINDRIFT_PR_INTENT run-nonce-abc123") {
		t.Errorf("SignalCarrier=log prompt missing the open-pr-create-outbox.md/if-blocked-pr-outbox.md substituted SPINDRIFT_PR_INTENT line: %q", logResult.Prompt)
	}
	// caveman-default.md also names the bare `driver-exec signal pr-intent`
	// verb unconditionally in its exemption paragraph, so anchor on the
	// socket fragments' own flag-bearing command line, not the bare verb.
	if strings.Contains(logResult.Prompt, `driver-exec signal pr-intent -title '<conventional title>'`) {
		t.Errorf("SignalCarrier=log prompt contains the socket fragments' driver-exec command line, want absent: %q", logResult.Prompt)
	}

	socketEnv := base
	socketEnv.SignalCarrier = "socket"
	socketResult, err := Assemble(socketEnv, reg)
	if err != nil {
		t.Fatalf("Assemble(socket): %v", err)
	}
	if !strings.Contains(socketResult.Prompt, `driver-exec signal pr-intent -title '<conventional title>'`) {
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
	if strings.Contains(logFilerPrompt, `driver-exec signal issue-intent -title '<title>' -type bug`) {
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
	if !strings.Contains(socketFilerPrompt, `driver-exec signal issue-intent -title '<title>' -type bug`) {
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
	base.FilerEnabled = true
	base.BoxWriteEnabled = true

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
	if err := os.WriteFile(
		filepath.Join(promptsFixtureDir, "review-prompt.md"),
		[]byte("# REVIEW\n"),
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

// overridePromptsDir stages a Consumer prompt-dir override (a
// SPINDRIFT_PROMPT_DIR mount): the given template files plus the real
// fragments dir, which the fragment loop reads whichever base is selected.
func overridePromptsDir(t *testing.T, templates map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	fragmentsDir, err := filepath.Abs(filepath.Join(promptsDir, "fragments"))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if err := os.Symlink(fragmentsDir, filepath.Join(dir, "fragments")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	for name, content := range templates {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}
	return dir
}

// Issue #420 / #455, the ported entrypoint-outcome-contract.bats cases: a
// runtime prompt-dir override lacking a shared block gets the canonical one
// appended exactly once, and an override already carrying it is left alone.
// Without the appended outcome contract the agent never emits the outcome
// line and the launcher never learns the PR.
func TestAssembleOverrideSharedBlocks(t *testing.T) {
	reg := loadTestRegistry(t)
	const reviewStub = "reviewer stub\n\nVERDICT: APPROVE or BLOCK\n"
	const ownBlocks = "stub\n\n# COMMS\n\nown comms\n\n# CHECK\n\nown check\n\n# LAND THE CHANGE\n\nown outcome\n"

	cases := []struct {
		name     string
		fixPass  int
		template string
		content  string
		appended bool
		want     []string
	}{
		{
			name:     "issue override lacking outcome contract gets it appended",
			template: "issue-prompt.md",
			content:  "issue stub, no contract here\n",
			appended: true,
			want:     []string{"canonical comms", "canonical check", "canonical outcome"},
		},
		{
			name:     "issue override already containing outcome contract is unchanged",
			template: "issue-prompt.md",
			content:  ownBlocks,
			want:     []string{"own comms", "own check", "own outcome"},
		},
		{
			name:     "fix override gets COMMS/CHECK/outcome appended",
			fixPass:  2,
			template: "fix-prompt.md",
			content:  "fix stub, no shared blocks here\n",
			appended: true,
			want:     []string{"canonical comms", "canonical check", "canonical outcome"},
		},
		{
			name:     "fix override already containing shared blocks is unchanged",
			fixPass:  2,
			template: "fix-prompt.md",
			content:  ownBlocks,
			want:     []string{"own comms", "own check", "own outcome"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			templates := map[string]string{
				"issue-prompt.md":  "issue stub\n",
				"review-prompt.md": reviewStub,
			}
			templates[tc.template] = tc.content
			contracts := t.TempDir()
			env := coveredEnv()
			env.FixPass = tc.fixPass
			env.PromptsDir = overridePromptsDir(t, templates)
			env.CommsContractFile = writeContractFile(t, contracts, "comms-contract.md", "# COMMS\n\ncanonical comms\n")
			env.CheckContractFile = writeContractFile(t, contracts, "check-contract.md", "# CHECK\n\ncanonical check\n")
			env.OutcomeContractFile = writeContractFile(t, contracts, "outcome-contract.md", "# LAND THE CHANGE\n\ncanonical outcome\n")

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			for _, marker := range []string{"# COMMS", "# CHECK", "# LAND THE CHANGE"} {
				if n := strings.Count(result.Prompt, marker); n != 1 {
					t.Errorf("%q occurs %d times, want 1:\n%s", marker, n, result.Prompt)
				}
			}
			for _, want := range tc.want {
				if !strings.Contains(result.Prompt, want) {
					t.Errorf("Prompt missing %q:\n%s", want, result.Prompt)
				}
			}
			if !tc.appended && strings.Contains(result.Prompt, "canonical") {
				t.Errorf("Prompt carries a canonical block despite the override already containing it:\n%s", result.Prompt)
			}
			if tc.appended {
				comms := strings.Index(result.Prompt, "# COMMS")
				check := strings.Index(result.Prompt, "# CHECK")
				outcome := strings.Index(result.Prompt, "# LAND THE CHANGE")
				if !(comms < check && check < outcome) {
					t.Errorf("blocks out of order: comms=%d check=%d outcome=%d", comms, check, outcome)
				}
			}
		})
	}
}

// A missing or unreadable OUTCOME_CONTRACT_FILE must fail Assemble loudly
// rather than proceed without the contract, the failure mode #420 exists to
// prevent (ported from entrypoint-outcome-contract.bats).
func TestAssembleMissingOutcomeContractFileFails(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.PromptsDir = overridePromptsDir(t, map[string]string{
		"issue-prompt.md":  "issue stub, no contract here\n",
		"review-prompt.md": "reviewer stub\n\nVERDICT: APPROVE or BLOCK\n",
	})
	env.OutcomeContractFile = filepath.Join(t.TempDir(), "does-not-exist.md")

	_, err := Assemble(env, reg)
	if err == nil {
		t.Fatal("Assemble succeeded, want an error for the missing outcome contract file")
	}
	if !strings.Contains(err.Error(), "does-not-exist.md") {
		t.Errorf("error %q does not name the missing contract file", err)
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

// The research kind's own outcome contract ("# POST THE VERDICT", issue #640)
// follows the same override rules as the work kind's (TestAssembleOverrideSharedBlocks),
// ported from the deleted research-kind bats suite.
func TestAssembleResearchOverrideVerdictBlock(t *testing.T) {
	reg := loadTestRegistry(t)
	cases := []struct {
		name     string
		content  string
		contract string
		want     string
		absent   string
	}{
		{
			name:     "override lacking the contract gets it appended once, tokens substituted",
			content:  "research stub, no contract here\n",
			contract: "# POST THE VERDICT\n\ncanonical research contract for issue ${ISSUE_NUMBER}\n",
			want:     "canonical research contract for issue 2349",
		},
		{
			name:     "override already containing the contract is unchanged",
			content:  "research stub\n\n# POST THE VERDICT\n\nalready has its own contract\n",
			contract: "# POST THE VERDICT\n\nshould not appear\n",
			want:     "already has its own contract",
			absent:   "should not appear",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.DispatchKind = "research"
			env.PromptsDir = overridePromptsDir(t, map[string]string{"research-prompt.md": tc.content})
			env.ResearchOutcomeContractFile = writeContractFile(t, t.TempDir(), "research-outcome-contract.md", tc.contract)

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if n := strings.Count(result.Prompt, "# POST THE VERDICT"); n != 1 {
				t.Errorf("%q occurs %d times, want 1:\n%s", "# POST THE VERDICT", n, result.Prompt)
			}
			if !strings.Contains(result.Prompt, tc.want) {
				t.Errorf("Prompt missing %q:\n%s", tc.want, result.Prompt)
			}
			if tc.absent != "" && strings.Contains(result.Prompt, tc.absent) {
				t.Errorf("Prompt contains %q, want it absent:\n%s", tc.absent, result.Prompt)
			}
		})
	}
}

// A missing research contract file fails Assemble loudly rather than letting
// the Box run without the verdict contract (the research twin of
// TestAssembleMissingOutcomeContractFileFails).
func TestAssembleMissingResearchOutcomeContractFileFails(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.DispatchKind = "research"
	env.PromptsDir = overridePromptsDir(t, map[string]string{"research-prompt.md": "research stub, no contract here\n"})
	env.ResearchOutcomeContractFile = filepath.Join(t.TempDir(), "does-not-exist.md")

	_, err := Assemble(env, reg)
	if err == nil {
		t.Fatal("Assemble succeeded, want an error for the missing research contract file")
	}
	if !strings.Contains(err.Error(), "does-not-exist.md") {
		t.Errorf("error %q does not name the missing contract file", err)
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
	env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x","effort":"review-effort-x"},"scout":{"model":"scout-model-y"}}`
	env.AgentsPromptFiles = `{"scout":"fragments/tdd-baked.md"}`
	env.IssueText = "issue body text"

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
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

// Issue #2698's commit-rework-orchestrator.md is an ungated row, so it
// renders on every fresh-work prompt.
// Only the marker is asserted here; byte-identity of the prompt
// is what the golden fixtures in tests/testdata/prompt-assembly-golden pin.
func TestAssembleOrchestratorCommitReworkFragment(t *testing.T) {
	reg := loadTestRegistry(t)
	const marker = "fold each fix into the commit it logically belongs to"

	env := coveredEnv()

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(result.Prompt, marker) {
		t.Errorf("Prompt missing commit-rework-orchestrator.md fragment text:\n%s", result.Prompt)
	}
}

// Issue #3214's land-pass-order-orchestrator.md is ungated like the
// two fragments above. This also
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
	if row.Gate != "" {
		t.Errorf("land-pass-order-orchestrator.md row gate = %q, want ungated", row.Gate)
	}
	if row.Var != "LAND_PASS_ORDER_ORCHESTRATOR_STEP" {
		t.Errorf("land-pass-order-orchestrator.md row var = %q, want LAND_PASS_ORDER_ORCHESTRATOR_STEP", row.Var)
	}

	const marker = "This ordering supersedes the COMMIT section's"

	env := coveredEnv()

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(result.Prompt, marker) {
		t.Errorf("Prompt missing land-pass-order-orchestrator.md fragment text:\n%s", result.Prompt)
	}
}

// With no reviewer key in the template, ReviewModel and ReviewEffort stay
// empty, mirroring jq's `.reviewer.model // empty`. review-prompt.md still
// renders: it does not depend on a reviewer being configured.
func TestAssembleOrchestratorNoReviewerKey(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
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

// A read-only box is a covered cell now, the filer
// relay precondition axis from issue #2353.
func TestAssembleOrchestratorBoxReadOnlyCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.BoxWriteEnabled = false

	if _, err := Assemble(env, reg); err != nil {
		t.Errorf("Assemble: %v, want nil error (box read-only is covered)", err)
	}
}

// The skills-absent cell (issue #2353) is covered,
// and the prompt omits the skill-preamble text.
func TestAssembleOrchestratorSkillsAbsentCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
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

// A fix pass is a covered cell (issue #2354), reachable
// in production because fix-pass Boxes run the same orchestrator path as
// fresh work. ReviewPromptFile stays empty, since
// only a fresh work dispatch populates it, but ReviewModel still populates:
// its extraction is unconditional.
func TestAssembleOrchestratorFixPassCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.FixPass = 1
	env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x"}}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v, want nil error (fix pass is covered)", err)
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
		t.Errorf("Handoff.ReviewModel = %q, want %q (extraction is unconditional)", result.Handoff.ReviewModel, "review-model-x")
	}
}

// A research dispatch is a covered cell (issue #2354).
// ReviewPromptFile stays empty because research never reviews (ADR 0022),
// while ReviewModel still populates, as in the fix-pass cell above.
func TestAssembleOrchestratorResearchCovered(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.DispatchKind = "research"
	env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x"}}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v, want nil error (research kind is covered)", err)
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
		t.Errorf("Handoff.ReviewModel = %q, want %q (extraction is unconditional)", result.Handoff.ReviewModel, "review-model-x")
	}
}

// A baked opencode agent file fixture: real frontmatter shape, and a body
// distinguishable from any real rendered prompt.
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

// agentFileFrontmatter returns the file up to and including its second fence.
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
// TestAssembleWorkerPromptNoCavemanWithSkillPreamble's renderAgentsJSON path.
func TestAssembleDriverAgentFilesWorkerNoCavemanWithSkillPreamble(t *testing.T) {
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
		// /caveman, not bare "caveman": SKILL_PREAMBLE still lists the caveman skill by name.
		if strings.Contains(body, "/caveman") {
			t.Errorf("worker.md body mentions caveman with CAVEMAN_BAKED on, want absent (issue #4562): %q", body)
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
		if strings.Contains(body, "caveman") {
			t.Errorf("worker.md body mentions caveman, want absent (issue #4562): %q", body)
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

// The scout counterpart of
// TestAssembleDriverAgentFilesWorkerNoCavemanWithSkillPreamble: the on-disk
// scout.md rewrite must reach the same no-anchor result for scout-prompt.md
// as TestAssembleScoutPromptNoCavemanWithSkillPreamble's renderAgentsJSON path.
func TestAssembleDriverAgentFilesScoutNoCavemanWithSkillPreamble(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("skills present", func(t *testing.T) {
		dir := t.TempDir()
		writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")

		env := coveredEnv()
		env.DriverAgentFilesDir = dir
		env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`

		if _, err := Assemble(env, reg); err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		body := agentFileBody(t, filepath.Join(dir, "scout.md"))
		// /caveman, not bare "caveman": SKILL_PREAMBLE still lists the caveman skill by name.
		if strings.Contains(body, "/caveman") {
			t.Errorf("scout.md body mentions caveman with CAVEMAN_BAKED on, want absent (issue #4562): %q", body)
		}
		if !strings.Contains(body, "Skills available:") {
			t.Errorf("scout.md body missing skill-preamble.md fragment text: %q", body)
		}
	})

	t.Run("skills absent", func(t *testing.T) {
		dir := t.TempDir()
		writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")

		env := coveredEnv()
		env.SkillsFound = ""
		env.CavemanSkillBaked = false
		env.TDDSkillBaked = false
		env.CommitSkillBaked = false
		env.CodeReviewSkillBaked = false
		env.DriverAgentFilesDir = dir
		env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`

		if _, err := Assemble(env, reg); err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		body := agentFileBody(t, filepath.Join(dir, "scout.md"))
		if strings.Contains(body, "caveman") {
			t.Errorf("scout.md body mentions caveman, want absent (issue #4562): %q", body)
		}
		if strings.Contains(body, "Skills available:") {
			t.Errorf("scout.md body contains skill-preamble.md fragment text, want absent (SKILLS_FOUND gate off): %q", body)
		}
		if strings.Contains(body, "${") {
			t.Errorf("scout.md body still contains an unsubstituted ${...} token: %q", body)
		}
	})
}

// The file-based reviewer drop (entrypoint.sh: 1141-1156): reviewer.md's
// `model:` scalar populates Handoff.ReviewModel and the file is removed, while
// a non-reviewer roster file still gets its body rewritten.
func TestAssembleDriverAgentFilesReviewerDrop(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")
	writeAgentFile(t, filepath.Join(dir, "reviewer.md"), "reviewer")

	env := coveredEnv()
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
		if bodies.review == nil {
			t.Fatalf("expected a review body for the work/FixPass==0 cell")
		}
		assertBodyMatches(t, bodies.review, result.ReviewPromptText)
	})
}

// butlerEnv is a fixture Env sitting in Assemble's covered ByChore cell
// (ADR 0056, issue #3875): no ISSUE_NUMBER/ISSUE_TITLE at all, in place of
// which CHORE_NAME/CHORE_HEAD/CHORE_DIFF_RANGE/CHORE_SLICE carry the Chore
// key dispatch.go's buildBoxEnv forwards for a Factory.NewChore Dispatch.
func butlerEnv() Env {
	env := coveredEnv()
	env.DispatchKind = "butler"
	env.IssueNumber = ""
	env.IssueTitle = ""
	env.Branch = "agent/butler-bugs"
	env.ChoreName = "bugs"
	env.DispatchKey = "butler-bugs" // dispatch.go's buildBoxEnv: dispatchkey.Chore(name).String()
	env.ChoreHead = "deadbeef"
	env.ChoreDiffRange = "cafef00d..deadbeef"
	env.ChoreSlice = "cmd/launcher/main.go\ncmd/launcher/internal/dispatch/dispatch.go"
	env.ChoreClasses = "flaky-test dead-code"
	env.ChoreClassList = "flaky-test dead-code typo"
	env.ChoreMaxFindings = "5"
	return env
}

// The butler cell renders butler-prompt.md, substitutes every CHORE_* var,
// and needs no ISSUE_NUMBER at all (the acceptance criteria driving this
// slice): the rendered prompt must carry none of the ISSUE_NUMBER-shaped
// text research-prompt.md would.
func TestAssembleButlerKindRendersButlerPrompt(t *testing.T) {
	reg := loadTestRegistry(t)
	env := butlerEnv()

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	for _, want := range []string{
		"bugs",
		"deadbeef",
		"cafef00d..deadbeef",
		"cmd/launcher/main.go",
		"Look for latent bugs",
		"flaky-test dead-code",
		"flaky-test dead-code typo",
		"Relay at most 5 findings",
	} {
		if !strings.Contains(result.Prompt, want) {
			t.Errorf("Prompt missing %q:\n%s", want, result.Prompt)
		}
	}
	for _, unwanted := range []string{"${CHORE_", "Research GitHub issue"} {
		if strings.Contains(result.Prompt, unwanted) {
			t.Errorf("Prompt contains %q, want absent:\n%s", unwanted, result.Prompt)
		}
	}
	if result.Handoff.SessionMode != "initial" {
		t.Errorf("Handoff.SessionMode = %q, want %q", result.Handoff.SessionMode, "initial")
	}
}

// The butler prompt's class rule must track signalwire.ValidClass: a Box
// reporting a class outside it loses the whole finding (issue #4040). The
// needle derives from ClassRule, itself pinned to MaxClassLen, so a grammar
// change fails here instead of leaving the prompt stale.
func TestButlerPromptStatesClassSlugRule(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(promptsDir, "butler-prompt.md"))
	if err != nil {
		t.Fatalf("reading butler-prompt.md: %v", err)
	}
	prompt := strings.Join(strings.Fields(string(b)), " ")

	rule := strings.TrimPrefix(signalwire.ClassRule, "must be ")
	rule = strings.ReplaceAll(rule, "'-'", "`-`")
	for _, want := range []string{rule, "rejected, and the finding with it"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("butler-prompt.md missing %q", want)
		}
	}
}

// The patch rung's prompt instructions (ADR 0057, issue #4073) render only
// when the host forwards a non-empty CHORE_PATCH_CLASSES: butlerEnv leaves
// it unset, matching a Chore with the patch rung off, or today's patch room
// spent.
func TestAssembleButlerPatchClassesGate(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name              string
		chorePatchClasses string
		wantRendered      bool
	}{
		{name: "unset", chorePatchClasses: "", wantRendered: false},
		{name: "set", chorePatchClasses: "docs-drift", wantRendered: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := butlerEnv()
			env.ChorePatchClasses = tc.chorePatchClasses

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			const marker = "Patch-eligible classes for this run"
			got := strings.Contains(result.Prompt, marker)
			if got != tc.wantRendered {
				t.Errorf("Prompt contains butler-patch.md text = %v, want %v:\n%s", got, tc.wantRendered, result.Prompt)
			}
			if tc.wantRendered && !strings.Contains(result.Prompt, tc.chorePatchClasses) {
				t.Errorf("Prompt missing substituted CHORE_PATCH_CLASSES value %q:\n%s", tc.chorePatchClasses, result.Prompt)
			}
		})
	}
}

// A ByChore kind with an unresolvable CHORE_NAME fails assembly outright
// (choreSection), unlike ISSUE_TEXT's silent-empty default.
func TestAssembleButlerUnknownChoreFails(t *testing.T) {
	reg := loadTestRegistry(t)
	env := butlerEnv()
	env.ChoreName = "no-such-chore"

	_, err := Assemble(env, reg)
	if err == nil {
		t.Fatal("Assemble: expected an error for an unknown chore, got nil")
	}
	if !strings.Contains(err.Error(), "no-such-chore") {
		t.Errorf("Assemble error = %v, want it to name the unresolved chore", err)
	}
}

// CHORE_NAME must be validated as a simple name before it ever reaches
// filepath.Join, so a path separator or ".." cannot escape prompts/chores/.
func TestAssembleButlerPathTraversalChoreNameFails(t *testing.T) {
	reg := loadTestRegistry(t)

	for _, name := range []string{"../../etc/passwd", "bugs/../../x", "bugs/x", ""} {
		env := butlerEnv()
		env.ChoreName = name

		_, err := Assemble(env, reg)
		if err == nil {
			t.Errorf("Assemble: expected an error for CHORE_NAME %q, got nil", name)
		}
	}
}

// Each built-in Chore's own prompt file renders under ${CHORE_PROMPT}.
func TestAssembleButlerRendersEachBuiltinChorePrompt(t *testing.T) {
	reg := loadTestRegistry(t)

	for _, name := range []string{"bugs", "refactor", "docs-drift"} {
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(promptsDir, "chores", name+".md"))
			if err != nil {
				t.Fatalf("read chore prompt %s: %v", name, err)
			}

			env := butlerEnv()
			env.ChoreName = name

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if !strings.Contains(result.Prompt, strings.TrimRight(string(want), "\n")) {
				t.Errorf("Prompt missing chore %s's own prompt text:\n%s", name, result.Prompt)
			}
		})
	}
}

// promptsDirWithChore builds a PromptsDir shaped like a Consumer's
// SPINDRIFT_PROMPT_DIR override: the real tree, except that chores/ holds
// only the one given Chore prompt file.
func promptsDirWithChore(t *testing.T, name, content string) string {
	t.Helper()
	dir, _ := promptsDirExceptSubdir(t, "chores")
	if err := os.Mkdir(filepath.Join(dir, "chores"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chores", name+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
	return dir
}

// A prompt-dir override (mkHarness choresDir / SPINDRIFT_PROMPT_DIR) can
// replace a built-in Chore's prompt file entirely: the override's own text
// renders, and the shipped default's text is gone.
func TestAssembleButlerPromptDirOverridesBuiltinChore(t *testing.T) {
	reg := loadTestRegistry(t)
	builtin, err := os.ReadFile(filepath.Join(promptsDir, "chores", "bugs.md"))
	if err != nil {
		t.Fatalf("read chore prompt bugs: %v", err)
	}
	env := butlerEnv()
	env.PromptsDir = promptsDirWithChore(t, "bugs", "Sentinel override text for the bugs chore.\n")

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(result.Prompt, "Sentinel override text for the bugs chore.") {
		t.Errorf("Prompt missing prompt-dir override text:\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, strings.TrimRight(string(builtin), "\n")) {
		t.Errorf("Prompt still contains the built-in bugs.md text despite the override:\n%s", result.Prompt)
	}
}

// A Consumer declares its own Chore -- one outside the built-in catalog --
// by shipping chores/<name>.md in its prompt-dir override; no launcher
// change is needed (lib/mkHarness.nix's choresDir contract).
func TestAssembleButlerRendersConsumerDeclaredChore(t *testing.T) {
	reg := loadTestRegistry(t)
	env := butlerEnv()
	env.ChoreName = "tidy-deps"
	env.PromptsDir = promptsDirWithChore(t, "tidy-deps", "Look for stale dependency pins.\n")

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(result.Prompt, "Look for stale dependency pins.") {
		t.Errorf("Prompt missing Consumer-declared chore text:\n%s", result.Prompt)
	}
}

// Issue #3875 (ADR 0056): with the Filer provisioned, the butler's own FILE
// FINDINGS section renders, carries SPINDRIFT_ISSUE_INTENT, and names the
// agent-butler-finding label the launcher applies host-side -- never the
// agent-research-finding label the same base gate produces for research.
func TestAssembleButlerFileFindingsRelay(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("filer enabled", func(t *testing.T) {
		env := butlerEnv()
		env.FilerEnabled = true

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if !strings.Contains(result.Prompt, "**File findings.**") {
			t.Errorf("Prompt missing the FILE FINDINGS section: %q", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "SPINDRIFT_ISSUE_INTENT") {
			t.Errorf("Prompt missing SPINDRIFT_ISSUE_INTENT from butler-file-issues-relay.md: %q", result.Prompt)
		}
		if !strings.Contains(result.Prompt, "agent-butler-finding") {
			t.Errorf("Prompt missing the agent-butler-finding label mention: %q", result.Prompt)
		}
		if strings.Contains(result.Prompt, "agent-research-finding") {
			t.Errorf("Prompt wrongly carries the research-only agent-research-finding label mention: %q", result.Prompt)
		}
	})

	t.Run("filer not enabled", func(t *testing.T) {
		env := butlerEnv()
		env.FilerEnabled = false

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		if strings.Contains(result.Prompt, "**File findings.**") {
			t.Errorf("Prompt unexpectedly contains the FILE FINDINGS section: %q", result.Prompt)
		}
	})
}

// FILER_FILE_RELAY_RESEARCH and FILER_FILE_RELAY_BUTLER are exactly the
// same shape of gate (AdviseOnly && FilerEnabled) split further on Settle,
// so a research Env must never trip the butler gate and vice versa
// (gates_tracker.go).
func TestGatesFilerFileRelayResearchAndButlerAreDisjoint(t *testing.T) {
	research := coveredEnv()
	research.DispatchKind = "research"
	research.FilerEnabled = true
	rg := Gates(research)
	if !rg["FILER_FILE_RELAY_RESEARCH"] {
		t.Error("FILER_FILE_RELAY_RESEARCH = false for a research Env with the Filer enabled, want true")
	}
	if rg["FILER_FILE_RELAY_BUTLER"] {
		t.Error("FILER_FILE_RELAY_BUTLER = true for a research Env, want false")
	}

	butler := butlerEnv()
	butler.FilerEnabled = true
	bg := Gates(butler)
	if bg["FILER_FILE_RELAY_RESEARCH"] {
		t.Error("FILER_FILE_RELAY_RESEARCH = true for a butler Env, want false")
	}
	if !bg["FILER_FILE_RELAY_BUTLER"] {
		t.Error("FILER_FILE_RELAY_BUTLER = false for a butler Env with the Filer enabled, want true")
	}
}

// Butler review findings #1 and #2 (ADR 0056, issue #3880): the butler is
// AdviseOnly, so it never gets the orchestrator's code-owned review pass
// (the AdviseOnly gate on review-prompt.md), which means its `reviewer`
// subagent is the only review a promotion candidate gets. Dropping the
// reviewer key (work's behavior) would leave no finding ever reviewed;
// rendering it from review-prompt.md (a branch-diff rubric) would leave the
// reviewer judging an issue, branch, and diff it never has.
func TestAssembleButlerReviewerKeptAndRendersButlerReviewPrompt(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("butler", func(t *testing.T) {
		env := butlerEnv()
		env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x"}}`
		env.AgentsPromptFiles = `{"reviewer":"review-prompt.md"}`

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
			t.Fatalf("AgentsJSON missing reviewer key, want kept for the butler's inline review: %s", result.AgentsJSON)
		}
		var reviewer struct {
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(reviewerRaw, &reviewer); err != nil {
			t.Fatalf("unmarshal reviewer entry: %v", err)
		}
		if !strings.Contains(reviewer.Prompt, "one butler finding handed to you") {
			t.Errorf("reviewer.prompt = %q, want the rendered butler-review-prompt.md, not review-prompt.md", reviewer.Prompt)
		}
		if strings.Contains(reviewer.Prompt, "adversarially review a branch diff") {
			t.Errorf("reviewer.prompt = %q, want no review-prompt.md content", reviewer.Prompt)
		}
		// A kind owning a reviewer prompt marks its review advisory so its
		// verdict never steers the orchestrator's pass loop (issue #3925).
		if !result.Handoff.AdvisoryReviewer {
			t.Errorf("Handoff.AdvisoryReviewer = false, want true")
		}
	})
}

// The reviewer's "Exact" rubric line (ADR 0057, issue #4073) only belongs
// in the rendered prompt on a run that can actually hand the reviewer a
// diff: CHORE_PATCH_CLASSES gates butler-review-patch.md exactly like it
// gates butler-patch.md (assemble_test.go's butlerEnv leaves it blank).
func TestAssembleButlerReviewerPromptCarriesExactRubricOnlyWithPatchClasses(t *testing.T) {
	reg := loadTestRegistry(t)

	renderReviewerPrompt := func(t *testing.T, env Env) string {
		t.Helper()
		env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x"}}`
		env.AgentsPromptFiles = `{"reviewer":"review-prompt.md"}`

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal([]byte(result.AgentsJSON), &parsed); err != nil {
			t.Fatalf("unmarshal AgentsJSON: %v\n%s", err, result.AgentsJSON)
		}
		var reviewer struct {
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(parsed["reviewer"], &reviewer); err != nil {
			t.Fatalf("unmarshal reviewer entry: %v", err)
		}
		return reviewer.Prompt
	}

	t.Run("without CHORE_PATCH_CLASSES", func(t *testing.T) {
		prompt := renderReviewerPrompt(t, butlerEnv())
		if strings.Contains(prompt, "**Exact**") {
			t.Errorf("reviewer.prompt = %q, want no Exact rubric line without CHORE_PATCH_CLASSES", prompt)
		}
	})

	t.Run("with CHORE_PATCH_CLASSES", func(t *testing.T) {
		env := butlerEnv()
		env.ChorePatchClasses = "docs-drift"
		prompt := renderReviewerPrompt(t, env)
		if !strings.Contains(prompt, "**Exact**") {
			t.Errorf("reviewer.prompt = %q, want the Exact rubric line with CHORE_PATCH_CLASSES set", prompt)
		}
	})
}

// A kind with no reviewer prompt of its own (work, research) never sets
// AdvisoryReviewer: its inline reviewer subagent is
// dropped there, so no advisory verdict exists to suppress (issue #3925).
func TestAssembleAdvisoryReviewerOnlyForKindWithOwnReviewerPrompt(t *testing.T) {
	reg := loadTestRegistry(t)

	for _, kind := range []string{"work", "research"} {
		t.Run(kind, func(t *testing.T) {
			env := coveredEnv()
			env.DispatchKind = kind
			if kind == "research" {
			}

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if result.Handoff.AdvisoryReviewer {
				t.Errorf("Handoff.AdvisoryReviewer = true for kind %q, want false", kind)
			}
		})
	}
}

// The opencode agent-files twin of the test above: reviewer.md must survive
// (never removed) and its body must come from the kind's own reviewer prompt.
func TestAssembleButlerDriverAgentFilesReviewerKeptAndRewritten(t *testing.T) {
	reg := loadTestRegistry(t)

	t.Run("butler", func(t *testing.T) {
		dir := t.TempDir()
		writeAgentFile(t, filepath.Join(dir, "reviewer.md"), "reviewer")

		env := butlerEnv()
		env.DriverAgentFilesDir = dir
		env.AgentsPromptFiles = `{"reviewer":"review-prompt.md"}`

		if _, err := Assemble(env, reg); err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		body := agentFileBody(t, filepath.Join(dir, "reviewer.md"))
		if !strings.Contains(body, "one butler finding handed to you") {
			t.Errorf("reviewer.md body = %q, want the rendered butler-review-prompt.md, not review-prompt.md", body)
		}
		if strings.Contains(body, "adversarially review a branch diff") {
			t.Errorf("reviewer.md body = %q, want no review-prompt.md content", body)
		}
	})
}

// work's reviewer-drop stays exactly as it was (TestAssembleOrchestratorReviewerDrop
// already pins the JSON path); this pins the opencode agent-files path's twin,
// which this issue's rewriteAgentFiles signature change could otherwise regress.
func TestAssembleWorkDriverAgentFilesReviewerStillDropped(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	writeAgentFile(t, filepath.Join(dir, "reviewer.md"), "reviewer")

	env := coveredEnv()
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"reviewer":"review-prompt.md"}`

	if _, err := Assemble(env, reg); err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "reviewer.md")); !os.IsNotExist(err) {
		t.Errorf("reviewer.md still exists (or unexpected stat error %v), want removed for work", err)
	}
}

// The research prompt's three verdict renderings (status alternation, verdict
// enum, verdict bullets) are Go-derived from the raw RESEARCH_VERDICTS JSON
// (issues #2630, #4159), so a raw-template prompt-dir override renders the
// configured set in order; empty keeps the default three.
func TestAssembleResearchVerdictRenderingsDerivedFromVerdicts(t *testing.T) {
	reg := loadTestRegistry(t)
	custom := `[{"verdict":"accept","label":"l-accept","description":"take it up"},{"verdict":"decline","label":"l-decline","description":"leave it"}]`
	for _, selfContained := range []bool{false, true} {
		for _, tc := range []struct {
			name, verdicts string
			want           []string
		}{
			{"default", "", []string{
				"status=<recommend|reject|unclear>",
				"`recommend` / `reject` / `unclear`",
				"- `recommend` — relevant, now enriched with real context; promote it.\n- `reject` — false positive, not worth doing, or a duplicate. Name the duplicate issue by number in your rationale; duplicate is a reason under `reject`, not a separate verdict.\n- `unclear` — relevance can't be determined without a human's answer.",
			}},
			{"custom", custom, []string{
				"status=<accept|decline>",
				"`accept` / `decline`",
				"- `accept` — take it up\n- `decline` — leave it",
			}},
		} {
			t.Run(fmt.Sprintf("%s/selfContained=%v", tc.name, selfContained), func(t *testing.T) {
				env := coveredEnv()
				env.DispatchKind = "research"
				env.SelfContained = selfContained
				env.ResearchVerdicts = tc.verdicts
				result, err := Assemble(env, reg)
				if err != nil {
					t.Fatalf("Assemble: %v", err)
				}
				for _, w := range tc.want {
					if !strings.Contains(result.Prompt, w) {
						t.Errorf("Prompt missing %q:\n%s", w, result.Prompt)
					}
				}
				for _, residue := range []string{"${RESEARCH_STATUS_ENUM}", "RESEARCH_VERDICT_BULLETS", "RESEARCH_VERDICT_ENUM"} {
					if strings.Contains(result.Prompt, residue) {
						t.Errorf("Prompt contains unrendered %q", residue)
					}
				}
			})
		}
	}
}

// A prompt nix already rendered at eval time (the baked path) carries none of
// the markers, so assembly with the matching RESEARCH_VERDICTS leaves it as is.
func TestAssembleResearchBakedPromptUnchanged(t *testing.T) {
	reg := loadTestRegistry(t)
	custom := `[{"verdict":"accept","label":"l-accept","description":"take it up"},{"verdict":"decline","label":"l-decline","description":"leave it"}]`
	verdicts, err := forge.ParseResearchVerdicts(custom)
	if err != nil {
		t.Fatalf("ParseResearchVerdicts: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(promptsDir, "research-prompt.md"))
	if err != nil {
		t.Fatalf("read research-prompt.md: %v", err)
	}
	dir, _ := promptsDirExceptSubdir(t, "research-prompt.md")
	baked := verdicts.RenderPrompt(string(raw))
	if forge.HasVerdictTargets(baked) {
		t.Fatalf("baked prompt still carries a target")
	}
	if err := os.WriteFile(filepath.Join(dir, "research-prompt.md"), []byte(baked), 0o644); err != nil {
		t.Fatalf("write baked prompt: %v", err)
	}

	env := coveredEnv()
	env.DispatchKind = "research"
	env.ResearchVerdicts = custom
	fromRaw, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble raw: %v", err)
	}
	env.PromptsDir = dir
	fromBaked, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble baked: %v", err)
	}
	if fromBaked.Prompt != fromRaw.Prompt {
		t.Errorf("baked prompt assembled differently from the raw template:\n%s\nvs\n%s", fromBaked.Prompt, fromRaw.Prompt)
	}
	if !strings.Contains(fromBaked.Prompt, "status=<accept|decline>") {
		t.Errorf("baked Prompt missing status=<accept|decline>:\n%s", fromBaked.Prompt)
	}
}

// RESEARCH_VERDICTS is read only when the base prompt carries a verdict
// marker, so a malformed value cannot abort a work assembly.
func TestAssembleWorkIgnoresMalformedResearchVerdicts(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.ResearchVerdicts = "not json"
	if _, err := Assemble(env, reg); err != nil {
		t.Fatalf("Assemble work with malformed RESEARCH_VERDICTS: %v", err)
	}
}

func TestAssembleResearchInvalidVerdictsFailsAssembly(t *testing.T) {
	reg := loadTestRegistry(t)
	env := coveredEnv()
	env.DispatchKind = "research"
	env.ResearchVerdicts = "not json"
	if _, err := Assemble(env, reg); err == nil || !strings.Contains(err.Error(), "RESEARCH_VERDICTS") {
		t.Fatalf("Assemble error = %v, want a RESEARCH_VERDICTS parse error", err)
	}
}

// Issue #3838: Result.Fragments records every lib/fragments.nix fragment whose
// bytes reach Prompt, ReviewPromptText, or an agent prompt in AgentsJSON.
func TestAssembleFragmentsCoverageRecord(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "fragments"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	files := map[string]string{
		"issue-prompt.md":       "base ${VAR_Z}\n",
		"review-prompt.md":      "review\n",
		"agent.md":              "agent ${VAR_A} ${VAR_Z} ${VAR_A}\n",
		"fragments/z-frag.md":   "zed\n",
		"fragments/a-frag.md":   "ay\n",
		"fragments/m-frag.md":   "unreferenced\n",
		"fragments/off-frag.md": "gated off\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	// Registry order is z before a, so a sorted result proves Assemble sorts
	// rather than echoing registry or reach order.
	reg := Registry{Rows: []FragmentRow{
		{Gate: "CAVEMAN_BAKED", Fragment: "z-frag.md", Var: "VAR_Z"},
		{Gate: "CAVEMAN_BAKED", Fragment: "a-frag.md", Var: "VAR_A"},
		{Gate: "CAVEMAN_BAKED", Fragment: "m-frag.md", Var: "VAR_M"},
		{Gate: "TDD_UNBAKED", Fragment: "off-frag.md", Var: "VAR_OFF"},
	}}

	env := coveredEnv()
	env.PromptsDir = dir
	env.AgentsJSONTemplate = `{"worker":{}}`
	env.AgentsPromptFiles = `{"worker":"agent.md"}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// a-frag.md reaches only the agent prompt; z-frag.md reaches both it and
	// the main prompt (de-duplicated); m-frag.md's gate is on but no template
	// references VAR_M; off-frag.md's gate is off.
	want := []string{"a-frag.md", "z-frag.md"}
	if !slices.Equal(result.Fragments, want) {
		t.Fatalf("Fragments = %v, want %v", result.Fragments, want)
	}
}

// A row that omits its gate renders unconditionally; a row naming an unknown
// gate stays off. The ungated rule is what lets the orchestrator fragments
// drop their old switch gate.
func TestAssembleUngatedRowRendersUnconditionally(t *testing.T) {
	reg := Registry{Rows: []FragmentRow{
		{Fragment: "land-pass-order-orchestrator.md", Var: "LAND_PASS_ORDER_ORCHESTRATOR_STEP"},
		{Gate: "NO_SUCH_GATE", Fragment: "commit-rework-orchestrator.md", Var: "COMMIT_REWORK_ORCHESTRATOR_STEP"},
	}}

	result, err := Assemble(coveredEnv(), reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(result.Prompt, "This ordering supersedes the COMMIT section's") {
		t.Errorf("Prompt missing the ungated row's fragment text:\n%s", result.Prompt)
	}
	if strings.Contains(result.Prompt, fragmentText(t, "commit-rework-orchestrator.md")) {
		t.Errorf("Prompt contains the fragment of a row whose gate is unknown, want absent")
	}
}

// The rewrite is generic over the roster (issue #264): every baked file whose
// name the prompt map carries is rewritten from its own prompt file, with no
// per-name branch, so a custom Nth agent is rewritten like scout and worker.
// The custom prompt names ISSUE_NUMBER, so this also proves substitution ran.
func TestAssembleDriverAgentFilesRewritesRosterGenerically(t *testing.T) {
	reg := loadTestRegistry(t)
	prompts := t.TempDir()
	if err := os.CopyFS(prompts, os.DirFS(promptsDir)); err != nil {
		t.Fatalf("CopyFS: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prompts, "auditor-prompt.md"), []byte("issue ${ISSUE_NUMBER} body\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dir := t.TempDir()
	for _, name := range []string{"scout", "worker", "auditor"} {
		writeAgentFile(t, filepath.Join(dir, name+".md"), name)
	}

	env := coveredEnv()
	env.PromptsDir = prompts
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"scout":"scout-prompt.md","worker":"worker-prompt.md","auditor":"auditor-prompt.md"}`

	if _, err := Assemble(env, reg); err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	for name, want := range map[string]string{
		"scout":   "Return only the brief's path",
		"worker":  "Stay inside the slice you were handed",
		"auditor": "issue 2349 body",
	} {
		body := agentFileBody(t, filepath.Join(dir, name+".md"))
		if !strings.Contains(body, want) {
			t.Errorf("%s.md body missing %q: %q", name, want, body)
		}
		if strings.Contains(body, "placeholder body") {
			t.Errorf("%s.md body not rewritten: %q", name, body)
		}
	}
	if got := agentFileBody(t, filepath.Join(dir, "auditor.md")); got != "issue 2349 body\n" {
		t.Errorf("auditor.md body = %q, want exactly the substituted prompt", got)
	}
}

// Roster names with no baked file are skipped and never created: opencode
// drops a file whose model is empty. With no reviewer.md either, there is no
// model to extract, so the handoff's ReviewModel stays empty.
func TestAssembleDriverAgentFilesAbsentRosterFilesAreNotCreated(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")

	env := coveredEnv()
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md"}`

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	for _, name := range []string{"reviewer", "filer", "worker"} {
		if _, err := os.Stat(filepath.Join(dir, name+".md")); !os.IsNotExist(err) {
			t.Errorf("%s.md exists (stat err %v), want it never created", name, err)
		}
	}
	if body := agentFileBody(t, filepath.Join(dir, "scout.md")); strings.Contains(body, "placeholder body") {
		t.Errorf("scout.md not rewritten: %q", body)
	}
	if result.Handoff.ReviewModel != "" {
		t.Errorf("Handoff.ReviewModel = %q, want empty (no reviewer.md)", result.Handoff.ReviewModel)
	}
}

// An unset DriverAgentFilesDir (a Driver with no on-disk agent files) leaves
// every agent file alone, even with a roster prompt map present.
func TestAssembleDriverAgentFilesUnsetDirLeavesFilesUntouched(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")
	before, err := os.ReadFile(filepath.Join(dir, "scout.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	env := coveredEnv()
	env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`

	if _, err := Assemble(env, reg); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "scout.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("scout.md changed with DriverAgentFilesDir unset:\nbefore: %q\nafter:  %q", before, after)
	}
}

// Cross-Driver parity (issue #2153, AC3): the same roster yields the same
// subagent prompt under either Driver. Claude's --agents JSON .scout.prompt
// and opencode's rewritten scout.md body differ only by the single trailing
// newline the file body carries.
func TestAssembleScoutPromptMatchesAcrossDrivers(t *testing.T) {
	reg := loadTestRegistry(t)
	const roster = `{"scout":"scout-prompt.md"}`

	claude := coveredEnv()
	claude.AgentsJSONTemplate = `{"scout":{"description":"fixture scout description","model":"opus","prompt":"","tools":["Read"]}}`
	claude.AgentsPromptFiles = roster
	jsonResult, err := Assemble(claude, reg)
	if err != nil {
		t.Fatalf("Assemble (claude): %v", err)
	}
	want := agentPromptFromJSON(t, jsonResult.AgentsJSON, "scout")
	if want == "" {
		t.Fatal("claude scout prompt is empty")
	}

	dir := t.TempDir()
	writeAgentFile(t, filepath.Join(dir, "scout.md"), "scout")
	opencode := coveredEnv()
	opencode.DriverAgentFilesDir = dir
	opencode.AgentsPromptFiles = roster
	if _, err := Assemble(opencode, reg); err != nil {
		t.Fatalf("Assemble (opencode): %v", err)
	}
	if got := strings.TrimSuffix(agentFileBody(t, filepath.Join(dir, "scout.md")), "\n"); got != want {
		t.Errorf("opencode scout.md body differs from claude's scout prompt:\nopencode: %q\nclaude:   %q", got, want)
	}
}
