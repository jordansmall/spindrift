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
// when .dogfood.pid names a running process under the given directory, and
// false when the file doesn't exist — the pair dogfood.sh's
// `echo $$ > .dogfood.pid` / `trap 'rm -f .dogfood.pid' EXIT` leaves behind.
func TestDogfoodNotice_PresentVsAbsent(t *testing.T) {
	dir := t.TempDir()

	if msg := DogfoodNotice(dir).(DogfoodNoticeMsg); msg.Live {
		t.Error("Live = true with no pid-file, want false")
	}

	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(filepath.Join(dir, ".dogfood.pid"), []byte(pid+"\n"), 0o644); err != nil {
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

	const deadPid = 99999
	orig := isProcessAlive
	isProcessAlive = func(pid int) bool {
		if pid != deadPid {
			t.Fatalf("isProcessAlive(%d), want %d", pid, deadPid)
		}
		return false
	}
	t.Cleanup(func() { isProcessAlive = orig })

	if err := os.WriteFile(filepath.Join(dir, ".dogfood.pid"), []byte(strconv.Itoa(deadPid)+"\n"), 0o644); err != nil {
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

	if err := os.WriteFile(filepath.Join(dir, ".dogfood.pid"), []byte("not-a-pid\n"), 0o644); err != nil {
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

	if err := os.WriteFile(filepath.Join(dir, ".dogfood.pid"), []byte("0\n"), 0o644); err != nil {
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

	if err := os.WriteFile(filepath.Join(dir, ".dogfood.pid"), []byte("-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := DogfoodNotice(dir).(DogfoodNoticeMsg); msg.Live {
		t.Error("Live = true with a pid-file containing \"-1\", want false")
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
