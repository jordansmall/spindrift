package reconcile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/runner"
)

// TestFSProbe_LogStale_NoLogIsStale verifies an issue that never wrote a Box
// log counts as stale — a genuine orphan with no history is not held
// indefinitely waiting for a log that will never appear.
func TestFSProbe_LogStale_NoLogIsStale(t *testing.T) {
	p := NewFSProbe(t.TempDir(), runner.NewFake())
	if !p.LogStale("42") {
		t.Errorf("LogStale = false, want true for an issue with no log on disk")
	}
}

// TestFSProbe_LogStale_RecentLogIsNotStale verifies a log written within the
// staleness threshold reports the issue as still live.
func TestFSProbe_LogStale_RecentLogIsNotStale(t *testing.T) {
	pwd := t.TempDir()
	writeLog(t, pwd, "42")

	p := NewFSProbe(pwd, runner.NewFake())
	p.now = func() time.Time { return time.Now() }
	if p.LogStale("42") {
		t.Errorf("LogStale = true, want false for a freshly written log")
	}
}

// TestFSProbe_LogStale_OldLogIsStale verifies a log last written beyond the
// staleness threshold reports the issue as dead.
func TestFSProbe_LogStale_OldLogIsStale(t *testing.T) {
	pwd := t.TempDir()
	writeLog(t, pwd, "42")

	p := NewFSProbe(pwd, runner.NewFake())
	p.now = func() time.Time { return time.Now().Add(staleAfter + time.Minute) }
	if !p.LogStale("42") {
		t.Errorf("LogStale = false, want true for a log last written beyond staleAfter")
	}
}

// TestFSProbe_ContainerLive_Present verifies ContainerLive reports live and
// reachable when the runner reports a matching sandbox running.
func TestFSProbe_ContainerLive_Present(t *testing.T) {
	r := runner.NewFake()
	r.RunningNames = []string{dispatch.BoxName("42")}

	p := NewFSProbe(t.TempDir(), r)
	live, reachable := p.ContainerLive("42")
	if !live || !reachable {
		t.Errorf("ContainerLive = (%v, %v), want (true, true)", live, reachable)
	}
}

// TestFSProbe_ContainerLive_Absent verifies ContainerLive reports not-live,
// reachable when the runner answers with no matching sandbox.
func TestFSProbe_ContainerLive_Absent(t *testing.T) {
	p := NewFSProbe(t.TempDir(), runner.NewFake())
	live, reachable := p.ContainerLive("42")
	if live || !reachable {
		t.Errorf("ContainerLive = (%v, %v), want (false, true)", live, reachable)
	}
}

// TestFSProbe_ContainerLive_Unreachable verifies ContainerLive reports
// unreachable — not live — when the runtime itself cannot be queried, so
// Run never mistakes "couldn't check" for "confirmed absent".
func TestFSProbe_ContainerLive_Unreachable(t *testing.T) {
	r := runner.NewFake()
	r.ListRunningErr = os.ErrClosed

	p := NewFSProbe(t.TempDir(), r)
	live, reachable := p.ContainerLive("42")
	if live || reachable {
		t.Errorf("ContainerLive = (%v, %v), want (false, false)", live, reachable)
	}
}

func writeLog(t *testing.T, pwd, number string) {
	t.Helper()
	dir := filepath.Join(pwd, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "issue-"+number+".log")
	if err := os.WriteFile(path, []byte("log"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
