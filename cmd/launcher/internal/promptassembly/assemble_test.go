package promptassembly

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/signalwire"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

var promptsDir = repopath.PromptsDir()

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
		OutboxRelayCapable:   true,
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

// Prompts carry literal shell text such as `echo $CODE_FORGE` that the agent
// must read verbatim; a bare $NAME has to survive assembly untouched while the
// braced form beside it is substituted.
func TestAssembleKeepsBareShellVariablesLiteral(t *testing.T) {
	reg, env := syntheticRegistry(t, nil, map[string]string{
		"issue-prompt.md": "run `echo $CODE_FORGE` for issue ${ISSUE_NUMBER}\n",
	})

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	const want = "run `echo $CODE_FORGE` for issue 2349"
	if !strings.Contains(result.Prompt, want) {
		t.Errorf("Prompt = %q, want it to contain %q", result.Prompt, want)
	}
}

// The fragment loop's "\n\n" separator. A fragment ending with a blank line
// on disk must not leak it into the prompt: bash stripped the fragment's own
// trailing newlines through $(...) before appending exactly two.
func TestAssembleFragmentSeparatorIsExactlyTwoNewlines(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{{Fragment: "blank-tail.md", Var: "BLANK_TAIL"}},
		map[string]string{
			"issue-prompt.md":         "${BLANK_TAIL}next\n",
			"fragments/blank-tail.md": "fragment end.\n\n\n",
		})

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	const want = "fragment end.\n\nnext"
	if !strings.Contains(result.Prompt, want) {
		t.Errorf("Prompt = %q, want it to contain %q (exactly two newlines after the fragment)", result.Prompt, want)
	}
}

