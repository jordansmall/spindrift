package console

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// TestLauncher_CapDefaultsToMaxParallel verifies the session's live
// parallelism cap (issue #653) starts at MaxParallel, with nothing running
// yet, before any Dispatch has launched.
func TestLauncher_CapDefaultsToMaxParallel(t *testing.T) {
	launch := &Launcher{MaxParallel: 3}
	if got := launch.Cap(); got != 3 {
		t.Fatalf("Cap: got %d, want 3", got)
	}
	if got := launch.Live(); got != 0 {
		t.Fatalf("Live: got %d, want 0", got)
	}
}

// TestLauncher_Pick_QueuesAndReturnsSnapshot verifies Pick mutates the
// private queue and hands back the fresh snapshot synchronously, in the
// same call — the tea side never has to pull Queue itself to see the row it
// just landed (issue #1542).
func TestLauncher_Pick_QueuesAndReturnsSnapshot(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	launch := &Launcher{}
	msg, picks := launch.Pick(f, "42", "fix the thing", KindWork)

	if _, ok := msg.(PickQueuedMsg); !ok {
		t.Fatalf("Pick msg = %#v, want PickQueuedMsg", msg)
	}
	if len(picks) != 1 || picks[0].Number != "42" || picks[0].State != PickQueued {
		t.Fatalf("Pick snapshot = %+v, want one PickQueued row for #42", picks)
	}
}

// TestLauncher_Pick_ResearchKind_PromotesOnResearchTracker verifies a
// KindResearch pick promotes through l.ResearchTracker — carrying the
// agent-research label family — rather than the work tracker the caller
// passed in, and that the work tracker sees no call at all (issue #1708):
// the two kinds' promotions must land on different tracker instances, since
// each instance's TransitionState resolves the same canonical DispatchState
// values through its own baked-in label family.
func TestLauncher_Pick_ResearchKind_PromotesOnResearchTracker(t *testing.T) {
	workTracker := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	workTracker.SetIssue(forge.Issue{Number: "42", Title: "research this"})
	researchTracker := forge.NewFake(forge.ResearchDispatchLabels())
	researchTracker.SetIssue(forge.Issue{Number: "42", Title: "research this"})

	launch := &Launcher{ResearchTracker: researchTracker}
	msg, picks := launch.Pick(workTracker, "42", "research this", KindResearch)

	if _, ok := msg.(PickQueuedMsg); !ok {
		t.Fatalf("Pick msg = %#v, want PickQueuedMsg", msg)
	}
	if len(picks) != 1 || picks[0].Number != "42" || picks[0].Kind != KindResearch {
		t.Fatalf("Pick snapshot = %+v, want one KindResearch row for #42", picks)
	}
	if len(workTracker.TransitionStateCalls) != 0 {
		t.Errorf("workTracker.TransitionStateCalls = %+v, want none — a research pick must never touch the work tracker", workTracker.TransitionStateCalls)
	}
	iss, err := researchTracker.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(iss, "agent-research") {
		t.Errorf("researchTracker issue #42 labels = %v, want agent-research promoted onto it", iss.Labels)
	}
}

// TestLauncher_Unpick_RemovesAndReturnsSnapshot verifies Unpick drops the
// queued pick from the private queue and hands back the fresh snapshot
// synchronously (issue #1542).
func TestLauncher_Unpick_RemovesAndReturnsSnapshot(t *testing.T) {
	launch := &Launcher{}
	launch.Pick(forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"}), "42", "fix the thing", KindWork)

	picks := launch.Unpick("42")
	if len(picks) != 0 {
		t.Fatalf("Unpick snapshot = %+v, want empty", picks)
	}
}

// TestLauncher_Wait_BlocksUntilBackgroundDrainFinishes verifies Wait
// doesn't return while tryLaunch's background RunContinuous drain still has
// a Box in flight — quitting the console must never race the caller's
// cleanup (e.g. the driver-cache teardown) against a live Dispatch (#646).
func TestLauncher_Wait_BlocksUntilBackgroundDrainFinishes(t *testing.T) {
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

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), queue: NewQueue()}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)

	waitDone := make(chan struct{})
	go func() {
		launch.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		t.Fatal("Wait returned while the Box was still running")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait never returned after the Box finished")
	}
}

