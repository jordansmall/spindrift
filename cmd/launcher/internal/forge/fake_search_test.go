package forge

import (
	"testing"

	"spindrift.dev/launcher/internal/backend"
)

func TestFake_SearchIssues_FiltersStateLabelTextAndCaps(t *testing.T) {
	f := NewFake()
	f.SetIssue(Issue{Number: "1", Title: "Flaky Login test", Labels: []string{"bug"}})
	f.SetIssue(Issue{Number: "2", Title: "other", Body: "the login is flaky", Labels: []string{"bug", "x"}})
	f.SetIssue(Issue{Number: "3", Title: "flaky login", State: IssueClosed, Labels: []string{"bug"}})
	f.SetIssue(Issue{Number: "4", Title: "flaky only"})

	var s IssueSearcher = f
	got, err := s.SearchIssues("LOGIN flaky", IssueOpen, "", 10)
	if err != nil || len(got) != 2 || got[0].Number != "2" || got[1].Number != "1" {
		t.Fatalf("open search = %+v, %v; want 2,1 newest first", got, err)
	}
	if got, _ = s.SearchIssues("login", IssueClosed, "", 10); len(got) != 1 || got[0].Number != "3" {
		t.Errorf("closed search = %+v, want only 3", got)
	}
	if got, _ = s.SearchIssues("login", IssueOpen, "x", 10); len(got) != 1 || got[0].Number != "2" {
		t.Errorf("label search = %+v, want only 2", got)
	}
	if got, _ = s.SearchIssues("flaky", IssueOpen, "", 1); len(got) != 1 || got[0].Number != "4" {
		t.Errorf("limit search = %+v, want only 4", got)
	}
}

func TestResolveCapabilities_ResolvesIssueSearcher(t *testing.T) {
	f := NewFake()
	if c := ResolveCapabilities(f, f, backend.Descriptor{}, backend.Descriptor{}); c.IssueSearcher == nil {
		t.Error("IssueSearcher = nil, want resolved from the tracker")
	}
}

func TestFake_SearchIssues_RejectsOtherStatesAndClampsNegativeLimit(t *testing.T) {
	f := NewFake()
	f.SetIssue(Issue{Number: "1", Title: "flaky"})
	if _, err := f.SearchIssues("flaky", IssueMerged, "", 10); err == nil {
		t.Error("state MERGED: err = nil, want unsupported-state error")
	}
	if got, err := f.SearchIssues("flaky", IssueOpen, "", -1); err != nil || len(got) != 0 {
		t.Errorf("limit -1 = %+v, %v; want no results", got, err)
	}
}
