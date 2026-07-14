package jira_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/jira"
)

// testLabels is the conventional lifecycle-label set, mirrored from
// lib/env-schema.nix (issue #460); this package's tests share it instead of
// each test restating the four label strings.
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// TestParseStatusMapping_Empty verifies an empty string parses to an empty
// mapping (every state falls back to its label) rather than an error.
func TestParseStatusMapping_Empty(t *testing.T) {
	m, err := jira.ParseStatusMapping("")
	if err != nil {
		t.Fatalf("ParseStatusMapping(\"\"): %v", err)
	}
	if len(m) != 0 {
		t.Errorf("want empty mapping, got %v", m)
	}
}

// TestParseStatusMapping_AllStates verifies the JSON knob format maps every
// dispatch-state key to its DispatchState.
func TestParseStatusMapping_AllStates(t *testing.T) {
	m, err := jira.ParseStatusMapping(`{"dispatchable":"To Do","inProgress":"In Progress","complete":"Done","failed":"Blocked"}`)
	if err != nil {
		t.Fatalf("ParseStatusMapping: %v", err)
	}
	want := map[forge.DispatchState]string{
		forge.Dispatchable: "To Do",
		forge.InProgress:   "In Progress",
		forge.Complete:     "Done",
		forge.Failed:       "Blocked",
	}
	for state, status := range want {
		if m[state] != status {
			t.Errorf("m[%v] = %q, want %q", state, m[state], status)
		}
	}
}

// TestParseStatusMapping_UnknownKey rejects a typo'd key instead of silently
// dropping it, so a misconfigured mapping fails fast at startup.
func TestParseStatusMapping_UnknownKey(t *testing.T) {
	if _, err := jira.ParseStatusMapping(`{"disptchable":"To Do"}`); err == nil {
		t.Fatal("want error for unknown key, got nil")
	}
}

// TestParseStatusMapping_InvalidJSON rejects malformed JSON.
func TestParseStatusMapping_InvalidJSON(t *testing.T) {
	if _, err := jira.ParseStatusMapping(`{not json`); err == nil {
		t.Fatal("want error for invalid JSON, got nil")
	}
}

// TestJiraClient_ImplementsIssueTracker asserts that NewJiraClient satisfies
// IssueTracker (Jira implements only this seam, per ADR 0013 — code still
// lands via the github CodeForge).
func TestJiraClient_ImplementsIssueTracker(t *testing.T) {
	var _ forge.IssueTracker = jira.NewJiraClient(jira.JiraConfig{})
}

// TestJiraClient_Probe_Success verifies Probe() confirms connectivity and
// returns the configured project key on success.
func TestJiraClient_Probe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/myself" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"accountId":"abc123"}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL:    srv.URL,
		ProjectKey: "PROJ",
		Token:      "tok",
	})

	slug, err := jc.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if slug != "PROJ" {
		t.Errorf("Probe() = %q, want %q", slug, "PROJ")
	}
}

// TestJiraClient_Probe_AuthFailure verifies Probe() surfaces ErrAuthFailure
// when Jira rejects the credentials.
func TestJiraClient_Probe_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL:    srv.URL,
		ProjectKey: "PROJ",
		Token:      "bad-token",
	})

	if _, err := jc.Probe(); !errors.Is(err, forge.ErrAuthFailure) {
		t.Fatalf("Probe() error = %v, want ErrAuthFailure", err)
	}
}

// TestJiraClient_Comment_PostsBody verifies Comment() POSTs the body to the
// issue's comment endpoint.
func TestJiraClient_Comment_PostsBody(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok"})
	if err := jc.Comment("PROJ-42", "hello from the agent"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/rest/api/2/issue/PROJ-42/comment" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["body"] != "hello from the agent" {
		t.Errorf("body = %v", gotBody)
	}
}

