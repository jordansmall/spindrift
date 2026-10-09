package jira_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/jira"
)

// testLabels mirrors the lifecycle-label set in lib/env-schema.nix (issue
// #460).
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// An empty mapping is valid, not an error: every state falls back to its label.
func TestParseStatusMapping_Empty(t *testing.T) {
	m, err := jira.ParseStatusMapping("")
	if err != nil {
		t.Fatalf("ParseStatusMapping(\"\"): %v", err)
	}
	if len(m) != 0 {
		t.Errorf("want empty mapping, got %v", m)
	}
}

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

// A typo'd key must fail fast at startup, not be silently dropped.
func TestParseStatusMapping_UnknownKey(t *testing.T) {
	if _, err := jira.ParseStatusMapping(`{"disptchable":"To Do"}`); err == nil {
		t.Fatal("want error for unknown key, got nil")
	}
}

func TestParseStatusMapping_InvalidJSON(t *testing.T) {
	if _, err := jira.ParseStatusMapping(`{not json`); err == nil {
		t.Fatal("want error for invalid JSON, got nil")
	}
}

// Jira implements only the IssueTracker seam, per ADR 0013. Code lands through
// whichever Code Forge CODE_FORGE selects.
func TestJiraClient_ImplementsIssueTracker(t *testing.T) {
	var _ forge.IssueTracker = jira.NewJiraClient(jira.JiraConfig{})
}

// Only the local adapter records a landing ref (ADR 0029); jira has no such
// concept, so it must not satisfy forge.LandingRecorder.
func TestJiraClient_DoesNotImplementLandingRecorder(t *testing.T) {
	it := jira.NewJiraClient(jira.JiraConfig{})
	if _, ok := it.(forge.LandingRecorder); ok {
		t.Error("JiraClient satisfies forge.LandingRecorder, want it hidden")
	}
}

// A jira state maps through a blend of StatusMapping and Labels, not a single
// DispatchLabels value, so PickIssue's double-box guard (#1742) must not
// shortcut it and keeps paying the ListIssues round trip.
func TestJiraClient_DoesNotImplementLabeledTracker(t *testing.T) {
	it := jira.NewJiraClient(jira.JiraConfig{})
	if _, ok := it.(forge.LabeledTracker); ok {
		t.Error("JiraClient satisfies forge.LabeledTracker, want it hidden")
	}
}

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

// blockerReady and ListIssues(Fake) depend on forge.Issue's OPEN|CLOSED
// contract, and the raw Jira status name (such as "Done") is never itself
// "CLOSED", so Issue must map the done status category instead.
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

// Jira takes the GITHUB arm of the tracker read gate (see promptassembly's
// gates_tracker_test.go), whose fragment tells the agent its last-10-comment
// snapshot is already in the # ISSUE TEXT section. Without CommentLister that
// claim would be false.
func TestJiraClient_ImplementsCommentLister(t *testing.T) {
	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: "http://example.invalid", Token: "tok"})
	if _, ok := jc.(forge.CommentLister); !ok {
		t.Fatal("jiraClient does not satisfy forge.CommentLister, want it implemented")
	}
}

// Jira returns comments oldest first, so Comments keeps the response order
// rather than sorting, and the assertion below depends on that order.
func TestJiraClient_Comments_MapsAuthorCreatedAtBodyOldestFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/issue/PROJ-9/comment" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"comments": [
			{"author": {"displayName": "Alice"}, "created": "2024-01-01T00:00:00.000+0000", "body": "first"},
			{"author": {"displayName": "Bob"}, "created": "2024-01-02T00:00:00.000+0000", "body": "second"}
		]}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok"})
	cl, ok := jc.(forge.CommentLister)
	if !ok {
		t.Fatal("jiraClient does not satisfy forge.CommentLister")
	}
	comments, err := cl.Comments("PROJ-9")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}

	want := []forge.Comment{
		{Author: "Alice", CreatedAt: "2024-01-01T00:00:00.000+0000", Body: "first"},
		{Author: "Bob", CreatedAt: "2024-01-02T00:00:00.000+0000", Body: "second"},
	}
	if !reflect.DeepEqual(comments, want) {
		t.Fatalf("Comments = %+v, want %+v", comments, want)
	}
}

