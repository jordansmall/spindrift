package forgejo_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
)

// TestForgejoClient_Priority_ResolvedFromLabels is a wiring smoke test, not
// the exhaustive label-matching matrix — that lives in
// forge.ResolvePriority's own TestResolvePriority (priority_test.go in the
// forge package), which every IssueTracker adapter shares instead of
// re-deriving the same switch (mirrors DispatchLabels.ClaimRemoveLabels's
// shared-helper precedent, and github's own exec_issues.go wiring covered by
// github/priority_test.go). This just confirms toForgeIssue — the single
// conversion function behind Issue, ListIssues, and ListOpenIssues — resolves
// Priority from the issue's labels.
func TestForgejoClient_Priority_ResolvedFromLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/issues/1":
			w.Write([]byte(`{"number":1,"title":"critical bug","state":"open","labels":[{"name":"ready-for-agent"},{"name":"agent-priority-critical"}]}`))
		case "/api/v1/repos/owner/repo/issues/2":
			w.Write([]byte(`{"number":2,"title":"unlabeled","state":"open","labels":[]}`))
		case "/api/v1/repos/owner/repo/issues":
			w.Write([]byte(`[{"number":1,"title":"critical bug","state":"open","labels":[{"name":"ready-for-agent"},{"name":"agent-priority-critical"}]},{"number":2,"title":"unlabeled","state":"open","labels":[]}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})

	iss, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if iss.Priority != forge.PriorityCritical {
		t.Errorf("Issue(1).Priority = %v, want PriorityCritical", iss.Priority)
	}

	open, err := fc.ListOpenIssues()
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
}
