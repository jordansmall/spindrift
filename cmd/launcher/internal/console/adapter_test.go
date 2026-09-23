package console

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func TestRefresh_WrapsListOpenIssuesResult(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "first", State: forge.IssueOpen})

	msg := Refresh(f)

	loaded, ok := msg.(IssuesLoadedMsg)
	if !ok {
		t.Fatalf("Refresh() = %T, want IssuesLoadedMsg", msg)
	}
	if loaded.Err != nil {
		t.Fatalf("Err = %v, want nil", loaded.Err)
	}
	if len(loaded.Issues) != 1 || loaded.Issues[0].Number != "1" {
		t.Errorf("Issues = %+v, want [#1]", loaded.Issues)
	}
}

// Refresh sorts by descending Priority (ADR 0040), oldest-first within a tier,
// instead of passing ListOpenIssues' raw oldest-first order through, so the
// Backlog renders in the same order the headless dispatch pool uses (#2284).
func TestRefresh_SortsByPriority(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "1", Title: "normal one", State: forge.IssueOpen})
	f.SetIssue(forge.Issue{Number: "2", Title: "low", State: forge.IssueOpen, Labels: []string{"agent-priority-low"}})
	f.SetIssue(forge.Issue{Number: "3", Title: "critical", State: forge.IssueOpen, Labels: []string{"agent-priority-critical"}})
	f.SetIssue(forge.Issue{Number: "4", Title: "normal two", State: forge.IssueOpen})

	msg := Refresh(f)

	loaded, ok := msg.(IssuesLoadedMsg)
	if !ok {
		t.Fatalf("Refresh() = %T, want IssuesLoadedMsg", msg)
	}
	if loaded.Err != nil {
		t.Fatalf("Err = %v, want nil", loaded.Err)
	}
	want := []string{"3", "1", "4", "2"}
	if len(loaded.Issues) != len(want) {
		t.Fatalf("Issues = %+v, want %d issues in order %v", loaded.Issues, len(want), want)
	}
	for i, num := range want {
		if loaded.Issues[i].Number != num {
			t.Errorf("Issues[%d].Number = %q, want %q (order %v)", i, loaded.Issues[i].Number, num, want)
		}
	}
}

func TestRefresh_TrackerErr_WrapsErr(t *testing.T) {
	f := forge.NewFake()

	msg := Refresh(errTracker{f})

	loaded, ok := msg.(IssuesLoadedMsg)
	if !ok {
		t.Fatalf("Refresh() = %T, want IssuesLoadedMsg", msg)
	}
	if loaded.Err == nil {
		t.Fatal("Err = nil, want the tracker error")
	}
}

// Refresh derives RecoverableCount from the ListOpenIssues call it already
// makes, with no extra tracker round trip, by counting the fetched issues that
// carry the Recoverable state's own label (issue #2255, ADR 0039 slice S4).
func TestRefresh_CountsRecoverableFromFetchedIssues(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Recoverable: "agent-recoverable"})
	f.SetIssue(forge.Issue{Number: "1", Title: "recoverable one", State: forge.IssueOpen, Labels: []string{"agent-recoverable"}})
	f.SetIssue(forge.Issue{Number: "2", Title: "plain", State: forge.IssueOpen})
	f.SetIssue(forge.Issue{Number: "3", Title: "recoverable two", State: forge.IssueOpen, Labels: []string{"agent-recoverable"}})

	msg := Refresh(f)

	loaded, ok := msg.(IssuesLoadedMsg)
	if !ok {
		t.Fatalf("Refresh() = %T, want IssuesLoadedMsg", msg)
	}
	if loaded.RecoverableCount != 2 {
		t.Errorf("RecoverableCount = %d, want 2", loaded.RecoverableCount)
	}
}

// An unmapped Recoverable label is the empty string, which would match every
// issue. issueInState documents the same caution (#1742).
func TestRefresh_RecoverableCount_UnmappedLabelIsZero(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{})
	f.SetIssue(forge.Issue{Number: "1", Title: "no marker", State: forge.IssueOpen})

	msg := Refresh(f)

	loaded, ok := msg.(IssuesLoadedMsg)
	if !ok {
		t.Fatalf("Refresh() = %T, want IssuesLoadedMsg", msg)
	}
	if loaded.RecoverableCount != 0 {
		t.Errorf("RecoverableCount = %d, want 0", loaded.RecoverableCount)
	}
}

// An unlisted terminal state (forge.Failed) must fall back to the generic
// default rather than an empty string. The case pins that fallback as
// intentional rather than dead code (#988).
func TestDispatchStateName_KnownAndUnlisted(t *testing.T) {
	tests := []struct {
		name  string
		state forge.DispatchState
		want  string
	}{
		{"InProgress", forge.InProgress, "in progress"},
		{"Complete", forge.Complete, "complete"},
		{"Failed falls back to default", forge.Failed, "in a terminal state"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dispatchStateName(tt.state); got != tt.want {
				t.Errorf("dispatchStateName(%v) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

type errTracker struct {
	forge.IssueTracker
}

func (errTracker) ListOpenIssues() ([]forge.Issue, error) {
	return nil, errBoom
}

// Embedding the interface promotes only the methods forge.IssueTracker itself
// declares, so plainTracker never satisfies a forge.LabeledTracker assertion
// even when the value it wraps implements StateLabels.
type plainTracker struct {
	forge.IssueTracker
}

// This test pins countRecoverable's !ok branch: a tracker that does not
// implement forge.LabeledTracker at all (Jira, ADR 0039) reports zero even when
// a fetched issue carries a label string that would otherwise match.
func TestRefresh_RecoverableCount_NonLabeledTrackerIsZero(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Recoverable: "agent-recoverable"})
	f.SetIssue(forge.Issue{Number: "1", Title: "looks recoverable", State: forge.IssueOpen, Labels: []string{"agent-recoverable"}})

	msg := Refresh(plainTracker{f})

	loaded, ok := msg.(IssuesLoadedMsg)
	if !ok {
		t.Fatalf("Refresh() = %T, want IssuesLoadedMsg", msg)
	}
	if loaded.Err != nil {
		t.Fatalf("Err = %v, want nil", loaded.Err)
	}
	if loaded.RecoverableCount != 0 {
		t.Errorf("RecoverableCount = %d, want 0 (tracker is not a LabeledTracker)", loaded.RecoverableCount)
	}
}
