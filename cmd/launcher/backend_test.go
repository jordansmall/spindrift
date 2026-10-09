package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
)

// Slice 1 of issue #2267 is purely additive: nothing calls backendRows yet, so
// this pins it against main.go's hardcoded per-axis switch for all 5 backends.
func TestBackendRowsShape(t *testing.T) {
	if len(backendRows) != 5 {
		t.Fatalf("len(backendRows) = %d, want 5", len(backendRows))
	}

	cases := []struct {
		name                    string
		validAsTracker          bool
		validAsCodeForge        bool
		tokenEnvVar             string
		boxTokenEnvVar          string
		doctorTokenHint         string
		doctorSlugHint          string
		hostMediatedRemote      bool
		outboxRelayCapable      bool
		inBoxUnreachableTracker bool

		hasValidateTracker      bool
		hasValidateCodeForge    bool
		hasNewIssueTracker      bool
		hasNewCodeForge         bool
		hasNewReadOnlyCodeForge bool
	}{
		{
			name:               "github",
			validAsTracker:     true,
			validAsCodeForge:   true,
			tokenEnvVar:        "GH_TOKEN",
			boxTokenEnvVar:     "BOX_GH_TOKEN",
			outboxRelayCapable: true,

			hasNewIssueTracker:      true,
			hasNewCodeForge:         true,
			hasNewReadOnlyCodeForge: true,
		},
		{
			name:               "forgejo",
			validAsTracker:     true,
			validAsCodeForge:   true,
			tokenEnvVar:        "FORGEJO_TOKEN",
			boxTokenEnvVar:     "BOX_FORGEJO_TOKEN",
			doctorTokenHint:    "FORGEJO_TOKEN",
			doctorSlugHint:     "FORGEJO_BASE_URL",
			outboxRelayCapable: true,

			hasValidateTracker:      true,
			hasValidateCodeForge:    true,
			hasNewIssueTracker:      true,
			hasNewCodeForge:         true,
			hasNewReadOnlyCodeForge: true,
		},
		{
			name:             "jira",
			validAsTracker:   true,
			validAsCodeForge: false,
			tokenEnvVar:      "JIRA_TOKEN",
			boxTokenEnvVar:   "",
			doctorTokenHint:  "JIRA_TOKEN",
			doctorSlugHint:   "JIRA_BASE_URL / JIRA_PROJECT_KEY",

			hasValidateTracker: true,
			hasNewIssueTracker: true,
		},
		{
			name:                    "local",
			validAsTracker:          true,
			validAsCodeForge:        true,
			hostMediatedRemote:      true,
			inBoxUnreachableTracker: true,

			hasValidateCodeForge: true,
			hasNewIssueTracker:   true,
			hasNewCodeForge:      true,
		},
		{
			name:             "git",
			validAsTracker:   false,
			validAsCodeForge: true,

			hasValidateCodeForge: true,
			hasNewCodeForge:      true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row, ok := backendByName(tc.name)
			if !ok {
				t.Fatalf("backendByName(%q) ok=false, want true", tc.name)
			}
			if row.Name != tc.name {
				t.Errorf("name = %q, want %q", row.Name, tc.name)
			}
			if row.ValidAsTracker != tc.validAsTracker {
				t.Errorf("validAsTracker = %v, want %v", row.ValidAsTracker, tc.validAsTracker)
			}
			if row.ValidAsCodeForge != tc.validAsCodeForge {
				t.Errorf("validAsCodeForge = %v, want %v", row.ValidAsCodeForge, tc.validAsCodeForge)
			}
			if row.TokenEnvVar != tc.tokenEnvVar {
				t.Errorf("tokenEnvVar = %q, want %q", row.TokenEnvVar, tc.tokenEnvVar)
			}
			if row.boxTokenEnvVar != tc.boxTokenEnvVar {
				t.Errorf("boxTokenEnvVar = %q, want %q", row.boxTokenEnvVar, tc.boxTokenEnvVar)
			}
			if row.DoctorTokenHint != tc.doctorTokenHint {
				t.Errorf("doctorTokenHint = %q, want %q", row.DoctorTokenHint, tc.doctorTokenHint)
			}
			if row.DoctorSlugHint != tc.doctorSlugHint {
				t.Errorf("doctorSlugHint = %q, want %q", row.DoctorSlugHint, tc.doctorSlugHint)
			}
			if row.HostMediatedRemote != tc.hostMediatedRemote {
				t.Errorf("hostMediatedRemote = %v, want %v", row.HostMediatedRemote, tc.hostMediatedRemote)
			}
			if row.OutboxRelayCapable != tc.outboxRelayCapable {
				t.Errorf("outboxRelayCapable = %v, want %v", row.OutboxRelayCapable, tc.outboxRelayCapable)
			}
			if row.InBoxUnreachableTracker != tc.inBoxUnreachableTracker {
				t.Errorf("inBoxUnreachableTracker = %v, want %v", row.InBoxUnreachableTracker, tc.inBoxUnreachableTracker)
			}

			if (row.validateTracker != nil) != tc.hasValidateTracker {
				t.Errorf("validateTracker present = %v, want %v", row.validateTracker != nil, tc.hasValidateTracker)
			}
			if (row.validateCodeForge != nil) != tc.hasValidateCodeForge {
				t.Errorf("validateCodeForge present = %v, want %v", row.validateCodeForge != nil, tc.hasValidateCodeForge)
			}
			if (row.newIssueTracker != nil) != tc.hasNewIssueTracker {
				t.Errorf("newIssueTracker present = %v, want %v", row.newIssueTracker != nil, tc.hasNewIssueTracker)
			}
			if (row.newCodeForge != nil) != tc.hasNewCodeForge {
				t.Errorf("newCodeForge present = %v, want %v", row.newCodeForge != nil, tc.hasNewCodeForge)
			}
			if (row.newReadOnlyCodeForge != nil) != tc.hasNewReadOnlyCodeForge {
				t.Errorf("newReadOnlyCodeForge present = %v, want %v", row.newReadOnlyCodeForge != nil, tc.hasNewReadOnlyCodeForge)
			}
		})
	}
}

