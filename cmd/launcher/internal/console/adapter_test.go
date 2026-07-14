package console

import (
	"os"
	"path/filepath"
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

// TestDogfoodNotice_PresentVsAbsent verifies DogfoodNotice reports Live true
// when .dogfood.pid exists under the given directory, and false when it
// doesn't — the presence check dogfood.sh's `echo $$ > .dogfood.pid` /
// `trap 'rm -f .dogfood.pid' EXIT` pair leaves behind.
func TestDogfoodNotice_PresentVsAbsent(t *testing.T) {
	dir := t.TempDir()

	if msg := DogfoodNotice(dir).(DogfoodNoticeMsg); msg.Live {
		t.Error("Live = true with no pid-file, want false")
	}

	if err := os.WriteFile(filepath.Join(dir, ".dogfood.pid"), []byte("123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := DogfoodNotice(dir).(DogfoodNoticeMsg); !msg.Live {
		t.Error("Live = false with a pid-file present, want true")
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