func TestAssembleHandoff(t *testing.T) {
	reg, baseEnv := syntheticRegistry(t, nil, map[string]string{
		"issue-prompt.md":  "issue stub\n",
		"fix-prompt.md":    "fix stub\n",
		"review-prompt.md": "review stub\n",
	})

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
			env := baseEnv
			env.ResumeAfterHold = tc.resumeAfterHold

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			if result.Handoff.SessionMode != tc.wantMode {
				t.Errorf("Handoff.SessionMode = %q, want %q", result.Handoff.SessionMode, tc.wantMode)
			}
			// Resuming after a hold changes only the session mode, never the prompt.
			if !strings.Contains(result.Prompt, "issue stub") {
				t.Errorf("Prompt missing issue-prompt.md's text:\n%s", result.Prompt)
			}
			if strings.Contains(result.Prompt, "fix stub") {
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

// The --agents JSON injection loop (renderAgentsJSON).
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

// Builds a shared-block contract-file fixture. Unlike the real nix-baked
// contract files (outcomeContractFile and its siblings in lib/mkHarness.nix),
// these are plain files whose content the tests here fully control.
func writeContractFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

// Shared-block injection, run on the fix-pass cell with a fix-prompt.md stub
// carrying none of the markers, so all three blocks append and stay observable.
func TestAssembleInjectsSharedBlocks(t *testing.T) {
	reg, env := syntheticRegistry(t, nil, map[string]string{"fix-prompt.md": "fix stub\n"})
	dir := t.TempDir()
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

// injectSharedBlockSegments's idempotent guard: a base template that already
// contains a block's marker does not get that block appended again, so the
// contract file's body text must appear zero times.
func TestAssembleSharedBlockAlreadyPresentIsNoOp(t *testing.T) {
	reg, env := syntheticRegistry(t, nil, map[string]string{
		"issue-prompt.md": "# TASK\n\nImplement issue #${ISSUE_NUMBER}.\n\n# COMMS\n\nalready here\n",
	})
	contractDir := t.TempDir()
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

// Issue #420 / #455, the ported entrypoint-outcome-contract.bats cases: a
// runtime prompt-dir override lacking a shared block gets the canonical one
// appended exactly once, and an override already carrying it is left alone.
// Without the appended outcome contract the agent never emits the outcome
// line and the launcher never learns the PR.
func TestAssembleOverrideSharedBlocks(t *testing.T) {
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
			reg, env := syntheticRegistry(t, nil, templates)
			contracts := t.TempDir()
			env.FixPass = tc.fixPass
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
	reg, env := syntheticRegistry(t, nil, map[string]string{
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

// The research branch of the injection step only ever attempts research-verdict
// injection, never comms/check/outcome, even with every contract-file field
// populated. The fixture omits the "# POST THE VERDICT" marker the real
// research-prompt.md already carries, so the injected block is observable.
func TestAssembleResearchCellOnlyInjectsResearchVerdict(t *testing.T) {
	reg, env := syntheticRegistry(t, nil, map[string]string{
		"research-prompt.md": "# TASK\n\nResearch issue #${ISSUE_NUMBER}.\n",
	})
	dir := t.TempDir()
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
			reg, env := syntheticRegistry(t, nil, map[string]string{"research-prompt.md": tc.content})
			env.DispatchKind = "research"
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
	reg, env := syntheticRegistry(t, nil, map[string]string{"research-prompt.md": "research stub, no contract here\n"})
	env.DispatchKind = "research"
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
// every other file Assemble renders.
func TestAssembleInjectedBlockSubstitutesTokens(t *testing.T) {
	reg, env := syntheticRegistry(t, nil, map[string]string{"fix-prompt.md": "fix stub\n"})
	dir := t.TempDir()
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

// A baked opencode agent file fixture: real frontmatter shape, and a body
// distinguishable from any real rendered prompt.
func writeAgentFile(t *testing.T, path, desc string) {
	t.Helper()
	writeAgentFileFrontmatter(t, path, desc, "")
}

// writeAgentFileFrontmatter is writeAgentFile with extra frontmatter lines
// (each newline-terminated) inserted after the `model:` line.
func writeAgentFileFrontmatter(t *testing.T, path, desc, extra string) {
	t.Helper()
	content := "---\n" +
		"description: \"" + desc + "\"\n" +
		"mode: \"subagent\"\n" +
		"model: \"opus\"\n" +
		extra +
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

// The DRIVER_AGENT_FILES_DIR file-rewrite twin (rewriteAgentFiles) of the
// --agents JSON injection loop: a baked agent file keeps its frontmatter and
// has its body overwritten with the substituted prompt.
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

// Issue #5330: an opencode Box whose roster carries the Filer bakes filer.md,
// and the rewrite must give it the real filer prompt rather than leave the
// placeholder body.
func TestAssembleDriverAgentFilesRewritesFilerBody(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	writeAgentFile(t, filepath.Join(dir, "filer.md"), "filer")
	frontmatterBefore := agentFileFrontmatter(t, filepath.Join(dir, "filer.md"))

	env := coveredEnv()
	env.DriverAgentFilesDir = dir
	env.AgentsPromptFiles = `{"filer":"filer-prompt.md"}`

	if _, err := Assemble(env, reg); err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	body := agentFileBody(t, filepath.Join(dir, "filer.md"))
	if strings.Contains(body, "placeholder body") {
		t.Errorf("filer.md body not rewritten: %q", body)
	}
	if !strings.Contains(body, "turn the review findings the work loop escalated") {
		t.Errorf("filer.md body missing the filer prompt text: %q", body)
	}
	if agentFileFrontmatter(t, filepath.Join(dir, "filer.md")) != frontmatterBefore {
		t.Errorf("filer.md frontmatter changed, want unchanged")
	}
}

// Issue #2706's second, independent render path: rewriteAgentFiles' on-disk
// worker.md rewrite must reach the same result for worker-prompt.md as
// TestAgentSkillPreambleFollowsSkillsFound's renderAgentsJSON path.
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
// as TestAgentSkillPreambleFollowsSkillsFound's renderAgentsJSON path.
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

// The file-based reviewer drop: reviewer.md's `model:` scalar populates
// Handoff.ReviewModel and the file is removed, while a non-reviewer roster file
// still gets its body rewritten.
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

// Precedence between the two reviewer-model extraction paths (Assemble's
// inline read of the --agents JSON reviewer entry, then rewriteAgentFiles).
// The file path runs second and overwrites unconditionally, so reviewer.md's
// frontmatter model wins over whatever AgentsJSONTemplate already set.
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

// Issue #5350: every Driver surfaces the roster reviewer's model and effort on
// the Handoff. Claude reads them from the --agents JSON entry; opencode and pi
// only have the baked reviewer.md frontmatter (`model:` / `reasoningEffort:`),
// pi's model carrying its effort as a `:<level>` suffix that is kept verbatim.
func TestAssembleReviewerModelAndEffortExtraction(t *testing.T) {
	reg := loadTestRegistry(t)

	tests := []struct {
		name          string
		driver        string // "claude", "opencode" or "pi"
		jsonTemplate  string // claude: AgentsJSONTemplate
		reviewerExtra string // opencode: extra reviewer.md frontmatter
		reviewerFile  string // pi: verbatim reviewer.md
		wantModel     string
		wantEffort    string
	}{
		{
			name:         "claude: JSON reviewer entry",
			driver:       "claude",
			jsonTemplate: `{"reviewer":{"model":"json-model","effort":"json-effort"}}`,
			wantModel:    "json-model",
			wantEffort:   "json-effort",
		},
		{
			name:          "opencode: reviewer.md model and reasoningEffort",
			driver:        "opencode",
			reviewerExtra: "reasoningEffort: \"high\"\n",
			wantModel:     "opus",
			wantEffort:    "high",
		},
		{
			name:         "pi: reviewer.md keeps the model suffix and reads reasoningEffort",
			driver:       "pi",
			reviewerFile: "---\nname: \"reviewer\"\ndescription: \"r\"\nmodel: \"anthropic/m:high\"\nreasoningEffort: \"high\"\n---\nbody\n",
			wantModel:    "anthropic/m:high",
			wantEffort:   "high",
		},
		{
			name:       "opencode: reviewer.md without reasoningEffort leaves effort empty",
			driver:     "opencode",
			wantModel:  "opus",
			wantEffort: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := coveredEnv()
			switch tt.driver {
			case "claude":
				env.AgentsJSONTemplate = tt.jsonTemplate
			case "opencode":
				dir := t.TempDir()
				writeAgentFileFrontmatter(t, filepath.Join(dir, "reviewer.md"), "reviewer", tt.reviewerExtra)
				env.DriverAgentFilesDir = dir
			case "pi":
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte(tt.reviewerFile), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				env.DriverAgentFilesDir = dir
			default:
				t.Fatalf("unknown driver %q", tt.driver)
			}

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if result.Handoff.ReviewModel != tt.wantModel {
				t.Errorf("Handoff.ReviewModel = %q, want %q", result.Handoff.ReviewModel, tt.wantModel)
			}
			if result.Handoff.ReviewEffort != tt.wantEffort {
				t.Errorf("Handoff.ReviewEffort = %q, want %q", result.Handoff.ReviewEffort, tt.wantEffort)
			}
		})
	}
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
		writeAgentFileFrontmatter(t, filepath.Join(dir, "reviewer.md"), "reviewer", "reasoningEffort: \"high\"\n")

		env := coveredEnv()
		env.DriverAgentFilesDir = dir
		env.AgentsJSONTemplate = `{"reviewer":{"model":"haiku"}}`
		env.ReviewModelOverride = "env-model"
		env.ReviewEffortOverride = "env-effort"

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if result.Handoff.ReviewModel != "env-model" {
			t.Errorf("Handoff.ReviewModel = %q, want %q (dispatch env wins over the file path's frontmatter model)", result.Handoff.ReviewModel, "env-model")
		}
		if result.Handoff.ReviewEffort != "env-effort" {
			t.Errorf("Handoff.ReviewEffort = %q, want %q (dispatch env wins over the file path's reasoningEffort)", result.Handoff.ReviewEffort, "env-effort")
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

// The missing-`model:` case of frontmatterScalar's missing-key fallback, where
// entrypoint.sh's `sed -n 's/^model: //p'` found no match:
// Handoff.ReviewModel stays empty. The effort key's case lives in
// TestAssembleReviewerModelAndEffortExtraction.
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
	// A gated fragment plus a ${...} token give the body fragment, var, and
	// template segments to sum, not just one literal run.
	reg, baseEnv := syntheticRegistry(t,
		[]FragmentRow{{Gate: "CAVEMAN_BAKED", Fragment: "caveman.md", Var: "CAVEMAN_STEP"}},
		map[string]string{
			"issue-prompt.md":      "issue ${ISSUE_NUMBER} ${CAVEMAN_STEP}done\n",
			"review-prompt.md":     "review ${ISSUE_NUMBER}\n",
			"fragments/caveman.md": "caveman body",
		})

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
		env := baseEnv

		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if !strings.Contains(result.Prompt, "caveman body") {
			t.Fatalf("Prompt missing the gated fragment, so the fragment segment goes unchecked:\n%s", result.Prompt)
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
	for _, unwanted := range []string{"${CHORE_", "Research issue #"} {
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

// work's reviewer-drop stays exactly as it was (TestAssembleOrchestratorReviewerKeyDrop
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

// Two rows may share one var when their gates are exclusive (issue #6344): the
// row whose gate is on binds it, and the off row must not clobber it, whichever
// comes first in the registry.
func TestAssembleRowsSharingVarWithExclusiveGates(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"issue-prompt.md":  "[${STEP}]\n",
		"review-prompt.md": "review\n",
		"fragments/on.md":  "on-text\n",
		"fragments/off.md": "off-text\n",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	onRow := FragmentRow{Gate: "TDD_BAKED", Fragment: "on.md", Var: "STEP"}
	offRow := FragmentRow{Gate: "TDD_UNBAKED", Fragment: "off.md", Var: "STEP"}
	orders := map[string][]FragmentRow{
		"on row first":  {onRow, offRow},
		"off row first": {offRow, onRow},
	}
	for name, rows := range orders {
		t.Run(name, func(t *testing.T) {
			env := coveredEnv()
			env.PromptsDir = dir
			env.TDDSkillBaked = true
			result, err := Assemble(env, Registry{Rows: rows})
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if !strings.Contains(result.Prompt, "[on-text") || strings.Contains(result.Prompt, "off-text") {
				t.Errorf("Prompt = %q, want the on row's text bound to the shared var", result.Prompt)
			}
		})
	}
}

// An axis row reads fragments/<backend>/<fragment> for the active tracker or
// forge, picked by the axis signal rather than the backend name; a folder without the file renders empty rather than failing, a gate
// still applies, and an unset backend takes the default.
func TestAssembleAxisRowResolvesBackendFolder(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"issue-prompt.md":             "[${STEP}]\n",
		"review-prompt.md":            "review\n",
		"fragments/github/step.md":    "github-step\n",
		"fragments/forgejo/step.md":   "forgejo-step\n",
		"fragments/forgejo/other.md":  "forgejo-other\n",
		"fragments/github/forge-s.md": "github-forge\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}

	cases := []struct {
		name         string
		row          FragmentRow
		mutate       func(*Env)
		want         string
		wantFragment string
	}{
		{"tracker axis picks the tracker folder", FragmentRow{Axis: "tracker", Fragment: "step.md", Var: "STEP"},
			func(e *Env) { e.IssueTracker, e.TrackerAxisRead = "forgejo", "FORGEJO" }, "[forgejo-step\n\n]", "forgejo/step.md"},
		{"tracker axis defaults an empty tracker", FragmentRow{Axis: "tracker", Fragment: "step.md", Var: "STEP"},
			func(e *Env) { e.IssueTracker = "" }, "[github-step\n\n]", "github/step.md"},
		{"forge axis follows the signal, so git rides the github folder", FragmentRow{Axis: "forge", Fragment: "forge-s.md", Var: "STEP"},
			func(e *Env) { e.ForgeBackend, e.CodeForge = "", "git" }, "[github-forge\n\n]", "github/forge-s.md"},
		{"forge axis prefers the pre-resolved backend", FragmentRow{Axis: "forge", Fragment: "forge-s.md", Var: "STEP"},
			func(e *Env) { e.ForgeBackend, e.CodeForge = "GH", "git" }, "[github-forge\n\n]", "github/forge-s.md"},
		{"forge axis defaults an empty forge", FragmentRow{Axis: "forge", Fragment: "forge-s.md", Var: "STEP"},
			func(e *Env) { e.ForgeBackend, e.CodeForge = "", "" }, "[github-forge\n\n]", "github/forge-s.md"},
		{"backend with no file renders empty", FragmentRow{Axis: "tracker", Fragment: "other.md", Var: "STEP"},
			func(e *Env) {}, "[]", ""},
		{"gate off renders empty", FragmentRow{Gate: "NO_SUCH_GATE", Axis: "tracker", Fragment: "step.md", Var: "STEP"},
			func(e *Env) {}, "[]", ""},
		{"gate on renders the axis file", FragmentRow{Gate: "BOX_ACCESS_READ_WRITE", Axis: "tracker", Fragment: "step.md", Var: "STEP"},
			func(e *Env) {}, "[github-step\n\n]", "github/step.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.PromptsDir = dir
			tc.mutate(&env)

			result, err := Assemble(env, Registry{Rows: []FragmentRow{tc.row}})
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if result.Prompt != tc.want {
				t.Errorf("Prompt = %q, want %q", result.Prompt, tc.want)
			}
			var wantFrags []string
			if tc.wantFragment != "" {
				wantFrags = []string{tc.wantFragment}
			}
			if !slices.Equal(result.Fragments, wantFrags) {
				t.Errorf("Fragments = %v, want %v", result.Fragments, wantFrags)
			}
		})
	}

	t.Run("unknown axis is an error", func(t *testing.T) {
		env := coveredEnv()
		env.PromptsDir = dir
		_, err := Assemble(env, Registry{Rows: []FragmentRow{{Axis: "bogus", Fragment: "step.md", Var: "STEP"}}})
		if err == nil || !strings.Contains(err.Error(), `unknown axis "bogus"`) {
			t.Fatalf("Assemble error = %v, want an unknown-axis error", err)
		}
	})
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

// whole prompt: a sibling fragment's text stays out.
func TestAssembleFragmentRendersSpan(t *testing.T) {
	renders := assembleRenders(t, "intro ${VAR_A} between ${VAR_B}\n",
		map[string]string{"a-frag.md": "ay on ${BASE_BRANCH}\n", "b-frag.md": "bee\n"},
		[]FragmentRow{
			{Gate: "CAVEMAN_BAKED", Fragment: "a-frag.md", Var: "VAR_A"},
			{Gate: "CAVEMAN_BAKED", Fragment: "b-frag.md", Var: "VAR_B"},
		})
	if got, want := trimAll(renders["a-frag.md"]), []string{"ay on main"}; !slices.Equal(got, want) {
		t.Errorf("a-frag.md renders = %q, want %q", got, want)
	}
	if got, want := trimAll(renders["b-frag.md"]), []string{"bee"}; !slices.Equal(got, want) {
		t.Errorf("b-frag.md renders = %q, want %q", got, want)
	}
}

// assembleRenders assembles a one-body prompt from files under fragments/ and
// the registry rows, and returns the recorded fragment renders.
func assembleRenders(t *testing.T, prompt string, frags map[string]string, rows []FragmentRow) map[string][]string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "fragments"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	files := map[string]string{"issue-prompt.md": prompt, "review-prompt.md": "review\n"}
	for name, content := range frags {
		files["fragments/"+name] = content
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	env := coveredEnv()
	env.PromptsDir = dir
	result, err := Assemble(env, Registry{Rows: rows})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return FragmentRenders(result)
}

// A parent's render keeps the vars substituted directly into it but not the
// prose of a fragment spliced between its own segments; the child's render
// holds that prose.
func TestAssembleFragmentRendersExcludeNestedFragment(t *testing.T) {
	renders := assembleRenders(t, "${PARENT}\n",
		map[string]string{
			"child.md":  "kid prose\n",
			"parent.md": "before ${CHILD}after on ${BASE_BRANCH}\n",
		},
		[]FragmentRow{
			{Gate: "CAVEMAN_BAKED", Fragment: "child.md", Var: "CHILD"},
			{Gate: "CAVEMAN_BAKED", Fragment: "parent.md", Var: "PARENT"},
		})

	parent := strings.Join(renders["parent.md"], "")
	if strings.Contains(parent, "kid prose") {
		t.Errorf("parent render holds the nested child's prose: %q", parent)
	}
	for _, want := range []string{"before", "after on main"} {
		if !strings.Contains(parent, want) {
			t.Errorf("parent render %q lacks its own text %q", parent, want)
		}
	}
	if want := "before \x00child.md\x00after"; !strings.Contains(parent, want) {
		t.Errorf("parent render %q does not name the spliced child in its gap, want %q", parent, want)
	}
	if got, want := trimAll(renders["child.md"]), []string{"kid prose"}; !slices.Equal(got, want) {
		t.Errorf("child.md renders = %q, want %q", got, want)
	}
}

// Prose between two renders of one fragment is not part of its span, and a
// clause cannot match across the gap.
func TestAssembleFragmentRendersExcludeTextBetweenRenders(t *testing.T) {
	renders := assembleRenders(t, "${VAR_A} middle ${VAR_A}\n",
		map[string]string{"a-frag.md": "ay on ${BASE_BRANCH}\n"},
		[]FragmentRow{{Gate: "CAVEMAN_BAKED", Fragment: "a-frag.md", Var: "VAR_A"}})

	got := strings.Join(renders["a-frag.md"], "")
	if strings.Contains(got, "middle") {
		t.Errorf("render holds text between the two renders: %q", got)
	}
	if n := strings.Count(got, "ay on main"); n != 2 {
		t.Errorf("render %q holds %d copies of the fragment, want 2", got, n)
	}
	if strings.Contains(strings.Join(strings.Fields(got), " "), "main ay") {
		t.Errorf("render %q lets a clause match across the gap between renders", got)
	}
	if !strings.Contains(got, "main\n\n\x00ay") {
		t.Errorf("render %q: template text between renders should leave a bare gap", got)
	}
}

func trimAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.TrimSpace(s)
	}
	return out
}
