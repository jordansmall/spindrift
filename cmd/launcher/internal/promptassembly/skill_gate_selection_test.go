package promptassembly

import "testing"

// AUTO_FORMAT and AUTO_LINT are plain passthroughs of the Env flags (issue
// #2354), each independent of the other, so a partial combination renders
// exactly the step its own flag selects.
func TestAssembleAutoFormatAndLintGates(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "AUTO_FORMAT", Fragment: "format.md", Var: "AUTO_FORMAT_STEP"},
			{Gate: "AUTO_LINT", Fragment: "lint.md", Var: "AUTO_LINT_STEP"},
		},
		map[string]string{
			"issue-prompt.md":     "[${AUTO_FORMAT_STEP}${AUTO_LINT_STEP}]\n",
			"fragments/format.md": "FORMAT,",
			"fragments/lint.md":   "LINT,",
		})

	cases := []struct {
		name         string
		format, lint bool
		want         string
	}{
		{"neither", false, false, "[]"},
		{"format only", true, false, "[FORMAT,]"},
		{"lint only", false, true, "[LINT,]"},
		{"both", true, true, "[FORMAT,LINT,]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := env
			env.AutoFormat, env.AutoLint = tc.format, tc.lint
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

// CI_FAILURE_SUMMARY is a presence check on the value itself (issue #2354):
// a non-empty CIFailureSummary both opens the gate and substitutes its own
// text into the fragment. fix-prompt.md is the only base template wiring
// ${CI_FAILURE_STEP}, hence the fix-pass Env.
func TestAssembleCIFailureSummaryGateSubstitutesItsValue(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "CI_FAILURE_SUMMARY", Fragment: "ci-failure.md", Var: "CI_FAILURE_STEP", ExtraSubstVars: []string{"CI_FAILURE_SUMMARY"}},
		},
		map[string]string{
			"fix-prompt.md":           "[${CI_FAILURE_STEP}]\n",
			"fragments/ci-failure.md": "CI(${CI_FAILURE_SUMMARY})",
		})
	env.FixPass = 1

	cases := []struct {
		name    string
		summary string
		want    string
	}{
		{"unset on a fix pass", "", "[]"},
		{"set on a fix pass", "go test ./... failed: TestFoo", "[CI(go test ./... failed: TestFoo)]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := env
			env.CIFailureSummary = tc.summary
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

// Assemble's fragment loop reproduces old bash's swallow: the failed command
// substitution sat as a printf argument, so `set -e` never saw a non-zero exit
// and a missing fragment resolved to an empty string. CAVEMAN_BAKED is on in
// coveredEnv while its fragment file is absent here, so the swallow is
// observable.
func TestAssembleMissingGatedFragmentFileIsSwallowed(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{{Gate: "CAVEMAN_BAKED", Fragment: "absent.md", Var: "CAVEMAN_STEP"}},
		map[string]string{"issue-prompt.md": "[${CAVEMAN_STEP}]\n"})

	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v, want nil (a missing gated fragment file must be swallowed, not hard-fail)", err)
	}
	if got := unseparated(result.Prompt); got != "[]" {
		t.Errorf("Prompt = %q, want %q (empty-string substitution)", got, "[]")
	}
}

// The review prompt's caveman directive rides CAVEMAN_BAKED alone (issue
// #2707), not TDD_BAKED or the general SKILLS_FOUND signal: flipping only the
// caveman flag moves it, and baking only caveman still renders it.
func TestAssembleReviewPromptCavemanGate(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{{Gate: "CAVEMAN_BAKED", Fragment: "caveman-review.md", Var: "CAVEMAN_STEP_REVIEW"}},
		map[string]string{
			"review-prompt.md":            "[${CAVEMAN_STEP_REVIEW}]\n",
			"fragments/caveman-review.md": "CAVEMAN-REVIEW,",
		})

	cases := []struct {
		name   string
		mutate func(*Env)
		want   string
	}{
		{"all skills baked", func(e *Env) {}, "[CAVEMAN-REVIEW,]"},
		{"no skills baked", func(e *Env) {
			e.SkillsFound = ""
			e.CavemanSkillBaked, e.TDDSkillBaked, e.CommitSkillBaked, e.CodeReviewSkillBaked = false, false, false, false
		}, "[]"},
		{"only caveman unbaked", func(e *Env) { e.CavemanSkillBaked = false }, "[]"},
		{"only caveman baked", func(e *Env) {
			e.TDDSkillBaked, e.CommitSkillBaked, e.CodeReviewSkillBaked = false, false, false
		}, "[CAVEMAN-REVIEW,]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := env
			tc.mutate(&env)
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if got := unseparated(result.ReviewPromptText); got != tc.want {
				t.Errorf("ReviewPromptText = %q, want %q", got, tc.want)
			}
		})
	}
}

