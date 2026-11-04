package jira_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/jira"
)

func statusOnlyClient(url string, statusOnly bool) forge.IssueTracker {
	return jira.NewJiraClient(jira.JiraConfig{
		BaseURL:    url,
		Token:      "tok",
		ProjectKey: "PROJ",
		StatusMapping: map[forge.DispatchState]jira.StatusTarget{
			forge.Dispatchable: {Status: "To Do"},
			forge.InProgress:   {Status: "In Progress"},
			forge.Complete:     {Status: "Done"},
		},
		Labels:     testLabels,
		StatusOnly: statusOnly,
	})
}

func TestJiraClient_StatusOnly_ReadsDropLabelClause(t *testing.T) {
	for _, statusOnly := range []bool{false, true} {
		var jqls []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jqls = append(jqls, r.URL.Query().Get("jql"))
			if r.URL.Query().Get("maxResults") == "0" {
				fmt.Fprint(w, `{"issues":[],"startAt":0,"maxResults":0,"total":1}`)
				return
			}
			fmt.Fprint(w, `{"issues":[],"startAt":0,"total":0}`)
		}))
		jc := statusOnlyClient(srv.URL, statusOnly)
		if _, err := jc.ListIssues(forge.Dispatchable); err != nil {
			t.Fatalf("ListIssues: %v", err)
		}
		if _, err := jc.(forge.DemandCounter).CountReady(false); err != nil {
			t.Fatalf("CountReady: %v", err)
		}
		srv.Close()
		if len(jqls) != 2 {
			t.Fatalf("statusOnly=%v: %d searches, want 2", statusOnly, len(jqls))
		}
		for _, jql := range jqls {
			if !strings.Contains(jql, `status = "To Do"`) {
				t.Errorf("statusOnly=%v: jql %q lacks the status clause", statusOnly, jql)
			}
			if has := strings.Contains(jql, "labels = "); has == statusOnly {
				t.Errorf("statusOnly=%v: jql %q label clause present = %v", statusOnly, jql, has)
			}
		}
	}
}

func TestJiraClient_StatusOnly_ClaimChecksNativeStatusOnly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		statusOnly  bool
		issueJSON   string
		wantClaimed bool
	}{
		{"status mode ignores a stale in-progress label", true, `{"fields":{"status":{"name":"To Do"},"labels":["agent-in-progress"]}}`, false},
		{"hybrid honours the in-progress label", false, `{"fields":{"status":{"name":"To Do"},"labels":["agent-in-progress"]}}`, true},
		{"status mode honours native In Progress", true, `{"fields":{"status":{"name":"In Progress"},"labels":[]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stripped bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-1":
					fmt.Fprint(w, tc.issueJSON)
				case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-1/transitions":
					fmt.Fprint(w, `{"transitions":[{"id":"11","to":{"name":"In Progress"}}]}`)
				case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue/PROJ-1/transitions":
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/PROJ-1":
					stripped = true
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
			}))
			defer srv.Close()
			err := statusOnlyClient(srv.URL, tc.statusOnly).TransitionState("PROJ-1", forge.Dispatchable, forge.InProgress)
			if got := errors.Is(err, forge.ErrAlreadyClaimed); got != tc.wantClaimed {
				t.Fatalf("ErrAlreadyClaimed = %v (err %v), want %v", got, err, tc.wantClaimed)
			}
			if !tc.wantClaimed && err != nil {
				t.Fatalf("claim: %v", err)
			}
			if !tc.wantClaimed && !stripped {
				t.Error("post-transition label cleanup did not run")
			}
		})
	}
}

func TestJiraClient_StatusOnly_RefusedTransitionNeverSwapsALabel(t *testing.T) {
	var puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/ENG-1":
			fmt.Fprint(w, `{"fields":{"status":{"name":"In Review"},"labels":[]}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/ENG-1/transitions":
			fmt.Fprint(w, `{"transitions":[{"id":"1","to":{"name":"Blocked"}},{"id":"2","to":{"name":"To Do"}}]}`)
		case r.Method == http.MethodPut:
			puts++
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	err := statusOnlyClient(srv.URL, true).TransitionState("ENG-1", forge.InProgress, forge.Complete)
	var refused *forge.TransitionRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *forge.TransitionRefusedError", err)
	}
	if refused.Issue != "ENG-1" || refused.From != "In Review" || refused.To != "Done" ||
		strings.Join(refused.Offered, ",") != "Blocked,To Do" {
		t.Errorf("refusal = %+v", refused)
	}
	want := `issue ENG-1 could not move from status "In Review" to "Done": the workflow offers transitions to "Blocked", "To Do"`
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if puts != 0 {
		t.Errorf("%d label PUTs, want none", puts)
	}
}

func TestTransitionRefusedError_NoOffers(t *testing.T) {
	err := &forge.TransitionRefusedError{Issue: "ENG-1", From: "A", To: "B"}
	want := `issue ENG-1 could not move from status "A" to "B": the workflow offers no transitions`
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// Research states always ride labels, so JIRA_LIFECYCLE=status must not reach
// the research tracker (ADR 0022).
func TestStrategyNewIssueTracker_StatusLifecycleSkipsResearchTracker(t *testing.T) {
	for _, tc := range []struct {
		tracker    dispatchkind.Tracker
		wantLabels bool
	}{
		{dispatchkind.TrackerWork, false},
		{dispatchkind.TrackerResearch, true},
	} {
		var jql string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jql, _ = url.QueryUnescape(r.URL.Query().Get("jql"))
			fmt.Fprint(w, `{"issues":[],"startAt":0,"total":0}`)
		}))
		env := map[string]string{
			"JIRA_BASE_URL": srv.URL, "JIRA_PROJECT_KEY": "PROJ", "JIRA_TOKEN": "tok",
			"JIRA_LIFECYCLE":      "status",
			"JIRA_STATUS_MAPPING": `{"dispatchable":"To Do","inProgress":"In Progress","complete":"Done","failed":"Failed","ambiguous":"Blocked"}`,
		}
		tr, err := jira.Strategy.NewIssueTracker(backendkit.Shared{Tracker: tc.tracker, Labels: testLabels}, func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		})
		if err != nil {
			t.Fatalf("NewIssueTracker: %v", err)
		}
		if _, err := tr.ListIssues(forge.Dispatchable); err != nil {
			t.Fatalf("ListIssues: %v", err)
		}
		srv.Close()
		if has := strings.Contains(jql, `labels = "ready-for-agent"`); has != tc.wantLabels {
			t.Errorf("tracker %v: jql %q label clause = %v, want %v", tc.tracker, jql, has, tc.wantLabels)
		}
	}
}

