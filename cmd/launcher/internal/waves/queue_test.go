package waves

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/testutil"
)

// TestDiscoverQueue_Pending_ReturnsError verifies QueueFromDiscoverer's
// Pending() errors rather than fabricating a confirmed-looking 0 (#2939
// review finding): a caller that reaches Pending despite
// QueueFromDiscoverer being documented as Discover-only must be routed to
// RunContinuous's heldBackUnknown path, not handed a fake confirmed count.
func TestDiscoverQueue_Pending_ReturnsError(t *testing.T) {
	q := QueueFromDiscoverer(func() (Batch, error) { return Batch{}, nil })

	n, err := q.Pending()
	if err == nil {
		t.Fatalf("Pending() = (%d, nil), want a non-nil error", n)
	}
}

// TestHeadlessQueue_Pending_DelegatesToClosure verifies Pending() forwards
// straight to the injected closure (#2939) -- discover/claimer stay nil
// since this test never calls Discover or Claim.
func TestHeadlessQueue_Pending_DelegatesToClosure(t *testing.T) {
	q := NewHeadlessQueue(nil, nil, func(map[string]bool) (int, error) { return 5, nil }, "")

	n, err := q.Pending()
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

	_, err := q.Pending()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Pending() error = %v, want %v", err, wantErr)
	}
}

// fakeClaimer is a Claimer whose Claim always succeeds -- the minimal fake
// TestHeadlessQueue_Claim_FeedsPendingClaimedSet needs to exercise
// headlessQueue.Claim without a real forge.IssueTracker.
type fakeClaimer struct{}

func (fakeClaimer) Claim(string) error { return nil }

// TestHeadlessQueue_Claim_FeedsPendingClaimedSet is the regression test for
// #2939's dropClaimed omission: headlessQueue must record every successful
// Claim so a later Pending() call excludes it, without RunContinuous having
// to expose its own private claimed map. Claim("42") followed by Pending()
// must observe claimed["42"] == true inside the pending closure.
func TestHeadlessQueue_Claim_FeedsPendingClaimedSet(t *testing.T) {
	var observed map[string]bool
	pending := func(claimed map[string]bool) (int, error) {
		observed = claimed
		return 0, nil
	}
	q := NewHeadlessQueue(nil, fakeClaimer{}, pending, "")

	if err := q.Claim("42"); err != nil {
		t.Fatalf("Claim(42): %v", err)
	}
	if _, err := q.Pending(); err != nil {
		t.Fatalf("Pending: %v", err)
	}

	if !observed["42"] {
		t.Fatalf("Pending's claimed set = %v, want claimed[%q] = true", observed, "42")
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