// Jira's comment payload carries no author standing, so an unknown
// association must fail closed.
func TestJiraClient_Comments_UnknownAssociationUntrusted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"comments": [
			{"author": {"displayName": "Alice"}, "created": "2024-01-01T00:00:00.000+0000", "body": "first"}
		]}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok"})
	cl, ok := jc.(forge.CommentLister)
	if !ok {
		t.Fatal("jiraClient does not satisfy forge.CommentLister")
	}
	comments, err := cl.Comments("PROJ-9")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(comments) == 0 {
		t.Fatal("Comments returned no comments, want at least one")
	}
	for i, c := range comments {
		if c.Association != "" || forge.CommentTrusted(c) {
			t.Fatalf("comments[%d] = %+v, want empty Association and untrusted", i, c)
		}
	}
}

// The fixture carries a prose "#3" in the description, an outward link and an
// unrelated link type on purpose: DepsOf must read only the native inward
// "is blocked by" links and ignore the rest.
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

// Mirrors the dedupe guard already fixed in the GitHub adapter's nativeDepsOf.
func TestJiraClient_DepsOf_DuplicateLinksDeduped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"key": "PROJ-11",
			"fields": {
				"summary": "s", "description": "",
				"status": {"name": "To Do"}, "labels": [],
				"issuelinks": [
					{"type": {"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
					 "inwardIssue": {"key": "PROJ-3"}},
					{"type": {"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
					 "inwardIssue": {"key": "PROJ-3"}}
				]
			}
		}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok"})
	deps, err := jc.DepsOf("PROJ-11")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 1 || deps[0] != (forge.Dependency{ID: "PROJ-3", Source: forge.DepSourceNative}) {
		t.Errorf("DepsOf = %v, want [PROJ-3 (native)] deduped", deps)
	}
}

// Jira's "Blocks" link type is bidirectional, so the reverse "blocks"
// direction is readable from the same issuelinks payload DepsOf already reads
// (issue #1744).
func TestJiraClient_ImplementsBlockersLister(t *testing.T) {
	if _, ok := jira.NewJiraClient(jira.JiraConfig{}).(forge.BlockersLister); !ok {
		t.Error("jiraClient does not satisfy forge.BlockersLister, want it implemented")
	}
}

// The fixture mirrors TestJiraClient_DepsOf_NativeLinks from the other
// direction: BlocksOf reads the outward "blocks" entries and ignores the
// inward one and any non-Blocks link type.
func TestJiraClient_BlocksOf_NativeLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"key": "PROJ-10",
			"fields": {
				"summary": "s", "description": "",
				"status": {"name": "To Do"}, "labels": [],
				"issuelinks": [
					{"type": {"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
					 "inwardIssue": {"key": "PROJ-3"}},
					{"type": {"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
					 "outwardIssue": {"key": "PROJ-99"}},
					{"type": {"name": "Relates", "inward": "relates to", "outward": "relates to"},
					 "outwardIssue": {"key": "PROJ-55"}}
				]
			}
		}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok"}).(forge.BlockersLister)
	blocks, err := jc.BlocksOf("PROJ-10")
	if err != nil {
		t.Fatalf("BlocksOf: %v", err)
	}
	if len(blocks) != 1 || blocks[0] != (forge.Dependency{ID: "PROJ-99", Source: forge.DepSourceNative}) {
		t.Errorf("BlocksOf = %v, want [PROJ-99 (native)] (native blocks link only)", blocks)
	}
}

// A native-status transition must also strip the stale from-state fallback
// label. ListIssues matches on status OR label, so an issue discovered by
// label would otherwise re-match and re-dispatch forever.
func TestJiraClient_TransitionState_MappedStatus(t *testing.T) {
	var postedTransitionID string
	var labelCleanupOps []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// TransitionState's already-claimed precondition (#3887) GETs the
		// issue before transitioning; report neither the fallback label nor
		// the mapped status so the claim proceeds.
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-1":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"fields":{"status":{"name":"To Do"},"labels":[]}}`))
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
	wantCleanup := []map[string]string{{"remove": "ready-for-agent"}, {"remove": "agent-complete"}, {"remove": "agent-failed"}}
	if !reflect.DeepEqual(labelCleanupOps, wantCleanup) {
		t.Errorf("label cleanup ops = %v, want %v (from label plus stale terminals, no add)", labelCleanupOps, wantCleanup)
	}
}

