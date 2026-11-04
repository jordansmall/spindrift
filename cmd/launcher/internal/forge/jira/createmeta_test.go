package jira_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/jira"
)

// metaField is one create-screen field as Jira's createmeta reports it.
type metaField struct {
	FieldID         string `json:"fieldId"`
	Name            string `json:"name"`
	Required        bool   `json:"required"`
	HasDefaultValue bool   `json:"hasDefaultValue"`
}

type metaType struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var (
	createMetaTypesRe  = regexp.MustCompile(`^/rest/api/2/issue/createmeta/([^/]+)/issuetypes$`)
	createMetaFieldsRe = regexp.MustCompile(`^/rest/api/2/issue/createmeta/([^/]+)/issuetypes/([^/]+)$`)
)

// createMeta serves the per-project create-metadata endpoints in the two wire
// shapes Jira ships: Cloud nests rows under "issueTypes"/"fields" and counts
// with "total"; Server/DC nests them under "values" and ends with "isLast".
func (h *jiraHarness) createMeta(w http.ResponseWriter, r *http.Request) {
	var rows []any
	if m := createMetaTypesRe.FindStringSubmatch(r.URL.Path); m != nil && m[1] == "PROJ" {
		for i, name := range h.issueTypes() {
			rows = append(rows, metaType{ID: strconv.Itoa(10001 + i), Name: name})
		}
		h.writeMetaPage(w, r, "issueTypes", rows)
		return
	}
	if m := createMetaFieldsRe.FindStringSubmatch(r.URL.Path); m != nil && m[1] == "PROJ" {
		for _, f := range h.metaFields {
			rows = append(rows, f)
		}
		h.writeMetaPage(w, r, "fields", rows)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (h *jiraHarness) writeMetaPage(w http.ResponseWriter, r *http.Request, cloudKey string, rows []any) {
	start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
	size, _ := strconv.Atoi(r.URL.Query().Get("maxResults"))
	if size <= 0 || (h.metaPageSize > 0 && size > h.metaPageSize) {
		size = h.metaPageSize
	}
	if size <= 0 {
		size = len(rows)
	}
	end := min(start+size, len(rows))
	page := []any{}
	if start < len(rows) {
		page = rows[start:end]
	}
	body := map[string]any{"startAt": start, "maxResults": size, "total": len(rows)}
	if h.cloud {
		body[cloudKey] = page
	} else {
		body["values"] = page
		body["isLast"] = end >= len(rows)
	}
	json.NewEncoder(w).Encode(body)
}

func issueTypeChecker(t *testing.T, h *jiraHarness, issueType string) forge.IssueTypeChecker {
	t.Helper()
	email := ""
	if h.cloud {
		email = "bot@example.com"
	}
	tr := jira.NewJiraClient(jira.JiraConfig{BaseURL: h.srv.URL, ProjectKey: "PROJ", Email: email, Token: "tok", IssueType: issueType})
	c, ok := tr.(forge.IssueTypeChecker)
	if !ok {
		t.Fatal("jira tracker does not implement forge.IssueTypeChecker")
	}
	return c
}

var bothShapes = map[string]func(*testing.T) *jiraHarness{
	"server": newJiraHarness,
	"cloud":  newJiraCloudHarness,
}

func TestJiraClient_CheckIssueType_BothWireShapes(t *testing.T) {
	for name, mk := range bothShapes {
		t.Run(name, func(t *testing.T) {
			h := mk(t)
			h.metaFields = []metaField{
				{FieldID: "project", Name: "Project", Required: true},
				{FieldID: "summary", Name: "Summary", Required: true},
				{FieldID: "issuetype", Name: "Issue Type", Required: true},
				{FieldID: "reporter", Name: "Reporter", Required: true},
				{FieldID: "priority", Name: "Priority", Required: true, HasDefaultValue: true},
				{FieldID: "customfield_10010", Name: "Team", Required: true},
				{FieldID: "customfield_10011", Name: "Notes"},
			}
			unfilled, err := issueTypeChecker(t, h, "Task").CheckIssueType()
			if err != nil {
				t.Fatalf("CheckIssueType: %v", err)
			}
			if len(unfilled) != 1 || !strings.Contains(unfilled[0], "Team") || !strings.Contains(unfilled[0], "customfield_10010") {
				t.Fatalf("unfilled = %v, want only Team (customfield_10010)", unfilled)
			}
		})
	}
}

func TestJiraClient_CheckIssueType_NoUnfilledFields(t *testing.T) {
	h := newJiraHarness(t)
	h.metaFields = []metaField{{FieldID: "summary", Name: "Summary", Required: true}}
	unfilled, err := issueTypeChecker(t, h, "Task").CheckIssueType()
	if err != nil || len(unfilled) != 0 {
		t.Fatalf("got (%v, %v), want no unfilled fields and no error", unfilled, err)
	}
}

func TestJiraClient_CheckIssueType_UnknownType(t *testing.T) {
	_, err := issueTypeChecker(t, newJiraHarness(t), "Story").CheckIssueType()
	if err == nil {
		t.Fatal("want an error for a type the project does not offer")
	}
	for _, want := range []string{"JIRA_ISSUE_TYPE", "Story", "PROJ", "Task", "Bug"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if !errors.Is(err, forge.ErrUnknownIssueType) {
		t.Errorf("error %v should wrap forge.ErrUnknownIssueType", err)
	}
}

func TestJiraClient_CheckIssueType_PaginatesBothCalls(t *testing.T) {
	for name, mk := range bothShapes {
		t.Run(name, func(t *testing.T) {
			h := mk(t)
			h.metaPageSize = 1 // Bug is the second type; Component the third field
			h.metaFields = []metaField{
				{FieldID: "summary", Name: "Summary", Required: true},
				{FieldID: "customfield_10010", Name: "Team", Required: true},
				{FieldID: "customfield_10012", Name: "Component", Required: true},
			}
			unfilled, err := issueTypeChecker(t, h, "Bug").CheckIssueType()
			if err != nil {
				t.Fatalf("CheckIssueType: %v", err)
			}
			if len(unfilled) != 2 {
				t.Fatalf("unfilled = %v, want Team and Component", unfilled)
			}
		})
	}
}