// A research prompt wires ${CAVEMAN_STEP_RESEARCH} on CAVEMAN_BAKED (issue
// #2708) and ${CHECK_HYGIENE_STEP} on CHECK_HYGIENE_BAKED (issue #3227), in
// both the repo-backed and the self-contained base, and never a skill
// preamble: its fallback prose only suits the tdd/commit/code-review guidance
// research does not render. The self-contained base has no repo to run
// anything in, so it never wires CHECK_HYGIENE_STEP whatever the skill's state.
func TestAssembleResearchSkillStepGates(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "SKILLS_FOUND", Fragment: "preamble.md", Var: "SKILL_PREAMBLE", ExtraSubstVars: []string{"SKILLS_FOUND"}},
			{Gate: "CAVEMAN_BAKED", Fragment: "caveman-research.md", Var: "CAVEMAN_STEP_RESEARCH"},
			{Gate: "CHECK_HYGIENE_BAKED", Fragment: "hygiene.md", Var: "CHECK_HYGIENE_STEP"},
		},
		map[string]string{
			"research-prompt.md":                "[${CAVEMAN_STEP_RESEARCH}${CHECK_HYGIENE_STEP}]\n",
			"research-self-contained-prompt.md": "[${CAVEMAN_STEP_RESEARCH}]\n",
			"fragments/preamble.md":             "PREAMBLE,",
			"fragments/caveman-research.md":     "CAVEMAN-RESEARCH,",
			"fragments/hygiene.md":              "HYGIENE,",
		})
	env.DispatchKind = "research"

	cases := []struct {
		name          string
		selfContained bool
		caveman       bool
		hygiene       bool
		want          string
	}{
		{"repo-backed, both baked", false, true, true, "[CAVEMAN-RESEARCH,HYGIENE,]"},
		{"repo-backed, caveman unbaked", false, false, true, "[HYGIENE,]"},
		{"repo-backed, hygiene unbaked", false, true, false, "[CAVEMAN-RESEARCH,]"},
		{"repo-backed, neither baked", false, false, false, "[]"},
		{"self-contained, both baked", true, true, true, "[CAVEMAN-RESEARCH,]"},
		{"self-contained, caveman unbaked", true, false, true, "[]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := env
			env.SelfContained = tc.selfContained
			env.CavemanSkillBaked, env.CheckHygieneSkillBaked = tc.caveman, tc.hygiene
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

// The research verdict's carrier forks on tracker backend and access (issue
// #2708): a writable github Box posts it itself, while a read-only github Box
// or a local tracker (no in-box client to post with, whatever BoxWriteEnabled
// says) relays it as a SPINDRIFT_COMMENT line, the local tracker from its own
// folder's fragment. The relay line carries the run nonce, and both bases
// (self-contained included) fork the same way, since the gates read the
// tracker and BoxWriteEnabled alone.
func TestAssembleResearchVerdictRelaySelection(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{
			{Axis: "tracker", Gate: "ISSUE_TRACKER_READWRITE", Fragment: "research-verdict.md", Var: "RESEARCH_VERDICT_STEP"},
			{Axis: "tracker", Shared: true, Gate: "ISSUE_TRACKER_RELAYED_LOG", Fragment: "research-verdict-relay.md", Var: "RESEARCH_VERDICT_RELAY_STEP"},
		},
		map[string]string{
			"research-prompt.md":                        "[${RESEARCH_VERDICT_STEP}${RESEARCH_VERDICT_RELAY_STEP}]\n",
			"research-self-contained-prompt.md":         "[${RESEARCH_VERDICT_STEP}${RESEARCH_VERDICT_RELAY_STEP}]\n",
			"fragments/github/research-verdict.md":      "DIRECT,",
			"fragments/research-verdict-relay.md":       "SHARED-RELAY(${RUN_NONCE}),",
			"fragments/local/research-verdict-relay.md": "LOCAL-RELAY(${RUN_NONCE}),",
		})
	env.DispatchKind = "research"

	cases := []struct {
		name          string
		local         bool
		boxWrite      bool
		selfContained bool
		want          string
	}{
		{"github read-write", false, true, false, "[DIRECT,]"},
		{"github read-only", false, false, false, "[SHARED-RELAY(run-nonce-abc123),]"},
		{"self-contained github read-write", false, true, true, "[DIRECT,]"},
		{"self-contained github read-only", false, false, true, "[SHARED-RELAY(run-nonce-abc123),]"},
		{"local, box write enabled", true, true, false, "[LOCAL-RELAY(run-nonce-abc123),]"},
		{"local, read-only", true, false, false, "[LOCAL-RELAY(run-nonce-abc123),]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := env
			if tc.local {
				env.IssueTracker, env.TrackerAxisRead, env.TrackerAxisWrite = "local", "LOCAL", ""
			}
			env.BoxWriteEnabled = tc.boxWrite
			env.SelfContained = tc.selfContained
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if got := unseparated(result.Prompt); got != tc.want {
				t.Errorf("Prompt = %q, want %q", got, tc.want)
			}
			assertNoUnsubstituted(t, t.Name(), result.Prompt)
		})
	}
}
