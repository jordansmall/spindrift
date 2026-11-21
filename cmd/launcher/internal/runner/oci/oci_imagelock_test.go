package oci

import (
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runner/runnertest"
)

// The warm path (image already present) must never touch the lock at all —
// issue #3632's "single-child path unchanged in cost" criterion.
func TestEnsureReady_ImagePresent_NoLockFileCreated(t *testing.T) {
	dir := redirectImageLockDir(t)
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	a := &ociAdapter{cli: script, boxImg: imageSpec{tag: "spindrift:abc123"}}

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: unexpected error: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, imageLockFilePrefix+"*"+imageLockFileSuffix))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("lock file(s) created on the warm path: %v", matches)
	}
}

// A losing child blocks on the winner's held lock, then re-probes and finds
// the image present: it must return without ever running a build, load or
// tag, and its log must explain both the wait and the skip.
func TestEnsureReady_LockHeldByAnother_BlocksThenObservesPresentWithoutRebuilding(t *testing.T) {
	cases := []struct {
		name  string
		probe runnertest.Call
	}{
		{"fast probe", runnertest.Call{Exit: 1}},
		{"slow probe", runnertest.Call{Exit: 1, Shell: "sleep 0.5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testLockHeldByAnother(t, tc.probe)
		})
	}
}

// testLockHeldByAnother holds the image lock, runs EnsureReady with probe as
// its outside-the-lock probe (call 0), and asserts it blocks until release,
// then skips without building, logging both the wait and the skip.
func testLockHeldByAnother(t *testing.T, probe runnertest.Call) {
	redirectImageLockDir(t)
	const image = "spindrift:abc123"

	holder, err := acquireImageLock(image, nil)
	if err != nil {
		t.Fatalf("acquireImageLock (holder): %v", err)
	}

	// call 0: the outside-the-lock probe, before this child even tries to
	// acquire — must see absent, or it would never attempt the lock at all.
	// The slow case delays it past the old fixed 200 ms release, reproducing
	// the flake in issue #7304.
	// call 1+: the re-probe under the lock, taken only after the holder
	// releases — scripted present, standing in for "the winner finished".
	cliScript, cliDir := runnertest.NewFakeCLI(t, probe, runnertest.Call{Exit: 0})

	origExec := execCommand
	nixInvoked := false
	execCommand = func(name string, args ...string) *exec.Cmd {
		// Never actually reached in the passing case (the loser must not
		// build at all); if a regression does reach it, run a harmless
		// no-op rather than a real "nix build" against a fake .drv path.
		nixInvoked = true
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = origExec })

	a := &ociAdapter{cli: cliScript, boxImg: imageSpec{tag: image, drv: "/nix/store/fake.drv"}}

	// The hook fires only after the waiter's non-blocking acquire hit EWOULDBLOCK,
	// so releasing after it is race-free regardless of how slow call 0 was.
	// Swapping the process-wide hook and execCommand means neither subtest may
	// call t.Parallel().
	waiting := make(chan struct{})
	var once sync.Once
	origHook := imageLockWaitHook
	imageLockWaitHook = func() { once.Do(func() { close(waiting) }) }
	t.Cleanup(func() { imageLockWaitHook = origHook })

	type result struct{ err error }
	done := make(chan result, 1)
	var res result

	stdout := runnertest.CaptureStdoutDuring(t, func() {
		go func() {
			done <- result{err: a.EnsureReady()}
		}()

		select {
		case r := <-done:
			t.Fatalf("EnsureReady returned early (err=%v) while the holder still held the lock", r.err)
		case <-waiting:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for EnsureReady to block on the held lock")
		}

		// The window checks the waiter stays blocked, not ordering.
		select {
		case r := <-done:
			t.Fatalf("EnsureReady returned (err=%v) after logging the wait but while the holder still held the lock", r.err)
		case <-time.After(200 * time.Millisecond):
		}

		if err := holder.Release(); err != nil {
			t.Fatalf("holder Release: %v", err)
		}

		select {
		case res = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for EnsureReady after holder release")
		}
	})

	if res.err != nil {
		t.Fatalf("EnsureReady: unexpected error: %v", res.err)
	}
	if nixInvoked {
		t.Error("nix build invoked; the loser must not repeat the winner's realize")
	}
	if got := runnertest.CallCount(t, cliDir); got != 2 {
		t.Errorf("fake CLI callCount = %d, want 2 (two inspects, no run/load/tag)", got)
	}
	if !strings.Contains(stdout, "is being realized elsewhere — waiting") {
		t.Errorf("stdout missing the wait line; got: %q", stdout)
	}
	if !strings.Contains(stdout, "already realized elsewhere; skipping the build") {
		t.Errorf("stdout missing the winner-did-it (skip) line; got: %q", stdout)
	}
}

