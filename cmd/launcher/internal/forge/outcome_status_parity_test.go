package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// TestResearchStatusesMatchVerdictLabels guards
// lib/prompt-contract.nix's outcomeStatusSets research row (regen-rendered
// into outcome.ResearchStatuses, issue #2504) against silently diverging
// from the pre-existing compiled-default research verdict vocabulary
// (forge.ResearchVerdictLabels, verdict.go). outcome.ResearchStatuses'
// trailing "blocked" is the research kind's own crash/no-verdict escape
// hatch (never a configured verdict token — see verdict.go's
// blockedVerdict), so it is stripped before comparing against the verdict
// tokens.
func TestResearchStatusesMatchVerdictLabels(t *testing.T) {
	if len(outcome.ResearchStatuses) == 0 {
		t.Fatal("outcome.ResearchStatuses is empty")
	}

	last := outcome.ResearchStatuses[len(outcome.ResearchStatuses)-1]
	if last != "blocked" {
		t.Fatalf("outcome.ResearchStatuses last entry = %q, want trailing escape-hatch %q", last, "blocked")
	}
	verdictStatuses := outcome.ResearchStatuses[:len(outcome.ResearchStatuses)-1]

	verdicts := forge.ResearchVerdictLabels().Verdicts()
	if len(verdictStatuses) != len(verdicts) {
		t.Fatalf("outcome.ResearchStatuses (minus trailing blocked) has %d entries, forge.ResearchVerdictLabels().Verdicts() has %d: %v vs %v",
			len(verdictStatuses), len(verdicts), verdictStatuses, verdicts)
	}
	for i, v := range verdicts {
		if verdictStatuses[i] != string(v) {
			t.Errorf("outcome.ResearchStatuses[%d] = %q, want %q (forge.ResearchVerdictLabels().Verdicts()[%d])",
				i, verdictStatuses[i], string(v), i)
		}
	}
}
