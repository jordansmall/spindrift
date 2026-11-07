package promptassembly

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
)

// stagedBases stages one base template per candidate, each carrying a distinct
// marker plus the four tokens every base substitutes, so a selection test
// reads which template rendered from the marker alone.
func stagedBases() map[string]string {
	body := func(marker string) string {
		return marker + " #${ISSUE_NUMBER}: ${ISSUE_TITLE} on ${BRANCH} from ${BASE_BRANCH}\n"
	}
	return map[string]string{
		"issue-prompt.md":                   body("WORK-BASE"),
		"fix-prompt.md":                     body("FIX-BASE"),
		"research-prompt.md":                body("RESEARCH-BASE"),
		"research-self-contained-prompt.md": body("RESEARCH-SELF-CONTAINED-BASE"),
	}
}

// The kind picks the base template (a research kind's own base prompt before
// FixPass, SelfContained only within a kind that has a sub-mode), and
// the base alone fixes the session mode: ResumeAfterHold moves only the
// default work cell. Only a fresh work dispatch populates the review prompt,
// while the reviewer's model is extracted from the roster for every cell.
func TestAssembleBaseTemplateSelection(t *testing.T) {
	reg, env := syntheticRegistry(t, nil, stagedBases())
	env.AgentsJSONTemplate = `{"reviewer":{"model":"review-model-x"}}`

	cases := []struct {
		name       string
		mutate     func(*Env)
		wantBase   string
		wantMode   string
		wantReview bool
	}{
		{"work", func(e *Env) {}, "WORK-BASE", "initial", true},
		{"work resumed after hold", func(e *Env) { e.ResumeAfterHold = true }, "WORK-BASE", "resume", true},
		{"unset kind defaults to work", func(e *Env) { e.DispatchKind = "" }, "WORK-BASE", "initial", true},
		{"fix pass", func(e *Env) { e.FixPass = 1 }, "FIX-BASE", "resume", false},
		{"fix pass ignores resume-after-hold", func(e *Env) { e.FixPass = 1; e.ResumeAfterHold = true }, "FIX-BASE", "resume", false},
		{"research", func(e *Env) { e.DispatchKind = "research" }, "RESEARCH-BASE", "initial", false},
		{"research ignores resume-after-hold", func(e *Env) { e.DispatchKind = "research"; e.ResumeAfterHold = true }, "RESEARCH-BASE", "initial", false},
		{"self-contained research", func(e *Env) { e.DispatchKind = "research"; e.SelfContained = true }, "RESEARCH-SELF-CONTAINED-BASE", "initial", false},
		{"research outranks fix pass", func(e *Env) { e.DispatchKind = "research"; e.FixPass = 1 }, "RESEARCH-BASE", "initial", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := env
			tc.mutate(&env)
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			want := tc.wantBase + " #2349: Add promptassembly.Assemble on agent/issue-2349 from main"
			if result.Prompt != want {
				t.Errorf("Prompt = %q, want %q", result.Prompt, want)
			}
			if result.Handoff.SessionMode != tc.wantMode {
				t.Errorf("Handoff.SessionMode = %q, want %q", result.Handoff.SessionMode, tc.wantMode)
			}
			if has := result.ReviewPromptText != ""; has != tc.wantReview {
				t.Errorf("ReviewPromptText = %q, populated = %v, want %v", result.ReviewPromptText, has, tc.wantReview)
			}
			if has := result.Handoff.ReviewPromptFile != ""; has {
				t.Errorf("Handoff.ReviewPromptFile = %q, want empty (issue #2975)", result.Handoff.ReviewPromptFile)
			}
			if result.Handoff.ReviewModel != "review-model-x" {
				t.Errorf("Handoff.ReviewModel = %q, want %q (extraction is unconditional)", result.Handoff.ReviewModel, "review-model-x")
			}
		})
	}
}

