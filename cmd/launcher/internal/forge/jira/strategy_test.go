package jira_test

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
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

func TestStrategyKnobErrorSurfaces(t *testing.T) {
	l := func(k string) (string, bool) {
		if k == "JIRA_INCLUDE_COMMENTS" {
			return "notabool", true
		}
		return "", false
	}
	_, err := jira.Strategy.NewIssueTracker(backendkit.Shared{}, l)
	if err == nil || !strings.Contains(err.Error(), "JIRA_INCLUDE_COMMENTS") {
		t.Errorf("err = %v, want one naming JIRA_INCLUDE_COMMENTS", err)
	}
}