// A native claim must strip settled labels BEFORE the transition and fail
// cleanly if that strip fails: shutdown.unsettled reads a surviving settled
// label as this run's own settle and would spare the live Box on abort.
func TestJiraClient_TransitionState_NativeClaimStripsSettledFirst(t *testing.T) {
	cases := []struct {
		name         string
		labels       forge.DispatchLabels
		verdicts     forge.VerdictLabels
		issueLabels  string
		failPuts     bool
		noTransition bool
		wantErr      bool
		wantSeq      []string
	}{
		{
			name:        "work stale failed, pre-strip fails",
			labels:      testLabels,
			issueLabels: `["agent-failed"]`,
			failPuts:    true,
			wantErr:     true,
			wantSeq: []string{
				"GET issue",
				`PUT [{"remove":"agent-failed"}]`,
			},
		},
		{
			name:        "research stale verdict, pre-strip fails",
			labels:      researchLabels,
			verdicts:    researchVerdictLabels,
			issueLabels: `["agent-research-unclear"]`,
			failPuts:    true,
			wantErr:     true,
			wantSeq: []string{
				"GET issue",
				`PUT [{"remove":"agent-research-unclear"}]`,
			},
		},
		{
			name:        "no settled label, no pre-strip",
			labels:      testLabels,
			issueLabels: `[]`,
			wantSeq: []string{
				"GET issue",
				"GET transitions",
				"POST transition 11",
				`PUT [{"remove":"ready-for-agent"},{"remove":"agent-complete"},{"remove":"agent-failed"}]`,
			},
		},
		{
			name:        "pre-strip succeeds then transition then cleanup",
			labels:      testLabels,
			issueLabels: `["agent-failed","unrelated"]`,
			wantSeq: []string{
				"GET issue",
				`PUT [{"remove":"agent-failed"}]`,
				"GET transitions",
				"POST transition 11",
				`PUT [{"remove":"ready-for-agent"},{"remove":"agent-complete"},{"remove":"agent-failed"}]`,
			},
		},
		{
			name:         "pre-strip succeeds then transition unavailable falls back to label",
			labels:       testLabels,
			issueLabels:  `["agent-failed"]`,
			noTransition: true,
			wantSeq: []string{
				"GET issue",
				`PUT [{"remove":"agent-failed"}]`,
				"GET transitions",
				`PUT [{"remove":"ready-for-agent"},{"remove":"agent-complete"},{"remove":"agent-failed"},{"add":"agent-in-progress"}]`,
			},
		},
		{
			name:        "post-transition cleanup fails, claim still succeeds",
			labels:      testLabels,
			issueLabels: `[]`,
			failPuts:    true,
			wantSeq: []string{
				"GET issue",
				"GET transitions",
				"POST transition 11",
				`PUT [{"remove":"ready-for-agent"},{"remove":"agent-complete"},{"remove":"agent-failed"}]`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seq []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-1":
					seq = append(seq, "GET issue")
					fmt.Fprintf(w, `{"fields":{"status":{"name":"To Do"},"labels":%s}}`, tc.issueLabels)
				case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-1/transitions":
					seq = append(seq, "GET transitions")
					if tc.noTransition {
						fmt.Fprint(w, `{"transitions":[]}`)
						return
					}
					fmt.Fprint(w, `{"transitions":[{"id":"11","name":"Start","to":{"name":"In Progress"}}]}`)
				case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue/PROJ-1/transitions":
					var body map[string]map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					seq = append(seq, "POST transition "+body["transition"]["id"])
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/PROJ-1":
					var body struct {
						Update struct {
							Labels json.RawMessage `json:"labels"`
						} `json:"update"`
					}
					json.NewDecoder(r.Body).Decode(&body)
					seq = append(seq, "PUT "+string(body.Update.Labels))
					if tc.failPuts {
						w.WriteHeader(http.StatusInternalServerError)
						return
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
				StatusMapping: map[forge.DispatchState]string{forge.InProgress: "In Progress"},
				Labels:        tc.labels,
				VerdictLabels: tc.verdicts,
			})

			err := jc.TransitionState("PROJ-1", forge.Dispatchable, forge.InProgress)
			if (err != nil) != tc.wantErr {
				t.Fatalf("TransitionState err = %v, wantErr %v", err, tc.wantErr)
			}
			// The REST client retries a 5xx (real backoff, ~600ms per failing
			// case), so collapse repeats of a failing PUT. Only then: on the
			// success path a doubled PUT is a regression to catch.
			if tc.failPuts {
				seq = slices.Compact(seq)
			}
			if !reflect.DeepEqual(seq, tc.wantSeq) {
				t.Errorf("request sequence:\n got %q\nwant %q", seq, tc.wantSeq)
			}
		})
	}
}

