package console

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/waves"
)

// TestRunContinuous_DrainsScriptedQueue_LaunchesOneDispatchEndToEnd drives
// the continuous engine directly (no console Model, View, or Run loop) with
// a scripted operator queue: one queued pick, single slot (MaxParallel=1).
// It proves the whole "one Dispatch end to end" AC — Pick's queued row
// claims, runs a Box, and settles — through the existing Discoverer seam
// the engine already exposes, with the engine itself staying UI-blind
// (#646).
func TestRunContinuous_DrainsScriptedQueue_LaunchesOneDispatchEndToEnd(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	fr := runner.NewFake()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
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

	discover := func() ([]waves.Issue, map[string][]string, error) { return q.Discover(f, f, "") }
	fresh := func() (bool, bool, string) { return false, true, "" }

	err = waves.RunContinuous(waves.Config{MaxParallel: 1}, f, f, dir, factory, qs, discover, fresh)
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

// TestLauncher_MaxParallel_CapsConcurrency_RefillsOnSettle verifies a
// Launcher with MaxParallel=2 and three queued picks runs exactly two at
// once, holding the third queued, then launches it as soon as one of the
// first two settles (#647 AC1).
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
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
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

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: q, MaxParallel: 2}
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

// TestQueue_Discover_HeldPickLaunchesOnceBlockerClears verifies a pick held
// on an open blocker (#650) re-evaluates on the refill that follows the
// blocker's own Dispatch settling, and launches with no operator action the
// moment the blocker reads ready — "do this, then that" queued in one
// sitting, driven end to end through the existing Discoverer seam.
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
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
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

	discover := func() ([]waves.Issue, map[string][]string, error) { return q.Discover(f, f, "agent-failed") }
	fresh := func() (bool, bool, string) { return false, true, "" }

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- waves.RunContinuous(waves.Config{MaxParallel: 2}, f, f, dir, factory, qs, discover, fresh)
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

// waitForPickStates polls q until every numbered pick in want holds the
// expected state, or fails the test after a two-second deadline — the same
// no-real-sleep-in-production, bounded-poll-in-test pattern the rest of the
// package's launcher tests use.
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
