package console

import (
	"os"
	"path/filepath"
	"strconv"
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

// TestRefresh_SortsByPriority verifies Refresh orders the wrapped Issues by
// descending Priority (ADR 0040), oldest-first within a tier, rather than
// passing ListOpenIssues' raw oldest-first-only order straight through — so
// the Backlog renders in the same priority order the headless dispatch pool
// uses (#2284).
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

// TestRefresh_CountsRecoverableFromFetchedIssues verifies Refresh derives
// RecoverableCount from the same ListOpenIssues call it already makes — no
// extra tracker round trip — by resolving the Recoverable state's own label
// (forge.LabeledTracker.StateLabels) and counting how many of the fetched
// issues carry it (issue #2255, ADR 0039 slice S4).
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

// TestRefresh_RecoverableCount_UnmappedLabelIsZero verifies a tracker whose
// label family leaves Recoverable unmapped (empty label string) reports zero
// rather than matching every issue — the same "unmapped state matches
// everything, so treat as zero" caution issueInState documents (#1742).
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

// TestDogfoodNotice_PresentVsAbsent verifies DogfoodNotice reports Live true
// when .spindrift/dogfood.pid names a running process under the given
// directory, and false when the file doesn't exist — the pair dogfood.sh's
// `echo $$ > .spindrift/dogfood.pid` / `trap 'rm -f .spindrift/dogfood.pid'
// EXIT` leaves behind.
func TestDogfoodNotice_PresentVsAbsent(t *testing.T) {
	dir := t.TempDir()

	if msg := DogfoodNotice(dir).(DogfoodNoticeMsg); msg.Live {
		t.Error("Live = true with no pid-file, want false")
	}

	pid := strconv.Itoa(os.Getpid())
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "dogfood.pid"), []byte(pid+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := DogfoodNotice(dir).(DogfoodNoticeMsg); !msg.Live {
		t.Error("Live = false with a pid-file naming the running test process, want true")
	}
}

// TestDogfoodNotice_StalePidReportsNotLive verifies a pid-file left behind by
// a crashed loop (EXIT trap never fired, #565) reports Live false rather than
// true on bare file presence — the process it names has already exited.
//
// Stubs isProcessAlive rather than spawning and reaping a real process to
// obtain a "dead" pid: the previous version raced the OS's pid allocator
// (kernel could theoretically reassign the reaped pid to a new process
// before the liveness probe ran), a real if rare flakiness source (#952).
func TestDogfoodNotice_StalePidReportsNotLive(t *testing.T) {
	dir := t.TempDir()

	const deadPid = 99999 // arbitrary — isProcessAlive is stubbed, never a real pid
	orig := isProcessAlive
	isProcessAlive = func(pid int) bool {
		if pid != deadPid {
			t.Fatalf("isProcessAlive(%d), want %d", pid, deadPid)
		}
		return false
	}
	t.Cleanup(func() { isProcessAlive = orig })

	if err := os.MkdirAll(filepath.Join(dir, ".spindrift"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "dogfood.pid"), []byte(strconv.Itoa(deadPid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := DogfoodNotice(dir).(DogfoodNoticeMsg); msg.Live {
		t.Error("Live = true with a stale pid-file (process exited), want false")
	}
}

// TestDogfoodNotice_MalformedPidReportsNotLive verifies a pid-file whose
// content isn't a parseable integer collapses to Live false rather than
// erroring or panicking.
func TestDogfoodNotice_MalformedPidReportsNotLive(t *testing.T) {
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, ".spindrift"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "dogfood.pid"), []byte("not-a-pid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := DogfoodNotice(dir).(DogfoodNoticeMsg); msg.Live {
		t.Error("Live = true with a malformed pid-file, want false")
	}
}

// TestDogfoodNotice_ZeroPidReportsNotLive verifies a pid-file containing "0"
// reports Live false rather than true — pid 0 targets the caller's own
// process group, so an unguarded kill(0, 0) always succeeds from inside the
// Box regardless of whether any dogfood.sh session is actually running.
func TestDogfoodNotice_ZeroPidReportsNotLive(t *testing.T) {
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, ".spindrift"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "dogfood.pid"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := DogfoodNotice(dir).(DogfoodNoticeMsg); msg.Live {
		t.Error("Live = true with a pid-file containing \"0\", want false")
	}
}

// TestDogfoodNotice_NegativePidReportsNotLive verifies a pid-file containing
// "-1" reports Live false rather than true — pid -1 is a broadcast probe
// that succeeds if the caller has permission to signal any process at all,
// so an unguarded kill(-1, 0) is a false positive independent of whether any
// dogfood.sh session is running.
func TestDogfoodNotice_NegativePidReportsNotLive(t *testing.T) {
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, ".spindrift"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "dogfood.pid"), []byte("-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := DogfoodNotice(dir).(DogfoodNoticeMsg); msg.Live {
		t.Error("Live = true with a pid-file containing \"-1\", want false")
	}
}

// TestDispatchStateName_KnownAndUnlisted verifies the two states PickIssue
// actually rejects on render their specific words, and an unlisted terminal
// state (forge.Failed) falls back to the generic default rather than an
// empty string — pinning the fallback as intentional, not dead code (#988).
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

// errTracker wraps a forge.IssueTracker so ListOpenIssues always errors,
// while every other method still delegates to the embedded tracker.
type errTracker struct {
	forge.IssueTracker
}

func (errTracker) ListOpenIssues() ([]forge.Issue, error) {
	return nil, errBoom
}