// With no status mapping for the target state, the label swap keeps the
// lifecycle moving.
func TestJiraClient_TransitionState_UnmappedFallsBackToLabel(t *testing.T) {
	var gotLabelOps []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// TransitionState's already-claimed precondition (#3887) GETs the
		// issue before transitioning, even in fallback-label mode.
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-2":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"fields":{"status":{"name":"To Do"},"labels":[]}}`))
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
	want := []map[string]string{{"remove": "ready-for-agent"}, {"remove": "agent-complete"}, {"remove": "agent-failed"}, {"add": "agent-in-progress"}}
	if !reflect.DeepEqual(gotLabelOps, want) {
		t.Errorf("label ops = %v, want %v", gotLabelOps, want)
	}
}

// A mapped transition the issue's current workflow does not offer also falls
// back to the label swap.
func TestJiraClient_TransitionState_BlockedFallsBackToLabel(t *testing.T) {
	var labelSwapped bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// TransitionState's already-claimed precondition (#3887).
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-9":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"fields":{"status":{"name":"To Do"},"labels":[]}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-9/transitions":
			// Only an irrelevant transition is offered, so "In Progress" is blocked.
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

// The label-swap fallback is for an unmapped or blocked workflow transition,
// not for infra errors, so a 500 listing transitions must surface as an error
// instead of being swallowed into a swap.
func TestJiraClient_TransitionState_InfraErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// TransitionState's already-claimed precondition (#3887).
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-5":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"fields":{"status":{"name":"To Do"},"labels":[]}}`))
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

// researchLabels and researchVerdictLabels mirror ResearchDispatchLabels and
// ResearchVerdictLabels so the research-kind tests below do not restate the
// label strings.
var (
	researchLabels        = forge.ResearchDispatchLabels()
	researchVerdictLabels = forge.ResearchVerdictLabels()
)

// No jira workflow-status mapping exists for research verdicts (ADR 0022), so
// CompleteVerdict uses the same swapLabel fallback TransitionState takes when
// a state is unmapped.
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
			case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-3":
				fmt.Fprint(w, `{"key":"PROJ-3","fields":{"labels":["agent-research-in-progress"]}}`)
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

// The work-kind construction path leaves VerdictLabels unset. CompleteVerdict
// must then error rather than send a Jira request with an empty label, matching
// the github adapter's guard.
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

// The retry gesture after a verdict terminal is TransitionState(Untriaged,
// Dispatchable). Untriaged has no "from" label, so that swap is add-only.
func TestJiraClient_CompleteVerdict_ThenRetryResearchable(t *testing.T) {
	var completeOps, retryOps []map[string]string
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-5":
			fmt.Fprint(w, `{"key":"PROJ-5","fields":{"labels":["agent-research-in-progress"]}}`)
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

// Failed strictly means the Box crashed or produced no verdict (ADR 0022),
// never a concluded verdict, so its label must stay distinct from every
// verdict terminal.
func TestJiraClient_ResearchDispatch_InProgressAndFailedUseResearchLabels(t *testing.T) {
	var ops []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// TransitionState's already-claimed precondition (#3887) GETs the
		// issue before a claim (PROJ-6, Dispatchable->InProgress); PROJ-7's
		// InProgress->Failed transition below never lands here.
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/api/2/issue/"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"fields":{"status":{"name":"To Do"},"labels":[]}}`))
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/rest/api/2/issue/"):
			var body struct {
				Update struct {
					Labels []map[string]string `json:"labels"`
				} `json:"update"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			ops = append(ops, body.Update.Labels...)
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

	if err := jc.TransitionState("PROJ-6", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState(Dispatchable, InProgress): %v", err)
	}
	if err := jc.TransitionState("PROJ-7", forge.InProgress, forge.Failed); err != nil {
		t.Fatalf("TransitionState(InProgress, Failed): %v", err)
	}

	wantAdds := map[string]bool{"agent-research-in-progress": true, "agent-research-failed": true}
	gotAdds := map[string]bool{}
	for _, op := range ops {
		if add, ok := op["add"]; ok && add != "" {
			gotAdds[add] = true
		}
	}
	if !reflect.DeepEqual(gotAdds, wantAdds) {
		t.Fatalf("added labels = %v, want %v", gotAdds, wantAdds)
	}
	terminals := []string{"agent-research-failed", researchVerdictLabels.Label(forge.Recommend), researchVerdictLabels.Label(forge.Reject), researchVerdictLabels.Label(forge.Unclear)}
	seen := map[string]bool{}
	for _, l := range terminals {
		if seen[l] {
			t.Fatalf("terminal label %q collides with another terminal: %v", l, terminals)
		}
		seen[l] = true
	}
}

// The order assertion below trusts the server's created-ascending ordering:
// ListIssues asks for it in the JQL and never re-sorts client side.
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

// ResultPageLimit is a per-request page size, not a cap on the backlog:
// doSearch walks every page through forge.WalkPages. The github adapter shares
// the same limit.
func TestJiraClient_ListIssues_PageSizeIsResultPageLimit(t *testing.T) {
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

// An issue closed in Jira while still carrying a stale dispatch label from a
// prior label-fallback transition must not be re-dispatched. This mirrors the
// github adapter's --state open.
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

// Unlike ListIssues, ListOpenIssues returns every open issue whatever its
// dispatch state, so its JQL carries no status or label clause.
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

// Pages merge in fetch order because the JQL already orders server-side, and
// the walk must stop at the last page rather than request one past it.
func TestJiraClient_ListIssues_WalksAllPages(t *testing.T) {
	var gotStartAt, gotMaxResults []string
	requests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		startAt := r.URL.Query().Get("startAt")
		maxResults := r.URL.Query().Get("maxResults")
		gotStartAt = append(gotStartAt, startAt)
		gotMaxResults = append(gotMaxResults, maxResults)

		w.WriteHeader(http.StatusOK)
		switch startAt {
		case "0":
			w.Write([]byte(`{
				"startAt": 0, "maxResults": 100, "total": 3,
				"issues": [
					{"key": "PROJ-1", "fields": {"summary": "first", "status": {"name": "To Do"}, "labels": []}},
					{"key": "PROJ-2", "fields": {"summary": "second", "status": {"name": "To Do"}, "labels": []}}
				]
			}`))
		case "2":
			w.Write([]byte(`{
				"startAt": 2, "maxResults": 100, "total": 3,
				"issues": [
					{"key": "PROJ-3", "fields": {"summary": "third", "status": {"name": "To Do"}, "labels": []}}
				]
			}`))
		default:
			t.Errorf("unexpected startAt %q; want no request beyond the last page", startAt)
			w.Write([]byte(`{"issues": []}`))
		}
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

	if requests != 2 {
		t.Fatalf("server received %d requests, want exactly 2", requests)
	}
	if len(issues) != 3 || issues[0].Number != "PROJ-1" || issues[1].Number != "PROJ-2" || issues[2].Number != "PROJ-3" {
		t.Fatalf("issues = %+v, want PROJ-1, PROJ-2, PROJ-3 in fetch order", issues)
	}
	wantStartAt := []string{"0", "2"}
	if len(gotStartAt) != len(wantStartAt) || gotStartAt[0] != wantStartAt[0] || gotStartAt[1] != wantStartAt[1] {
		t.Errorf("startAt per request = %v, want %v", gotStartAt, wantStartAt)
	}
	for _, mr := range gotMaxResults {
		if mr != "100" {
			t.Errorf("maxResults per request = %v, want every request to send 100", gotMaxResults)
		}
	}
}

// An empty page before total must fail loudly rather than return a short
// result under WalksAllPages' completeness promise.
func TestJiraClient_ListIssues_EmptyPageBeforeTotalErrors(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 5 {
			t.Errorf("walk issued %d requests, want it bounded", requests)
			w.Write([]byte(`{"issues": [], "total": 0}`))
			return
		}
		if r.URL.Query().Get("startAt") == "0" {
			w.Write([]byte(`{"total": 5, "issues": [
				{"key": "PROJ-1", "fields": {"summary": "first", "status": {"name": "To Do"}, "labels": []}},
				{"key": "PROJ-2", "fields": {"summary": "second", "status": {"name": "To Do"}, "labels": []}}
			]}`))
			return
		}
		w.Write([]byte(`{"total": 5, "issues": []}`))
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
	if err == nil {
		t.Fatalf("ListIssues = %+v, nil error; want error for empty page before total", issues)
	}
	if !strings.Contains(err.Error(), "startAt 2") || !strings.Contains(err.Error(), "total 5") {
		t.Errorf("err = %q, want it to name startAt 2 and total 5", err)
	}
	if requests != 2 {
		t.Errorf("server received %d requests, want 2", requests)
	}
}

// A server that ignores startAt replays the same page; the walk must error
// rather than append duplicates until total is reached.
func TestJiraClient_ListIssues_RepeatedPageErrors(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 5 {
			t.Errorf("walk issued %d requests, want it bounded", requests)
			w.Write([]byte(`{"issues": [], "total": 0}`))
			return
		}
		w.Write([]byte(`{"total": 5, "issues": [
			{"key": "PROJ-1", "fields": {"summary": "first", "status": {"name": "To Do"}, "labels": []}},
			{"key": "PROJ-2", "fields": {"summary": "second", "status": {"name": "To Do"}, "labels": []}}
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
	if err == nil {
		t.Fatalf("ListIssues = %+v, nil error; want error for a repeated page", issues)
	}
	if !strings.Contains(err.Error(), "PROJ-1") {
		t.Errorf("err = %q, want it to name the repeated key PROJ-1", err)
	}
	if requests != 2 {
		t.Errorf("server received %d requests, want 2", requests)
	}
}

func TestJiraClient_ImplementsLabeledBacklogLister(t *testing.T) {
	if _, ok := jira.NewJiraClient(jira.JiraConfig{}).(forge.LabeledBacklogLister); !ok {
		t.Error("jiraClient does not satisfy forge.LabeledBacklogLister, want it implemented")
	}
}

// ListIssuesWithLabels covers every label in one "labels in (...)" JQL clause,
// unlike github's and forgejo's per-label calls (issue #3873).
func TestJiraClient_ListIssuesWithLabels_JQLAndOrder(t *testing.T) {
	var gotJQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotJQL = r.URL.Query().Get("jql")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"issues": [
			{"key": "PROJ-9", "fields": {"summary": "newer", "status": {"name": "Done"}, "labels": ["agent-research-finding"]}},
			{"key": "PROJ-5", "fields": {"summary": "older", "status": {"name": "Done"}, "labels": ["agent-review-finding"]}}
		]}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok", ProjectKey: "PROJ", Labels: testLabels}).(forge.LabeledBacklogLister)
	issues, err := jc.ListIssuesWithLabels(forge.IssueClosed, []string{"agent-review-finding", "agent-research-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: %v", err)
	}
	if len(issues) != 2 || issues[0].Number != "PROJ-9" || issues[1].Number != "PROJ-5" {
		t.Fatalf("issues = %+v, want fetch order [PROJ-9, PROJ-5]", issues)
	}
	if !strings.Contains(gotJQL, `project = "PROJ"`) {
		t.Errorf("jql = %q, want project scope", gotJQL)
	}
	if !strings.Contains(gotJQL, `labels in ("agent-review-finding", "agent-research-finding")`) {
		t.Errorf("jql = %q, want a labels-in clause covering both labels", gotJQL)
	}
	if !strings.Contains(gotJQL, "statusCategory = Done") {
		t.Errorf("jql = %q, want a Done statusCategory clause for IssueClosed", gotJQL)
	}
	if !strings.Contains(gotJQL, "order by created desc") {
		t.Errorf("jql = %q, want newest-first order", gotJQL)
	}
}

// forge.IssueOpen must exclude Done issues, the open half of the dedup scan
// (issue #3873).
func TestJiraClient_ListIssuesWithLabels_OpenStateExcludesDone(t *testing.T) {
	var gotJQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotJQL = r.URL.Query().Get("jql")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"issues": []}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok", ProjectKey: "PROJ", Labels: testLabels}).(forge.LabeledBacklogLister)
	if _, err := jc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding"}); err != nil {
		t.Fatalf("ListIssuesWithLabels: %v", err)
	}
	if !strings.Contains(gotJQL, "statusCategory != Done") {
		t.Errorf("jql = %q, want statusCategory != Done for IssueOpen", gotJQL)
	}
}

// A state outside forge.IssueOpen/forge.IssueClosed must error before any
// search request runs.
func TestJiraClient_ListIssuesWithLabels_UnsupportedStateErrorsWithoutRequest(t *testing.T) {
	requested := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"issues": []}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok", ProjectKey: "PROJ", Labels: testLabels}).(forge.LabeledBacklogLister)
	if _, err := jc.ListIssuesWithLabels(forge.IssueMerged, []string{"agent-review-finding"}); err == nil {
		t.Fatal("ListIssuesWithLabels(IssueMerged): want error, got nil")
	}
	if requested {
		t.Error("ListIssuesWithLabels(IssueMerged): want no search request, got one")
	}
}

// No labels is the doctor-advisory-label-missing edge folded to zero inputs:
// no search request, no failure, an empty result.
func TestJiraClient_ListIssuesWithLabels_NoLabelsReturnsEmptyWithoutRequest(t *testing.T) {
	requested := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"issues": []}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Token: "tok", ProjectKey: "PROJ", Labels: testLabels}).(forge.LabeledBacklogLister)
	issues, err := jc.ListIssuesWithLabels(forge.IssueOpen, nil)
	if err != nil {
		t.Fatalf("ListIssuesWithLabels(nil): %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("want empty result, got %+v", issues)
	}
	if requested {
		t.Error("ListIssuesWithLabels(nil): want no search request, got one")
	}
}

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

