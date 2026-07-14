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
	if err := jira.ValidateJiraEnv("https://example.atlassian.net", "PROJ", "tok", ""); err != nil {
		t.Fatalf("fully configured jira env should validate: %v", err)
	}
	if err := jira.ValidateJiraEnv("", "PROJ", "tok", ""); err == nil || !strings.Contains(err.Error(), "JIRA_BASE_URL") {
		t.Errorf("ValidateJiraEnv should require JIRA_BASE_URL, got: %v", err)
	}
	if err := jira.ValidateJiraEnv("https://example.atlassian.net", "", "tok", ""); err == nil || !strings.Contains(err.Error(), "JIRA_PROJECT_KEY") {
		t.Errorf("ValidateJiraEnv should require JIRA_PROJECT_KEY, got: %v", err)
	}
	if err := jira.ValidateJiraEnv("https://example.atlassian.net", "PROJ", "", ""); err == nil || !strings.Contains(err.Error(), "JIRA_TOKEN") {
		t.Errorf("ValidateJiraEnv should require JIRA_TOKEN, got: %v", err)
	}
}