// DispatchKind is the one axis checkCoveredCell still validates (issue #2540);
// a self-contained run of a kind with a base prompt but no self-contained
// base has nothing to render and is rejected up front, rather than falling
// through to an empty baseName that reads PromptsDir itself; and a Descriptor
// with no prompt contract fails loudly rather than injecting no block. The
// synthetic Descriptors stand in for kinds no real row declares.
func TestAssembleRejectsUnsupportedCells(t *testing.T) {
	// Mutates package global dispatchkind.All, so no test in this package may call t.Parallel.
	withKind := func(t *testing.T, name string) {
		t.Helper()
		synth := &dispatchkind.Descriptor{
			Name:    name,
			Verb:    "synthetic",
			Keying:  dispatchkind.ByIssue,
			Labels:  dispatchkind.LabelsConfigured,
			Prompts: dispatchkind.Prompts{Base: "issue-prompt.md"},
		}
		dispatchkind.All = append(dispatchkind.All, synth)
		t.Cleanup(func() { dispatchkind.All = dispatchkind.All[:len(dispatchkind.All)-1] })
	}

	cases := []struct {
		name        string
		setup       func(*testing.T, *Env)
		wantCell    bool
		wantMessage string
	}{
		{"unrecognized dispatch kind", func(t *testing.T, e *Env) { e.DispatchKind = "bogus" }, true, `dispatch kind "bogus"`},
		{"self-contained without a sub-mode", func(t *testing.T, e *Env) {
			withKind(t, "synthetic-no-self-contained")
			e.DispatchKind = "synthetic-no-self-contained"
			e.SelfContained = true
		}, true, "self-contained not supported"},
		{"kind with no prompt contract", func(t *testing.T, e *Env) {
			withKind(t, "synthetic-no-contract")
			e.DispatchKind = "synthetic-no-contract"
		}, false, "unknown prompt contract"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, env := syntheticRegistry(t, nil, nil)
			tc.setup(t, &env)

			_, err := Assemble(env, reg)
			if err == nil {
				t.Fatal("Assemble: got nil error, want a rejection")
			}
			if tc.wantCell && !errors.Is(err, ErrUnsupportedCell) {
				t.Errorf("Assemble error = %v, want it to wrap ErrUnsupportedCell", err)
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Errorf("Assemble error = %v, want it to mention %q", err, tc.wantMessage)
			}
		})
	}
}

// Axis resolution lives in nix (issue #2533) and upstream validation, so
// checkCoveredCell no longer rejects a bogus tracker or forge (issue #2540),
// and every CodeForge x BoxWriteEnabled cell beyond github read-write renders.
// A future change re-adding allowlist validation here must show up as a
// deliberate edit to this test.
func TestAssembleAxisValuesAreNotRejected(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Env)
	}{
		{"bogus issue tracker", func(e *Env) { e.IssueTracker = "bogus-tracker" }},
		{"bogus code forge", func(e *Env) { e.CodeForge = "bogus-forge" }},
		{"unset tracker, forge and kind", func(e *Env) { e.IssueTracker = ""; e.CodeForge = ""; e.DispatchKind = "" }},
		{"forgejo read-write", func(e *Env) { e.CodeForge = "forgejo" }},
		{"forgejo read-only", func(e *Env) { e.CodeForge = "forgejo"; e.BoxWriteEnabled = false }},
		{"github read-only", func(e *Env) { e.BoxWriteEnabled = false }},
		{"git forge", func(e *Env) { e.CodeForge = "git" }},
		{"local forge", func(e *Env) { e.CodeForge = "local" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, env := syntheticRegistry(t, nil, nil)
			tc.mutate(&env)
			if _, err := Assemble(env, reg); err != nil {
				t.Fatalf("Assemble: %v, want no error (the cell is covered)", err)
			}
		})
	}
}

// The CODE_FORGE=git block's three steps hang off box access alone: a
// read-write Box pushes and then stops, a read-only Box only stops (its
// eval-time assert, issue #2526, makes read-only plus git unbuildable, so the
// read-only push step is gone). The fragments number themselves, so which
// pair renders is what keeps the numbering contiguous.
func TestAssembleLandGitStepsFollowBoxAccess(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "BOX_ACCESS_READ_WRITE", Fragment: "push.md", Var: "LAND_GIT_PUSH_READ_WRITE_STEP"},
			{Gate: "BOX_ACCESS_READ_WRITE", Fragment: "stop-rw.md", Var: "LAND_GIT_STOP_READ_WRITE_STEP"},
			{Gate: "BOX_ACCESS_READ_ONLY", Fragment: "stop-ro.md", Var: "LAND_GIT_STOP_READ_ONLY_STEP"},
		},
		map[string]string{
			"issue-prompt.md":      "[${LAND_GIT_PUSH_READ_WRITE_STEP}${LAND_GIT_STOP_READ_WRITE_STEP}${LAND_GIT_STOP_READ_ONLY_STEP}]\n",
			"fragments/push.md":    "PUSH,",
			"fragments/stop-rw.md": "STOP-RW,",
			"fragments/stop-ro.md": "STOP-RO,",
		})

	for want, boxWrite := range map[string]bool{
		"[PUSH,STOP-RW,]": true,
		"[STOP-RO,]":      false,
	} {
		env.BoxWriteEnabled = boxWrite
		result, err := Assemble(env, reg)
		if err != nil {
			t.Fatalf("Assemble(BoxWriteEnabled=%v): %v", boxWrite, err)
		}
		if got := unseparated(result.Prompt); got != want {
			t.Errorf("BoxWriteEnabled=%v: Prompt = %q, want %q", boxWrite, got, want)
		}
	}
}

