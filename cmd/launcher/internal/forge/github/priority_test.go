package github

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestExecClient_Priority_ResolvedFromLabels is a wiring smoke test. Every
// IssueTracker adapter shares forge.ResolvePriority rather than re-deriving
// the label switch, so the exhaustive matrix lives in the forge package's
// TestResolvePriority. This only confirms the three call sites in
// exec_issues.go (ListIssues, ListOpenIssues, Issue) call it.
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
