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

	stale, msg, rebuilding, rebuildErr, _, _ := launch.StaleStatus()
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

// TestLauncher_TryLaunch_NotApplicableFreshnessChecker_DoesNotHoldLaunches
// verifies a Fresh checker reporting Applicable=false — the freshness.Probe
// verdict for a pwd that isn't a git repository (issue #1579), mirroring the
// pre-existing bwrap-runtime not-applicable case — never holds a queued pick:
// dispatch proceeds against the already-loaded image and StaleStatus reports
// no held-launch state, unlike the Applicable=true/Fresh=false case in
// TestLauncher_TryLaunch_StaleFreshnessChecker_HoldsNewLaunches above.
func TestLauncher_TryLaunch_NotApplicableFreshnessChecker_DoesNotHoldLaunches(t *testing.T) {
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
		Fresh:     func() (bool, bool, string) { return false, false, "not applicable (not a git repository)" },
	}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)
	launch.Wait()

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "42" {
		t.Errorf("RunCalls = %+v, want one Box run for #42 despite Applicable=false", fr.RunCalls)
	}
	snap := launch.Queue.Snapshot()
	if len(snap) != 1 || snap[0].State != PickSettled {
		t.Errorf("queue pick = %+v, want it to run and settle, not hold", snap)
	}

	stale, _, rebuilding, rebuildErr, _, _ := launch.StaleStatus()
	if stale {
		t.Error("StaleStatus stale = true, want false when Applicable is false")
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
		RebuildFn: func() (string, string, error) {
			stale.Store(false)
			return "", "", nil
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
	if stale, _, rebuilding, rebuildErr, _, _ := launch.StaleStatus(); stale || rebuilding || rebuildErr != "" {
		t.Errorf("StaleStatus after successful rebuild = stale:%v rebuilding:%v err:%q, want all cleared", stale, rebuilding, rebuildErr)
	}
}

// TestLauncher_Rebuild_Success_PropagatesCapturedOutput verifies a non-empty
// RebuildFn output string threads through to StaleStatus's 5th return value
// — every other Rebuild test in this file returns "", which never exercised
// this leg of the propagation (issue #1129).
func TestLauncher_Rebuild_Success_PropagatesCapturedOutput(t *testing.T) {
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

	const wantOutput = "nix: building '/nix/store/abc-spindrift-1.2.3.drv'...\n"

	launch := &Launcher{
		CodeForge: f,
		Factory:   factory,
		Settle:    settle.NewFake(),
		Queue:     NewQueue(),
		Fresh:     func() (bool, bool, string) { return true, false, "rebuild needed" },
		RebuildFn: func() (string, string, error) { return wantOutput, "", nil },
	}

	launch.Rebuild(f, dir)
	launch.Wait()

	if _, _, _, _, rebuildOutput, _ := launch.StaleStatus(); rebuildOutput != wantOutput {
		t.Errorf("StaleStatus rebuildOutput = %q, want %q", rebuildOutput, wantOutput)
	}
}

// TestLauncher_Rebuild_Success_PropagatesBranchSwitchNotice verifies a
// non-empty RebuildFn notice threads through to StaleStatus's 6th return
// value — the seam consoleGitSync's off-branch switch notice (issue #1141)
// needs to reach the console's rendered status through.
func TestLauncher_Rebuild_Success_PropagatesBranchSwitchNotice(t *testing.T) {
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

	const wantNotice = "switched off-branch tree from feature to main"

	launch := &Launcher{
		CodeForge: f,
		Factory:   factory,
		Settle:    settle.NewFake(),
		Queue:     NewQueue(),
		Fresh:     func() (bool, bool, string) { return true, false, "rebuild needed" },
		RebuildFn: func() (string, string, error) { return "", wantNotice, nil },
	}

	launch.Rebuild(f, dir)
	launch.Wait()

	if _, _, _, _, _, notice := launch.StaleStatus(); notice != wantNotice {
		t.Errorf("StaleStatus branchSwitchNotice = %q, want %q", notice, wantNotice)
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
		RebuildFn: func() (string, string, error) { return "", "", errBoom },
	}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)
	launch.Wait()

	launch.Rebuild(f, dir)
	launch.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, _, rebuildErr, _, _ := launch.StaleStatus(); rebuildErr != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rebuild error never surfaced")
		}
		time.Sleep(time.Millisecond)
	}

	stale, _, rebuilding, rebuildErr, _, _ := launch.StaleStatus()
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