// Recoverable can never be mapped in status mode, so its state clause is
// empty; a query then would match every open card.
func TestJiraClient_StatusOnly_UnmappedStateMatchesNothing(t *testing.T) {
	var searches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		searches++
		fmt.Fprint(w, `{"issues":[{"key":"PROJ-9","fields":{}}],"startAt":0,"total":1}`)
	}))
	defer srv.Close()
	jc := statusOnlyClient(srv.URL, true)
	issues, err := jc.ListIssues(forge.Recoverable)
	if err != nil || len(issues) != 0 {
		t.Fatalf("ListIssues(Recoverable) = %v, %v; want none", issues, err)
	}
	if n, err := jc.(forge.DemandCounter).CountReady(false); err != nil || n != 1 {
		t.Fatalf("CountReady(Dispatchable) = %d, %v; want a mapped state still counts", n, err)
	}
	if searches != 1 {
		t.Errorf("%d searches, want only the Dispatchable count", searches)
	}
}

func TestJiraClient_StatusOnly_UnmappedTargetIsRefusedWithoutLabelEdit(t *testing.T) {
	var puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-1":
			fmt.Fprint(w, `{"fields":{"status":{"name":"In Progress"},"labels":[]}}`)
		case r.Method == http.MethodPut:
			puts++
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	err := statusOnlyClient(srv.URL, true).TransitionState("PROJ-1", forge.InProgress, forge.Recoverable)
	var refused *forge.TransitionRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *forge.TransitionRefusedError", err)
	}
	if refused.Issue != "PROJ-1" || refused.From != "In Progress" || refused.To != "" || refused.State != "recoverable" {
		t.Errorf("refusal = %+v", refused)
	}
	want := `issue PROJ-1 could not move from status "In Progress": the tracker maps no status to state recoverable`
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if puts != 0 {
		t.Errorf("%d label PUTs, want none", puts)
	}
}

func TestJiraClient_StatusOnly_RefusalSurvivesFailedStatusRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/ENG-1/transitions":
			fmt.Fprint(w, `{"transitions":[{"id":"1","to":{"name":"Blocked"}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/ENG-1":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	err := statusOnlyClient(srv.URL, true).TransitionState("ENG-1", forge.InProgress, forge.Complete)
	var refused *forge.TransitionRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *forge.TransitionRefusedError", err)
	}
	want := `issue ENG-1 could not move to "Done": the workflow offers transitions to "Blocked"`
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}
