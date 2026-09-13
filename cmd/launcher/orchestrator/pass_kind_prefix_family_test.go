package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/deltareview"
	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/runstate"
)

// guardPromptsDir/guardRegistryPath resolve the real templates tree and
// fragment registry from this package's own directory, the same
// repo-root-relative convention promptassembly's own tests use (see its
// promptsDir constant and loadTestRegistry helper) -- so this guard renders
// through the actual templates a later edit could break, not a fixture that
// can drift out of sync with them.
const (
	guardPromptsDir   = "../../../templates/default/prompts"
	guardRegistryPath = "../internal/promptassembly/testdata/registry.json"
)

// guardEnv builds one orchestrator-on, fresh-work Env sitting in Assemble's
// covered cell (mirrors promptassembly's own coveredEnv fixture), with
// issueText forwarded as Env.IssueText -- the one axis this test varies
// across its two Assemble calls (see loadGuardBase).
func guardEnv(issueText string) promptassembly.Env {
	return promptassembly.Env{
		IssueTracker:           "github",
		TrackerAxisRead:        "GITHUB",
		TrackerAxisWrite:       "GITHUB",
		TrackerAxisFiler:       "GH",
		CodeForge:              "github",
		ForgeBackend:           "GH",
		BoxWriteEnabled:        true,
		DispatchKind:           "work",
		FixPass:                0,
		OrchestratorEnabled:    true,
		ReviewLoopOrchestrator: true,
		SkillsFound:            "caveman, tdd, commit, code-review",
		CavemanSkillBaked:      true,
		TDDSkillBaked:          true,
		CommitSkillBaked:       true,
		CodeReviewSkillBaked:   true,
		AutoFormatSkillBaked:   true,
		AutoLintSkillBaked:     true,
		PromptsDir:             guardPromptsDir,
		IssueNumber:            "3445",
		IssueTitle:             "Guard the pass-kind prefix family",
		Branch:                 "agent/issue-3445",
		BaseBranch:             "main",
		InProgressLabel:        "agent-in-progress",
		CompleteLabel:          "agent-complete",
		RunNonce:               "run-nonce-guard",
		IssueText:              issueText,
	}
}

// writeTemp writes content to a fresh file under t.TempDir() -- the
// seeders below all take a promptFile PATH, not text, so every rendered
// body this test builds needs an on-disk home before it can seed anything.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// longestCommonPrefix returns the byte-for-byte longest shared prefix
// across every one of strs, computed purely from the strings themselves --
// deliberately never from a value the caller already believes is that
// prefix, so that two family members drifting apart from EACH OTHER (not
// just from an assumed constant) still shows up here.
func longestCommonPrefix(strs ...string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		i := 0
		for i < len(prefix) && i < len(s) && prefix[i] == s[i] {
			i++
		}
		prefix = prefix[:i]
	}
	return prefix
}

// namedPromptText pairs a pass kind's label with the actual prompt text it
// receives, for assertFamilyPrefix's diagnostics.
type namedPromptText struct {
	kind string
	text string
}

