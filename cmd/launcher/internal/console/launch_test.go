package console

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/testutil"
	"spindrift.dev/launcher/internal/waves"
)

// Pins #646: the continuous engine drains a scripted operator queue and runs
// one Dispatch end to end through the Discoverer seam, claiming the queued
// row, running a Box, and settling, while the engine itself stays UI-blind.
func TestRunContinuous_DrainsScriptedQueue_LaunchesOneDispatchEndToEnd(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	fr := runner.NewFake()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	inner := settle.NewFake()
	qs := queueSettler{Settler: inner, q: q}

	discover := func() (waves.Batch, error) {
		return q.Discover(f, f, "", KindWork)
	}
	fresh := func() (bool, bool, string) { return false, true, "" }

	err = waves.RunContinuous(waves.Config{MaxParallel: 1}, nil, f, f, factory, qs, waves.QueueFromDiscoverer(discover), fresh)
	if err != nil {
		t.Fatalf("RunContinuous: %v", err)
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "42" {
		t.Fatalf("RunCalls = %+v, want one Box run for #42", fr.RunCalls)
	}
	if len(inner.SettleCalls) != 1 || inner.SettleCalls[0].Num != "42" {
		t.Fatalf("SettleCalls = %+v, want one settle for #42", inner.SettleCalls)
	}
	if got := q.Snapshot()[0].State; got != PickSettled {
		t.Errorf("pick state = %v, want settled", got)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(iss, "agent-in-progress") {
		t.Errorf("issue #42 labels = %v, want agent-in-progress (claimed)", iss.Labels)
	}
}

// Pins the coupling launcher.go's drain loop relies on (#706): Queue.Discover
// (queue.go) already performs the Dispatchable to InProgress transition when
// it claims the pick, and RunContinuous's own claim goes through
// waves.QueueFromDiscoverer's no-op Claim (#2938), so exactly one
// TransitionState call happens.
func TestRunContinuous_ConsoleConfig_SkipsRedundantClaim(t *testing.T) {
	f, _, factory, qs, discover, fresh := setupForgeQueueFactory(t)

	// The zero-value Label/InProgressLabel/OverlapGate match launcher.go's own
	// waves.Config construction. MaxParallel stands in for the Limiter that
	// field would otherwise build internally.
	err := waves.RunContinuous(waves.Config{MaxParallel: 1}, nil, f, f, factory, qs, waves.QueueFromDiscoverer(discover), fresh)
	if err != nil {
		t.Fatalf("RunContinuous: %v", err)
	}

	if len(f.TransitionStateCalls) != 1 {
		t.Errorf("TransitionStateCalls = %+v, want exactly one (redundant claim must be skipped)", f.TransitionStateCalls)
	}
}

// Pins #647 AC1: MaxParallel=2 with three queued picks runs exactly two at
// once, holds the third queued, and launches it as soon as one of the first
// two settles.
func TestLauncher_MaxParallel_CapsConcurrency_RefillsOnSettle(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "first", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Title: "second", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "44", Title: "third", Labels: []string{"ready-for-agent"}})

	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "first", State: PickQueued})
	q.Add(Pick{Number: "43", Title: "second", State: PickQueued})
	q.Add(Pick{Number: "44", Title: "third", State: PickQueued})

	fr := runner.NewFake()
	release42 := make(chan struct{})
	release43 := make(chan struct{})
	release44 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		switch box.Issue {
		case "42":
			<-release42
		case "43":
			<-release43
		case "44":
			<-release44
		}
		return nil
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), queue: q, MaxParallel: 2}
	launch.tryLaunch(f, dir)

	waitForPickStates(t, q, map[string]PickState{"42": PickRunning, "43": PickRunning, "44": PickQueued})

	close(release42)
	waitForPickStates(t, q, map[string]PickState{"44": PickRunning})

	close(release43)
	close(release44)
	launch.Wait()

	snap := q.Snapshot()
	for _, p := range snap {
		if p.State != PickSettled {
			t.Errorf("pick #%s state = %v, want settled", p.Number, p.State)
		}
	}
}

