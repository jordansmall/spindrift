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

// A stale Fresh verdict never claims a queued pick, so the pick holds at
// PickQueued, and StaleStatus reports the verdict and its message for the
// banner (issue #652 AC1).
func TestLauncher_TryLaunch_StaleFreshnessChecker_HoldsNewLaunches(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
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
		queue:     NewQueue(),
		Fresh:     func() (bool, bool, string) { return true, false, "rebuild needed" },
	}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)
	launch.Wait()

	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls = %+v, want none while stale", fr.RunCalls)
	}
	snap := launch.queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickQueued {
		t.Errorf("queue pick = %+v, want it still PickQueued", snap)
	}

	status := launch.StaleStatus()
	if !status.Stale {
		t.Error("StaleStatus stale = false, want true")
	}
	if status.Message != "rebuild needed" {
		t.Errorf("StaleStatus message = %q, want %q", status.Message, "rebuild needed")
	}
	if status.Rebuilding {
		t.Error("StaleStatus rebuilding = true, want false")
	}
	if status.Err != "" {
		t.Errorf("StaleStatus err = %q, want empty", status.Err)
	}
}

// A Fresh checker reporting Applicable=false, the freshness.Probe verdict for a
// pwd that isn't a git repository (issue #1579), never holds a queued pick:
// dispatch proceeds against the already-loaded image and StaleStatus reports no
// held-launch state, unlike the Applicable=true, Fresh=false case above.
func TestLauncher_TryLaunch_NotApplicableFreshnessChecker_DoesNotHoldLaunches(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
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
		queue:     NewQueue(),
		Fresh:     func() (bool, bool, string) { return false, false, "not applicable (not a git repository)" },
	}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)
	launch.Wait()

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "42" {
		t.Errorf("RunCalls = %+v, want one Box run for #42 despite Applicable=false", fr.RunCalls)
	}
	snap := launch.queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickSettled {
		t.Errorf("queue pick = %+v, want it to run and settle, not hold", snap)
	}

	status := launch.StaleStatus()
	if status.Stale {
		t.Error("StaleStatus stale = true, want false when Applicable is false")
	}
	if status.Rebuilding {
		t.Error("StaleStatus rebuilding = true, want false")
	}
	if status.Err != "" {
		t.Errorf("StaleStatus err = %q, want empty", status.Err)
	}
}

// Staleness only gates a slot refill, so a Box already running when the checker
// turns stale rides out to its normal settle (issue #652 AC2).
func TestLauncher_TryLaunch_StaleDuringRun_RunningBoxFinishesUnaffected(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
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
		queue:     NewQueue(),
		Fresh: func() (bool, bool, string) {
			if stale.Load() {
				return true, false, "rebuild needed"
			}
			return true, true, ""
		},
	}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)

	waitForPickStates(t, launch.queue, map[string]PickState{"42": PickRunning})
	stale.Store(true)

	close(release)
	launch.Wait()

	snap := launch.queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickSettled {
		t.Errorf("queue pick = %+v, want the running Box to settle normally despite staleness", snap)
	}
}

// A successful RebuildFn clears the stale gate and resumes draining, so a pick
// that held at PickQueued through the stale window launches without being
// re-picked (issue #652 AC3/AC4).
func TestLauncher_Rebuild_Success_ClearsStaleAndResumesHeldLaunch(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
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
		queue:     NewQueue(),
		Fresh: func() (bool, bool, string) {
			if stale.Load() {
				return true, false, "rebuild needed"
			}
			return true, true, ""
		},
		RebuildFn: func() (string, string, error) {
			stale.Store(false)
			return "", "", nil
		},
	}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)
	launch.Wait()

	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls = %+v, want none before rebuild", fr.RunCalls)
	}

	launch.Rebuild(f, dir)
	launch.Wait()

	waitForPickStates(t, launch.queue, map[string]PickState{"42": PickSettled})
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "42" {
		t.Errorf("RunCalls = %+v, want one Box run for #42 after rebuild", fr.RunCalls)
	}
	if status := launch.StaleStatus(); status.Stale || status.Rebuilding || status.Err != "" {
		t.Errorf("StaleStatus after successful rebuild = stale:%v rebuilding:%v err:%q, want all cleared", status.Stale, status.Rebuilding, status.Err)
	}
}

