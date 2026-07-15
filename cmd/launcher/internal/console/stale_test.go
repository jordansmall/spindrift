package console

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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

// TestLauncher_Rebuild_Success_ClearsStaleAndResumesHeldLaunch verifies a
// successful RebuildFn clears the stale gate and resumes draining, so a
// pick that held at PickQueued through the stale window launches without
// being re-picked (issue #652 AC3/AC4).
func TestLauncher_Rebuild_Success_ClearsStaleAndResumesHeldLaunch(t *testing.T) {
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

	var stale atomic.Bool
	stale.Store(true)
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
		RebuildFn: func() error {
			stale.Store(false)
			return nil
		},
	}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)
	launch.Wait()

	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls = %+v, want none before rebuild", fr.RunCalls)
	}

	launch.Rebuild(f, dir)
	launch.Wait()

	waitForPickStates(t, launch.Queue, map[string]PickState{"42": PickSettled})
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "42" {
		t.Errorf("RunCalls = %+v, want one Box run for #42 after rebuild", fr.RunCalls)
	}
	if stale, _, rebuilding, rebuildErr := launch.StaleStatus(); stale || rebuilding || rebuildErr != "" {
		t.Errorf("StaleStatus after successful rebuild = stale:%v rebuilding:%v err:%q, want all cleared", stale, rebuilding, rebuildErr)
	}
}

// TestLauncher_Rebuild_Failure_SurfacesErrorAndKeepsHeld verifies a failing
// RebuildFn surfaces the error through StaleStatus and leaves the queued
// pick held — a failed rebuild must never silently resume launches (issue
// #652 AC5).
func TestLauncher_Rebuild_Failure_SurfacesErrorAndKeepsHeld(t *testing.T) {
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
		RebuildFn: func() error { return errBoom },
	}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)
	launch.Wait()

	launch.Rebuild(f, dir)
	launch.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, _, rebuildErr := launch.StaleStatus(); rebuildErr != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rebuild error never surfaced")
		}
		time.Sleep(time.Millisecond)
	}

	stale, _, rebuilding, rebuildErr := launch.StaleStatus()
	if !stale {
		t.Error("stale = false after a failed rebuild, want true (still held)")
	}
	if rebuilding {
		t.Error("rebuilding = true after the rebuild finished, want false")
	}
	if rebuildErr != errBoom.Error() {
		t.Errorf("rebuildErr = %q, want %q", rebuildErr, errBoom.Error())
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls = %+v, want none after a failed rebuild", fr.RunCalls)
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickQueued {
		t.Errorf("queue pick = %+v, want it still held at PickQueued", snap)
	}
}

// TestLauncher_Rebuild_MarksRebuildingWhileInFlight verifies StaleStatus
// reports Rebuilding while RebuildFn is still running — the "progress
// surfaced" half of issue #652 AC3 — and clears it once RebuildFn returns.
func TestLauncher_Rebuild_MarksRebuildingWhileInFlight(t *testing.T) {
	release := make(chan struct{})
	launch := &Launcher{
		Queue:     NewQueue(),
		Fresh:     func() (bool, bool, string) { return true, false, "rebuild needed" },
		RebuildFn: func() error { <-release; return nil },
	}

	launch.Rebuild(nil, "")

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, rebuilding, _ := launch.StaleStatus(); rebuilding {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Rebuilding never observed true while RebuildFn was in flight")
		}
		time.Sleep(time.Millisecond)
	}

	close(release)
	launch.Wait()

	if _, _, rebuilding, _ := launch.StaleStatus(); rebuilding {
		t.Error("Rebuilding = true after RebuildFn returned, want false")
	}
}