// TestJiraClient_Issue_FetchesFields verifies Issue() populates Number,
// Title, Body (description), State, and Labels from the Jira fields payload.
func TestJiraClient_Issue_FetchesFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/issue/PROJ-7" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"key": "PROJ-7",
			"fields": {
				"summary": "Fix the thing",
				"description": "Detailed description here.",
				"status": {"name": "To Do"},
				"labels": ["ready-for-agent"]
			}
		}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok"})
	iss, err := jc.Issue("PROJ-7")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.Number != "PROJ-7" {
		t.Errorf("Number = %q", iss.Number)
	}
	if iss.Title != "Fix the thing" {
		t.Errorf("Title = %q", iss.Title)
	}
	if iss.Body != "Detailed description here." {
		t.Errorf("Body = %q", iss.Body)
	}
	if iss.State != forge.IssueOpen {
		t.Errorf("State = %q, want %q (per forge.Issue's OPEN|CLOSED contract)", iss.State, forge.IssueOpen)
	}
	if len(iss.Labels) != 1 || iss.Labels[0] != "ready-for-agent" {
		t.Errorf("Labels = %v", iss.Labels)
	}
}

// TestJiraClient_Issue_DoneStatusCategoryIsClosed verifies Issue() maps
// Jira's "done" status category to the forge.Issue OPEN|CLOSED contract,
// which blockerReady and ListIssues(Fake) depend on — the raw Jira status
// name (e.g. "Done") is never itself "CLOSED".
func TestJiraClient_Issue_DoneStatusCategoryIsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"key": "PROJ-8",
			"fields": {
				"summary": "s", "description": "d",
				"status": {"name": "Done", "statusCategory": {"key": "done"}},
				"labels": []
			}
		}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok"})
	iss, err := jc.Issue("PROJ-8")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueClosed {
		t.Errorf("State = %q, want %q for a done-category status", iss.State, forge.IssueClosed)
	}
}

// TestJiraClient_Issue_IncludeComments verifies that when IncludeComments is
// set, Issue() appends the comment thread to Body; by default comments are
// left out to keep the prompt-injection surface tight.
func TestJiraClient_Issue_IncludeComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/issue/PROJ-7":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"key": "PROJ-7",
				"fields": {"summary": "s", "description": "desc", "status": {"name": "To Do"}, "labels": []}
			}`))
		case "/rest/api/2/issue/PROJ-7/comment":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"comments": [{"body": "a comment from a user"}]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	// Default: no comments included.
	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok"})
	iss, err := jc.Issue("PROJ-7")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if strings.Contains(iss.Body, "a comment from a user") {
		t.Errorf("Body must not include comments by default, got %q", iss.Body)
	}

	// Opt-in: comments appended.
	jcWithComments := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok", IncludeComments: true})
	iss2, err := jcWithComments.Issue("PROJ-7")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.Contains(iss2.Body, "a comment from a user") {
		t.Errorf("Body must include comments when opted in, got %q", iss2.Body)
	}
}

// TestJiraClient_Issue_IncludeComments_MultilineCommentIsOneBullet verifies
// that Issue() renders each comment as a single bullet line, collapsing
// embedded newlines to spaces — the same formatting the local adapter's
// shared comment-append helper applies, so both consumers produce
// consistent "## Comments" sections.
func TestJiraClient_Issue_IncludeComments_MultilineCommentIsOneBullet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/issue/PROJ-8":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"key": "PROJ-8",
				"fields": {"summary": "s", "description": "desc", "status": {"name": "To Do"}, "labels": []}
			}`))
		case "/rest/api/2/issue/PROJ-8/comment":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"comments": [{"body": "line one\nline two"}]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok", IncludeComments: true})
	iss, err := jc.Issue("PROJ-8")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.Contains(iss.Body, "- line one line two") {
		t.Errorf("Body = %q, want a single bullet with embedded newlines collapsed to spaces", iss.Body)
	}
}