// diffWindow returns a short slice of s centered on offset, for
// assertFamilyPrefix's failure output -- printing the whole prompt (tens of
// KB) would bury the one byte that actually matters.
func diffWindow(s string, offset int) string {
	const radius = 40
	start := offset - radius
	if start < 0 {
		start = 0
	}
	end := offset + radius
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

// assertFamilyPrefix asserts every pass in the family leads with the exact
// byte-identical block "expected" (the assembled base or review prompt),
// failing with a diagnosis a future editor who broke prompt caching can act
// on directly: which pass diverged, at what byte offset, and a short window
// of both texts there -- rather than a bare boolean or a full-prompt dump.
func assertFamilyPrefix(t *testing.T, family string, passes []namedPromptText, expected string) {
	t.Helper()

	texts := make([]string, len(passes))
	for i, p := range passes {
		texts[i] = p.text
	}
	got := longestCommonPrefix(texts...)
	if got == expected {
		return
	}

	for _, p := range passes {
		if strings.HasPrefix(p.text, expected) {
			continue
		}
		offset := 0
		for offset < len(expected) && offset < len(p.text) && expected[offset] == p.text[offset] {
			offset++
		}
		t.Fatalf(
			"%s family: pass %q stops leading with the byte-identical %s block at offset %d.\n"+
				"Prompt caching is a prefix match: every pass in this family must share an "+
				"identical leading prefix, or the driver re-reads the whole prompt from scratch "+
				"on every seeded pass instead of hitting cache.\n"+
				"expected here: %q\n"+
				"got here:      %q",
			family, p.kind, family, offset, diffWindow(expected, offset), diffWindow(p.text, offset),
		)
	}
	t.Fatalf("%s family: computed common prefix (%d bytes) does not equal the assembled %s prompt (%d bytes), though every pass individually leads with it -- the family shares MORE than the assembled prompt, which should be impossible by construction; investigate before trusting this guard", family, len(got), family, len(expected))
}

// TestPassKindsLeadWithFamilyPrefix is the end-to-end guard issue #3445
// asks for: over the actual set of orchestrator pass kinds, every
// implement/fix/land pass must lead with the byte-identical assembled base
// prompt, and every review/delta-review pass must lead with the
// byte-identical assembled review prompt -- so a later fragment or seeder
// edit that silently reintroduces a prepend, or otherwise perturbs the
// leading bytes, fails a test instead of only degrading prompt-cache hit
// rate in production. Slice 4's seed_prefix_invariant_test.go guards each
// seeder in isolation against a hand-written fixture; this asserts the
// family-wide invariant through the real templates and fragment registry.
func TestPassKindsLeadWithFamilyPrefix(t *testing.T) {
	if _, err := os.Stat(guardPromptsDir); err != nil {
		t.Fatalf("templates tree not present at %s: %v -- this guard renders nothing and asserts nothing without the real templates tree", guardPromptsDir, err)
	}
	reg, err := promptassembly.LoadRegistryFile(guardRegistryPath)
	if err != nil {
		t.Fatalf("load fragment registry: %v", err)
	}

	const issueText = "Fix the frobnicator so it stops double-counting widgets on retry."

	// templateOnly isolates the rendered template body ALONE (Env.IssueText
	// unset), so the layering assertion below can tell the template-body
	// layer apart from the "# ISSUE TEXT" layer Assemble appends after it --
	// both are baked into result.Prompt/result.ReviewPromptText together.
	templateOnly, err := promptassembly.Assemble(guardEnv(""), reg)
	if err != nil {
		t.Fatalf("Assemble (no issue text): %v", err)
	}
	result, err := promptassembly.Assemble(guardEnv(issueText), reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if result.Prompt == "" || result.ReviewPromptText == "" {
		t.Fatalf("guardEnv did not render a base prompt and a review prompt; got base=%d bytes review=%d bytes -- fixture no longer sits in Assemble's covered orchestrator-on cell", len(result.Prompt), len(result.ReviewPromptText))
	}

	basePromptFile := writeTemp(t, "base-prompt.txt", result.Prompt)
	reviewPromptFile := writeTemp(t, "review-prompt.txt", result.ReviewPromptText)

	// ---- base family: implement, fix, land ----

	// implement: cold start, no run state at all.
	implementText := result.Prompt

	fixState := runstate.RunState{
		LastVerdict:    "BLOCK",
		ReviewFindings: "VERDICT: BLOCK\n\n## Blocking\n- run.go:42 -- missing nil check",
	}
	fixFile, err := seedPromptFromState(basePromptFile, fixState)
	if err != nil {
		t.Fatalf("seedPromptFromState (fix): %v", err)
	}
	fixBytes, err := os.ReadFile(fixFile)
	if err != nil {
		t.Fatalf("read fix prompt: %v", err)
	}

	landState := runstate.RunState{LastVerdict: "APPROVE"}
	landFile, err := seedPromptFromState(basePromptFile, landState)
	if err != nil {
		t.Fatalf("seedPromptFromState (land): %v", err)
	}
	landBytes, err := os.ReadFile(landFile)
	if err != nil {
		t.Fatalf("read land prompt: %v", err)
	}

	assertFamilyPrefix(t, "base", []namedPromptText{
		{"implement", implementText},
		{"fix", string(fixBytes)},
		{"land", string(landBytes)},
	}, result.Prompt)

	// ---- review family: review round 1, review round 2, delta-review ----

	reviewRound1Text := result.ReviewPromptText

	reviewRound2State := runstate.RunState{
		ReviewFindings:       "VERDICT: BLOCK\n\n## Blocking\n- run.go:42 -- missing nil check",
		DispositionsLogPath:  writeTemp(t, "dispositions-log.txt", "## Round 1\n- run.go:42 -- fixed, added the nil check\n"),
		ReviewedCommitAnchor: strings.Repeat("a", 40),
	}
	reviewRound2File, err := seedReviewPromptFromState(reviewPromptFile, reviewRound2State)
	if err != nil {
		t.Fatalf("seedReviewPromptFromState: %v", err)
	}
	reviewRound2Bytes, err := os.ReadFile(reviewRound2File)
	if err != nil {
		t.Fatalf("read review round 2 prompt: %v", err)
	}

	deltaState := runstate.RunState{ReviewFindings: "VERDICT: APPROVE\n\n## Non-blocking\n- run.go:1 -- nit"}
	delta := landdelta.Delta{Known: true, Files: 2, Insertions: 3, Deletions: 1, Paths: []string{"go.mod", "run.go"}}
	trigger := deltareview.Trigger{Fire: true, Reason: "land delta touches paths beyond the reviewer's findings: go.mod", Beyond: []string{"go.mod"}}
	deltaFile, err := seedDeltaReviewPrompt(reviewPromptFile, deltaState, delta, trigger)
	if err != nil {
		t.Fatalf("seedDeltaReviewPrompt: %v", err)
	}
	deltaBytes, err := os.ReadFile(deltaFile)
	if err != nil {
		t.Fatalf("read delta-review prompt: %v", err)
	}

	assertFamilyPrefix(t, "review", []namedPromptText{
		{"review round 1", reviewRound1Text},
		{"review round 2", string(reviewRound2Bytes)},
		{"delta-review", string(deltaBytes)},
	}, result.ReviewPromptText)

	// ---- layering within each leading block: template body, then ISSUE
	// TEXT, then (outside this section) the pass-specific seeded block ----

	assertTemplateThenIssueText(t, "base", templateOnly.Prompt, result.Prompt)
	assertTemplateThenIssueText(t, "review", templateOnly.ReviewPromptText, result.ReviewPromptText)
}

// assertTemplateThenIssueText asserts that assembled (the leading block
// every pass kind in one family shares) is exactly templateOnly followed by
// the "# ISSUE TEXT" section promptassembly's issueTextSection appends
// (assemble.go) -- i.e. cross-run-stable content (the rendered template,
// identical for every issue) precedes run-stable content (the issue
// text, identical across a run's own passes but not across issues), which
// in turn precedes the pass-specific block the seeders in this package
// append. issueTextSection itself is unexported, so this checks its known
// literal header rather than reaching into promptassembly internals.
func assertTemplateThenIssueText(t *testing.T, family, templateOnly, assembled string) {
	t.Helper()
	if !strings.HasPrefix(assembled, templateOnly) {
		t.Fatalf("%s: assembled prompt does not lead with the rendered template body -- template body must precede the ISSUE TEXT section (issue #3445)", family)
	}
	rest := assembled[len(templateOnly):]
	if !strings.HasPrefix(rest, "\n\n# ISSUE TEXT") {
		t.Fatalf("%s: template body is not immediately followed by the \"# ISSUE TEXT\" section; got %q after the template body", family, diffWindow(rest, 0))
	}
}

// scoutPromptText extracts .scout.prompt out of a rendered Result.AgentsJSON
// string -- the scout brief isn't Result.Prompt/ReviewPromptText at all, so
// TestScoutAndResearchPromptsLeadWithIssueText has to reach into the roster
// JSON to get at it.
func scoutPromptText(t *testing.T, agentsJSON string) string {
	t.Helper()
	var roster map[string]struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(agentsJSON), &roster); err != nil {
		t.Fatalf("unmarshal agents json: %v", err)
	}
	scout, ok := roster["scout"]
	if !ok {
		t.Fatalf("agents json has no \"scout\" key: %s", agentsJSON)
	}
	return scout.Prompt
}

// TestScoutAndResearchPromptsLeadWithIssueText extends the guard to the
// three single-pass prompts TestPassKindsLeadWithFamilyPrefix's two
// families (base and review) don't reach: research, self-contained
// research, and the scout roster prompt. None of the three has a seeder or
// a multi-pass family of its own, so the assertion here is layering
// (rendered template body, then the "# ISSUE TEXT" section
// assertTemplateThenIssueText already checks), not a prefix shared across
// several passes.
func TestScoutAndResearchPromptsLeadWithIssueText(t *testing.T) {
	if _, err := os.Stat(guardPromptsDir); err != nil {
		t.Fatalf("templates tree not present at %s: %v -- this guard renders nothing and asserts nothing without the real templates tree", guardPromptsDir, err)
	}
	reg, err := promptassembly.LoadRegistryFile(guardRegistryPath)
	if err != nil {
		t.Fatalf("load fragment registry: %v", err)
	}

	const issueText = "Fix the frobnicator so it stops double-counting widgets on retry."

	assemblePair := func(t *testing.T, without, with promptassembly.Env) (promptassembly.Result, promptassembly.Result) {
		t.Helper()
		withoutResult, err := promptassembly.Assemble(without, reg)
		if err != nil {
			t.Fatalf("Assemble (no issue text): %v", err)
		}
		withResult, err := promptassembly.Assemble(with, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		return withoutResult, withResult
	}

	t.Run("research", func(t *testing.T) {
		without := guardEnv("")
		without.DispatchKind = "research"
		with := guardEnv(issueText)
		with.DispatchKind = "research"

		withoutResult, withResult := assemblePair(t, without, with)
		assertTemplateThenIssueText(t, "research", withoutResult.Prompt, withResult.Prompt)
	})

	t.Run("self-contained research", func(t *testing.T) {
		without := guardEnv("")
		without.DispatchKind = "research"
		without.SelfContained = true
		with := guardEnv(issueText)
		with.DispatchKind = "research"
		with.SelfContained = true

		withoutResult, withResult := assemblePair(t, without, with)
		assertTemplateThenIssueText(t, "self-contained research", withoutResult.Prompt, withResult.Prompt)
	})

	t.Run("scout", func(t *testing.T) {
		// scout-prompt.md is the one subagent prompt referencing
		// ${ISSUE_TEXT} directly rather than riding Assemble's automatic
		// append (docs/reference.md) -- so it needs a roster entry wired up
		// to actually render, unlike the base/review prompts above.
		without := guardEnv("")
		without.AgentsJSONTemplate = `{"scout":{"model":"opus"}}`
		without.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`
		without.ScoutProvisioned = true
		with := guardEnv(issueText)
		with.AgentsJSONTemplate = `{"scout":{"model":"opus"}}`
		with.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`
		with.ScoutProvisioned = true

		withoutResult, withResult := assemblePair(t, without, with)
		assertTemplateThenIssueText(t, "scout", scoutPromptText(t, withoutResult.AgentsJSON), scoutPromptText(t, withResult.AgentsJSON))
	})
}
