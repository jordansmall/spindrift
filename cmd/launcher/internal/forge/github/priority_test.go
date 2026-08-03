package github

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestExecClient_Priority_ResolvedFromLabels is a wiring smoke test, not the
// exhaustive label-matching matrix — that lives in forge.ResolvePriority's
// own TestResolvePriority (priority_test.go in the forge package), which
// every IssueTracker adapter shares instead of re-deriving the same switch
// (mirrors DispatchLabels.ClaimRemoveLabels's shared-helper precedent).
// This just confirms the three call sites in exec_issues.go (ListIssues,
// ListOpenIssues, Issue) actually call it, exercised through the
// contract_test.go fake-gh harness.
func TestExecClient_Priority_ResolvedFromLabels(t *testing.T) {
	h := newGithubHarness(t)
	h.SeedIssue(forge.Issue{
		Number: "1",
		Title:  "critical bug",
		Labels: []string{"ready-for-agent", "agent-priority-critical"},
	})
	h.SeedIssue(forge.Issue{
		Number: "2",
		Title:  "unlabeled",
	})
	tr := h.Tracker()

	iss, err := tr.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if iss.Priority != forge.PriorityCritical {
		t.Errorf("Issue(1).Priority = %v, want PriorityCritical", iss.Priority)
	}

	open, err := tr.ListOpenIssues()
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	got := map[string]forge.Priority{}
	for _, i := range open {
		got[i.Number] = i.Priority
	}
	if got["1"] != forge.PriorityCritical {
		t.Errorf("ListOpenIssues()[1].Priority = %v, want PriorityCritical", got["1"])
	}
	if got["2"] != forge.PriorityNormal {
		t.Errorf("ListOpenIssues()[2].Priority = %v, want PriorityNormal", got["2"])
	}

	listed, err := tr.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues(Dispatchable): %v", err)
	}
	if len(listed) != 1 || listed[0].Priority != forge.PriorityCritical {
		t.Fatalf("ListIssues(Dispatchable) = %+v, want one issue with PriorityCritical", listed)
	}
}
