package jira_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/jira"
)

// jiraIssueRecord is one scripted issue in jiraHarness's in-memory backend.
type jiraIssueRecord struct {
	title, body string
	labels      []string
	nativeDeps  []string
	failGET     bool // simulates the native-API error the issue's GET returns
}

// jiraHarness is a forgetest.Harness backed by an httptest server that
// stands in for the Jira REST API. Jira's DepsOf and Issue share one
// underlying GET request (unlike github's separate dependencies/blocked_by
// call), so this harness implements forgetest.NativeCapable but not
// NativeFailureIsolatable — the contract's native-error-fallback scenario
// (AC2, issue #1544) is scoped to the Fake and github adapters only.
type jiraHarness struct {
	mu     sync.Mutex
	order  []string
	issues map[string]*jiraIssueRecord

	srv *httptest.Server
	tr  forge.IssueTracker
}

var jqlLabelClause = regexp.MustCompile(`labels = "([^"]+)"`)

func newJiraHarness(t *testing.T) *jiraHarness {
	h := &jiraHarness{issues: map[string]*jiraIssueRecord{}}
	h.srv = httptest.NewServer(http.HandlerFunc(h.handle))
	t.Cleanup(h.srv.Close)
	h.tr = jira.NewJiraClient(jira.JiraConfig{
		BaseURL:       h.srv.URL,
		ProjectKey:    "PROJ",
		Token:         "tok",
		Labels:        testLabels,
		VerdictLabels: forge.ResearchVerdictLabels(),
	})
	return h
}

func (h *jiraHarness) Tracker() forge.IssueTracker { return h.tr }

func (h *jiraHarness) SeedIssue(iss forge.Issue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.issues[iss.Number]; !ok {
		h.order = append(h.order, iss.Number)
	}
	h.issues[iss.Number] = &jiraIssueRecord{title: iss.Title, body: iss.Body, labels: append([]string(nil), iss.Labels...)}
}

func (h *jiraHarness) SeedNativeDeps(num string, ids []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.issues[num].nativeDeps = ids
}

func (h *jiraHarness) FailNativeDeps(num string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.issues[num].failGET = true
}

type jiraPayloadLink struct {
	Type struct {
		Inward string `json:"inward"`
	} `json:"type"`
	InwardIssue *struct {
		Key string `json:"key"`
	} `json:"inwardIssue"`
}

func (h *jiraHarness) payload(key string, rec *jiraIssueRecord) map[string]any {
	links := make([]jiraPayloadLink, len(rec.nativeDeps))
	for i, id := range rec.nativeDeps {
		l := jiraPayloadLink{}
		l.Type.Inward = "is blocked by"
		l.InwardIssue = &struct {
			Key string `json:"key"`
		}{Key: id}
		links[i] = l
	}
	return map[string]any{
		"key": key,
		"fields": map[string]any{
			"summary":     rec.title,
			"description": rec.body,
			"status": map[string]any{
				"name": "Open",
				"statusCategory": map[string]any{
					"key": "new",
				},
			},
			"labels":     rec.labels,
			"issuelinks": links,
		},
	}
}

func (h *jiraHarness) handle(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/search":
		q := r.URL.Query().Get("jql")
		var wantLabel string
		if sub := jqlLabelClause.FindStringSubmatch(q); sub != nil {
			wantLabel = sub[1]
		}
		var out []map[string]any
		for _, num := range h.order {
			rec := h.issues[num]
			if wantLabel != "" && !contains(rec.labels, wantLabel) {
				continue
			}
			out = append(out, h.payload(num, rec))
		}
		json.NewEncoder(w).Encode(map[string]any{"issues": out})
		return

	case r.Method == http.MethodGet && matchIssuePath(r.URL.Path) != "":
		num := matchIssuePath(r.URL.Path)
		rec, ok := h.issues[num]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if rec.failGET {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"errorMessages":["simulated native lookup failure"]}`)
			return
		}
		json.NewEncoder(w).Encode(h.payload(num, rec))
		return

	case r.Method == http.MethodPut && matchIssuePath(r.URL.Path) != "":
		num := matchIssuePath(r.URL.Path)
		rec, ok := h.issues[num]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Update struct {
				Labels []map[string]string `json:"labels"`
			} `json:"update"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, op := range body.Update.Labels {
			if remove, ok := op["remove"]; ok {
				rec.labels = removeString(rec.labels, remove)
			}
			if add, ok := op["add"]; ok && !contains(rec.labels, add) {
				rec.labels = append(rec.labels, add)
			}
		}
		w.WriteHeader(http.StatusOK)
		return

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

var issuePathRe = regexp.MustCompile(`^/rest/api/2/issue/([^/]+)$`)

func matchIssuePath(path string) string {
	m := issuePathRe.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return m[1]
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func removeString(ss []string, s string) []string {
	var out []string
	for _, v := range ss {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

func TestJiraClient_TrackerContract(t *testing.T) {
	forgetest.RunTrackerContract(t, newJiraHarness(t))
}
