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
// ResumeAfterHold, and ReviewPromptFile/ReviewModel both stay empty.
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
		{name: "wrong issue tracker", mutate: func(e *Env) { e.IssueTracker = "forgejo" }},
		{name: "wrong code forge", mutate: func(e *Env) { e.CodeForge = "forgejo" }},
		{name: "box read-only", mutate: func(e *Env) { e.BoxWriteEnabled = false }},
		{name: "unrecognized dispatch kind", mutate: func(e *Env) { e.DispatchKind = "bogus" }},
		{name: "orchestrator on", mutate: func(e *Env) { e.OrchestratorEnabled = true }},
		{name: "skills not fully baked", mutate: func(e *Env) { e.TDDSkillBaked = false }},
		{name: "no skills found", mutate: func(e *Env) { e.SkillsFound = "" }},
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

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.Prompt, "self-contained research dispatch (ADR 0022, issue #2202)") {
		t.Errorf("Prompt missing research-self-contained-prompt.md's distinguishing text:\n%s", result.Prompt)
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