// A non-empty RebuildFn output threads through to StaleStatus. Every other
// Rebuild test here returns "", so this leg was never exercised (issue #1129).
func TestLauncher_Rebuild_Success_PropagatesCapturedOutput(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
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

	const wantOutput = "nix: building '/nix/store/abc-spindrift-1.2.3.drv'...\n"

	launch := &Launcher{
		CodeForge: f,
		Factory:   factory,
		Settle:    settle.NewFake(),
		queue:     NewQueue(),
		Fresh:     func() (bool, bool, string) { return true, false, "rebuild needed" },
		RebuildFn: func() (string, string, error) { return wantOutput, "", nil },
	}

	launch.Rebuild(f, dir)
	launch.Wait()

	if status := launch.StaleStatus(); status.Output != wantOutput {
		t.Errorf("StaleStatus output = %q, want %q", status.Output, wantOutput)
	}
}

// A non-empty RebuildFn notice threads through to StaleStatus, so
// consoleGitSync's off-branch switch notice (issue #1141) reaches the console's
// rendered status.
func TestLauncher_Rebuild_Success_PropagatesBranchSwitchNotice(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
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

	const wantNotice = "switched off-branch tree from feature to main"

	launch := &Launcher{
		CodeForge: f,
		Factory:   factory,
		Settle:    settle.NewFake(),
		queue:     NewQueue(),
		Fresh:     func() (bool, bool, string) { return true, false, "rebuild needed" },
		RebuildFn: func() (string, string, error) { return "", wantNotice, nil },
	}

	launch.Rebuild(f, dir)
	launch.Wait()

	if status := launch.StaleStatus(); status.BranchSwitchNotice != wantNotice {
		t.Errorf("StaleStatus branchSwitchNotice = %q, want %q", status.BranchSwitchNotice, wantNotice)
	}
}

// A failing RebuildFn surfaces the error through StaleStatus and leaves the
// queued pick held. A failed rebuild must never silently resume launches
// (issue #652 AC5).
func TestLauncher_Rebuild_Failure_SurfacesErrorAndKeepsHeld(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
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
		queue:     NewQueue(),
		Fresh:     func() (bool, bool, string) { return true, false, "rebuild needed" },
		RebuildFn: func() (string, string, error) { return "", "", errBoom },
	}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)
	launch.Wait()

	launch.Rebuild(f, dir)
	launch.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if status := launch.StaleStatus(); status.Err != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rebuild error never surfaced")
		}
		time.Sleep(time.Millisecond)
	}

	status := launch.StaleStatus()
	if !status.Stale {
		t.Error("stale = false after a failed rebuild, want true (still held)")
	}
	if status.Rebuilding {
		t.Error("rebuilding = true after the rebuild finished, want false")
	}
	if status.Err != errBoom.Error() {
		t.Errorf("err = %q, want %q", status.Err, errBoom.Error())
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls = %+v, want none after a failed rebuild", fr.RunCalls)
	}
	snap := launch.queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickQueued {
		t.Errorf("queue pick = %+v, want it still held at PickQueued", snap)
	}
}

// This test reproduces the reviewer-flagged race (#652): with MaxParallel=2,
// slot 2's refill latched RunContinuous's one-shot stale flag while slot 1's
// Box was still running, so a concurrent Rebuild's tryLaunch was a no-op
// (l.launching still true) and the completion callback never consulted fresh()
// again. Drain must re-check freshness or the second pick strands at PickQueued.
func TestLauncher_Rebuild_WhileOtherSlotAlreadyLatchedStale_ResumesBothPicks(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "first", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Title: "second", Labels: []string{"ready-for-agent"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	fr := runner.NewFake()
	release42 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		if box.Issue == "42" {
			<-release42
		}
		return nil
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	// Fresh reports fresh exactly once (slot 1 claiming #42), then stale, which
	// latches RunContinuous's one-shot flag on slot 2's refill, until RebuildFn
	// flips forcedFresh, which every call checks first.
	var calls atomic.Int32
	var forcedFresh atomic.Bool
	launch := &Launcher{
		CodeForge:   f,
		Factory:     factory,
		Settle:      settle.NewFake(),
		queue:       NewQueue(),
		MaxParallel: 2,
		Fresh: func() (bool, bool, string) {
			if forcedFresh.Load() {
				return true, true, ""
			}
			if calls.Add(1) <= 1 {
				return true, true, ""
			}
			return true, false, "rebuild needed"
		},
		RebuildFn: func() (string, string, error) {
			forcedFresh.Store(true)
			return "", "", nil
		},
	}
	launch.queue.Add(Pick{Number: "42", Title: "first", State: PickQueued})
	launch.queue.Add(Pick{Number: "43", Title: "second", State: PickQueued})
	launch.tryLaunch(f, dir)

	waitForPickStates(t, launch.queue, map[string]PickState{"42": PickRunning, "43": PickQueued})

	launch.Rebuild(f, dir)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if status := launch.StaleStatus(); !status.Rebuilding && status.Err == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rebuild never finished")
		}
		time.Sleep(time.Millisecond)
	}

	close(release42)
	launch.Wait()

	waitForPickStates(t, launch.queue, map[string]PickState{"42": PickSettled, "43": PickSettled})
}

