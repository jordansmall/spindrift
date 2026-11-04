package jira_test

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/jira"
)

func TestStrategyShape(t *testing.T) {
	s := jira.Strategy
	if !reflect.DeepEqual(s.Descriptor, backend.Jira) {
		t.Errorf("Descriptor = %+v, want backend.Jira", s.Descriptor)
	}
	if s.ValidateTracker == nil || s.NewIssueTracker == nil {
		t.Error("jira must fill ValidateTracker and NewIssueTracker")
	}
	if s.ValidateCodeForge != nil || s.NewCodeForge != nil ||
		s.NewReadOnlyCodeForge != nil || s.NewLedger != nil {
		t.Error("jira is a tracker-only backend")
	}
}

// TestStrategyNewIssueTracker_JiraIssueTypeReachesPostIssue proves the
// JIRA_ISSUE_TYPE knob flows through ReadKnobs into the client PostIssue
// creates with; the harness accepts Task and Bug and rejects Epic.
func TestStrategyNewIssueTracker_JiraIssueTypeReachesPostIssue(t *testing.T) {
	h := newJiraHarness(t)
	for _, tc := range []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{"unset defaults to Task", nil, false},
		{"Bug", map[string]string{"JIRA_ISSUE_TYPE": "Bug"}, false},
		{"Epic is rejected by the project", map[string]string{"JIRA_ISSUE_TYPE": "Epic"}, true},
	} {
		env := map[string]string{"JIRA_BASE_URL": h.srv.URL, "JIRA_PROJECT_KEY": "PROJ", "JIRA_TOKEN": "tok"}
		for k, v := range tc.env {
			env[k] = v
		}
		tr, err := jira.Strategy.NewIssueTracker(backendkit.Shared{}, func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		})
		if err != nil {
			t.Fatalf("%s: NewIssueTracker: %v", tc.name, err)
		}
		_, err = tr.(forge.HostPostedIssueFiler).PostIssue("t", "b", nil)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: PostIssue err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}