// TestLauncher_Rebuild_WhileOtherSlotAlreadyLatchedStale_ResumesBothPicks
// reproduces the reviewer-flagged race (#652): with MaxParallel=2, one
// slot's refill already saw a stale verdict and latched RunContinuous's own
// one-shot `stale` flag for that whole invocation, while the other slot's
// Box is still running. A concurrent Rebuild success flips the checker
// fresh and calls tryLaunch, but that call is a no-op — the drain that
// launched the running Box hasn't returned yet, so l.launching is still
// true. Once the running Box finishes, RunContinuous's completion callback
// short-circuits on its latched stale flag without ever consulting fresh()
// again, so the *second* pick must not be permanently stranded at
// PickQueued: drain must re-check freshness before deciding to park.
func TestLauncher_Rebuild_WhileOtherSlotAlreadyLatchedStale_ResumesBothPicks(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "first", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Title: "second", Labels: []string{"ready-for-agent"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
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

	// Fresh reports fresh exactly once (slot 1 claiming #42), then stale
	// (slot 2's refill, latching RunContinuous's one-shot flag) — until
	// RebuildFn flips forcedFresh, which every call checks first.
	var calls atomic.Int32
	var forcedFresh atomic.Bool
	launch := &Launcher{
		CodeForge:   f,
		Factory:     factory,
		Settle:      settle.NewFake(),
		Queue:       NewQueue(),
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
	launch.Queue.Add(Pick{Number: "42", Title: "first", State: PickQueued})
	launch.Queue.Add(Pick{Number: "43", Title: "second", State: PickQueued})
	launch.tryLaunch(f, dir)

	waitForPickStates(t, launch.Queue, map[string]PickState{"42": PickRunning, "43": PickQueued})

	launch.Rebuild(f, dir)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, rebuilding, rebuildErr, _, _ := launch.StaleStatus(); !rebuilding && rebuildErr == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rebuild never finished")
		}
		time.Sleep(time.Millisecond)
	}

	close(release42)
	launch.Wait()

	waitForPickStates(t, launch.Queue, map[string]PickState{"42": PickSettled, "43": PickSettled})
}

// TestLauncher_Rebuild_MarksRebuildingWhileInFlight verifies StaleStatus
// reports Rebuilding while RebuildFn is still running — the "progress
// surfaced" half of issue #652 AC3 — and clears it once RebuildFn returns.
func TestLauncher_Rebuild_MarksRebuildingWhileInFlight(t *testing.T) {
	release := make(chan struct{})
	launch := &Launcher{
		Queue:     NewQueue(),
		Fresh:     func() (bool, bool, string) { return true, false, "rebuild needed" },
		RebuildFn: func() (string, string, error) { <-release; return "", "", nil },
	}

	launch.Rebuild(nil, "")

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, rebuilding, _, _, _ := launch.StaleStatus(); rebuilding {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Rebuilding never observed true while RebuildFn was in flight")
		}
		time.Sleep(time.Millisecond)
	}

	close(release)
	launch.Wait()

	if _, _, rebuilding, _, _, _ := launch.StaleStatus(); rebuilding {
		t.Error("Rebuilding = true after RebuildFn returned, want false")
	}
}

// TestLauncher_Rebuild_Retry_ClearsPriorErrorImmediately verifies a retry's
// Rebuild call clears the previous attempt's rebuildErr as soon as the
// launch guard passes, not only once the retry's own RebuildFn returns —
// otherwise StaleStatus briefly reports rebuilding=true alongside the stale
// error from the prior failed attempt (issue #760).
func TestLauncher_Rebuild_Retry_ClearsPriorErrorImmediately(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	launch := &Launcher{
		Queue: NewQueue(),
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
	if _, _, _, rebuildErr, _, _ := launch.StaleStatus(); rebuildErr != errBoom.Error() {
		t.Fatalf("rebuildErr after first attempt = %q, want %q", rebuildErr, errBoom.Error())
	}

	launch.Rebuild(nil, "")

	deadline := time.Now().Add(2 * time.Second)
	var rebuilding bool
	var rebuildErr string
	for {
		if _, _, rebuilding, rebuildErr, _, _ = launch.StaleStatus(); rebuilding {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry's Rebuilding never observed true")
		}
		time.Sleep(time.Millisecond)
	}

	if !rebuilding || rebuildErr != "" {
		t.Errorf("StaleStatus mid-retry = rebuilding:%v rebuildErr:%q, want rebuilding:true rebuildErr:\"\"", rebuilding, rebuildErr)
	}

	close(release)
	launch.Wait()
}

// TestLauncher_FreshnessChecker_SignalsOnlyOnFreshToStaleTransition verifies
// the closure freshnessChecker returns calls signalRefresh only on the
// fresh->stale edge, not on every verdict (issue #1124): a repeated stale
// verdict and a fresh verdict must not signal, since Rebuild already signals
// the stale->fresh clear itself.
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
