package reconcile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/runner"
)

// An orphan with no history must not be held forever waiting for a log that
// will never appear, so no log at all counts as stale.
func TestFSProbe_LogStale_NoLogIsStale(t *testing.T) {
	p := NewFSProbe(t.TempDir(), runner.NewFake())
	if !p.LogStale("42") {
		t.Errorf("LogStale = false, want true for an issue with no log on disk")
	}
}

func TestFSProbe_LogStale_RecentLogIsNotStale(t *testing.T) {
	pwd := t.TempDir()
	writeLog(t, pwd, "42")

	p := NewFSProbe(pwd, runner.NewFake())
	p.now = func() time.Time { return time.Now() }
	if p.LogStale("42") {
		t.Errorf("LogStale = true, want false for a freshly written log")
	}
}

func TestFSProbe_LogStale_OldLogIsStale(t *testing.T) {
	pwd := t.TempDir()
	writeLog(t, pwd, "42")

	p := NewFSProbe(pwd, runner.NewFake())
	p.now = func() time.Time { return time.Now().Add(staleAfter + time.Minute) }
	if !p.LogStale("42") {
		t.Errorf("LogStale = false, want true for a log last written beyond staleAfter")
	}
}

func TestFSProbe_ContainerLive_Present(t *testing.T) {
	r := runner.NewFake()
	r.RunningNames = []string{dispatch.BoxName("42")}

	p := NewFSProbe(t.TempDir(), r)
	live, reachable := p.ContainerLive("42")
	if !live || !reachable {
		t.Errorf("ContainerLive = (%v, %v), want (true, true)", live, reachable)
	}
}

func TestFSProbe_ContainerLive_Absent(t *testing.T) {
	p := NewFSProbe(t.TempDir(), runner.NewFake())
	live, reachable := p.ContainerLive("42")
	if live || !reachable {
		t.Errorf("ContainerLive = (%v, %v), want (false, true)", live, reachable)
	}
}

// A runtime that cannot be queried reports unreachable and not live, so Run
// never mistakes "couldn't check" for "confirmed absent".
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
	dir := filepath.Join(pwd, ".spindrift", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "issue-"+number+".log")
	if err := os.WriteFile(path, []byte("log"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
