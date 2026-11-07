package promptassembly

import (
	"strings"
	"testing"
)

// agentPrompt assembles env against reg and returns the named roster agent's
// rendered prompt, whose template is staged as <agent>-prompt.md.
func agentPrompt(t *testing.T, env Env, reg Registry, agent string) string {
	t.Helper()
	env.AgentsJSONTemplate = `{"` + agent + `":{"model":"x"}}`
	env.AgentsPromptFiles = `{"` + agent + `":"` + agent + `-prompt.md"}`
	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return agentPromptFromJSON(t, result.AgentsJSON, agent)
}

// Issue #4562: a roster subagent's SKILL_PREAMBLE follows SKILLS_FOUND alone,
// not the per-skill baked flags (the scout and worker carry no caveman anchor,
// which the clause contracts pin on their templates).
func TestAgentSkillPreambleFollowsSkillsFound(t *testing.T) {
	for _, agent := range []string{"scout", "worker"} {
		t.Run(agent, func(t *testing.T) {
			reg, env := syntheticRegistry(t,
				[]FragmentRow{{Gate: "SKILLS_FOUND", Fragment: "skill-preamble.md", Var: "SKILL_PREAMBLE"}},
				map[string]string{
					agent + "-prompt.md":          "${SKILL_PREAMBLE}persona\n",
					"fragments/skill-preamble.md": "PREAMBLE\n",
				})

			env.CavemanSkillBaked = false
			if got := agentPrompt(t, env, reg, agent); !strings.Contains(got, "PREAMBLE") {
				t.Errorf("SkillsFound set: prompt = %q, want the preamble regardless of the caveman flag", got)
			}

			env.SkillsFound = ""
			got := agentPrompt(t, env, reg, agent)
			if strings.Contains(got, "PREAMBLE") {
				t.Errorf("SkillsFound empty: prompt = %q, want no preamble", got)
			}
			if strings.Contains(got, "${") {
				t.Errorf("SkillsFound empty: prompt = %q, want no unsubstituted ${...} token", got)
			}
		})
	}
}

// Issue #3157: WORKER_SCOUT_BRIEF follows ScoutProvisioned; with no scout the
// worker prompt degrades with no brief reference and no dangling token.
func TestWorkerPromptScoutBriefFollowsScoutProvisioned(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{{Gate: "WORKER_SCOUT_BRIEF", Fragment: "worker-scout-brief.md", Var: "WORKER_SCOUT_BRIEF_STEP"}},
		map[string]string{
			"worker-prompt.md":                "${WORKER_SCOUT_BRIEF_STEP}persona\n",
			"fragments/worker-scout-brief.md": "BRIEF-STEP\n",
		})

	for _, scout := range []bool{true, false} {
		env.ScoutProvisioned = scout
		got := agentPrompt(t, env, reg, "worker")
		if has := strings.Contains(got, "BRIEF-STEP"); has != scout {
			t.Errorf("ScoutProvisioned=%v: brief step rendered = %v, want %v: %q", scout, has, scout, got)
		}
		if strings.Contains(got, "${") {
			t.Errorf("ScoutProvisioned=%v: prompt = %q, want no unsubstituted ${...} token", scout, got)
		}
	}
}

