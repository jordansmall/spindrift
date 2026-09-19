package waves

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

// Pending() errors rather than fabricating a confirmed-looking 0 (#2939 review
// finding). A caller that reaches Pending despite QueueFromDiscoverer being
// documented as Discover-only must be routed to RunContinuous's heldBackUnknown
// path, not handed a fake confirmed count.
func TestDiscoverQueue_Pending_ReturnsError(t *testing.T) {
	q := QueueFromDiscoverer(func() (Batch, error) { return Batch{}, nil })

	n, err := q.Pending(nil)
	if err == nil {
		t.Fatalf("Pending() = (%d, nil), want a non-nil error", n)
	}
}

// Pending() forwards straight to the injected closure (#2939). The discover and
// claimer arguments stay nil because this test never calls Discover or Claim.
func TestHeadlessQueue_Pending_DelegatesToClosure(t *testing.T) {
	q := NewHeadlessQueue(nil, nil, func(map[string]bool) (int, error) { return 5, nil }, "")

	n, err := q.Pending(nil)
	if err != nil {
		t.Fatalf("Pending() error = %v, want nil", err)
	}
	if n != 5 {
		t.Fatalf("Pending() = %d, want 5", n)
	}
}

func TestHeadlessQueue_Pending_PropagatesError(t *testing.T) {
	wantErr := errors.New("transient query failure")
	q := NewHeadlessQueue(nil, nil, func(map[string]bool) (int, error) { return 0, wantErr }, "")

	_, err := q.Pending(nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Pending() error = %v, want %v", err, wantErr)
	}
}

// headlessQueue.Pending forwards its caller's claimed map to the pending
// closure verbatim. headlessQueue keeps no claimed set of its own to merge in
// (issue #3035).
func TestHeadlessQueue_Pending_ForwardsCallersClaimedSet(t *testing.T) {
	var observed map[string]bool
	pending := func(claimed map[string]bool) (int, error) {
		observed = claimed
		return 0, nil
	}
	q := NewHeadlessQueue(nil, nil, pending, "")

	want := map[string]bool{"42": true}
	if _, err := q.Pending(want); err != nil {
		t.Fatalf("Pending: %v", err)
	}

	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("Pending's claimed set = %v, want %v", observed, want)
	}
}

// ReportStaleDrain prints report.Console() to stdout and appends
// report.HostLog() to pwd's stale-drain.log (#2939), mirroring continuous.go's
// emitStaleDrainReport.
func TestHeadlessQueue_ReportStaleDrain_PrintsAndAppendsLog(t *testing.T) {
	dir := tempLogDir(t)
	q := NewHeadlessQueue(nil, nil, noopPending, dir)

	report := StaleDrainReport{
		StaleAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		DrainedAt: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC),
		HeldBack:  2,
	}

	stdout := testutil.CaptureStdout(t, func() {
		q.ReportStaleDrain(report)
	})
	if !strings.Contains(stdout, "==> stale-drain:") {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, "==> stale-drain:")
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), staleDrainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	if !strings.Contains(string(logBytes), "STALE_DRAIN ") {
		t.Fatalf("log content = %q, want it to contain %q", string(logBytes), "STALE_DRAIN ")
	}
}

// This pins a review finding on #2678: ReportStaleDrain's stale-drain.log open
// failure goes to stderr rather than failing the run. dir is a bare t.TempDir()
// whose .spindrift/logs subdirectory was never created, unlike every real
// RunContinuous call, so os.OpenFile's O_CREATE cannot create the file inside a
// missing parent.
func TestHeadlessQueue_ReportStaleDrain_OpenFailureLogsToStderr(t *testing.T) {
	dir := t.TempDir()
	q := NewHeadlessQueue(nil, nil, func(map[string]bool) (int, error) { return 0, nil }, dir)
	report := StaleDrainReport{StaleAt: time.Now(), DrainedAt: time.Now(), HeldBack: 1}

	stderr := testutil.CaptureStderr(t, func() {
		q.ReportStaleDrain(report)
	})

	if !strings.Contains(stderr, "continuous: open") {
		t.Errorf("stderr: got %q, want an \"continuous: open ...\" line reporting the failure", stderr)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), staleDrainMarker)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("Stat(%s): got err=%v, want a not-exist error (no file should have been created)", logPath, err)
	}
}