// Pins #650: a pick held on an open blocker re-evaluates on the refill that
// follows the blocker's own Dispatch settling, and launches with no operator
// action the moment the blocker reads ready.
func TestQueue_Discover_HeldPickLaunchesOnceBlockerClears(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Failed: "agent-failed"})
	f.SetIssue(forge.Issue{Number: "41", Title: "first", Labels: []string{"ready-for-agent"}, State: forge.IssueOpen})
	f.SetIssue(forge.Issue{Number: "42", Title: "then", Labels: []string{"ready-for-agent"}})
	f.NativeDeps = map[string][]string{"42": {"41"}}

	q := NewQueue()
	q.Add(Pick{Number: "41", Title: "first", State: PickQueued})
	q.Add(Pick{Number: "42", Title: "then", State: PickQueued})

	fr := runner.NewFake()
	release41 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		if box.Issue == "41" {
			<-release41
		}
		return nil
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	inner := settle.NewFake()
	qs := queueSettler{Settler: inner, q: q}

	discover := func() (waves.Batch, error) {
		return q.Discover(f, f, "agent-failed", KindWork)
	}
	fresh := func() (bool, bool, string) { return false, true, "" }

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- waves.RunContinuous(waves.Config{MaxParallel: 2}, nil, f, f, factory, qs, waves.QueueFromDiscoverer(discover), fresh)
	}()

	waitForPickStates(t, q, map[string]PickState{"42": PickHeld})
	if got := q.Snapshot(); len(got) != 2 || got[1].BlockedBy == "" {
		t.Fatalf("pick #42 = %+v, want BlockedBy naming #41", got)
	}

	f.SetIssue(forge.Issue{Number: "41", Title: "first", Labels: []string{"agent-in-progress"}, State: forge.IssueClosed})
	close(release41)

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("RunContinuous: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return after the blocker cleared")
	}

	snap := q.Snapshot()
	got := map[string]PickState{}
	for _, p := range snap {
		got[p.Number] = p.State
	}
	if got["41"] != PickSettled || got["42"] != PickSettled {
		t.Errorf("pick states = %+v, want both settled", got)
	}
}

// setupForgeQueueFactory wires the fake forge, a single-pick queue, and a
// dispatch factory for TestRunContinuous_ConsoleConfig_SkipsRedundantClaim
// (#706, #980).
func setupForgeQueueFactory(t *testing.T) (f *forge.Fake, dir string, factory *dispatch.Factory, qs queueSettler, discover func() (waves.Batch, error), fresh func() (bool, bool, string)) {
	t.Helper()

	f = forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	fr := runner.NewFake()
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err = dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	inner := settle.NewFake()
	qs = queueSettler{Settler: inner, q: q}

	discover = func() (waves.Batch, error) {
		return q.Discover(f, f, "", KindWork)
	}
	fresh = func() (bool, bool, string) { return false, true, "" }

	return f, dir, factory, qs, discover, fresh
}

