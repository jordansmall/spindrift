package jira_test

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge/jira"
)

// A missing connection field fails at startup, not at the first Jira API call.
func TestValidateJiraEnv_RequiresBaseURLAndProjectKey(t *testing.T) {
	t.Parallel()
	if err := jira.ValidateJiraEnv("https://example.atlassian.net", "PROJ", "", "hybrid"); err != nil {
		t.Fatalf("fully configured jira env should validate: %v", err)
	}
	if err := jira.ValidateJiraEnv("", "PROJ", "", "hybrid"); err == nil || !strings.Contains(err.Error(), "JIRA_BASE_URL") {
		t.Errorf("ValidateJiraEnv should require JIRA_BASE_URL, got: %v", err)
	}
	if err := jira.ValidateJiraEnv("https://example.atlassian.net", "", "", "hybrid"); err == nil || !strings.Contains(err.Error(), "JIRA_PROJECT_KEY") {
		t.Errorf("ValidateJiraEnv should require JIRA_PROJECT_KEY, got: %v", err)
	}
}

// A status-mapping share other than failed+ambiguous fails startup, naming both
// keys; failed and ambiguous may share one status.
func TestValidateJiraEnv_StatusMappingSharing(t *testing.T) {
	t.Parallel()
	const url, proj = "https://example.atlassian.net", "PROJ"
	err := jira.ValidateJiraEnv(url, proj, `{"dispatchable":"To Do","inProgress":"to do"}`, "hybrid")
	if err == nil || !strings.Contains(err.Error(), "dispatchable") || !strings.Contains(err.Error(), "inProgress") {
		t.Errorf("want an error naming dispatchable and inProgress, got: %v", err)
	}
	if err := jira.ValidateJiraEnv(url, proj, `{"failed":"Blocked","ambiguous":"Blocked"}`, "hybrid"); err != nil {
		t.Errorf("failed and ambiguous may share a status, got: %v", err)
	}
}

// status mode requires every work state mapped; an unknown mode is rejected.
func TestValidateJiraEnv_Lifecycle(t *testing.T) {
	t.Parallel()
	const url, proj = "https://example.atlassian.net", "PROJ"
	err := jira.ValidateJiraEnv(url, proj, `{"dispatchable":"To Do"}`, "status")
	if err == nil || !strings.Contains(err.Error(), "unmapped: ambiguous, complete, failed, inProgress") {
		t.Errorf("want an error naming the unmapped states, got: %v", err)
	}
	full := `{"dispatchable":"To Do","inProgress":"In Progress","complete":"Done","failed":"Blocked","ambiguous":"Needs Spec"}`
	if err := jira.ValidateJiraEnv(url, proj, full, "status"); err != nil {
		t.Errorf("fully mapped status mode should validate: %v", err)
	}
	if err := jira.ValidateJiraEnv(url, proj, "", "bogus"); err == nil || !strings.Contains(err.Error(), "JIRA_LIFECYCLE") {
		t.Errorf("want a JIRA_LIFECYCLE error for an unknown mode, got: %v", err)
	}
}
