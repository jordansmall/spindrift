package jira_test

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge/jira"
)

// TestValidateJiraEnv_RequiresBaseURLProjectKeyToken verifies ValidateJiraEnv
// fails fast when any of the three required Jira connection fields is empty,
// rather than deferring to a runtime Jira API error.
func TestValidateJiraEnv_RequiresBaseURLProjectKeyToken(t *testing.T) {
	if err := jira.ValidateJiraEnv("https://example.atlassian.net", "PROJ", "tok", "", "hybrid"); err != nil {
		t.Fatalf("fully configured jira env should validate: %v", err)
	}
	if err := jira.ValidateJiraEnv("", "PROJ", "tok", "", "hybrid"); err == nil || !strings.Contains(err.Error(), "JIRA_BASE_URL") {
		t.Errorf("ValidateJiraEnv should require JIRA_BASE_URL, got: %v", err)
	}
	if err := jira.ValidateJiraEnv("https://example.atlassian.net", "", "tok", "", "hybrid"); err == nil || !strings.Contains(err.Error(), "JIRA_PROJECT_KEY") {
		t.Errorf("ValidateJiraEnv should require JIRA_PROJECT_KEY, got: %v", err)
	}
	if err := jira.ValidateJiraEnv("https://example.atlassian.net", "PROJ", "", "", "hybrid"); err == nil || !strings.Contains(err.Error(), "JIRA_TOKEN") {
		t.Errorf("ValidateJiraEnv should require JIRA_TOKEN, got: %v", err)
	}
}

// A status-mapping share other than failed+ambiguous fails startup, naming both
// keys; failed and ambiguous may share one status.
func TestValidateJiraEnv_StatusMappingSharing(t *testing.T) {
	const url, proj, tok = "https://example.atlassian.net", "PROJ", "tok"
	err := jira.ValidateJiraEnv(url, proj, tok, `{"dispatchable":"To Do","inProgress":"to do"}`, "hybrid")
	if err == nil || !strings.Contains(err.Error(), "dispatchable") || !strings.Contains(err.Error(), "inProgress") {
		t.Errorf("want an error naming dispatchable and inProgress, got: %v", err)
	}
	if err := jira.ValidateJiraEnv(url, proj, tok, `{"failed":"Blocked","ambiguous":"Blocked"}`, "hybrid"); err != nil {
		t.Errorf("failed and ambiguous may share a status, got: %v", err)
	}
}

// status mode requires every work state mapped; an unknown mode is rejected.
func TestValidateJiraEnv_Lifecycle(t *testing.T) {
	const url, proj, tok = "https://example.atlassian.net", "PROJ", "tok"
	err := jira.ValidateJiraEnv(url, proj, tok, `{"dispatchable":"To Do"}`, "status")
	if err == nil || !strings.Contains(err.Error(), "unmapped: ambiguous, complete, failed, inProgress") {
		t.Errorf("want an error naming the unmapped states, got: %v", err)
	}
	full := `{"dispatchable":"To Do","inProgress":"In Progress","complete":"Done","failed":"Blocked","ambiguous":"Needs Spec"}`
	if err := jira.ValidateJiraEnv(url, proj, tok, full, "status"); err != nil {
		t.Errorf("fully mapped status mode should validate: %v", err)
	}
	if err := jira.ValidateJiraEnv(url, proj, tok, "", "bogus"); err == nil || !strings.Contains(err.Error(), "JIRA_LIFECYCLE") {
		t.Errorf("want a JIRA_LIFECYCLE error for an unknown mode, got: %v", err)
	}
}
