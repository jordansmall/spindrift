package console

import (
	"errors"
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

	discover := func() ([]waves.Issue, map[string][]string, waves.Sources, map[string]bool, error) {
		issues, edges, sources, err := q.Discover(f, f, "")
		return issues, edges, sources, nil, err
	}
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

// TestRunContinuous_ConsoleConfig_SkipsRedundantClaim pins the coupling
// launcher.go's drain loop relies on (#706): passing waves.Config with
// Label and InProgressLabel both left zero-value (equal) makes claimIssue
// (engine.go) skip a second Dispatchable->InProgress transition, because
// Queue.Discover (queue.go) already performed that transition when it
// claimed the pick. Only one TransitionState call should ever happen.
func TestRunContinuous_ConsoleConfig_SkipsRedundantClaim(t *testing.T) {
	f, dir, factory, qs, discover, fresh := setupForgeQueueFactory(t)

	// Same zero-value Label/InProgressLabel/OverlapGate as launcher.go's
	// own waves.Config construction — MaxParallel stands in for the
	// Limiter that field would otherwise build internally.
	err := waves.RunContinuous(waves.Config{MaxParallel: 1}, f, f, dir, factory, qs, discover, fresh)
	if err != nil {
		t.Fatalf("RunContinuous: %v", err)
	}

	if len(f.TransitionStateCalls) != 1 {
		t.Errorf("TransitionStateCalls = %+v, want exactly one (redundant claim must be skipped)", f.TransitionStateCalls)
	}
}

// TestRunContinuous_DivergentLabels_DoubleClaims is the negative case for
// TestRunContinuous_ConsoleConfig_SkipsRedundantClaim (#706): if Label and
// InProgressLabel are ever passed as different values, claimIssue no longer
// recognizes Queue.Discover's upstream claim as already done and issues a
// second Dispatchable->InProgress transition. This is why the Console's
// drain-path Config must keep the two equal (both left zero-value).
func TestRunContinuous_DivergentLabels_DoubleClaims(t *testing.T) {
	f, dir, factory, qs, discover, fresh := setupForgeQueueFactory(t)

	err := waves.RunContinuous(waves.Config{MaxParallel: 1, Label: "ready-for-agent", InProgressLabel: "agent-in-progress"}, f, f, dir, factory, qs, discover, fresh)
	if err != nil {
		t.Fatalf("RunContinuous: %v", err)
	}

	if len(f.TransitionStateCalls) != 2 {
		t.Errorf("TransitionStateCalls = %+v, want exactly two (Queue.Discover's claim plus claimIssue's redundant one)", f.TransitionStateCalls)
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

	discover := func() ([]waves.Issue, map[string][]string, waves.Sources, map[string]bool, error) {
		issues, edges, sources, err := q.Discover(f, f, "agent-failed")
		return issues, edges, sources, nil, err
	}
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

// setupForgeQueueFactory wires the fake forge, a single-pick queue, and a
// dispatch factory shared by TestRunContinuous_ConsoleConfig_SkipsRedundantClaim
// and TestRunContinuous_DivergentLabels_DoubleClaims (#706, #980): both drive
// waves.RunContinuous over the same queued #42 pick and differ only in the
// waves.Config they pass and the assertion on f.TransitionStateCalls.
func setupForgeQueueFactory(t *testing.T) (f *forge.Fake, dir string, factory *dispatch.Factory, qs queueSettler, discover func() ([]waves.Issue, map[string][]string, waves.Sources, map[string]bool, error), fresh func() (bool, bool, string)) {
	t.Helper()

	f = forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	fr := runner.NewFake()
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
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

	discover = func() ([]waves.Issue, map[string][]string, waves.Sources, map[string]bool, error) {
		issues, edges, sources, err := q.Discover(f, f, "")
		return issues, edges, sources, nil, err
	}
	fresh = func() (bool, bool, string) { return false, true, "" }

	return f, dir, factory, qs, discover, fresh
}

// TestQueue_Discover_AlreadyInProgressPick_NeverLaunches drives PickIssue's
// rejection of an already-InProgress issue all the way through
// teaModel.landPick's message wiring and Queue.Discover/RunContinuous,
// proving the "only one box launches" guarantee (#707) end to end rather
// than through PickIssue's own return value and TransitionStateCalls proxy
// alone (pick_adapter_test.go's
// TestPickIssue_AlreadyInProgress_ReturnsDissolvedMsg_NoTransition). A
// PickIssue-rejected issue lands PickDissolved, never PickQueued/PickHeld,
// so claimable() (queue.go) structurally excludes it from Discover's claim
// step — there is no state-check inside Discover itself for this case; this
// test proves the wiring around it, not a branch that doesn't exist (#985).
func TestQueue_Discover_AlreadyInProgressPick_NeverLaunches(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	q := NewQueue()
	launch := &Launcher{Queue: q}

	msg := PickIssue(f, "42", "fix the thing", KindWork)
	if _, ok := msg.(PickDissolvedMsg); !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg", msg)
	}
	teaModel{launch: launch}.landPick(msg)

	if got := q.Snapshot(); len(got) != 1 || got[0].State != PickDissolved {
		t.Fatalf("queue snapshot = %+v, want one PickDissolved row", got)
	}

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

	discover := func() ([]waves.Issue, map[string][]string, waves.Sources, map[string]bool, error) {
		issues, edges, sources, err := q.Discover(f, f, "")
		return issues, edges, sources, nil, err
	}
	fresh := func() (bool, bool, string) { return false, true, "" }

	// The dissolved pick's issue is still open on the tracker but
	// claimable() (queue.go) excludes it, so RunContinuous finds nothing to
	// dispatch and returns ErrOpenNoneDispatchable rather than nil — the
	// same "open issues exist but none are dispatchable" signal any other
	// all-blocked batch produces (waves/continuous.go), not a launch error.
	err = waves.RunContinuous(waves.Config{MaxParallel: 1}, f, f, dir, factory, qs, discover, fresh)
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

// TestQueue_Discover_AlreadyCompletePick_NeverLaunches mirrors
// TestQueue_Discover_AlreadyInProgressPick_NeverLaunches for the other
// terminal state a stray pick must never relabel out of (#707, #985).
func TestQueue_Discover_AlreadyCompletePick_NeverLaunches(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", Complete: "agent-complete"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-complete"}})

	q := NewQueue()
	launch := &Launcher{Queue: q}

	msg := PickIssue(f, "42", "fix the thing", KindWork)
	if _, ok := msg.(PickDissolvedMsg); !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg", msg)
	}
	teaModel{launch: launch}.landPick(msg)

	if got := q.Snapshot(); len(got) != 1 || got[0].State != PickDissolved {
		t.Fatalf("queue snapshot = %+v, want one PickDissolved row", got)
	}

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

	discover := func() ([]waves.Issue, map[string][]string, waves.Sources, map[string]bool, error) {
		issues, edges, sources, err := q.Discover(f, f, "")
		return issues, edges, sources, nil, err
	}
	fresh := func() (bool, bool, string) { return false, true, "" }

	err = waves.RunContinuous(waves.Config{MaxParallel: 1}, f, f, dir, factory, qs, discover, fresh)
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
