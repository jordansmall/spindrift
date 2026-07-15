package console

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// TestLauncher_TryLaunch_StaleFreshnessChecker_HoldsNewLaunches verifies a
// Launcher whose Fresh checker reports the loaded image stale never claims
// a queued pick — the pick holds at PickQueued — and StaleStatus reports
// the stale verdict and its message for the banner (issue #652 AC1).
func TestLauncher_TryLaunch_StaleFreshnessChecker_HoldsNewLaunches(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	fr := runner.NewFake()
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	launch := &Launcher{
		CodeForge: f,
		Factory:   factory,
		Settle:    settle.NewFake(),
		Queue:     NewQueue(),
		Fresh:     func() (bool, bool, string) { return true, false, "rebuild needed" },
	}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)
	launch.Wait()

	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls = %+v, want none while stale", fr.RunCalls)
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickQueued {
		t.Errorf("queue pick = %+v, want it still PickQueued", snap)
	}

	stale, msg, rebuilding, rebuildErr := launch.StaleStatus()
	if !stale {
		t.Error("StaleStatus stale = false, want true")
	}
	if msg != "rebuild needed" {
		t.Errorf("StaleStatus message = %q, want %q", msg, "rebuild needed")
	}
	if rebuilding {
		t.Error("StaleStatus rebuilding = true, want false")
	}
	if rebuildErr != "" {
		t.Errorf("StaleStatus rebuildErr = %q, want empty", rebuildErr)
	}
}

// TestLauncher_TryLaunch_StaleDuringRun_RunningBoxFinishesUnaffected
// verifies a Box already running when the freshness checker turns stale
// rides out to its normal settle — staleness only gates a slot refill, it
// never touches an in-flight Dispatch (issue #652 AC2).
func TestLauncher_TryLaunch_StaleDuringRun_RunningBoxFinishesUnaffected(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	fr := runner.NewFake()
	release := make(chan struct{})
	fr.RunFunc = func(runner.Box) error {
		<-release
		return nil
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	var stale atomic.Bool
	launch := &Launcher{
		CodeForge: f,
		Factory:   factory,
		Settle:    settle.NewFake(),
		Queue:     NewQueue(),
		Fresh: func() (bool, bool, string) {
			if stale.Load() {
				return true, false, "rebuild needed"
			}
			return true, true, ""
		},
	}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)

	waitForPickStates(t, launch.Queue, map[string]PickState{"42": PickRunning})
	stale.Store(true)

	close(release)
	launch.Wait()

	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickSettled {
		t.Errorf("queue pick = %+v, want the running Box to settle normally despite staleness", snap)
	}
}
