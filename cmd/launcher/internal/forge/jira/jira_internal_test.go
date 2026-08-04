package jira

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestJiraAuthStrategy_Basic verifies Apply sets the Basic Authorization
// header when Email is set: "Basic " + base64("email:token").
func TestJiraAuthStrategy_Basic(t *testing.T) {
	a := jiraAuthStrategy{email: "user@example.com", token: "tok"}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	a.Apply(req)

	want := "Basic dXNlckBleGFtcGxlLmNvbTp0b2s="
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
}

// TestJiraAuthStrategy_Bearer verifies Apply sets the Bearer Authorization
// header when Email is empty.
func TestJiraAuthStrategy_Bearer(t *testing.T) {
	a := jiraAuthStrategy{token: "tok"}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	a.Apply(req)

	want := "Bearer tok"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
}

// TestJiraStatusMap verifies jiraStatusMap's per-status sentinel mapping,
// preserving the semantics jira.go's existing status-branch sites (Probe's
// 401/403 -> forge.ErrAuthFailure) already apply, and adding the generic
// per-resource 404 -> forge.ErrNotFound sentinel.
func TestJiraStatusMap(t *testing.T) {
	m := jiraStatusMap()
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, forge.ErrAuthFailure},
		{http.StatusForbidden, forge.ErrAuthFailure},
		{http.StatusNotFound, forge.ErrNotFound},
	}
	for _, tc := range cases {
		got, ok := m[tc.status]
		if !ok {
			t.Errorf("jiraStatusMap()[%d]: missing entry, want %v", tc.status, tc.want)
			continue
		}
		if !errors.Is(got, tc.want) {
			t.Errorf("jiraStatusMap()[%d] = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// TestJiraStatusMap_NoOtherEntries guards against silently widening the
// table beyond the statuses jira.go's status-branch sites actually map to a
// sentinel today.
func TestJiraStatusMap_NoOtherEntries(t *testing.T) {
	m := jiraStatusMap()
	if len(m) != 3 {
		t.Fatalf("jiraStatusMap() has %d entries, want 3 (401, 403, 404): %v", len(m), m)
	}
}

// TestNewJiraClient_BuildsRESTClient asserts NewJiraClient populates the
// jiraClient's rest field (issue #2264's migration seam).
func TestNewJiraClient_BuildsRESTClient(t *testing.T) {
	tracker := NewJiraClient(JiraConfig{BaseURL: "https://jira.example.test", Token: "tok"})
	jc, ok := tracker.(*jiraClient)
	if !ok {
		t.Fatalf("NewJiraClient(...) = %T, want *jiraClient", tracker)
	}
	if jc.rest == nil {
		t.Fatal("jiraClient.rest is nil, want a constructed *rest.Client")
	}
}