// StaleStatus reports Rebuilding while RebuildFn is still running, the progress
// half of issue #652 AC3, and clears it once RebuildFn returns.
func TestLauncher_Rebuild_MarksRebuildingWhileInFlight(t *testing.T) {
	release := make(chan struct{})
	launch := &Launcher{
		queue:     NewQueue(),
		Fresh:     func() (bool, bool, string) { return true, false, "rebuild needed" },
		RebuildFn: func() (string, string, error) { <-release; return "", "", nil },
	}

	launch.Rebuild(nil, "")

	deadline := time.Now().Add(2 * time.Second)
	for {
		if status := launch.StaleStatus(); status.Rebuilding {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Rebuilding never observed true while RebuildFn was in flight")
		}
		time.Sleep(time.Millisecond)
	}

	close(release)
	launch.Wait()

	if status := launch.StaleStatus(); status.Rebuilding {
		t.Error("Rebuilding = true after RebuildFn returned, want false")
	}
}

// A retry's Rebuild clears the previous attempt's rebuildErr as soon as the
// launch guard passes, not only once the retry's own RebuildFn returns.
// Otherwise StaleStatus briefly reports rebuilding=true alongside the stale
// error from the prior failed attempt (issue #760).
func TestLauncher_Rebuild_Retry_ClearsPriorErrorImmediately(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	launch := &Launcher{
		queue: NewQueue(),
		Fresh: func() (bool, bool, string) { return true, false, "rebuild needed" },
		RebuildFn: func() (string, string, error) {
			if calls.Add(1) == 1 {
				return "", "", errBoom
			}
			<-release
			return "", "", nil
		},
	}

	launch.Rebuild(nil, "")
	launch.Wait()
	if status := launch.StaleStatus(); status.Err != errBoom.Error() {
		t.Fatalf("err after first attempt = %q, want %q", status.Err, errBoom.Error())
	}

	launch.Rebuild(nil, "")

	deadline := time.Now().Add(2 * time.Second)
	var status RebuildStatus
	for {
		if status = launch.StaleStatus(); status.Rebuilding {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry's Rebuilding never observed true")
		}
		time.Sleep(time.Millisecond)
	}

	if !status.Rebuilding || status.Err != "" {
		t.Errorf("StaleStatus mid-retry = rebuilding:%v err:%q, want rebuilding:true err:\"\"", status.Rebuilding, status.Err)
	}

	close(release)
	launch.Wait()
}

// The closure that freshnessChecker returns calls signalRefresh only when a
// fresh verdict turns stale, not on every verdict (issue #1124). A repeated
// stale verdict and a fresh verdict must not signal, since Rebuild already
// signals the clear back to fresh itself.
func TestLauncher_FreshnessChecker_SignalsOnlyOnFreshToStaleTransition(t *testing.T) {
	var fresh bool
	launch := &Launcher{
		Fresh: func() (bool, bool, string) { return true, fresh, "" },
	}
	checker := launch.freshnessChecker()
	signals := launch.Refreshes()

	drain := func() bool {
		select {
		case <-signals:
			return true
		default:
			return false
		}
	}

	fresh = true
	checker()
	if drain() {
		t.Error("fresh verdict signaled, want no signal")
	}

	fresh = false
	checker()
	if !drain() {
		t.Error("fresh->stale verdict did not signal, want signal")
	}

	checker()
	if drain() {
		t.Error("repeated stale verdict signaled, want no signal")
	}

	fresh = true
	checker()
	if drain() {
		t.Error("stale->fresh verdict signaled, want no signal (Rebuild's own signal covers this)")
	}
}