// TestJiraClient_DepsOf_NativeLinks verifies DepsOf resolves dependencies
// from native Jira "is blocked by" issue links, not prose parsing, and
// ignores unrelated link types/directions.
func TestJiraClient_DepsOf_NativeLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/issue/PROJ-10" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"key": "PROJ-10",
			"fields": {
				"summary": "s", "description": "This issue depends on #3 in prose, ignored.",
				"status": {"name": "To Do"}, "labels": [],
				"issuelinks": [
					{"type": {"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
					 "inwardIssue": {"key": "PROJ-3"}},
					{"type": {"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
					 "outwardIssue": {"key": "PROJ-99"}},
					{"type": {"name": "Relates", "inward": "relates to", "outward": "relates to"},
					 "inwardIssue": {"key": "PROJ-55"}}
				]
			}
		}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok"})
	deps, err := jc.DepsOf("PROJ-10")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 1 || deps[0] != (forge.Dependency{ID: "PROJ-3", Source: forge.DepSourceNative}) {
		t.Errorf("DepsOf = %v, want [PROJ-3 (native)] (native is-blocked-by link only)", deps)
	}
}

// TestJiraClient_TransitionState_MappedStatus verifies TransitionState finds
// and performs the workflow transition matching the configured status
// mapping, and cleans up any stale from-state fallback label (e.g. an issue
// discovered via the label — ListIssues matches on status OR label — must
// not still carry that label after a successful native-status transition, or
// ListIssues would re-match and re-dispatch it indefinitely).
func TestJiraClient_TransitionState_MappedStatus(t *testing.T) {
	var postedTransitionID string
	var labelCleanupOps []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-1/transitions":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"transitions": [
				{"id": "11", "name": "Start Progress", "to": {"name": "In Progress"}},
				{"id": "21", "name": "Done", "to": {"name": "Done"}}
			]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue/PROJ-1/transitions":
			var body map[string]map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			postedTransitionID = body["transition"]["id"]
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/PROJ-1":
			var body struct {
				Update struct {
					Labels []map[string]string `json:"labels"`
				} `json:"update"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			labelCleanupOps = body.Update.Labels
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL: srv.URL,
		Token:   "tok",
		StatusMapping: map[forge.DispatchState]string{
			forge.InProgress: "In Progress",
		},
		Labels: testLabels,
	})

	if err := jc.TransitionState("PROJ-1", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}
	if postedTransitionID != "11" {
		t.Errorf("posted transition id = %q, want 11", postedTransitionID)
	}
	if len(labelCleanupOps) != 1 || labelCleanupOps[0]["remove"] != "ready-for-agent" || labelCleanupOps[0]["add"] != "" {
		t.Errorf("label cleanup ops = %v, want a single remove of ready-for-agent (no add)", labelCleanupOps)
	}
}

// TestJiraClient_TransitionState_UnmappedFallsBackToLabel verifies that when
// no status mapping exists for the target state, TransitionState swaps the
// fallback label instead — the lifecycle still makes progress.
func TestJiraClient_TransitionState_UnmappedFallsBackToLabel(t *testing.T) {
	var gotLabelOps []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/PROJ-2":
			var body struct {
				Update struct {
					Labels []map[string]string `json:"labels"`
				} `json:"update"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			gotLabelOps = body.Update.Labels
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s (transitions should not be queried when unmapped)", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL:       srv.URL,
		Token:         "tok",
		StatusMapping: map[forge.DispatchState]string{}, // no mapping for InProgress
		Labels:        testLabels,
	})

	if err := jc.TransitionState("PROJ-2", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}
	want := []map[string]string{{"remove": "ready-for-agent"}, {"add": "agent-in-progress"}}
	if len(gotLabelOps) != len(want) || gotLabelOps[0]["remove"] != want[0]["remove"] || gotLabelOps[1]["add"] != want[1]["add"] {
		t.Errorf("label ops = %v, want %v", gotLabelOps, want)
	}
}