// Issue #6182: the worker prompt splices the test-first pair and the two
// principle anchors, each on its own baked flag, and never an unsubstituted
// token.
func TestWorkerPromptTestFirstAndPrincipleGates(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "TDD_BAKED", Fragment: "tdd-baked.md", Var: "TDD_BAKED_STEP"},
			{Gate: "TDD_UNBAKED", Fragment: "tdd-unbaked.md", Var: "TDD_UNBAKED_STEP"},
			{Gate: "PRINCIPLE_LAZINESS_PROTOCOL_BAKED", Fragment: "laziness.md", Var: "PRINCIPLE_LAZINESS_PROTOCOL_STEP"},
			{Gate: "PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_BAKED", Fragment: "redesign.md", Var: "PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_STEP"},
		},
		map[string]string{
			"worker-prompt.md":         "${TDD_BAKED_STEP}${TDD_UNBAKED_STEP}${PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_STEP}${PRINCIPLE_LAZINESS_PROTOCOL_STEP}persona\n",
			"fragments/tdd-baked.md":   "TDD-ON\n",
			"fragments/tdd-unbaked.md": "TDD-OFF\n",
			"fragments/laziness.md":    "LAZY\n",
			"fragments/redesign.md":    "REDESIGN\n",
		})

	cases := []struct {
		name                    string
		tdd, laziness, redesign bool
	}{
		{"all baked", true, true, true},
		{"none baked", false, false, false},
		{"tdd only", true, false, false},
		{"principles only", false, true, true},
		{"laziness only", false, true, false},
		{"redesign only", false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := env
			env.TDDSkillBaked = tc.tdd
			env.PrincipleLazinessProtocolSkillBaked = tc.laziness
			env.PrincipleRedesignFromFirstPrinciplesSkillBaked = tc.redesign
			got := agentPrompt(t, env, reg, "worker")
			for marker, want := range map[string]bool{
				"TDD-ON": tc.tdd, "TDD-OFF": !tc.tdd, "LAZY": tc.laziness, "REDESIGN": tc.redesign,
			} {
				if has := strings.Contains(got, marker); has != want {
					t.Errorf("%s rendered = %v, want %v: %q", marker, has, want, got)
				}
			}
			if strings.Contains(got, "${") {
				t.Errorf("prompt = %q, want no unsubstituted ${...} token", got)
			}
		})
	}
}

// Issue #3157: the `# SCOUT` section's SCOUT_PROVISIONED/SCOUT_ABSENT pair is
// exactly-one-on, and COORDINATOR_SCOUT_BRIEF needs both a worker and a scout
// (issue #3449), independent of the scout-only SCOUT_PROVISIONED arm.
func TestIssuePromptScoutAndCoordinatorBriefGates(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "SCOUT_PROVISIONED", Fragment: "scout-delegate.md", Var: "SCOUT_DELEGATE_STEP"},
			{Gate: "SCOUT_ABSENT", Fragment: "scout-absent.md", Var: "SCOUT_ABSENT_STEP"},
			{Gate: "WORKER_PROVISIONED", Fragment: "coordinator.md", Var: "COORDINATOR_STEP"},
			{Gate: "COORDINATOR_SCOUT_BRIEF", Fragment: "coordinator-scout-brief.md", Var: "COORDINATOR_SCOUT_BRIEF_STEP"},
		},
		map[string]string{
			"issue-prompt.md":                      "${SCOUT_DELEGATE_STEP}${SCOUT_ABSENT_STEP}${COORDINATOR_STEP}${COORDINATOR_SCOUT_BRIEF_STEP}end\n",
			"fragments/scout-delegate.md":          "DELEGATE\n",
			"fragments/scout-absent.md":            "ABSENT\n",
			"fragments/coordinator.md":             "COORDINATOR\n",
			"fragments/coordinator-scout-brief.md": "COORD-BRIEF\n",
		})

	cases := []struct {
		name           string
		worker, scout  bool
		wantMarkers    []string
		wantNotMarkers []string
	}{
		{"scout only", false, true, []string{"DELEGATE"}, []string{"ABSENT", "COORDINATOR", "COORD-BRIEF"}},
		{"neither", false, false, []string{"ABSENT"}, []string{"DELEGATE", "COORDINATOR", "COORD-BRIEF"}},
		{"worker only", true, false, []string{"ABSENT", "COORDINATOR"}, []string{"DELEGATE", "COORD-BRIEF"}},
		{"worker and scout", true, true, []string{"DELEGATE", "COORDINATOR", "COORD-BRIEF"}, []string{"ABSENT"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := env
			env.WorkerProvisioned = tc.worker
			env.ScoutProvisioned = tc.scout
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			for _, m := range tc.wantMarkers {
				if !strings.Contains(result.Prompt, m) {
					t.Errorf("Prompt missing %s:\n%s", m, result.Prompt)
				}
			}
			for _, m := range tc.wantNotMarkers {
				if strings.Contains(result.Prompt, m) {
					t.Errorf("Prompt contains %s, want absent:\n%s", m, result.Prompt)
				}
			}
			if strings.Contains(result.Prompt, "${") {
				t.Errorf("Prompt has an unsubstituted ${...} token:\n%s", result.Prompt)
			}
		})
	}
}
