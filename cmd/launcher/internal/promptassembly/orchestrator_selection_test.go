package promptassembly

import (
	"encoding/json"
	"strings"
	"testing"
)

// Assemble's reviewer drop, for a kind with no reviewer prompt of its own: the
// model and effort are extracted from the reviewer entry before the key is
// deleted from the agents JSON (issue #2277), and the per-agent injection loop
// still runs for every other agent. With no reviewer key, or no template at
// all, the extraction stays empty (jq's `.reviewer.model // empty`) and
// review-prompt.md still renders: it does not depend on a reviewer being
// configured.
func TestAssembleOrchestratorReviewerKeyDrop(t *testing.T) {
	cases := []struct {
		name       string
		template   string
		wantModel  string
		wantEffort string
		wantAgents bool
	}{
		{
			name:       "reviewer entry extracted then dropped",
			template:   `{"reviewer":{"model":"review-model-x","effort":"review-effort-x"},"scout":{"model":"scout-model-y"}}`,
			wantModel:  "review-model-x",
			wantEffort: "review-effort-x",
			wantAgents: true,
		},
		{
			name:       "no reviewer key",
			template:   `{"scout":{"model":"scout-model-y"}}`,
			wantAgents: true,
		},
		{name: "empty template"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, env := syntheticRegistry(t,
				[]FragmentRow{{Gate: "TDD_BAKED", Fragment: "baked.md", Var: "BAKED_STEP"}},
				map[string]string{
					"review-prompt.md":   "review ${ISSUE_NUMBER}\n",
					"scout-prompt.md":    "scout [${BAKED_STEP}]\n",
					"fragments/baked.md": "BAKED-BODY",
				})
			env.AgentsJSONTemplate = tc.template
			if tc.wantAgents {
				env.AgentsPromptFiles = `{"scout":"scout-prompt.md"}`
			}
			env.IssueText = "issue body text"

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			if result.Handoff.ReviewModel != tc.wantModel {
				t.Errorf("Handoff.ReviewModel = %q, want %q", result.Handoff.ReviewModel, tc.wantModel)
			}
			if result.Handoff.ReviewEffort != tc.wantEffort {
				t.Errorf("Handoff.ReviewEffort = %q, want %q", result.Handoff.ReviewEffort, tc.wantEffort)
			}
			if !strings.Contains(result.ReviewPromptText, "review 2349") {
				t.Errorf("ReviewPromptText = %q, want it rendered with the substituted ISSUE_NUMBER", result.ReviewPromptText)
			}
			// Issue #3445: the issue text is appended to the review prompt
			// whenever env.IssueText is set, whichever fragments render.
			if !strings.Contains(result.ReviewPromptText, "Issue #2349's body") {
				t.Errorf("ReviewPromptText missing the appended issue text section:\n%s", result.ReviewPromptText)
			}
			if result.Handoff.ReviewPromptFile != "" {
				t.Errorf("Handoff.ReviewPromptFile = %q, want empty (Assemble no longer writes rendered text there, issue #2975)", result.Handoff.ReviewPromptFile)
			}

			if !tc.wantAgents {
				if result.AgentsJSON != "" {
					t.Errorf("AgentsJSON = %q, want empty", result.AgentsJSON)
				}
				return
			}
			var parsed map[string]json.RawMessage
			if err := json.Unmarshal([]byte(result.AgentsJSON), &parsed); err != nil {
				t.Fatalf("unmarshal AgentsJSON: %v\n%s", err, result.AgentsJSON)
			}
			if _, ok := parsed["reviewer"]; ok {
				t.Errorf("AgentsJSON still contains reviewer key, want it dropped: %s", result.AgentsJSON)
			}
			if got := agentPromptFromJSON(t, result.AgentsJSON, "scout"); !strings.Contains(got, "BAKED-BODY") {
				t.Errorf("scout.prompt = %q, want the other agent still rendered", got)
			}
		})
	}
}

// Issues #2698 and #3214: commit-rework-orchestrator.md and
// land-pass-order-orchestrator.md are ungated rows, so they render on every
// fresh-work prompt whichever skill, scout, or worker state the run is in. The
// rows use the real var names; the production prose is pinned by the clause
// contracts.
func TestAssembleOrchestratorFragmentsAreUngated(t *testing.T) {
	reg, env := syntheticRegistry(t,
		[]FragmentRow{
			{Fragment: "commit-rework-orchestrator.md", Var: "COMMIT_REWORK_ORCHESTRATOR_STEP"},
			{Fragment: "land-pass-order-orchestrator.md", Var: "LAND_PASS_ORDER_ORCHESTRATOR_STEP"},
		},
		map[string]string{
			"issue-prompt.md":                           "[${COMMIT_REWORK_ORCHESTRATOR_STEP}|${LAND_PASS_ORDER_ORCHESTRATOR_STEP}]\n",
			"fragments/commit-rework-orchestrator.md":   "REWORK-BODY",
			"fragments/land-pass-order-orchestrator.md": "LAND-PASS-BODY",
		})

	for _, tddBaked := range []bool{true, false} {
		for _, scout := range []bool{true, false} {
			for _, worker := range []bool{true, false} {
				env.TDDSkillBaked = tddBaked
				env.ScoutProvisioned = scout
				env.WorkerProvisioned = worker
				result, err := Assemble(env, reg)
				if err != nil {
					t.Fatalf("Assemble: %v", err)
				}
				for _, body := range []string{"REWORK-BODY", "LAND-PASS-BODY"} {
					if !strings.Contains(result.Prompt, body) {
						t.Errorf("tdd=%v scout=%v worker=%v: Prompt = %q, missing %q", tddBaked, scout, worker, result.Prompt, body)
					}
				}
			}
		}
	}
}
