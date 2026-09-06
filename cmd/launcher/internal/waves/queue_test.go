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

// TestDiscoverQueue_Pending_ReturnsError verifies QueueFromDiscoverer's
// Pending() errors rather than fabricating a confirmed-looking 0 (#2939
// review finding): a caller that reaches Pending despite
// QueueFromDiscoverer being documented as Discover-only must be routed to
// RunContinuous's heldBackUnknown path, not handed a fake confirmed count.
func TestDiscoverQueue_Pending_ReturnsError(t *testing.T) {
	q := QueueFromDiscoverer(func() (Batch, error) { return Batch{}, nil })

	n, err := q.Pending(nil)
	if err == nil {
		t.Fatalf("Pending() = (%d, nil), want a non-nil error", n)
	}
}

// TestHeadlessQueue_Pending_DelegatesToClosure verifies Pending() forwards
// straight to the injected closure (#2939) -- discover/claimer stay nil
// since this test never calls Discover or Claim.
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

// TestHeadlessQueue_Pending_PropagatesError verifies a Pending closure error
// round-trips unchanged through headlessQueue, rather than being swallowed
// or wrapped.
func TestHeadlessQueue_Pending_PropagatesError(t *testing.T) {
	wantErr := errors.New("transient query failure")
	q := NewHeadlessQueue(nil, nil, func(map[string]bool) (int, error) { return 0, wantErr }, "")

	_, err := q.Pending(nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Pending() error = %v, want %v", err, wantErr)
	}
}

// TestHeadlessQueue_Pending_ForwardsCallersClaimedSet verifies
// headlessQueue.Pending forwards whatever claimed map its caller hands it
// straight to the pending closure, verbatim -- headlessQueue keeps no
// claimed set of its own to merge in (issue #3035).
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

// TestHeadlessQueue_ReportStaleDrain_PrintsAndAppendsLog verifies
// ReportStaleDrain prints report.Console() to stdout and appends
// report.HostLog() to pwd's stale-drain.log (#2939, mirroring
// continuous.go's emitStaleDrainReport).
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

// TestHeadlessQueue_ReportStaleDrain_OpenFailureLogsToStderr verifies a
// review finding on #2678: ReportStaleDrain's stale-drain.log open failure
// is swallowed to stderr rather than failing the run, and does not crash or
// panic. dir is a directory whose .spindrift/logs subdirectory was never
// created (unlike every real RunContinuous call, which always
// os.MkdirAll's it first), so dispatch.HostLogDirFor(dir) names a path with
// no such directory and os.OpenFile's O_CREATE cannot create the file
// inside a missing parent.
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

// TestHeadlessQueue_EnsureLogDirExists_CreatesLogDir verifies
// EnsureLogDirExists actually creates dispatch.HostLogDirFor(pwd) under a
// fresh temp dir that does not have it yet (issue #3036) -- unlike
// tempLogDir(t), dir here is a bare t.TempDir() whose .spindrift/logs
// subdirectory was never pre-created.
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

// TestHeadlessQueue_EnsureLogDirExists_Idempotent verifies a second call
// against an already-existing log dir still succeeds, mirroring
// os.MkdirAll's own idempotence.
func TestHeadlessQueue_EnsureLogDirExists_Idempotent(t *testing.T) {
	dir := tempLogDir(t)
	q := NewHeadlessQueue(nil, nil, noopPending, dir)

	if err := q.EnsureLogDirExists(); err != nil {
		t.Fatalf("EnsureLogDirExists() on a pre-existing log dir: %v", err)
	}
}

// TestRunContinuous_HeadlessQueue_CreatesLogDirBeforeDispatch pins the
// consolidated log-directory ownership this issue (#3036) sets up:
// RunContinuous no longer MkdirAlls dispatch.HostLogDirFor(pwd) itself --
// it relies entirely on queue.EnsureLogDirExists, called before any Box's
// dispatch.runOnce opens its log file (box.go's os.Create has no fallback
// when the parent is missing). Driving through NewHeadlessQueue's real,
// non-no-op EnsureLogDirExists over a bare t.TempDir() -- deliberately not
// tempLogDir(t)'s pre-created variant every other RunContinuous scenario in
// this package uses -- proves RunContinuous has exactly one source of
// truth for this directory: the Queue it was handed.
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

// TestDiscoverQueue_EnsureLogDirExists_ReturnsNil verifies
// QueueFromDiscoverer's EnsureLogDirExists is a plain no-op: it returns nil
// without ever deriving or creating a directory (issue #3036) -- this
// adapter has no pwd of its own and its ReportStaleDrain never writes to
// disk either, so it has no log directory to create.
func TestDiscoverQueue_EnsureLogDirExists_ReturnsNil(t *testing.T) {
	q := QueueFromDiscoverer(func() (Batch, error) { return Batch{}, nil })

	if err := q.EnsureLogDirExists(); err != nil {
		t.Fatalf("EnsureLogDirExists() = %v, want nil", err)
	}
}
