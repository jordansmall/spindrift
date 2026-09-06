package console

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/waves"
	"spindrift.dev/launcher/internal/waves/wavestest"
)

// TestRunContinuousQueue_Pending verifies Pending delegates to the closure
// it was built with rather than hardcoding a value -- unlike headless's
// waves.QueueFromDiscoverer adapter, whose Pending is an unconditional
// no-op, nothing in this package exercised
// runContinuousQueue.Pending/.ReportStaleDrain directly before this test.
func TestRunContinuousQueue_Pending(t *testing.T) {
	q := runContinuousQueue{pending: func() int { return 3 }}

	got, err := q.Pending(nil)
	if got != 3 {
		t.Errorf("Pending() = %d, want 3", got)
	}
	if err != nil {
		t.Errorf("Pending() err = %v, want nil", err)
	}
}

// TestRunContinuousQueue_ReportStaleDrain verifies ReportStaleDrain
// actually invokes the closure it was built with.
func TestRunContinuousQueue_ReportStaleDrain(t *testing.T) {
	called := false
	q := runContinuousQueue{report: func(waves.StaleDrainReport) { called = true }}

	q.ReportStaleDrain(waves.StaleDrainReport{})

	if !called {
		t.Error("ReportStaleDrain did not invoke the report closure")
	}
}

// TestRunContinuousQueue_EnsureLogDirExists verifies EnsureLogDirExists is a
// plain no-op: it returns nil and never touches the filesystem (issue
// #3036) -- Console's Queue has no on-disk log directory of its own,
// unlike headless's filesystem-backed adapter.
func TestRunContinuousQueue_EnsureLogDirExists(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, ".spindrift", "logs")

	q := runContinuousQueue{}
	if err := q.EnsureLogDirExists(); err != nil {
		t.Fatalf("EnsureLogDirExists() = %v, want nil", err)
	}
	if _, err := os.Stat(logDir); !os.IsNotExist(err) {
		t.Fatalf("Stat(%s): got err=%v, want a not-exist error (runContinuousQueue must not touch the filesystem)", logDir, err)
	}
}

// consoleHarness backs the shared wavestest.RunQueueContract suite with a
// bare runContinuousQueue.
type consoleHarness struct {
	// discoverCalls is a pointer so it survives struct copies --
	// consoleHarness is a plain value struct passed by value into
	// wavestest.RunQueueContract.
	discoverCalls *int
}

func newConsoleHarness() consoleHarness {
	return consoleHarness{discoverCalls: new(int)}
}

func (h consoleHarness) Queue() waves.Queue {
	return runContinuousQueue{
		discover: func() (waves.Batch, error) { *h.discoverCalls++; return waves.Batch{}, nil },
		pending:  func() int { return 0 },
		report:   func(waves.StaleDrainReport) {},
	}
}

// SeedDispatchable is a no-op: runContinuousQueue.Claim is a permanent
// no-op regardless of backing state (see its doc comment in launcher.go),
// so there is no dispatchable state for this harness to seed.
func (consoleHarness) SeedDispatchable(num string) {}

// ClaimTransitions and DiscoverCalls implement wavestest.SideEffectObserver:
// DiscoverCalls counts this harness's own discover closure, so a leak like
// Pending calling through to Discover shows up here. ClaimTransitions is a
// constant 0 -- runContinuousQueue.Claim is a documented permanent no-op
// with no backing collaborator a leak could transition.
func (h consoleHarness) ClaimTransitions() int { return 0 }
func (h consoleHarness) DiscoverCalls() int    { return *h.discoverCalls }

var _ wavestest.SideEffectObserver = consoleHarness{}

func TestRunContinuousQueue_QueueContract(t *testing.T) {
	wavestest.RunQueueContract(t, newConsoleHarness())
}
