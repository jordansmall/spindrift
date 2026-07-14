package console

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestRefresh_WrapsListOpenIssuesResult verifies Refresh calls
// IssueTracker.ListOpenIssues and wraps the result into an IssuesLoadedMsg
// Update can apply directly — the thin adapter between the backend seam and
// the pure core.
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

// TestRefresh_TrackerErr_WrapsErr verifies a tracker failure surfaces as
// IssuesLoadedMsg.Err rather than a panic or a silently empty list.
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

// errTracker wraps a forge.IssueTracker so ListOpenIssues always errors,
// while every other method still delegates to the embedded tracker.
type errTracker struct {
	forge.IssueTracker
}

func (errTracker) ListOpenIssues() ([]forge.Issue, error) {
	return nil, errBoom
}