// TestLauncher_TryLaunch_SkipsWhenQueueEmpty verifies tryLaunch never spawns
// a drain goroutine when Queue has nothing queued or held (#754) — the
// background poll tick (tea.go pollTickMsg) fires this every interval
// regardless of queue state, so an empty queue must be a real no-op rather
// than a wasted RunContinuous pass.
func TestLauncher_TryLaunch_SkipsWhenQueueEmpty(t *testing.T) {
	launch := &Launcher{queue: NewQueue()}
	launch.tryLaunch(nil, "")

	if launch.launching {
		t.Fatal("tryLaunch set launching=true with an empty queue")
	}

	done := make(chan struct{})
	go func() {
		launch.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Wait blocked — a drain goroutine was spawned despite an empty queue")
	}
}

// TestLauncher_TryLaunch_HeldPickLaunchesAfterBlockerClearsOutOfBand
// verifies a background-poll-driven tryLaunch call (tea.go's pollTickMsg
// case, which fires every interval regardless of queue state) still
// re-evaluates and launches a held pick whose blocker cleared out-of-band —
// another agent or a human merge, with no sibling Dispatch in this session
// to trigger RunContinuous's own refill-on-completion. See Queue.Empty's
// doc comment (#650) for why #754's queue-empty skip in tryLaunch must
// gate on PickHeld as well as PickQueued, or this regresses — restores the
// coverage the pre-Bubble-Tea-rewrite TestRun_HeldPick_LaunchesOnBackgroundPollAfterDrainIdles
// carried.
func TestLauncher_TryLaunch_HeldPickLaunchesAfterBlockerClearsOutOfBand(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "41", State: forge.IssueOpen})
	f.NativeDeps = map[string][]string{"42": {"41"}}

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

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), queue: NewQueue()}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)
	launch.Wait()

	if got := launch.queue.Snapshot()[0].State; got != PickHeld {
		t.Fatalf("pick state = %v, want PickHeld (blocked on #41)", got)
	}

	// Blocker clears out-of-band: no Add, no operator action.
	f.SetIssue(forge.Issue{Number: "41", State: forge.IssueClosed})

	// Stands in for the background poll tick (tea.go pollTickMsg) firing on
	// its own interval — the only thing left to re-evaluate a held pick
	// once its blocker clears with nothing else driving a fresh drain.
	launch.tryLaunch(f, dir)
	launch.Wait()

	if got := launch.queue.Snapshot()[0].State; got != PickRunning && got != PickSettled {
		t.Fatalf("pick state = %v, want it to have launched (Running or Settled)", got)
	}
}

