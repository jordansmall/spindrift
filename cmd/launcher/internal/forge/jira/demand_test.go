package jira_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/jira"
)

// The probe must count the same set ListIssues(Dispatchable) lists, so the two
// queries differ only in the order clause and the zero-row page size.
func TestJiraClient_CountReady_ServerDC_ZeroRowSearchReadsTotal(t *testing.T) {
	var queries []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		queries = append(queries, map[string]string{
			"jql": q.Get("jql"), "maxResults": q.Get("maxResults"), "startAt": q.Get("startAt"),
		})
		if q.Get("maxResults") != "0" {
			// A real page walk over an empty backlog; a nonzero total here would
			// be an empty page before total, which doSearch rightly rejects.
			w.Write([]byte(`{"issues":[],"startAt":0,"total":0}`))
			return
		}
		w.Write([]byte(`{"issues":[],"startAt":0,"maxResults":0,"total":7}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL:       srv.URL,
		Token:         "tok",
		ProjectKey:    "PROJ",
		StatusMapping: map[forge.DispatchState]string{forge.Dispatchable: "To Do"},
		Labels:        testLabels,
	})

	dc, ok := jc.(forge.DemandCounter)
	if !ok {
		t.Fatal("jira client does not implement forge.DemandCounter")
	}
	got, err := dc.CountReady(false)
	if err != nil {
		t.Fatalf("CountReady: %v", err)
	}
	if got != 7 {
		t.Errorf("CountReady = %d, want 7", got)
	}
	if len(queries) != 1 {
		t.Fatalf("CountReady made %d searches, want exactly 1", len(queries))
	}
	count := queries[0]
	if count["maxResults"] != "0" {
		t.Errorf("maxResults = %q, want 0", count["maxResults"])
	}
	if count["startAt"] != "" {
		t.Errorf("startAt = %q, want none", count["startAt"])
	}

	if _, err := jc.ListIssues(forge.Dispatchable); err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	listJQL := queries[len(queries)-1]["jql"]
	if want := strings.TrimSuffix(listJQL, " order by created asc"); count["jql"] != want {
		t.Errorf("CountReady jql = %q, want ListIssues jql sans order = %q", count["jql"], want)
	}
}

// Jira Cloud removed GET /rest/api/2/search (410), so a client with an Email
// counts through the approximate-count endpoint instead.
func TestJiraClient_CountReady_Cloud_ApproximateCount(t *testing.T) {
	var jqls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/search/approximate-count" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			JQL string `json:"jql"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		jqls = append(jqls, body.JQL)
		w.Write([]byte(`{"count":7}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL:       srv.URL,
		Email:         "me@example.com",
		Token:         "tok",
		ProjectKey:    "PROJ",
		StatusMapping: map[forge.DispatchState]string{forge.Dispatchable: "To Do"},
		Labels:        testLabels,
	})
	dc, ok := jc.(forge.DemandCounter)
	if !ok {
		t.Fatal("jira client does not implement forge.DemandCounter")
	}
	got, err := dc.CountReady(false)
	if err != nil {
		t.Fatalf("CountReady: %v", err)
	}
	if got != 7 {
		t.Errorf("CountReady = %d, want 7", got)
	}
	if len(jqls) != 1 {
		t.Fatalf("CountReady made %d requests, want exactly 1", len(jqls))
	}
	jql := jqls[0]
	for _, want := range []string{`project = "PROJ"`, `status = "To Do"`, `labels = "` + testLabels.Label(forge.Dispatchable) + `"`, "statusCategory != Done"} {
		if !strings.Contains(jql, want) {
			t.Errorf("jql %q missing %q", jql, want)
		}
	}
	if strings.Contains(strings.ToLower(jql), "order by") {
		t.Errorf("jql %q must not carry an order clause", jql)
	}
}

func TestJiraClient_ProbeInterval(t *testing.T) {
	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: "http://unused"})
	dc, ok := jc.(forge.DemandCounter)
	if !ok {
		t.Fatal("jira client does not implement forge.DemandCounter")
	}
	if got := dc.ProbeInterval(); got != 5*time.Minute {
		t.Errorf("ProbeInterval = %v, want 5m", got)
	}
}

func TestJiraClient_CountReady_SearchErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok", ProjectKey: "PROJ", Labels: testLabels})
	dc, ok := jc.(forge.DemandCounter)
	if !ok {
		t.Fatal("jira client does not implement forge.DemandCounter")
	}
	if _, err := dc.CountReady(false); err == nil {
		t.Fatal("want error on 500, got nil")
	}
}
