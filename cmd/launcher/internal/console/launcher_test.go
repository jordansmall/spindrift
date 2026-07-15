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

	launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
	launch.Queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
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

		launch := &Launcher{CodeForge: f, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
		launch.Queue.Add(Pick{Number: "42", Title: "first", State: PickQueued})
		launch.tryLaunch(f, dir)

		go func() {
			launch.Queue.Add(Pick{Number: "43", Title: "second", State: PickQueued})
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
			snap := launch.Queue.Snapshot()
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
