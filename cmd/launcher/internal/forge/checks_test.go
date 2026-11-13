package forge

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestMissingStatuses(t *testing.T) {
	tests := []struct {
		name         string
		mapped, have []string
		want         []string
	}{
		{"all present", []string{"Done"}, []string{"Done", "To Do"}, nil},
		{"case-insensitive", []string{"DONE"}, []string{"done"}, nil},
		{"sorted", []string{"Zed", "Alpha"}, nil, []string{"Alpha", "Zed"}},
		{"case-only duplicate once, sorted spelling", []string{"b", "B"}, nil, []string{"B"}},
		{"empty", nil, []string{"Done"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MissingStatuses(tc.mapped, tc.have); !slices.Equal(got, tc.want) {
				t.Errorf("MissingStatuses = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMissingStatusesDoesNotReorderMapped(t *testing.T) {
	mapped := []string{"b", "a"}
	MissingStatuses(mapped, nil)
	if mapped[0] != "b" {
		t.Errorf("mapped reordered: %v", mapped)
	}
}

func TestIssueTypeNotOffered(t *testing.T) {
	if err := IssueTypeNotOffered("PROJ", "Task", []string{"Bug", "Task"}); err != nil {
		t.Errorf("offered type: %v", err)
	}
	err := IssueTypeNotOffered("PROJ", "Task", []string{"Bug", "Aufgabe"})
	if !errors.Is(err, ErrUnknownIssueType) {
		t.Fatalf("err = %v, want ErrUnknownIssueType", err)
	}
	for _, want := range []string{`JIRA_ISSUE_TYPE "Task"`, "project PROJ", "offered: Bug, Aufgabe;"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestUnfilledCreateFields(t *testing.T) {
	fields := []CreateField{
		{ID: "summary", Name: "Summary", Required: true},
		{ID: "reporter", Name: "Reporter", Required: true},
		{ID: "customfield_1", Name: "Team", Required: true},
		{ID: "customfield_2", Name: "Sprint", Required: true, HasDefault: true},
		{ID: "customfield_3", Name: "Notes"},
	}
	want := []string{`"Team" (customfield_1)`}
	if got := UnfilledCreateFields(fields); !slices.Equal(got, want) {
		t.Errorf("UnfilledCreateFields = %v, want %v", got, want)
	}
}