// Each of the four skills is baked independently (issue #2354), matching
// Gates() and lib/image.nix's per-skill baking, so a real Consumer can bake
// any subset: a partial combination renders exactly the arms its own flags
// select, and the skill preamble follows SkillsFound alone.
func TestAssembleSkillArmsAreIndependent(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "SKILLS_FOUND", Fragment: "preamble.md", Var: "SKILL_PREAMBLE", ExtraSubstVars: []string{"SKILLS_FOUND"}},
			{Gate: "CAVEMAN_BAKED", Fragment: "caveman.md", Var: "CAVEMAN_STEP"},
			{Gate: "TDD_BAKED", Fragment: "tdd-on.md", Var: "TDD_BAKED_STEP"},
			{Gate: "TDD_UNBAKED", Fragment: "tdd-off.md", Var: "TDD_UNBAKED_STEP"},
			{Gate: "COMMIT_BAKED", Fragment: "commit-on.md", Var: "COMMIT_BAKED_STEP"},
			{Gate: "COMMIT_UNBAKED", Fragment: "commit-off.md", Var: "COMMIT_UNBAKED_STEP"},
			{Gate: "CODE_REVIEW_BAKED", Fragment: "review-on.md", Var: "CODE_REVIEW_BAKED_STEP"},
			{Gate: "CODE_REVIEW_UNBAKED", Fragment: "review-off.md", Var: "CODE_REVIEW_UNBAKED_STEP"},
		},
		map[string]string{
			"issue-prompt.md": "[${SKILL_PREAMBLE}${CAVEMAN_STEP}${TDD_BAKED_STEP}${TDD_UNBAKED_STEP}" +
				"${COMMIT_BAKED_STEP}${COMMIT_UNBAKED_STEP}${CODE_REVIEW_BAKED_STEP}${CODE_REVIEW_UNBAKED_STEP}]\n",
			"fragments/preamble.md":   "SKILLS(${SKILLS_FOUND}),",
			"fragments/caveman.md":    "CAVEMAN,",
			"fragments/tdd-on.md":     "TDD-ON,",
			"fragments/tdd-off.md":    "TDD-OFF,",
			"fragments/commit-on.md":  "COMMIT-ON,",
			"fragments/commit-off.md": "COMMIT-OFF,",
			"fragments/review-on.md":  "REVIEW-ON,",
			"fragments/review-off.md": "REVIEW-OFF,",
		})

	cases := []struct {
		name                         string
		found                        string
		caveman, tdd, commit, review bool
		want                         string
	}{
		{"none baked", "", false, false, false, false, "[TDD-OFF,COMMIT-OFF,REVIEW-OFF,]"},
		{"tdd only", "tdd", false, true, false, false, "[SKILLS(tdd),TDD-ON,COMMIT-OFF,REVIEW-OFF,]"},
		{"caveman only", "caveman", true, false, false, false, "[SKILLS(caveman),CAVEMAN,TDD-OFF,COMMIT-OFF,REVIEW-OFF,]"},
		{"commit only", "commit", false, false, true, false, "[SKILLS(commit),TDD-OFF,COMMIT-ON,REVIEW-OFF,]"},
		{"code-review only", "code-review", false, false, false, true, "[SKILLS(code-review),TDD-OFF,COMMIT-OFF,REVIEW-ON,]"},
		{"all baked", "caveman, tdd, commit, code-review", true, true, true, true, "[SKILLS(caveman, tdd, commit, code-review),CAVEMAN,TDD-ON,COMMIT-ON,REVIEW-ON,]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := env
			env.SkillsFound = tc.found
			env.CavemanSkillBaked, env.TDDSkillBaked, env.CommitSkillBaked, env.CodeReviewSkillBaked = tc.caveman, tc.tdd, tc.commit, tc.review
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if got := unseparated(result.Prompt); got != tc.want {
				t.Errorf("Prompt = %q, want %q", got, tc.want)
			}
		})
	}
}