// Jira has no label-registration endpoint; labels are free text created on
// first use, so CreateLabel must issue no request and still return nil.
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

// Cloud removed v2 /search, so doSearch walks /search/jql: it must name its
// fields (Cloud returns only the id otherwise) and hand each page's
// nextPageToken back, stopping at isLast.
func TestJiraClient_ListIssues_CloudWalksSearchJQLByToken(t *testing.T) {
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/search/jql" {
			t.Errorf("path = %q, want /rest/api/2/search/jql", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("fields"); got != "summary,description,status,labels" {
			t.Errorf("fields = %q, want summary,description,status,labels", got)
		}
		tokens = append(tokens, q.Get("nextPageToken"))
		if q.Get("nextPageToken") == "" {
			w.Write([]byte(`{"issues":[{"key":"PROJ-1","fields":{"summary":"a"}}],"nextPageToken":"tok-2","isLast":false}`))
			return
		}
		w.Write([]byte(`{"issues":[{"key":"PROJ-2","fields":{"summary":"b"}}],"isLast":true}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Email: "bot@example.com", Token: "tok", ProjectKey: "PROJ", Labels: testLabels})
	issues, err := jc.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 2 || issues[0].Number != "PROJ-1" || issues[1].Number != "PROJ-2" {
		t.Errorf("issues = %+v, want PROJ-1 then PROJ-2", issues)
	}
	if want := []string{"", "tok-2"}; !slices.Equal(tokens, want) {
		t.Errorf("nextPageToken per request = %q, want %q", tokens, want)
	}
}

func TestJiraClient_ListIssues_CloudFailsOnRepeatedNextPageToken(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		// Cap the stub so a missing guard fails the test instead of hanging it.
		if requests > 5 {
			w.Write([]byte(`{"issues":[],"isLast":true}`))
			return
		}
		w.Write([]byte(`{"issues":[{"key":"PROJ-1","fields":{"summary":"a"}}],"nextPageToken":"same","isLast":false}`))
	}))
	defer srv.Close()

	jc := jira.NewJiraClient(jira.JiraConfig{BaseURL: srv.URL, Email: "bot@example.com", Token: "tok", ProjectKey: "PROJ", Labels: testLabels})
	_, err := jc.ListIssues(forge.Dispatchable)
	if err == nil {
		t.Fatalf("ListIssues error = nil after %d requests, want a repeated-nextPageToken error", requests)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2 (first page, then the repeat)", requests)
	}
}

// A landing in label-fallback mode removes the from label and a stale
// agent-failed in one update, so a recovered issue parked agent-failed ends
// wearing only agent-complete (#4651).
func TestJiraClient_TransitionState_CompleteFallbackStripsStaleFailed(t *testing.T) {
	var gotLabelOps []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/PROJ-8":
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
		BaseURL: srv.URL,
		Token:   "tok",
		Labels:  testLabels,
	})

	if err := jc.TransitionState("PROJ-8", forge.InProgress, forge.Complete); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}
	want := []map[string]string{{"remove": "agent-in-progress"}, {"remove": "agent-failed"}, {"add": "agent-complete"}}
	if !reflect.DeepEqual(gotLabelOps, want) {
		t.Errorf("label ops = %v, want %v", gotLabelOps, want)
	}
}