// With the lock uncontended, EnsureReady must still run today's build path —
// the lock changes nothing about the single-child, image-absent case.
func TestEnsureReady_LockUncontended_StillRunsHostBuild(t *testing.T) {
	redirectImageLockDir(t)
	cliScript, cliDir := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 1}, // image inspect: absent (outside the lock)
		runnertest.Call{Exit: 1}, // image inspect: still absent (re-probe under the lock)
		runnertest.Call{},        // load
		runnertest.Call{},        // tag
	)

	nixScript, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	origExec := execCommand
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(nixScript, args...)
	}
	t.Cleanup(func() { execCommand = origExec })

	a := &ociAdapter{
		boxImg: imageSpec{tag: "spindrift:abc123", drv: "/nix/store/fake.drv", archive: "/tmp/spindrift-image.tar"},
		cli:    cliScript,
	}

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: unexpected error: %v", err)
	}
	if gotName != "nix" {
		t.Errorf("execCommand called with %q, want %q", gotName, "nix")
	}
	if got := runnertest.CallCount(t, cliDir); got != 4 {
		t.Errorf("fake CLI callCount = %d, want 4 (two inspects, load, tag)", got)
	}
}

// A lock dir that cannot be created/opened must degrade to a stderr warning
// and an unlocked build, not a failed EnsureReady (issue #3632).
func TestEnsureReady_LockAcquireFails_ContinuesUnlockedWithWarning(t *testing.T) {
	// A nonexistent parent makes os.OpenFile fail outright, unlike a
	// contended lock, which opens and blocks on flock. t.TempDir() is
	// captured out here for the reason redirectImageLockDir documents.
	missing := filepath.Join(t.TempDir(), "nonexistent-parent", "nested")
	orig := imageLockDir
	imageLockDir = func() string { return missing }
	t.Cleanup(func() { imageLockDir = orig })

	cliScript, cliDir := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 1}, // image inspect: absent (outside the lock)
		runnertest.Call{Exit: 1}, // image inspect: still absent (re-probe, despite the failed acquire)
		runnertest.Call{},        // load
		runnertest.Call{},        // tag
	)

	nixScript, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	origExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(nixScript, args...)
	}
	t.Cleanup(func() { execCommand = origExec })

	a := &ociAdapter{
		boxImg: imageSpec{tag: "spindrift:abc123", drv: "/nix/store/fake.drv", archive: "/tmp/spindrift-image.tar"},
		cli:    cliScript,
	}

	var ensureErr error
	stderr := runnertest.CaptureStderrDuring(t, func() { ensureErr = a.EnsureReady() })

	if ensureErr != nil {
		t.Fatalf("EnsureReady: unexpected error: %v", ensureErr)
	}
	if !strings.Contains(stderr, "could not lock image") || !strings.Contains(stderr, "continuing unlocked") {
		t.Errorf("stderr missing the acquire-failure degrade warning; got: %q", stderr)
	}
	if got := runnertest.CallCount(t, cliDir); got != 4 {
		t.Errorf("fake CLI callCount = %d, want 4 (two inspects, load, tag) — the build must still proceed unlocked", got)
	}
}
