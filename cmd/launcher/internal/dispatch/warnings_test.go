package dispatch

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runner"
)

// Result.Warnings carries the text of each stderr warning line minus its
// "    ?? #<n>: " prefix (issue #3744).
func TestOutcomeResult_WarningsMirrorStderr(t *testing.T) {
	d := newSocketDispatch(t)
	logPath := writeEmptyLog(t, d)
	manifest := filepath.Join(OutboxDirFor(d.pwd, d.number), "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	var got Result
	stderr := captureStderr(t, func() { got = d.outcomeResult(logPath, resolvedReady) })

	if len(got.Warnings) != 1 || !strings.HasPrefix(got.Warnings[0], "pass-manifest scan: ") {
		t.Fatalf("Warnings: got %q, want one pass-manifest scan warning", got.Warnings)
	}
	if want := "    ?? #" + d.number + ": " + got.Warnings[0] + "\n"; stderr != want {
		t.Errorf("stderr: got %q, want %q", stderr, want)
	}
}

func TestOutcomeResult_StaleMarkerWarningsLandInResult(t *testing.T) {
	d := newSocketDispatch(t)
	stale := "SPINDRIFT_COMMENT " + d.nonce + "-stale Zm9v\n"
	logPath := d.logPath()
	if err := os.WriteFile(logPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	var got Result
	captureStderr(t, func() { got = d.outcomeResult(logPath, resolvedReady) })

	// The nonce-mismatched line also fails the log scanner, which warns first.
	want := []string{
		"comment scan: comment line found but did not verify: nonce mismatch",
		"comment marker line found in log under BOX_SIGNAL_CARRIER=socket; ignored",
	}
	if !reflect.DeepEqual(got.Warnings, want) {
		t.Errorf("Warnings: got %q, want %q", got.Warnings, want)
	}
}

func TestOutcomeResult_NoWarningsWhenClean(t *testing.T) {
	d := newSocketDispatch(t)
	logPath := writeEmptyLog(t, d)
	var got Result
	captureStderr(t, func() { got = d.outcomeResult(logPath, resolvedReady) })
	if len(got.Warnings) != 0 {
		t.Errorf("Warnings: got %q, want none", got.Warnings)
	}
}

func TestRecordWarnings_WritesOneLinePerWarning(t *testing.T) {
	d := newSocketDispatch(t)
	d.RecordWarnings([]string{"comment scan: boom", "pass-manifest scan: bad"})

	b, err := os.ReadFile(warningsPath(d.pwd, d.number))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if want := "comment scan: boom\npass-manifest scan: bad\n"; string(b) != want {
		t.Errorf("sidecar: got %q, want %q", b, want)
	}
}

// A fresh Dispatch whose first settle has no warnings removes an earlier
// run's sidecar so its warnings never outlive a clean settle.
func TestRecordWarnings_EmptyRemovesStaleSidecar(t *testing.T) {
	d := newSocketDispatch(t)
	path := seedSidecar(t, d, "stale\n")
	d.RecordWarnings(nil)
	if fileExists(path) {
		t.Error("sidecar must be removed when a settle has no warnings")
	}
	// Removing an absent sidecar is not an error.
	if out := captureStderr(t, func() { d.RecordWarnings(nil) }); out != "" {
		t.Errorf("unexpected stderr: %q", out)
	}
}

// The sidecar must never be mistaken for an attempt log.
func TestRecordWarnings_SidecarIsNotAnAttemptLog(t *testing.T) {
	d := newSocketDispatch(t)
	d.RecordWarnings([]string{"x"})
	if got := AllAttemptLogPaths(d.pwd, d.number); len(got) != 0 {
		t.Errorf("AllAttemptLogPaths: got %v, want none", got)
	}
	if got := LogPaths(d.pwd, d.number); len(got) != 0 {
		t.Errorf("LogPaths: got %v, want none", got)
	}
}

func TestFake_RecordWarningsRecordsCalls(t *testing.T) {
	f := NewFake()
	f.RecordWarnings([]string{"a"})
	f.RecordWarnings(nil)
	want := [][]string{{"a"}, nil}
	if !reflect.DeepEqual(f.RecordedWarnings, want) {
		t.Errorf("RecordedWarnings: got %v, want %v", f.RecordedWarnings, want)
	}
}

// A fix pass records after the initial run: the sidecar accumulates, and the
// first call on a fresh Dispatch replaces an earlier run's file.
func TestRecordWarnings_AccumulatesAcrossCalls(t *testing.T) {
	d := newSocketDispatch(t)
	path := seedSidecar(t, d, "older run\n")

	d.RecordWarnings([]string{"a"})
	d.RecordWarnings([]string{"fix pass 1: b"})
	// A later settle with no warnings must not clear the accumulated set.
	d.RecordWarnings(nil)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if want := "a\nfix pass 1: b\n"; string(b) != want {
		t.Errorf("sidecar: got %q, want %q", b, want)
	}
}

// seedSidecar writes content as d's sidecar, standing in for an earlier run's
// file, and returns its path.
func seedSidecar(t *testing.T, d *Dispatch, content string) string {
	t.Helper()
	path := warningsPath(d.pwd, d.number)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The scan-error path end to end: a corrupt pass manifest makes outcomeResult
// warn, and the same text lands in the on-disk sidecar.
func TestRecordWarnings_ScanWarningReachesSidecar(t *testing.T) {
	d := newSocketDispatch(t)
	logPath := writeEmptyLog(t, d)
	manifest := filepath.Join(OutboxDirFor(d.pwd, d.number), "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	var got Result
	captureStderr(t, func() { got = d.outcomeResult(logPath, resolvedReady) })
	if len(got.Warnings) != 1 || !strings.HasPrefix(got.Warnings[0], "pass-manifest scan: ") {
		t.Fatalf("Warnings: got %q, want one pass-manifest scan warning", got.Warnings)
	}
	d.RecordWarnings(got.Warnings)

	b, err := os.ReadFile(warningsPath(d.pwd, d.number))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if want := got.Warnings[0] + "\n"; string(b) != want {
		t.Errorf("sidecar: got %q, want %q", b, want)
	}
}

// A butler Chore Dispatch is keyed "butler-<chore>", so its sidecar is
// issue-butler-<chore>.warnings.
func TestRecordWarnings_ChoreKeyedSidecarName(t *testing.T) {
	dir := tempLogDir(t)
	var sleeps []time.Duration
	f, err := NewFactory(retryConfig(3, 0, 0), dir, runner.NewFake(), fakeDriver{}, fakeClock(time.Time{}, &sleeps))
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(f.Cleanup)
	d := f.newDispatch(choreSubject(Chore{Name: "bugs"}))

	d.RecordWarnings([]string{"x"})

	if want := filepath.Join(HostLogDirFor(dir), "issue-butler-bugs.warnings"); !fileExists(want) {
		t.Errorf("sidecar %s not written", want)
	}
}
