package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogPaths_InitialOnly(t *testing.T) {
	dir := tempLogDir(t)
	if err := os.WriteFile(filepath.Join(HostLogDirFor(dir), "issue-1.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := LogPaths(dir, "1")
	if len(got) != 1 {
		t.Fatalf("LogPaths = %+v, want 1 entry", got)
	}
	if got[0].Label != "initial" {
		t.Errorf("Label = %q, want %q", got[0].Label, "initial")
	}
	if got[0].Path != filepath.Join(HostLogDirFor(dir), "issue-1.log") {
		t.Errorf("Path = %q, want the initial log path", got[0].Path)
	}
}

// The wanted order is chronological, not alphabetical: initial, each fix pass
// by number, then conflict-resolve.
func TestLogPaths_OrdersInitialFixesAndConflictResolve(t *testing.T) {
	dir := tempLogDir(t)
	logsDir := HostLogDirFor(dir)
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

// The fixture deliberately leaves a hole at fix-2. LogPaths probes
// consecutively, so it stops at the hole and never reports fix-3.
func TestLogPaths_StopsAtFirstMissingFixPass(t *testing.T) {
	dir := tempLogDir(t)
	logsDir := HostLogDirFor(dir)
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

func TestLogPaths_NoLogsOnDisk_ReturnsEmpty(t *testing.T) {
	dir := tempLogDir(t)
	got := LogPaths(dir, "999")
	if len(got) != 0 {
		t.Errorf("LogPaths = %+v, want empty", got)
	}
}

func TestAllAttemptLogPaths_NoRetries_MatchesLogPaths(t *testing.T) {
	dir := tempLogDir(t)
	if err := os.WriteFile(filepath.Join(HostLogDirFor(dir), "issue-1.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := AllAttemptLogPaths(dir, "1")
	if len(got) != 1 {
		t.Fatalf("AllAttemptLogPaths = %+v, want 1 entry", got)
	}
	if got[0].Label != "initial" {
		t.Errorf("Label = %q, want %q", got[0].Label, "initial")
	}
	if got[0].Path != filepath.Join(HostLogDirFor(dir), "issue-1.log") {
		t.Errorf("Path = %q, want the initial log path", got[0].Path)
	}
}

// Rotated attempts come before the current bare log, so the wanted order is
// oldest first and the unsuffixed label comes last.
func TestAllAttemptLogPaths_RotatedAttemptsThenCurrent(t *testing.T) {
	dir := tempLogDir(t)
	logsDir := HostLogDirFor(dir)
	for _, name := range []string{"issue-1.log.1", "issue-1.log.2", "issue-1.log"} {
		if err := os.WriteFile(filepath.Join(logsDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := AllAttemptLogPaths(dir, "1")
	wantLabels := []string{"initial.1", "initial.2", "initial"}
	if len(got) != len(wantLabels) {
		t.Fatalf("AllAttemptLogPaths = %+v, want %d entries", got, len(wantLabels))
	}
	for i, label := range wantLabels {
		if got[i].Label != label {
			t.Errorf("entry %d Label = %q, want %q", i, got[i].Label, label)
		}
	}
	wantPaths := []string{
		filepath.Join(logsDir, "issue-1.log.1"),
		filepath.Join(logsDir, "issue-1.log.2"),
		filepath.Join(logsDir, "issue-1.log"),
	}
	for i, p := range wantPaths {
		if got[i].Path != p {
			t.Errorf("entry %d Path = %q, want %q", i, got[i].Path, p)
		}
	}
}

// A rotated attempt with no bare log is what sits on disk between a rotate and
// the next attempt, or after a crash. AllAttemptLogPaths must still report it.
func TestAllAttemptLogPaths_RotatedAttemptWithNoCurrentLog(t *testing.T) {
	dir := tempLogDir(t)
	logsDir := HostLogDirFor(dir)
	if err := os.WriteFile(filepath.Join(logsDir, "issue-1.log.1"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := AllAttemptLogPaths(dir, "1")
	if len(got) != 1 {
		t.Fatalf("AllAttemptLogPaths = %+v, want 1 entry", got)
	}
	if got[0].Label != "initial.1" {
		t.Errorf("Label = %q, want %q", got[0].Label, "initial.1")
	}
	if got[0].Path != filepath.Join(logsDir, "issue-1.log.1") {
		t.Errorf("Path = %q, want the rotated attempt path", got[0].Path)
	}
}

// Only the initial pass was retried, so the fixture checks that one pass's
// rotated attempts stay with that pass instead of appearing under the next.
func TestAllAttemptLogPaths_MultiplePassesIndependentRotationHistory(t *testing.T) {
	dir := tempLogDir(t)
	logsDir := HostLogDirFor(dir)
	for _, name := range []string{"issue-1.log.1", "issue-1.log", "issue-1-fix-1.log"} {
		if err := os.WriteFile(filepath.Join(logsDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := AllAttemptLogPaths(dir, "1")
	wantLabels := []string{"initial.1", "initial", "fix-1"}
	if len(got) != len(wantLabels) {
		t.Fatalf("AllAttemptLogPaths = %+v, want %d entries", got, len(wantLabels))
	}
	for i, label := range wantLabels {
		if got[i].Label != label {
			t.Errorf("entry %d Label = %q, want %q", i, got[i].Label, label)
		}
	}
}

func TestAllAttemptLogPaths_NoLogsOnDisk_ReturnsEmpty(t *testing.T) {
	dir := tempLogDir(t)
	got := AllAttemptLogPaths(dir, "999")
	if len(got) != 0 {
		t.Errorf("AllAttemptLogPaths = %+v, want empty", got)
	}
}

// HostLogDirFor is the single source of truth for a pwd's log directory. The
// table pins every naming function to it so the directory cannot drift from
// the host-side code that reads or creates it.
func TestHostLogDirFor(t *testing.T) {
	pwd := filepath.Join(string(filepath.Separator), "tmp", "x")
	number := "42"

	want := filepath.Join(pwd, ".spindrift", "logs")
	if got := HostLogDirFor(pwd); got != want {
		t.Errorf("HostLogDirFor(%q) = %q, want %q", pwd, got, want)
	}

	cases := []struct {
		name string
		got  string
	}{
		{"logPathFor", logPathFor(pwd, number)},
		{"fixLogPathFor", fixLogPathFor(pwd, number, 1)},
		{"conflictLogPathFor", conflictLogPathFor(pwd, number)},
	}
	for _, c := range cases {
		if dir := filepath.Dir(c.got); dir != want {
			t.Errorf("%s(%q, %q) dir = %q, want %q", c.name, pwd, number, dir, want)
		}
	}
}

// Factory must expose the Driver it was constructed with so a Console drill-in
// renders a Dispatch's logs without the Factory growing a second rendering
// path (#648).
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