// Pins the "only one box launches" guarantee (#707, #985) end to end, where
// pick_adapter_test.go pins only PickIssue's own return value: a rejected issue
// lands PickDissolved, never PickQueued or PickHeld, so claimable() (queue.go)
// excludes it from Discover's claim step. Discover holds no state check for
// this case, so the test pins the wiring around it, not a missing branch.
func TestQueue_Discover_AlreadyInProgressPick_NeverLaunches(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	q := NewQueue()
	launch := &Launcher{queue: q}

	msg := PickIssue(f, "42", "fix the thing", KindWork)
	if _, ok := msg.(PickDissolvedMsg); !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg", msg)
	}
	launch.Land(msg)

	if got := q.Snapshot(); len(got) != 1 || got[0].State != PickDissolved {
		t.Fatalf("queue snapshot = %+v, want one PickDissolved row", got)
	}

	fr := runner.NewFake()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	inner := settle.NewFake()
	qs := queueSettler{Settler: inner, q: q}

	discover := func() (waves.Batch, error) {
		return q.Discover(f, f, "", KindWork)
	}
	fresh := func() (bool, bool, string) { return false, true, "" }

	// The dissolved pick's issue is still open on the tracker but claimable()
	// (queue.go) excludes it, so RunContinuous finds nothing to dispatch and
	// returns ErrOpenNoneDispatchable rather than nil. That is the same signal
	// any other all-blocked batch produces (waves/continuous.go), not a launch
	// error.
	err = waves.RunContinuous(waves.Config{MaxParallel: 1}, nil, f, f, factory, qs, waves.QueueFromDiscoverer(discover), fresh)
	if !errors.Is(err, waves.ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable", err)
	}

	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls = %+v, want none — a dissolved pick must never launch", fr.RunCalls)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — Discover must never claim a dissolved pick", f.TransitionStateCalls)
	}
	if got := q.Snapshot()[0].State; got != PickDissolved {
		t.Errorf("pick state = %v, want dissolved (never claimed)", got)
	}
}

// Mirrors TestQueue_Discover_AlreadyInProgressPick_NeverLaunches for the other
// terminal state a stray pick must never relabel out of (#707, #985).
func TestQueue_Discover_AlreadyCompletePick_NeverLaunches(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", Complete: "agent-complete"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-complete"}})

	q := NewQueue()
	launch := &Launcher{queue: q}

	msg := PickIssue(f, "42", "fix the thing", KindWork)
	if _, ok := msg.(PickDissolvedMsg); !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg", msg)
	}
	launch.Land(msg)

	if got := q.Snapshot(); len(got) != 1 || got[0].State != PickDissolved {
		t.Fatalf("queue snapshot = %+v, want one PickDissolved row", got)
	}

	fr := runner.NewFake()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	inner := settle.NewFake()
	qs := queueSettler{Settler: inner, q: q}

	discover := func() (waves.Batch, error) {
		return q.Discover(f, f, "", KindWork)
	}
	fresh := func() (bool, bool, string) { return false, true, "" }

	err = waves.RunContinuous(waves.Config{MaxParallel: 1}, nil, f, f, factory, qs, waves.QueueFromDiscoverer(discover), fresh)
	if !errors.Is(err, waves.ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable", err)
	}

	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls = %+v, want none — a dissolved pick must never launch", fr.RunCalls)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — Discover must never claim a dissolved pick", f.TransitionStateCalls)
	}
	if got := q.Snapshot()[0].State; got != PickDissolved {
		t.Errorf("pick state = %v, want dissolved (never claimed)", got)
	}
}

// Pins a review finding on #2678: runStack wires runContinuousQueue.Pending to
// Queue.PendingCount, so the stale-drain report's heldBack number reflects the
// still-queued and held picks instead of staying stuck at 0. Console cannot
// re-run Queue.Discover just to count, because claiming is an inseparable side
// effect. The report reaches Console via StaleStatus().StaleDrainSummary (#2939).
func TestLauncher_StaleDrainReportsPendingCountAsHeldBack(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "first", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Title: "second", Labels: []string{"ready-for-agent"}})

	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "first", State: PickQueued})
	q.Add(Pick{Number: "43", Title: "second", State: PickQueued})

	fr := runner.NewFake()
	release42 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		if box.Issue == "42" {
			<-release42
		}
		return nil
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	// Fresh for the first refill, which claims and launches #42, then stale for
	// every refill after, including the attempt to fill the second slot, so #43
	// stays queued rather than being claimed by a second Discover call.
	var freshMu sync.Mutex
	freshCalls := 0
	freshFn := func() (bool, bool, string) {
		freshMu.Lock()
		defer freshMu.Unlock()
		freshCalls++
		if freshCalls == 1 {
			return true, true, "fresh"
		}
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), queue: q, MaxParallel: 2, Fresh: freshFn}

	testutil.CaptureStdout(t, func() {
		launch.tryLaunch(f, dir)
		close(release42)
		launch.Wait()
	})

	summary := launch.StaleStatus().StaleDrainSummary
	if !strings.Contains(summary, "1 issue(s) held back") {
		t.Fatalf("StaleDrainSummary: got %q, want a drain report mentioning 1 issue(s) held back (pick #43, still queued)", summary)
	}

	snap := q.Snapshot()
	got := map[string]PickState{}
	for _, p := range snap {
		got[p.Number] = p.State
	}
	if got["43"] != PickQueued {
		t.Errorf("pick #43 state = %v, want still PickQueued (never claimed by the stale refill attempt)", got["43"])
	}
}

