// Package contract is the executable lifecycle contract for runner.Runner
// (issue #5680): one shared suite the OCI adapter, the bwrap adapter and
// runner.Fake each run from their own package, so semantic drift between them
// fails CI. It is a sibling of runnertest because it imports runner, which
// runnertest must not (runner's own tests import runnertest).
package contract

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

const (
	// runReturnTimeout bounds the wait for a killed Run to return.
	runReturnTimeout = 10 * time.Second
	// settleTimeout bounds polling for async state (bwrap's cgroup-based
	// IsRunning lags an async Kill).
	settleTimeout = 10 * time.Second
)

// LifecycleHarness builds a fresh Runner per scenario, each over a world the
// contract cannot reach through the Runner interface alone. Methods take the
// scenario's t so a harness can swap package-global seams and restore them
// with t.Cleanup.
type LifecycleHarness interface {
	// Exiting returns a Runner with nothing running whose Run of a Box named
	// name exits that Box with code.
	Exiting(t *testing.T, name string, code int) runner.Runner
	// Foreign returns a Runner that sees a Box named name already running,
	// started outside it (a sibling launcher's).
	Foreign(t *testing.T, name string) runner.Runner
	// Killable returns a Runner whose Run of a Box named name keeps the Box
	// live until Kill(name), and started, which blocks until Kill can reach it.
	Killable(t *testing.T, name string) (r runner.Runner, started func())
}

// Lifecycle runs the shared lifecycle conformance suite against h. Scenarios
// run sequentially: harnesses may swap package-global seams.
func Lifecycle(t *testing.T, h LifecycleHarness) {
	t.Run("Idle", func(t *testing.T) { testIdle(t, h) })
	t.Run("RunExitZero", func(t *testing.T) { testRunExitZero(t, h) })
	t.Run("RunNonZeroExit", func(t *testing.T) { testRunNonZeroExit(t, h) })
	t.Run("AlreadyRunning", func(t *testing.T) { testAlreadyRunning(t, h) })
	t.Run("Kill", func(t *testing.T) { testKill(t, h) })
	t.Run("RemoveHostTree", func(t *testing.T) { testRemoveHostTree(t, h) })
}

const boxName = "contract-box"

func testIdle(t *testing.T, h LifecycleHarness) {
	r := h.Exiting(t, boxName, 0)
	if r.IsRunning(boxName) {
		t.Error("IsRunning before any Run: got true, want false")
	}
	assertListed(t, r, false)
	if err := r.Kill(boxName); err != nil {
		t.Errorf("Kill of a Box that is not running: got %v, want nil (a sandbox already gone is not an error)", err)
	}
}

func testRunExitZero(t *testing.T, h LifecycleHarness) {
	r := h.Exiting(t, boxName, 0)
	if err := r.Run(runner.Box{Name: boxName}); err != nil {
		t.Fatalf("Run of a Box exiting 0: got %v, want nil", err)
	}
}

func testRunNonZeroExit(t *testing.T, h LifecycleHarness) {
	const code = 3
	r := h.Exiting(t, boxName, code)
	err := r.Run(runner.Box{Name: boxName})
	var runErr *runner.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("Run of a Box exiting %d: got %v, want a *runner.RunError", code, err)
	}
	if runErr.ExitCode != code {
		t.Errorf("RunError.ExitCode: got %d, want %d", runErr.ExitCode, code)
	}
	if runner.KilledBySignal(err) {
		t.Errorf("KilledBySignal(exit %d): got true, want false", code)
	}
}

func testAlreadyRunning(t *testing.T, h LifecycleHarness) {
	r := h.Foreign(t, boxName)
	if !r.IsRunning(boxName) {
		t.Error("IsRunning of a Box started elsewhere: got false, want true")
	}
	assertListed(t, r, true)
	if err := r.Run(runner.Box{Name: boxName}); !errors.Is(err, runner.ErrAlreadyRunning) {
		t.Errorf("Run of a Box already running: got %v, want runner.ErrAlreadyRunning", err)
	}
}

func testKill(t *testing.T, h LifecycleHarness) {
	r, started := h.Killable(t, boxName)
	runDone := make(chan error, 1)
	go func() { runDone <- r.Run(runner.Box{Name: boxName}) }()
	started()

	if !r.IsRunning(boxName) {
		t.Error("IsRunning of a live Box: got false, want true")
	}
	assertListed(t, r, true)
	if err := r.Run(runner.Box{Name: boxName}); !errors.Is(err, runner.ErrAlreadyRunning) {
		t.Errorf("second Run of a live Box: got %v, want runner.ErrAlreadyRunning", err)
	}

	if err := r.Kill(boxName); err != nil {
		t.Fatalf("Kill of a live Box: got %v, want nil", err)
	}
	select {
	case err := <-runDone:
		if !runner.KilledBySignal(err) {
			t.Errorf("Run of a killed Box: got %v, want an error with KilledBySignal true", err)
		}
	case <-time.After(runReturnTimeout):
		t.Fatalf("Run did not return within %s of Kill", runReturnTimeout)
	}

	runnertest.Eventually(t, settleTimeout, "IsRunning false after Kill", func() bool { return !r.IsRunning(boxName) })
	if err := r.Kill(boxName); err != nil {
		t.Errorf("second Kill of a gone Box: got %v, want nil", err)
	}
}

func testRemoveHostTree(t *testing.T, h LifecycleHarness) {
	r := h.Exiting(t, boxName, 0)
	root := filepath.Join(t.TempDir(), "tree")
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(root, "top.txt"), filepath.Join(sub, "leaf.txt")} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := r.RemoveHostTree(root); err != nil {
		t.Fatalf("RemoveHostTree: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("tree after RemoveHostTree: Stat err = %v, want not-exist", err)
	}
	if err := r.RemoveHostTree(root); err != nil {
		t.Errorf("RemoveHostTree of an already-gone path: got %v, want nil", err)
	}
}

// assertListed checks that ListRunning succeeds and does (or does not) name
// boxName.
func assertListed(t *testing.T, r runner.Runner, want bool) {
	t.Helper()
	names, err := r.ListRunning()
	if err != nil {
		t.Errorf("ListRunning: got error %v, want nil", err)
		return
	}
	if got := slices.Contains(names, boxName); got != want {
		t.Errorf("ListRunning = %v: contains %q = %v, want %v", names, boxName, got, want)
	}
}