// TestJiraClient_TransitionState_BlockedFallsBackToLabel verifies that when a
// mapped transition exists but is not available on the issue's current
// workflow (blocked), TransitionState falls back to the label swap.
func TestJiraClient_TransitionState_BlockedFallsBackToLabel(t *testing.T) {
	var labelSwapped bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-9/transitions":
			// Only an irrelevant transition is available — "In Progress" is blocked.
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"transitions": [{"id": "99", "name": "Reopen", "to": {"name": "Backlog"}}]}`))
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/PROJ-9":
			labelSwapped = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL: srv.URL,
		Token:   "tok",
		StatusMapping: map[forge.DispatchState]string{
			forge.InProgress: "In Progress",
		},
		Labels: testLabels,
	})

	if err := jc.TransitionState("PROJ-9", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}
	if !labelSwapped {
		t.Error("want label fallback when the mapped transition is blocked")
	}
}

// TestJiraClient_TransitionState_InfraErrorPropagates verifies that a genuine
// infra failure (e.g. a 500 listing transitions) is surfaced as an error
// rather than silently swallowed into a label-swap fallback — the fallback
// is for an unmapped/blocked workflow transition, not for infra errors.
func TestJiraClient_TransitionState_InfraErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-5/transitions":
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/PROJ-5":
			t.Error("must not fall back to a label swap on an infra error")
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL: srv.URL,
		Token:   "tok",
		StatusMapping: map[forge.DispatchState]string{
			forge.InProgress: "In Progress",
		},
		Labels: testLabels,
	})

	if err := jc.TransitionState("PROJ-5", forge.Dispatchable, forge.InProgress); err == nil {
		t.Fatal("want an error surfaced for an infra failure, got nil")
	}
}

// TestJiraClient_ListIssues_JQLAndOrder verifies ListIssues queries by the
// mapped status (falling back to the label for issues stuck there) scoped to
// the project, and trusts the server's created-ascending ordering.
// researchLabels/researchVerdictLabels mirror ResearchDispatchLabels /
// ResearchVerdictLabels so research-kind jira tests don't restate the label
// strings.
var researchLabels = forge.ResearchDispatchLabels()
var researchVerdictLabels = forge.ResearchVerdictLabels()

// TestJiraClient_CompleteVerdict_SwapsInProgressForVerdictLabel verifies
// CompleteVerdict rides the same label-fallback swapLabel mechanism
// TransitionState uses when a state is unmapped — no jira workflow-status
// mapping exists for research verdicts (ADR 0022) — for each of the three
// verdicts, removing the InProgress fallback label.
func TestJiraClient_CompleteVerdict_SwapsInProgressForVerdictLabel(t *testing.T) {
	cases := []struct {
		verdict   forge.Verdict
		wantLabel string
	}{
		{forge.Recommend, "agent-research-recommend"},
		{forge.Reject, "agent-research-reject"},
		{forge.Unclear, "agent-research-unclear"},
	}
	for _, tc := range cases {
		var gotLabelOps []map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/PROJ-3":
				var body struct {
					Update struct {
						Labels []map[string]string `json:"labels"`
					} `json:"update"`
				}
				json.NewDecoder(r.Body).Decode(&body)
				gotLabelOps = body.Update.Labels
				w.WriteHeader(http.StatusOK)
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer srv.Close()

		jc := jira.NewJiraClient(jira.JiraConfig{
			BaseURL:       srv.URL,
			Token:         "tok",
			Labels:        researchLabels,
			VerdictLabels: researchVerdictLabels,
		})

		if err := jc.CompleteVerdict("PROJ-3", tc.verdict); err != nil {
			t.Fatalf("CompleteVerdict(%v): %v", tc.verdict, err)
		}
		want := []map[string]string{{"remove": "agent-research-in-progress"}, {"add": tc.wantLabel}}
		if len(gotLabelOps) != len(want) || gotLabelOps[0]["remove"] != want[0]["remove"] || gotLabelOps[1]["add"] != want[1]["add"] {
			t.Errorf("verdict %v: label ops = %v, want %v", tc.verdict, gotLabelOps, want)
		}
	}
}

// TestJiraClient_CompleteVerdict_UnconfiguredErrorsWithoutRequest verifies
// that CompleteVerdict on a client constructed with no VerdictLabels (the
// work-kind construction path) errors instead of issuing a Jira request with
// an empty label — matching the github adapter's guard.
func TestJiraClient_CompleteVerdict_UnconfiguredErrorsWithoutRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL: srv.URL,
		Token:   "tok",
		Labels:  researchLabels,
	})

	if err := jc.CompleteVerdict("PROJ-4", forge.Recommend); err == nil {
		t.Fatal("want error for unconfigured VerdictLabels, got nil")
	}
}

// TestJiraClient_CompleteVerdict_ThenRetryResearchable verifies that after a
// verdict terminal lands, re-marking the issue researchable (the retry
// gesture, TransitionState(Untriaged, Dispatchable)) still works — the
// Untriaged "from" label is empty, so the swap is add-only.
func TestJiraClient_CompleteVerdict_ThenRetryResearchable(t *testing.T) {
	var completeOps, retryOps []map[string]string
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/PROJ-5":
			var body struct {
				Update struct {
					Labels []map[string]string `json:"labels"`
				} `json:"update"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			call++
			if call == 1 {
				completeOps = body.Update.Labels
			} else {
				retryOps = body.Update.Labels
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL:       srv.URL,
		Token:         "tok",
		Labels:        researchLabels,
		VerdictLabels: researchVerdictLabels,
	})

	if err := jc.CompleteVerdict("PROJ-5", forge.Reject); err != nil {
		t.Fatalf("CompleteVerdict: %v", err)
	}
	if len(completeOps) != 2 || completeOps[1]["add"] != "agent-research-reject" {
		t.Fatalf("CompleteVerdict ops = %v, want add agent-research-reject", completeOps)
	}

	if err := jc.TransitionState("PROJ-5", forge.Untriaged, forge.Dispatchable); err != nil {
		t.Fatalf("TransitionState(Untriaged, Dispatchable): %v", err)
	}
	if len(retryOps) != 1 || retryOps[0]["add"] != "agent-research" {
		t.Errorf("retry ops = %v, want a single add of agent-research", retryOps)
	}
}

