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

// TestLogPaths_OrdersInitialFixesAndConflictResolve verifies LogPaths
// concatenates every pass that exists on disk in chronological order:
// initial, each fix pass by number, then conflict-resolve.
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

// TestLogPaths_StopsAtFirstMissingFixPass verifies a gap in fix-pass
// numbering (fix-1 present, fix-2 missing, fix-3 present) truncates the
// probe at the gap rather than skipping over it — fix-3 never appears.
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

// TestLogPaths_NoLogsOnDisk_ReturnsEmpty verifies an issue with no Dispatch
// history yet returns an empty slice, not an error.
func TestLogPaths_NoLogsOnDisk_ReturnsEmpty(t *testing.T) {
	dir := tempLogDir(t)
	got := LogPaths(dir, "999")
	if len(got) != 0 {
		t.Errorf("LogPaths = %+v, want empty", got)
	}
}

// TestAllAttemptLogPaths_NoRetries_MatchesLogPaths verifies a pass with only
// its bare log on disk (no rotated attempts) behaves like LogPaths for that
// pass -- a single entry with the bare label.
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

// TestAllAttemptLogPaths_RotatedAttemptsThenCurrent verifies a pass with two
// rotated-aside attempts (issue-1.log.1, issue-1.log.2) plus its current
// bare log all appear, oldest first, labeled initial.1, initial.2, initial.
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

// TestAllAttemptLogPaths_RotatedAttemptWithNoCurrentLog verifies a pass
// mid-retry -- a rotated-aside attempt on disk but no fresh bare log yet
// (the rotate happened but a new attempt hasn't started, or the run crashed
// before creating one) -- still surfaces the rotated attempt rather than
// dropping it because the bare log is missing.
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

// TestAllAttemptLogPaths_MultiplePassesIndependentRotationHistory verifies
// each pass's rotated attempts and current log stay grouped together in
// order, even when only some passes were retried: initial has one rotated
// attempt plus its current log, fix-1 has only its current log.
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

// TestAllAttemptLogPaths_NoLogsOnDisk_ReturnsEmpty verifies an issue with no
// Dispatch history yet returns an empty slice, not an error.
func TestAllAttemptLogPaths_NoLogsOnDisk_ReturnsEmpty(t *testing.T) {
	dir := tempLogDir(t)
	got := AllAttemptLogPaths(dir, "999")
	if len(got) != 0 {
		t.Errorf("AllAttemptLogPaths = %+v, want empty", got)
	}
}

// TestHostLogDirFor verifies HostLogDirFor is the single source of truth for
// a pwd's log directory, and that logPathFor, fixLogPathFor, and
// conflictLogPathFor all place their files inside it — so the directory can
// never drift between the naming functions and any other host-side site
// that reads or creates it.
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
