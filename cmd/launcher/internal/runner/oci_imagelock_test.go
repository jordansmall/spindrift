package runner

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The warm path (image already present) must never touch the lock at all —
// issue #3632's "single-child path unchanged in cost" criterion.
func TestEnsureReady_ImagePresent_NoLockFileCreated(t *testing.T) {
	dir := redirectImageLockDir(t)
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	a := &ociAdapter{cli: script, image: "spindrift:abc123"}

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
	redirectImageLockDir(t)
	const image = "spindrift:abc123"

	holder, err := acquireImageLock(image, nil)
	if err != nil {
		t.Fatalf("acquireImageLock (holder): %v", err)
	}

	// call 0: the outside-the-lock probe, before this child even tries to
	// acquire — must see absent, or it would never attempt the lock at all.
	// call 1+: the re-probe under the lock, taken only after the holder
	// releases — scripted present, standing in for "the winner finished".
	cliScript, cliDir := newFakeCLI(t, fakeCall{exit: 1}, fakeCall{exit: 0})

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

	a := &ociAdapter{cli: cliScript, image: image, imageDrv: "/nix/store/fake.drv"}

	type result struct{ err error }
	done := make(chan result, 1)
	var res result

	stdout := captureStdoutDuring(t, func() {
		go func() {
			done <- result{err: a.EnsureReady()}
		}()

		select {
		case r := <-done:
			t.Fatalf("EnsureReady returned early (err=%v) while the holder still held the lock", r.err)
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
	if got := callCount(t, cliDir); got != 2 {
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
	cliScript, cliDir := newFakeCLI(t,
		fakeCall{exit: 1}, // image inspect: absent (outside the lock)
		fakeCall{exit: 1}, // image inspect: still absent (re-probe under the lock)
		fakeCall{},        // load
		fakeCall{},        // tag
	)

	nixScript, _ := newFakeCLI(t, fakeCall{exit: 0})
	origExec := execCommand
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(nixScript, args...)
	}
	t.Cleanup(func() { execCommand = origExec })

	a := &ociAdapter{
		cli:          cliScript,
		image:        "spindrift:abc123",
		imageDrv:     "/nix/store/fake.drv",
		imageArchive: "/tmp/spindrift-image.tar",
		imageTag:     "spindrift:abc123",
	}

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: unexpected error: %v", err)
	}
	if gotName != "nix" {
		t.Errorf("execCommand called with %q, want %q", gotName, "nix")
	}
	if got := callCount(t, cliDir); got != 4 {
		t.Errorf("fake CLI callCount = %d, want 4 (two inspects, load, tag)", got)
	}
}

// A lock dir that cannot even be created/opened must degrade to a stderr
// warning and an unlocked build, not to a failed EnsureReady — issue
// #3632's acquire-failure degrade path, previously asserted only by a
// comment in oci.go.
func TestEnsureReady_LockAcquireFails_ContinuesUnlockedWithWarning(t *testing.T) {
	// A nonexistent parent makes acquireImageLock's os.OpenFile fail
	// outright, standing in for "the lock dir cannot be created/opened" —
	// distinct from a contended lock (which succeeds at OpenFile and blocks
	// on flock instead). t.TempDir()'s result is captured out here, not
	// called inside the closure, for the reason redirectImageLockDir
	// documents.
	missing := filepath.Join(t.TempDir(), "nonexistent-parent", "nested")
	orig := imageLockDir
	imageLockDir = func() string { return missing }
	t.Cleanup(func() { imageLockDir = orig })

	cliScript, cliDir := newFakeCLI(t,
		fakeCall{exit: 1}, // image inspect: absent (outside the lock)
		fakeCall{exit: 1}, // image inspect: still absent (re-probe, despite the failed acquire)
		fakeCall{},        // load
		fakeCall{},        // tag
	)

	nixScript, _ := newFakeCLI(t, fakeCall{exit: 0})
	origExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(nixScript, args...)
	}
	t.Cleanup(func() { execCommand = origExec })

	a := &ociAdapter{
		cli:          cliScript,
		image:        "spindrift:abc123",
		imageDrv:     "/nix/store/fake.drv",
		imageArchive: "/tmp/spindrift-image.tar",
		imageTag:     "spindrift:abc123",
	}

	var ensureErr error
	stderr := captureStderrDuring(t, func() { ensureErr = a.EnsureReady() })

	if ensureErr != nil {
		t.Fatalf("EnsureReady: unexpected error: %v", ensureErr)
	}
	if !strings.Contains(stderr, "could not lock image") || !strings.Contains(stderr, "continuing unlocked") {
		t.Errorf("stderr missing the acquire-failure degrade warning; got: %q", stderr)
	}
	if got := callCount(t, cliDir); got != 4 {
		t.Errorf("fake CLI callCount = %d, want 4 (two inspects, load, tag) — the build must still proceed unlocked", got)
	}
}