// TestLauncher_TryLaunch_ResearchPick_UsesResearchFactoryAndSettle verifies
// drain routes a KindResearch pick through ResearchFactory/ResearchSettle —
// never the work Factory/Settle — claiming on ResearchTracker's own
// agent-research label family (issue #1708). Both stacks share MaxParallel=1
// and thus the same underlying dispatch.Factory-driven Box run; only the
// wiring (which tracker claims, which Settler settles) differs by kind.
func TestLauncher_TryLaunch_ResearchPick_UsesResearchFactoryAndSettle(t *testing.T) {
	workTracker := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	researchTracker := forge.NewFake(forge.ResearchDispatchLabels())
	researchTracker.SetIssue(forge.Issue{Number: "42", Title: "research this", Labels: []string{"agent-research"}})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	fr := runner.NewFake()
	researchFactory, err := dispatch.NewFactory(dispatch.Config{Kind: "research"}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(researchFactory.Cleanup)

	workSettle := settle.NewFake()
	researchSettle := settle.NewFake()

	launch := &Launcher{
		CodeForge:       workTracker,
		Factory:         nil,
		Settle:          workSettle,
		ResearchTracker: researchTracker,
		ResearchFactory: researchFactory,
		ResearchSettle:  researchSettle,
		queue:           NewQueue(),
	}
	launch.queue.Add(Pick{Number: "42", Title: "research this", State: PickQueued, Kind: KindResearch})
	launch.tryLaunch(workTracker, dir)
	launch.Wait()

	if len(workSettle.SettleCalls) != 0 {
		t.Errorf("workSettle.SettleCalls = %+v, want none — a research pick must never settle through the work Settler", workSettle.SettleCalls)
	}
	if len(researchSettle.SettleCalls) != 1 || researchSettle.SettleCalls[0].Num != "42" {
		t.Errorf("researchSettle.SettleCalls = %+v, want one call for #42", researchSettle.SettleCalls)
	}
	if len(workTracker.TransitionStateCalls) != 0 {
		t.Errorf("workTracker.TransitionStateCalls = %+v, want none — a research pick must never claim on the work tracker", workTracker.TransitionStateCalls)
	}
	foundClaim := false
	for _, call := range researchTracker.TransitionStateCalls {
		if call.Num == "42" && call.From == forge.Dispatchable && call.To == forge.InProgress {
			foundClaim = true
		}
	}
	if !foundClaim {
		t.Errorf("researchTracker.TransitionStateCalls = %+v, want a Dispatchable->InProgress claim for #42", researchTracker.TransitionStateCalls)
	}
	if got := launch.queue.Snapshot()[0].State; got != PickSettled {
		t.Fatalf("pick state = %v, want settled", got)
	}
}

// TestLauncher_Pick_ResearchKind_UntriagedIssue_LaunchesAndSettles drives a
// KindResearch pick through the actual operator gesture — Launcher.Pick,
// then tryLaunch, exactly as pickHighlighted (tea.go) chains them — starting
// from an untriaged Backlog issue with no labels at all, rather than
// pre-seeding queue state or the agent-research label directly (#1742). This
// is the exact shape that used to false-fire: research's DispatchLabels
// leaves Complete unmapped, so the pre-fix double-box guard queried
// ListIssues(Complete), got every open issue back, and dissolved the pick
// with a bogus "already complete" reason before it ever reached
// transitionToDispatchable, let alone tryLaunch/drain. Confirms the full
// chain — queue, not dissolve; promote onto agent-research; claim; run;
// settle through ResearchSettle, never the work Settler — actually reaches
// drain rather than just PickIssue's promotion step in isolation.
func TestLauncher_Pick_ResearchKind_UntriagedIssue_LaunchesAndSettles(t *testing.T) {
	workTracker := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	researchTracker := forge.NewFake(forge.ResearchDispatchLabels())
	researchTracker.SetIssue(forge.Issue{Number: "42", Title: "research this"}) // untriaged: no labels

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	fr := runner.NewFake()
	researchFactory, err := dispatch.NewFactory(dispatch.Config{Kind: "research"}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(researchFactory.Cleanup)

	workSettle := settle.NewFake()
	researchSettle := settle.NewFake()

	launch := &Launcher{
		CodeForge:       workTracker,
		Settle:          workSettle,
		ResearchTracker: researchTracker,
		ResearchFactory: researchFactory,
		ResearchSettle:  researchSettle,
		queue:           NewQueue(),
	}

	msg, _ := launch.Pick(workTracker, "42", "research this", KindResearch)
	if _, ok := msg.(PickQueuedMsg); !ok {
		t.Fatalf("Pick() = %#v, want PickQueuedMsg", msg)
	}
	launch.tryLaunch(workTracker, dir)
	launch.Wait()

	iss, err := researchTracker.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(iss, "agent-research-in-progress") {
		t.Errorf("researchTracker issue #42 labels = %v, want claimed onto agent-research-in-progress", iss.Labels)
	}
	if len(workSettle.SettleCalls) != 0 {
		t.Errorf("workSettle.SettleCalls = %+v, want none — a research pick must never settle through the work Settler", workSettle.SettleCalls)
	}
	if len(researchSettle.SettleCalls) != 1 || researchSettle.SettleCalls[0].Num != "42" {
		t.Errorf("researchSettle.SettleCalls = %+v, want one call for #42", researchSettle.SettleCalls)
	}
	if got := launch.queue.Snapshot()[0].State; got != PickSettled {
		t.Fatalf("pick state = %v, want settled", got)
	}
}

// TestLauncher_Stacks_ResearchFailedLabelIsResearchFamily verifies the
// research stack's failedLabel is the fixed agent-research-failed label, not
// l.FailedLabel (the work family's own failed label) — a research pick's
// blocker-readiness check must never consult the wrong family when deciding
// whether a blocker counts as failed (issue #1708).
func TestLauncher_Stacks_ResearchFailedLabelIsResearchFamily(t *testing.T) {
	launch := &Launcher{
		FailedLabel:     "agent-failed",
		ResearchTracker: forge.NewFake(forge.ResearchDispatchLabels()),
		ResearchFactory: &dispatch.Factory{},
		ResearchSettle:  settle.NewFake(),
	}

	stacks := launch.stacks(forge.NewFake())

	if len(stacks) != 2 {
		t.Fatalf("stacks() = %+v, want 2 (work + research)", stacks)
	}
	if stacks[0].failedLabel != "agent-failed" {
		t.Errorf("work stack failedLabel = %q, want %q", stacks[0].failedLabel, "agent-failed")
	}
	if stacks[1].failedLabel != "agent-research-failed" {
		t.Errorf("research stack failedLabel = %q, want %q", stacks[1].failedLabel, "agent-research-failed")
	}
}

// TestLauncher_TryLaunch_ResearchPickNoResearchStack_DrainStopsInsteadOfSpinning
// verifies drain terminates instead of busy-spinning forever when a
// KindResearch pick is queued but no research stack is wired (ResearchFactory/
// ResearchTracker both nil): the work-only stacks() never claims a
// research-kind pick, so the old unconditional q.hasQueued() check treated
// that untouched pick as "still work to do" and looped without ever making
// progress. The pick is left stranded at PickQueued — there is truly nowhere
// for it to launch — but drain itself must still return (issue #1708).
func TestLauncher_TryLaunch_ResearchPickNoResearchStack_DrainStopsInsteadOfSpinning(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})

	launch := &Launcher{CodeForge: f, Settle: settle.NewFake(), queue: NewQueue()}
	launch.queue.Add(Pick{Number: "42", Title: "research this", State: PickQueued, Kind: KindResearch})
	launch.tryLaunch(f, t.TempDir())

	done := make(chan struct{})
	go func() {
		launch.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drain never returned — busy-spinning on an unserviceable research pick")
	}

	if got := launch.queue.Snapshot()[0].State; got != PickQueued {
		t.Fatalf("pick state = %v, want left at PickQueued (no research stack to claim it)", got)
	}
}

