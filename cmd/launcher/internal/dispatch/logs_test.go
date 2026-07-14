package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLogPaths_InitialOnly verifies LogPaths returns just the initial run's
// log, labeled "initial", when no fix or conflict-resolve pass ever ran.
func TestLogPaths_InitialOnly(t *testing.T) {
	dir := tempLogDir(t)
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-1.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := LogPaths(dir, "1")
	if len(got) != 1 {
		t.Fatalf("LogPaths = %+v, want 1 entry", got)
	}
	if got[0].Label != "initial" {
		t.Errorf("Label = %q, want %q", got[0].Label, "initial")
	}
	if got[0].Path != filepath.Join(dir, "logs", "issue-1.log") {
		t.Errorf("Path = %q, want the initial log path", got[0].Path)
	}
}

// TestLogPaths_OrdersInitialFixesAndConflictResolve verifies LogPaths
// concatenates every pass that exists on disk in chronological order:
// initial, each fix pass by number, then conflict-resolve.
func TestLogPaths_OrdersInitialFixesAndConflictResolve(t *testing.T) {
	dir := tempLogDir(t)
	logsDir := filepath.Join(dir, "logs")
	for _, name := range []string{
		"issue-1.log",
		"issue-1-fix-1.log",
		"issue-1-fix-2.log",
		"issue-1-conflict-resolve.log",
	} {
		if err := os.WriteFile(filepath.Join(logsDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := LogPaths(dir, "1")
	wantLabels := []string{"initial", "fix-1", "fix-2", "conflict-resolve"}
	if len(got) != len(wantLabels) {
		t.Fatalf("LogPaths = %+v, want %d entries", got, len(wantLabels))
	}
	for i, label := range wantLabels {
		if got[i].Label != label {
			t.Errorf("entry %d Label = %q, want %q", i, got[i].Label, label)
		}
	}
}

// TestLogPaths_StopsAtFirstMissingFixPass verifies a gap in fix-pass
// numbering (fix-1 present, fix-2 missing, fix-3 present) truncates the
// probe at the gap rather than skipping over it — fix-3 never appears.
func TestLogPaths_StopsAtFirstMissingFixPass(t *testing.T) {
	dir := tempLogDir(t)
	logsDir := filepath.Join(dir, "logs")
	for _, name := range []string{"issue-1.log", "issue-1-fix-1.log", "issue-1-fix-3.log"} {
		if err := os.WriteFile(filepath.Join(logsDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := LogPaths(dir, "1")
	wantLabels := []string{"initial", "fix-1"}
	if len(got) != len(wantLabels) {
		t.Fatalf("LogPaths = %+v, want %d entries (stop at the gap)", got, len(wantLabels))
	}
}

// TestLogPaths_NoLogsOnDisk_ReturnsEmpty verifies an issue with no Dispatch
// history yet returns an empty slice, not an error.
func TestLogPaths_NoLogsOnDisk_ReturnsEmpty(t *testing.T) {
	dir := tempLogDir(t)
	got := LogPaths(dir, "999")
	if len(got) != 0 {
		t.Errorf("LogPaths = %+v, want empty", got)
	}
}

// TestFactory_Driver_ReturnsConfiguredDriver verifies Factory exposes the
// Driver strategy it was constructed with, so a Console drill-in can render
// a Dispatch's logs without the Factory growing a second rendering path
// (#648).
func TestFactory_Driver_ReturnsConfiguredDriver(t *testing.T) {
	drv := fakeDriver{}
	f, err := NewFactory(Config{}, tempLogDir(t), nil, drv, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if f.Driver().Name() != drv.Name() {
		t.Errorf("Driver().Name() = %q, want %q", f.Driver().Name(), drv.Name())
	}
}