func TestBackendByNameUnknown(t *testing.T) {
	if _, ok := backendByName("nonexistent"); ok {
		t.Fatalf("backendByName(%q) ok=true, want false", "nonexistent")
	}
}

// A malformed JIRA_STATUS_MAPPING must fall back to an empty map and still
// yield a non-nil tracker, matching main.go's newIssueTracker "jira" case.
func TestJiraNewIssueTrackerMalformedStatusMapping(t *testing.T) {
	row, ok := backendByName("jira")
	if !ok {
		t.Fatal("backendByName(\"jira\") ok=false")
	}
	if row.newIssueTracker == nil {
		t.Fatal("jira row.newIssueTracker is nil")
	}
	c := config{schemaConfig: schemaConfig{
		jiraBaseURL:       "https://example.atlassian.net",
		jiraProjectKey:    "PROJ",
		jiraToken:         "tok",
		jiraStatusMapping: "{not valid json",
	}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("newIssueTracker panicked: %v", r)
		}
	}()
	tracker := row.newIssueTracker(c)
	if tracker == nil {
		t.Fatal("newIssueTracker returned nil tracker")
	}
}

// backendRows and internal/backend's ByName are two registry-fallback paths over
// the same generated descriptors, so a row reaching one but not the other splits
// them silently rather than failing a build (issue #3062). Descriptor holds only
// strings and bools, so comparing whole values also catches a row that carries a
// stale copy instead of embedding the generated package variable.
func TestBackendRowsCoverRegistry(t *testing.T) {
	for _, d := range backend.Registry {
		row, ok := backendByName(d.Name)
		if !ok {
			t.Errorf("backendByName(%q) ok=false, want true (backend.Registry descriptor missing from backendRows)", d.Name)
			continue
		}
		if row.Descriptor != d {
			t.Errorf("backendByName(%q).Descriptor = %+v, want %+v", d.Name, row.Descriptor, d)
		}
	}
	for _, row := range backendRows {
		if _, ok := backend.ByName(row.Name); !ok {
			t.Errorf("backend.ByName(%q) ok=false, want true (backendRows row missing from backend.Registry)", row.Name)
		}
	}
}