// TestLauncher_TryLaunch_BoxFailureReachesPickFailed verifies that a Box
// which runs and exits non-zero moves its queue row to the terminal
// PickFailed state instead of stranding it at PickRunning — the gap issue
// #705 closes: RunContinuous's failure branch previously transitioned only
// the tracker issue, never the Console's own queue.
func TestLauncher_TryLaunch_BoxFailureReachesPickFailed(t *testing.T) {
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
	fr.RunErr = errors.New("exit 1")
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), queue: NewQueue()}
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	launch.tryLaunch(f, dir)
	launch.Wait()

	if got := launch.queue.Snapshot()[0].State; got != PickFailed {
		t.Fatalf("pick state = %v, want PickFailed", got)
	}
}

// TestLauncher_LiveIssues_ExcludesPickFailed verifies LiveIssues filters out
// a PickFailed row rather than only ever seeing PickRunning rows in
// practice (issue #974) — the exclusion was true by construction but had no
// test asserting it directly.
func TestLauncher_LiveIssues_ExcludesPickFailed(t *testing.T) {
	launch := &Launcher{queue: NewQueue()}
	launch.queue.Add(Pick{Number: "41", Title: "running one", State: PickRunning})
	launch.queue.Add(Pick{Number: "42", Title: "failed one", State: PickFailed})

	got := launch.LiveIssues()
	want := []string{"41"}
	if !slices.Equal(got, want) {
		t.Fatalf("LiveIssues() = %v, want %v", got, want)
	}
}

// TestLauncher_TryLaunch_RacingAddNeverStrands stress-tests the lost-wakeup
// window between a drain's last (empty) discover() and l.launching clearing:
// a second pick is Add()ed and tryLaunch is called from a separate goroutine
// timed to race the first pick's Box finishing. Run many times so real
// goroutine-scheduling jitter has a chance to land in that window; every
// iteration must still settle both picks — a stranded PickQueued pick means
// the race reopened (#646).
func TestLauncher_TryLaunch_RacingAddNeverStrands(t *testing.T) {
	for i := 0; i < 200; i++ {
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
		factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
		if err != nil {
			t.Fatalf("dispatch.NewFactory: %v", err)
		}

		launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), queue: NewQueue()}
		launch.queue.Add(Pick{Number: "42", Title: "first", State: PickQueued})
		launch.tryLaunch(f, dir)

		go func() {
			launch.queue.Add(Pick{Number: "43", Title: "second", State: PickQueued})
			launch.tryLaunch(f, dir)
		}()

		// Poll for both picks settled instead of Launcher.Wait(): the
		// racing goroutine above may call tryLaunch (wg.Add) after this
		// drain's wg has already dropped to zero, and a concurrent Add
		// racing a Wait observing zero is the exact misuse
		// sync.WaitGroup's own contract forbids — a test-only concern, not
		// a production one (Run only calls Wait once, after its single
		// read loop has already stopped accepting "p" commands).
		deadline := time.Now().Add(2 * time.Second)
		for {
			snap := launch.queue.Snapshot()
			if len(snap) == 2 && snap[0].State == PickSettled && snap[1].State == PickSettled {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d: a pick is stranded: %+v", i, snap)
			}
			time.Sleep(time.Millisecond)
		}
		factory.Cleanup()
	}
}