func TestJiraClient_ListIssues_JQLAndOrder(t *testing.T) {
	var gotJQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		gotJQL = r.URL.Query().Get("jql")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"issues": [
			{"key": "PROJ-1", "fields": {"summary": "first", "status": {"name": "To Do"}, "labels": ["ready-for-agent"]}},
			{"key": "PROJ-4", "fields": {"summary": "second", "status": {"name": "To Do"}, "labels": ["ready-for-agent"]}}
		]}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL:    srv.URL,
		Token:      "tok",
		ProjectKey: "PROJ",
		StatusMapping: map[forge.DispatchState]string{
			forge.Dispatchable: "To Do",
		},
		Labels: testLabels,
	})

	issues, err := jc.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 2 || issues[0].Number != "PROJ-1" || issues[1].Number != "PROJ-4" {
		t.Fatalf("issues = %+v", issues)
	}
	if !strings.Contains(gotJQL, `project = "PROJ"`) {
		t.Errorf("jql = %q, want project scope", gotJQL)
	}
	if !strings.Contains(gotJQL, `status = "To Do"`) {
		t.Errorf("jql = %q, want status clause", gotJQL)
	}
	if !strings.Contains(gotJQL, `labels = "ready-for-agent"`) {
		t.Errorf("jql = %q, want label fallback clause", gotJQL)
	}
	if !strings.Contains(gotJQL, "order by created asc") {
		t.Errorf("jql = %q, want canonical created-ascending order", gotJQL)
	}
}

// TestJiraClient_ListIssues_CapsMaxResults verifies ListIssues bounds the
// search page size (the shared resultPageLimit, also used by the github
// adapter) so a large backlog degrades to a warning instead of an
// unbounded response.
func TestJiraClient_ListIssues_CapsMaxResults(t *testing.T) {
	var gotMaxResults string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMaxResults = r.URL.Query().Get("maxResults")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"issues": []}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok", ProjectKey: "PROJ", Labels: testLabels})
	if _, err := jc.ListIssues(forge.Dispatchable); err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if gotMaxResults != "100" {
		t.Errorf("maxResults = %q, want 100", gotMaxResults)
	}
}

