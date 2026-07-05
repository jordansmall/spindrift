package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// With ISSUE_NUMBER set, discovery must target exactly that issue — never a
// different one that happens to share the in-progress label (e.g. a run
// stranded by an earlier crash).
func TestDiscoverIssues_ByNumber(t *testing.T) {
	c := baseConfig()
	c.label = c.inProgressLabel
	c.issueNumber = "152"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "152", Title: "the claimed one", Labels: []string{c.inProgressLabel}})
	fc.SetIssue(forge.Issue{Number: "99", Title: "a stranded run", Labels: []string{c.inProgressLabel}})

	issues, err := discoverIssues(c, fc)
	if err != nil {
		t.Fatalf("discoverIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected exactly one issue, got %+v", issues)
	}
	if issues[0].number != "152" || issues[0].title != "the claimed one" {
		t.Errorf("got %+v, want {number:152 title:the claimed one}", issues[0])
	}
}

// Without ISSUE_NUMBER, discovery falls back to querying every open issue that
// carries the discovery label.
func TestDiscoverIssues_ByLabel(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Title: "ready", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "2", Title: "not ready", Labels: []string{"backlog"}})

	issues, err := discoverIssues(c, fc)
	if err != nil {
		t.Fatalf("discoverIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].number != "1" {
		t.Fatalf("expected only issue #1 by label, got %+v", issues)
	}
}
