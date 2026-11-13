package jira_test

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/dispatchkind"
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

// connectorLifecycle builds the Connector's lifecycle from jira.WorkLifecycle
// plus the labels and issue type. It lives in the test package because
// forge/jira is linked into the Box, which must not pull in the Connector host.
func connectorLifecycle(t dispatchkind.Tracker, labels forge.DispatchLabels, verdicts forge.VerdictLabels, mapping, lifecycle, issueType string) (connectortracker.Lifecycle, error) {
	wl, err := jira.WorkLifecycle(t, mapping, lifecycle)
	if err != nil {
		return connectortracker.Lifecycle{}, err
	}
	return connectortracker.Lifecycle{
		Labels:        labels,
		VerdictLabels: verdicts,
		IssueType:     issueType,
		Statuses:      wl.Statuses,
		Resolutions:   wl.Resolutions,
		StatusOnly:    wl.StatusOnly,
	}, nil
}

func TestConnectorLifecycle(t *testing.T) {
	const mapping = `{"dispatchable":"To Do","inProgress":"In Progress","complete":{"status":"Done","resolution":"Fixed"}}`
	labels := forge.DispatchLabels{Dispatchable: "d", InProgress: "p"}
	verdicts := forge.ResearchVerdictLabels()

	work, err := connectorLifecycle(dispatchkind.TrackerWork, labels, verdicts, mapping, jira.LifecycleStatus, "Task")
	if err != nil {
		t.Fatal(err)
	}
	wantStatuses := map[forge.DispatchState]string{forge.Dispatchable: "To Do", forge.InProgress: "In Progress", forge.Complete: "Done"}
	if !reflect.DeepEqual(work.Statuses, wantStatuses) {
		t.Errorf("work Statuses = %v, want %v", work.Statuses, wantStatuses)
	}
	if want := map[forge.DispatchState]string{forge.Complete: "Fixed"}; !reflect.DeepEqual(work.Resolutions, want) {
		t.Errorf("work Resolutions = %v, want %v", work.Resolutions, want)
	}
	if !work.StatusOnly || work.IssueType != "Task" || work.Labels != labels || !reflect.DeepEqual(work.VerdictLabels, verdicts) {
		t.Errorf("work lifecycle = %+v", work)
	}

	// Research verdicts ride labels in either lifecycle (ADR 0072).
	research, err := connectorLifecycle(dispatchkind.TrackerResearch, labels, verdicts, mapping, jira.LifecycleStatus, "Task")
	if err != nil {
		t.Fatal(err)
	}
	if len(research.Statuses) != 0 || len(research.Resolutions) != 0 || research.StatusOnly {
		t.Errorf("research lifecycle = %+v, want label-only", research)
	}

	hybrid, err := connectorLifecycle(dispatchkind.TrackerWork, labels, verdicts, mapping, jira.LifecycleHybrid, "")
	if err != nil {
		t.Fatal(err)
	}
	if hybrid.StatusOnly || len(hybrid.Statuses) != 3 {
		t.Errorf("hybrid lifecycle = %+v", hybrid)
	}

	if _, err := connectorLifecycle(dispatchkind.TrackerWork, labels, verdicts, `{"nope":"x"}`, jira.LifecycleHybrid, ""); err == nil {
		t.Error("an invalid mapping must be refused")
	}
}