// EnsureLogDirExists creates dispatch.HostLogDirFor(pwd) under a fresh temp dir
// that does not have it yet (issue #3036). Unlike tempLogDir(t), dir here is a
// bare t.TempDir() whose .spindrift/logs subdirectory was never pre-created.
func TestHeadlessQueue_EnsureLogDirExists_CreatesLogDir(t *testing.T) {
	dir := t.TempDir()
	logDir := dispatch.HostLogDirFor(dir)
	if _, err := os.Stat(logDir); !os.IsNotExist(err) {
		t.Fatalf("Stat(%s) before EnsureLogDirExists: got err=%v, want a not-exist error", logDir, err)
	}

	q := NewHeadlessQueue(nil, nil, noopPending, dir)
	if err := q.EnsureLogDirExists(); err != nil {
		t.Fatalf("EnsureLogDirExists(): %v", err)
	}

	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("Stat(%s) after EnsureLogDirExists: %v", logDir, err)
	}
	if !info.IsDir() {
		t.Fatalf("Stat(%s) after EnsureLogDirExists: got a file, want a directory", logDir)
	}
}

func TestHeadlessQueue_EnsureLogDirExists_Idempotent(t *testing.T) {
	dir := tempLogDir(t)
	q := NewHeadlessQueue(nil, nil, noopPending, dir)

	if err := q.EnsureLogDirExists(); err != nil {
		t.Fatalf("EnsureLogDirExists() on a pre-existing log dir: %v", err)
	}
}

// RunContinuous no longer MkdirAlls dispatch.HostLogDirFor(pwd) itself (#3036).
// It relies entirely on queue.EnsureLogDirExists, called before any Box's
// dispatch.runOnce opens its log file (box.go's os.Create has no fallback when
// the parent is missing). Driving a bare t.TempDir() through the real
// EnsureLogDirExists, not tempLogDir(t), pins the Queue as the one owner.
func TestRunContinuous_HeadlessQueue_CreatesLogDirBeforeDispatch(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	dir := t.TempDir() // not tempLogDir(t): .spindrift/logs must not exist yet
	logDir := dispatch.HostLogDirFor(dir)
	if _, err := os.Stat(logDir); !os.IsNotExist(err) {
		t.Fatalf("Stat(%s) before RunContinuous: got err=%v, want a not-exist error", logDir, err)
	}

	discover := func() (Batch, error) {
		return Batch{Issues: []Issue{{Number: "1", Title: "one"}}}, nil
	}
	claimer := NewLabelClaimer(fc, label, dispatchLabels(c, label).InProgress)
	queue := NewHeadlessQueue(discover, claimer, noopPending, dir)

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	fresh := func() (bool, bool, string) { return false, true, "" }

	if err := RunContinuous(c, nil, fc, fc, f, s, queue, fresh); err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (dispatch.runOnce's os.Create needs the log dir to already exist)", len(fr.RunCalls))
	}
	if info, err := os.Stat(logDir); err != nil || !info.IsDir() {
		t.Fatalf("Stat(%s) after RunContinuous: got info=%v err=%v, want an existing directory", logDir, info, err)
	}
}

// QueueFromDiscoverer's EnsureLogDirExists is a plain no-op, returning nil
// without deriving or creating a directory (issue #3036). This adapter has no
// pwd of its own and its ReportStaleDrain never writes to disk, so it has no
// log directory to create.
func TestDiscoverQueue_EnsureLogDirExists_ReturnsNil(t *testing.T) {
	q := QueueFromDiscoverer(func() (Batch, error) { return Batch{}, nil })

	if err := q.EnsureLogDirExists(); err != nil {
		t.Fatalf("EnsureLogDirExists() = %v, want nil", err)
	}
}
