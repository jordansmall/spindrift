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