// TestJiraClient_ListIssues_ExcludesDoneCategory verifies ListIssues always
// excludes done-category issues (mirroring the github adapter's --state
// open): an issue resolved/closed in Jira while still carrying a stale
// dispatch label (e.g. a prior label-fallback transition) must not be
// re-dispatched.
func TestJiraClient_ListIssues_ExcludesDoneCategory(t *testing.T) {
	var gotJQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotJQL = r.URL.Query().Get("jql")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"issues": []}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok", ProjectKey: "PROJ", Labels: testLabels})
	if _, err := jc.ListIssues(forge.Dispatchable); err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if !strings.Contains(gotJQL, "statusCategory != Done") {
		t.Errorf("jql = %q, want a statusCategory != Done exclusion", gotJQL)
	}
}

// TestJiraClient_ListIssues_UnmappedStateUsesLabelOnly verifies that when a
// state has no status mapping, ListIssues queries by label alone.
func TestJiraClient_ListIssues_UnmappedStateUsesLabelOnly(t *testing.T) {
	var gotJQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotJQL = r.URL.Query().Get("jql")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"issues": []}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL:       srv.URL,
		Token:         "tok",
		ProjectKey:    "PROJ",
		StatusMapping: map[forge.DispatchState]string{},
		Labels:        testLabels,
	})

	if _, err := jc.ListIssues(forge.InProgress); err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if strings.Contains(gotJQL, "status =") {
		t.Errorf("jql = %q, must not reference status when unmapped", gotJQL)
	}
	if !strings.Contains(gotJQL, `labels = "agent-in-progress"`) {
		t.Errorf("jql = %q, want label clause", gotJQL)
	}
}

// TestJiraClient_ListOpenIssues_NoStateClauseExcludesDone verifies
// ListOpenIssues scopes to the project and excludes done-category issues
// but places no status/label clause — unlike ListIssues, it returns every
// open issue regardless of dispatch state.
func TestJiraClient_ListOpenIssues_NoStateClauseExcludesDone(t *testing.T) {
	var gotJQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotJQL = r.URL.Query().Get("jql")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"issues": [
			{"key": "PROJ-2", "fields": {"summary": "untriaged", "status": {"name": "Backlog"}, "labels": []}},
			{"key": "PROJ-3", "fields": {"summary": "in progress", "status": {"name": "In Progress"}, "labels": ["agent-in-progress"]}}
		]}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{
		BaseURL:    srv.URL,
		Token:      "tok",
		ProjectKey: "PROJ",
		Labels:     testLabels,
	})

	issues, err := jc.ListOpenIssues()
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(issues) != 2 || issues[0].Number != "PROJ-2" || issues[1].Number != "PROJ-3" {
		t.Fatalf("issues = %+v", issues)
	}
	if !strings.Contains(gotJQL, `project = "PROJ"`) {
		t.Errorf("jql = %q, want project clause", gotJQL)
	}
	if !strings.Contains(gotJQL, "statusCategory != Done") {
		t.Errorf("jql = %q, want done-category exclusion", gotJQL)
	}
	if strings.Contains(gotJQL, "status =") || strings.Contains(gotJQL, "labels =") {
		t.Errorf("jql = %q, must not scope by status or label", gotJQL)
	}
}

// TestJiraClient_ListLabels_ReturnsSiteLabels verifies ListLabels reads
// Jira's site-wide label list.
func TestJiraClient_ListLabels_ReturnsSiteLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/label" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"values": ["ready-for-agent", "agent-in-progress"]}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok"})
	labels, err := jc.ListLabels()
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(labels) != 2 || labels[0] != "ready-for-agent" || labels[1] != "agent-in-progress" {
		t.Errorf("labels = %v", labels)
	}
}

// TestJiraClient_CreateLabel_NoOp verifies CreateLabel is a no-op: Jira has
// no label-registration endpoint (labels are free text, auto-created on
// first use), so it must not issue any request nor error.
func TestJiraClient_CreateLabel_NoOp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("CreateLabel must not make any request, got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok"})
	if err := jc.CreateLabel("agent-failed", "desc", "d93f0b"); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
}