// JIRA_INCLUDE_COMMENTS is a deprecated no-op: the thread reaches the prompt
// once, via CommentLister, whether or not it is set (issue #3747).
func TestJiraIncludeCommentsIsNoOp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/issue/PROJ-7":
			w.Write([]byte(`{"key": "PROJ-7", "fields": {"summary": "s", "description": "desc", "status": {"name": "To Do"}, "labels": []}}`))
		case "/rest/api/2/issue/PROJ-7/comment":
			w.Write([]byte(`{"comments": [{"body": "INJECTED"}]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	row, ok := backendByName("jira")
	if !ok {
		t.Fatal("backendByName(\"jira\") ok=false")
	}
	for _, include := range []bool{false, true} {
		c := config{schemaConfig: schemaConfig{
			jiraBaseURL:         srv.URL,
			jiraProjectKey:      "PROJ",
			jiraToken:           "tok",
			jiraIncludeComments: include,
		}}
		text, err := forge.IssueText(row.newIssueTracker(c), "PROJ-7", io.Discard)
		if err != nil {
			t.Fatalf("include=%v: IssueText: %v", include, err)
		}
		if n := strings.Count(text, "INJECTED"); n != 1 {
			t.Errorf("include=%v: %d occurrences of the comment, want 1:\n%s", include, n, text)
		}
	}
}

// fakeJiraWorkflow serves one issue whose labels track PUT updates and
// records every request, so a test sees both the wire calls a tracker made and
// the label state it left behind.
type fakeJiraWorkflow struct {
	mu          sync.Mutex
	labels      []string
	transitions int
	jqls        []string
	status      string // the issue's workflow status; empty means "To Do"
}

func (f *fakeJiraWorkflow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/transitions"):
		f.transitions++
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/transitions"):
		w.Write([]byte(`{"transitions": [{"id": "21", "to": {"name": "In Progress"}}]}`))
	case strings.HasSuffix(r.URL.Path, "/search"):
		f.jqls = append(f.jqls, r.URL.Query().Get("jql"))
		w.Write([]byte(`{"issues": [], "total": 0}`))
	case r.Method == http.MethodPut:
		var body struct {
			Update struct {
				Labels []map[string]string `json:"labels"`
			} `json:"update"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, op := range body.Update.Labels {
			if rm, ok := op["remove"]; ok {
				kept := []string{}
				for _, l := range f.labels {
					if l != rm {
						kept = append(kept, l)
					}
				}
				f.labels = kept
			}
			if add, ok := op["add"]; ok {
				f.labels = append(f.labels, add)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		labels, _ := json.Marshal(append([]string{}, f.labels...))
		status := f.status
		if status == "" {
			status = "To Do"
		}
		w.Write([]byte(`{"key": "PROJ-1", "fields": {"summary": "s", "description": "d", "status": {"name": "` + status + `"}, "labels": ` + string(labels) + `}}`))
	}
}

// JIRA_STATUS_MAPPING maps the work lifecycle only: the research tracker must
// ride the label fallback, so a claim adds agent-research-in-progress (which
// CompleteVerdict then requires) instead of posting a workflow transition
// (issue #4919). The work tracker keeps the native path.
func TestJiraStatusMappingAppliesToWorkTrackerOnly(t *testing.T) {
	const mapping = `{"dispatchable":"To Do","inProgress":"In Progress"}`
	row, ok := backendByName("jira")
	if !ok {
		t.Fatal(`backendByName("jira") ok=false`)
	}

	cases := []struct {
		name           string
		kind           *dispatchkind.Descriptor
		status         string
		wantTransition bool
		wantStatusJQL  bool
	}{
		{"research", dispatchkind.Research, "To Do", false, false},
		{"research on a work in-progress issue", dispatchkind.Research, "In Progress", false, false},
		{"work", dispatchkind.Work, "To Do", true, true},
		{"recover", dispatchkind.Recover, "To Do", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeJiraWorkflow{status: tc.status}
			srv := httptest.NewServer(fake)
			defer srv.Close()

			c := applyDispatchKind(config{schemaConfig: schemaConfig{
				jiraBaseURL:       srv.URL,
				jiraProjectKey:    "PROJ",
				jiraToken:         "tok",
				jiraStatusMapping: mapping,
				label:             "ready-for-agent",
				inProgressLabel:   "agent-in-progress",
				completeLabel:     "agent-complete",
				failedLabel:       "agent-failed",
			}}, tc.kind)
			tracker := row.newIssueTracker(c)

			if err := tracker.TransitionState("PROJ-1", forge.Dispatchable, forge.InProgress); err != nil {
				t.Fatalf("TransitionState: %v", err)
			}
			if got := fake.transitions > 0; got != tc.wantTransition {
				t.Errorf("workflow transition posted = %v, want %v", got, tc.wantTransition)
			}

			if tc.kind == dispatchkind.Research {
				if len(fake.labels) != 1 || fake.labels[0] != "agent-research-in-progress" {
					t.Errorf("labels after claim = %v, want [agent-research-in-progress]", fake.labels)
				}
				if err := tracker.CompleteVerdict("PROJ-1", forge.Recommend); err != nil {
					t.Errorf("CompleteVerdict: %v", err)
				}
			}

			if _, err := tracker.ListIssues(forge.Dispatchable); err != nil {
				t.Fatalf("ListIssues: %v", err)
			}
			if len(fake.jqls) != 1 {
				t.Fatalf("searches = %d, want 1", len(fake.jqls))
			}
			if got := strings.Contains(fake.jqls[0], "status ="); got != tc.wantStatusJQL {
				t.Errorf("JQL %q has a status clause = %v, want %v", fake.jqls[0], got, tc.wantStatusJQL)
			}
		})
	}
}