// Pins the other half of #2678 AC 5: a stale drain's report once reached only
// stdout and stale-drain.log (headlessQueue's destination, queue.go), which a
// Console session under tea.WithAltScreen() never renders. runStack now wires
// runContinuousQueue.ReportStaleDrain to Launcher.recordStaleDrainReport, so
// the report reaches the field the renderer reads.
func TestLauncher_StaleDrainReportSurfacesOnStaleStatus(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "first", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Title: "second", Labels: []string{"ready-for-agent"}})

	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "first", State: PickQueued})
	q.Add(Pick{Number: "43", Title: "second", State: PickQueued})

	fr := runner.NewFake()
	release42 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		if box.Issue == "42" {
			<-release42
		}
		return nil
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	var freshMu sync.Mutex
	freshCalls := 0
	freshFn := func() (bool, bool, string) {
		freshMu.Lock()
		defer freshMu.Unlock()
		freshCalls++
		if freshCalls == 1 {
			return true, true, "fresh"
		}
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), queue: q, MaxParallel: 2, Fresh: freshFn}

	testutil.CaptureStdout(t, func() {
		launch.tryLaunch(f, dir)
		close(release42)
		launch.Wait()
	})

	summary := launch.StaleStatus().StaleDrainSummary
	if summary == "" {
		t.Fatalf("StaleStatus().StaleDrainSummary is empty, want the stale-drain report's rendered summary")
	}
	if !strings.Contains(summary, "1 issue(s) held back") {
		t.Errorf("StaleDrainSummary = %q, want it to mention 1 issue(s) held back (pick #43, still queued)", summary)
	}
	if !strings.Contains(summary, "stale-drain:") {
		t.Errorf("StaleDrainSummary = %q, want it to contain the stale-drain: prefix", summary)
	}
}

// Pins a #2678 review finding: a successful Rebuild clears
// lastStaleDrainSummary alongside the stale gate, because the report is about
// staleness the rebuild just resolved. rebuildOutput stays on success so an
// operator can still inspect it. Before the fix, StaleDrainSummary stayed
// pinned to the recorded report for the rest of the Console session.
func TestLauncher_Rebuild_Success_ClearsLastStaleDrainSummary(t *testing.T) {
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
		RebuildFn: func() (string, string, error) { return "", "", nil },
	}

	now := time.Now()
	launch.recordStaleDrainReport(waves.StaleDrainReport{StaleAt: now, DrainedAt: now.Add(time.Second), FreeSlotSecs: 1.5, HeldBack: 2})
	if launch.StaleStatus().StaleDrainSummary == "" {
		t.Fatalf("StaleStatus().StaleDrainSummary is empty after recordStaleDrainReport, want the recorded summary")
	}

	launch.Rebuild(f, dir)
	launch.Wait()

	if summary := launch.StaleStatus().StaleDrainSummary; summary != "" {
		t.Errorf("StaleStatus().StaleDrainSummary after successful Rebuild = %q, want cleared", summary)
	}
}

// waitForPickStates polls q until every numbered pick in want holds the
// expected state, or fails the test after a two-second deadline.
func waitForPickStates(t *testing.T, q *Queue, want map[string]PickState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := q.Snapshot()
		got := make(map[string]PickState, len(snap))
		for _, p := range snap {
			got[p.Number] = p.State
		}
		ok := true
		for num, state := range want {
			if got[num] != state {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pick states = %+v, want %+v", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}
