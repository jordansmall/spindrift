package jira_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/jira"
)

var projectStatusesPathRe = regexp.MustCompile(`^/rest/api/2/project/([^/]+)/statuses$`)

// projectStatuses serves the per-issue-type status list Jira's project
// statuses endpoint returns: the same workflow statuses repeat under each type.
func (h *jiraHarness) projectStatuses(w http.ResponseWriter, r *http.Request) {
	h.statusCalls++ // handle holds h.mu
	if h.statusesFail {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if projectStatusesPathRe.FindStringSubmatch(r.URL.Path)[1] != "PROJ" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	type status struct {
		Name string `json:"name"`
	}
	var statuses []status
	for _, n := range h.workflowStatuses {
		statuses = append(statuses, status{Name: n})
	}
	out := []map[string]any{}
	for _, ty := range h.issueTypes() {
		out = append(out, map[string]any{"name": ty, "statuses": statuses})
	}
	json.NewEncoder(w).Encode(out)
}

func statusChecker(t *testing.T, h *jiraHarness, mapping string) forge.StatusMappingChecker {
	t.Helper()
	m, err := jira.ParseStatusMapping(mapping)
	if err != nil {
		t.Fatalf("ParseStatusMapping: %v", err)
	}
	email := ""
	if h.cloud {
		email = "bot@example.com"
	}
	tr := jira.NewJiraClient(jira.JiraConfig{BaseURL: h.srv.URL, ProjectKey: "PROJ", Email: email, Token: "tok", StatusMapping: m})
	c, ok := tr.(forge.StatusMappingChecker)
	if !ok {
		t.Fatal("jira tracker does not implement forge.StatusMappingChecker")
	}
	return c
}

func TestJiraClient_CheckMappedStatuses_PresentStatusesPass(t *testing.T) {
	for name, mk := range bothShapes {
		t.Run(name, func(t *testing.T) {
			h := mk(t)
			h.workflowStatuses = []string{"To Do", "In Progress", "Done"}
			// Case differs from the workflow: the adapter matches with EqualFold.
			missing, err := statusChecker(t, h, `{"dispatchable":"to do","inProgress":"In Progress","complete":{"status":"DONE","resolution":"Fixed"}}`).CheckMappedStatuses()
			if err != nil || len(missing) != 0 {
				t.Fatalf("got (%v, %v), want nothing missing", missing, err)
			}
		})
	}
}

func TestJiraClient_CheckMappedStatuses_ReportsMissingSortedAndDeduped(t *testing.T) {
	h := newJiraHarness(t)
	h.workflowStatuses = []string{"To Do", "Done"}
	missing, err := statusChecker(t, h, `{"dispatchable":"To Do","inProgress":"Wip","complete":"Dnoe","failed":"Blocked","ambiguous":"blocked"}`).CheckMappedStatuses()
	if err != nil {
		t.Fatalf("CheckMappedStatuses: %v", err)
	}
	if want := []string{"Blocked", "Dnoe", "Wip"}; !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing = %v, want %v", missing, want)
	}
}

func TestJiraClient_CheckMappedStatuses_EndpointErrorSurfaces(t *testing.T) {
	h := newJiraHarness(t)
	h.statusesFail = true
	_, err := statusChecker(t, h, `{"dispatchable":"To Do"}`).CheckMappedStatuses()
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the 403 surfaced", err)
	}
}

func TestJiraClient_CheckMappedStatuses_EmptyMappingMakesNoCall(t *testing.T) {
	h := newJiraHarness(t)
	missing, err := statusChecker(t, h, "").CheckMappedStatuses()
	if err != nil || len(missing) != 0 {
		t.Fatalf("got (%v, %v), want nothing missing", missing, err)
	}
	if h.statusCalls != 0 {
		t.Errorf("statuses endpoint read %d times, want 0", h.statusCalls)
	}
}
