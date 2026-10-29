package bwrap

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runner/runnertest"
)

// nixOnlyExec stubs execCommand so every nix call succeeds and sqlite3's
// VACUUM INTO writes content after sleeping, counting invocations.
func nixOnlyExec(t *testing.T, content string, sleep time.Duration) *atomic.Int32 {
	t.Helper()
	var vacuums atomic.Int32
	fakeVacuumInto(t, "true", content, 0, func(string) {
		vacuums.Add(1)
		time.Sleep(sleep)
	})
	return &vacuums
}

// ensureReadyWithin fails the test when EnsureReady has not returned by the
// deadline: a Box holding the generation's shared lock must never block it.
func ensureReadyWithin(t *testing.T, a *bwrapAdapter, d time.Duration) {
	t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- a.EnsureReady() }()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("EnsureReady: %v", err)
		}
	case <-time.After(d):
		t.Fatalf("EnsureReady still blocked after %v", d)
	}
}

func lockShared(t *testing.T, path string) *os.File {
	t.Helper()
	lf, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lf.Close() })
	return lf
}

// staleGeneration makes a sibling generation dir with a db.sqlite under f's root.
func staleGeneration(t *testing.T, f readyFixture, name string) string {
	t.Helper()
	dir := filepath.Join(f.a.prepare.nixVarSnapshotRoot, name)
	seedSnapshot(t, versionedDBPath(dir), "old")
	return dir
}

// Concurrent cold starts on one generation write the snapshot once; the rest
// wait on the realize lock and find it installed.
func TestBwrapRunEnsureReady_ConcurrentColdStartsVacuumOnce(t *testing.T) {
	runnertest.InstallImageBuildPipe(t)
	vacuums := nixOnlyExec(t, "complete snapshot", 200*time.Millisecond)
	f := newReadyFixture(t)

	const n = 4
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		a := &bwrapAdapter{prepare: f.a.prepare}
		wg.Add(1)
		go func() { defer wg.Done(); errs <- a.EnsureReady() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("EnsureReady: %v", err)
		}
	}
	if got := vacuums.Load(); got != 1 {
		t.Errorf("VACUUM INTO ran %d times, want 1", got)
	}
	got, err := os.ReadFile(f.dbPath)
	if err != nil || string(got) != "complete snapshot" {
		t.Errorf("db.sqlite = %q, %v; want the complete snapshot", got, err)
	}
}

// A launcher that finds the realize lock held says so, then skips the copy
// once the holder has installed the snapshot.
func TestBwrapRunEnsureReady_WaiterSkipsVacuumAfterSiblingInstalls(t *testing.T) {
	runnertest.InstallImageBuildPipe(t)
	vacuums := nixOnlyExec(t, "must not be written", 0)
	f := newReadyFixture(t)
	root := f.a.prepare.nixVarSnapshotRoot
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := lockRealize(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Released on every path, so a failed assertion cannot leave the lock held
	// past the test; UnlockSnapshot is a no-op on an already-closed file.
	t.Cleanup(func() { UnlockSnapshot(held) })

	// fn runs on the test goroutine and uses only Error/Errorf; EnsureReady's
	// goroutine only reports back on errc.
	errc := make(chan error, 1)
	out := runnertest.CaptureStdoutDuring(t, func() {
		go func() { errc <- f.a.EnsureReady() }()
		select {
		case err := <-errc:
			t.Errorf("EnsureReady returned %v while the realize lock was held", err)
			return
		case <-time.After(300 * time.Millisecond):
		}
		seedSnapshot(t, f.dbPath, "sibling's snapshot")
		UnlockSnapshot(held)
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("EnsureReady: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("EnsureReady did not resume after the realize lock was released")
		}
	})
	if !strings.Contains(out, "a sibling launcher is writing a nix-var snapshot under "+f.a.prepare.nixVarSnapshotRoot) {
		t.Errorf("output lacks the waiting line:\n%s", out)
	}
	if got := vacuums.Load(); got != 0 {
		t.Errorf("VACUUM INTO ran %d times, want 0 for a waiter", got)
	}
	got, _ := os.ReadFile(f.dbPath)
	if string(got) != "sibling's snapshot" {
		t.Errorf("db.sqlite = %q, want the sibling's", got)
	}
}

// The realize lock lives beside the snapshot root: inside it, reclaim would
// sweep it as a generation lock.
func TestRealizeLockPath_IsSiblingOfRoot(t *testing.T) {
	root := NixVarSnapshotRoot(t.TempDir())
	got := RealizeLockPath(root)
	if filepath.Dir(got) != filepath.Dir(root) || got == SnapshotLockPath(root) {
		t.Errorf("RealizeLockPath(%q) = %q, want a distinct sibling of the root", root, got)
	}
}

// A running Box holds the generation lock shared for its lifetime; readiness
// must never queue behind it.
func TestBwrapRunEnsureReady_RunningBoxNeverBlocksReadiness(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot bool
	}{
		{"closure missing, snapshot present", true},
		{"snapshot absent", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runnertest.InstallImageBuildPipe(t)
			nixOnlyExec(t, "fresh", 0)
			f := newReadyFixture(t)
			if tc.snapshot {
				seedSnapshot(t, f.dbPath, "running box reads this")
				if err := os.Remove(f.paths[1]); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(f.a.prepare.nixVarSnapshotRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			lockShared(t, SnapshotLockPath(f.a.prepare.nixVarSnapshotDir))

			ensureReadyWithin(t, f.a, 10*time.Second)
		})
	}
}

func TestBwrapRunEnsureReady_ColdReclaimsUnlockedStaleGeneration(t *testing.T) {
	runnertest.InstallImageBuildPipe(t)
	nixOnlyExec(t, "fresh", 0)
	f := newReadyFixture(t)
	stale := staleGeneration(t, f, "old-gen")

	if err := f.a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale generation Stat = %v, want IsNotExist", err)
	}
	if _, err := os.Stat(f.dbPath); err != nil {
		t.Errorf("current snapshot Stat = %v, want it kept", err)
	}
}

func TestBwrapRunEnsureReady_ColdKeepsGenerationWithLiveLock(t *testing.T) {
	runnertest.InstallImageBuildPipe(t)
	nixOnlyExec(t, "fresh", 0)
	f := newReadyFixture(t)
	stale := staleGeneration(t, f, "old-gen")
	lockShared(t, SnapshotLockPath(stale))

	if err := f.a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("locked generation Stat = %v, want it kept", err)
	}
}

// TestHelperHoldFD is the child of the inherited-fd test, not a test of its
// own: it holds the lock fd it was handed until the parent closes its stdin.
// It blocks in a read rather than select {}: with no timer pending (a re-exec
// gets no -test.timeout) the runtime would call select {} a deadlock and exit,
// dropping the lock.
func TestHelperHoldFD(t *testing.T) {
	if os.Getenv("SPINDRIFT_TEST_HOLD_FD") != "1" {
		t.Skip("helper process only")
	}
	_, _ = os.Stdout.WriteString("holding\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// A launcher inherited across a Daemon in-place restart holds its generation
// lock through an fd another process opened; that lock must still protect the
// generation once the opener closed its own fd.
func TestBwrapRunEnsureReady_ColdKeepsGenerationLockedByInheritedFD(t *testing.T) {
	runnertest.InstallImageBuildPipe(t)
	nixOnlyExec(t, "fresh", 0)
	f := newReadyFixture(t)
	stale := staleGeneration(t, f, "old-gen")

	lf := lockShared(t, SnapshotLockPath(stale))
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldFD$")
	cmd.Env = append(os.Environ(), "SPINDRIFT_TEST_HOLD_FD=1")
	cmd.ExtraFiles = []*os.File{lf}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	// Proves the child is alive, and so still holds the lock, once lf closes.
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "holding\n" {
		t.Fatalf("helper child did not start holding the lock: %q, %v; stderr: %s", line, err, stderr.String())
	}
	lf.Close()

	if err := f.a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("generation locked by the inherited fd Stat = %v, want it kept", err)
	}
}

// The flat/legacy path has no sibling generations, only unrelated neighbours
// under .spindrift that a sweep must never touch.
func TestBwrapRunEnsureReady_ColdUnscopedSnapshotNeverReclaims(t *testing.T) {
	runnertest.InstallImageBuildPipe(t)
	nixOnlyExec(t, "fresh", 0)
	f := newReadyFixture(t)
	root := f.a.prepare.nixVarSnapshotRoot
	f.a.prepare.nixVarGeneration = ""
	f.a.prepare.nixVarSnapshotDir = root
	neighbour := filepath.Join(filepath.Dir(root), "accum.git")
	inside := filepath.Join(root, "other-gen")
	for _, d := range []string{neighbour, inside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	out := runnertest.CaptureStdoutDuring(t, func() {
		if err := f.a.EnsureReady(); err != nil {
			t.Errorf("EnsureReady: %v", err)
		}
	})
	for _, p := range []string{neighbour, inside} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("Stat(%s) = %v, want it kept", p, err)
		}
	}
	if !strings.Contains(out, "unscoped") {
		t.Errorf("output lacks the unscoped line:\n%s", out)
	}
}

func TestBwrapRunEnsureReady_WarmNeverReclaims(t *testing.T) {
	runnertest.InstallImageBuildPipe(t)
	failExec(t)
	f := newReadyFixture(t)
	seedSnapshot(t, f.dbPath, "snapshot")
	stale := staleGeneration(t, f, "old-gen")

	if err := f.a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("stale generation Stat = %v, want a warm start to leave it", err)
	}
}

// The build adapter takes the realize lock too: a launcher build that finds it
// held waits, then skips the copy once the holder has installed the snapshot.
func TestBwrapBuildEnsureReady_WaiterSkipsVacuumAfterSiblingInstalls(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	var vacuums atomic.Int32
	fakeVacuumInto(t, script, "must not be written", 0, func(string) { vacuums.Add(1) })
	root := filepath.Join(t.TempDir(), "nix-var-snapshot")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	const generation = "gen1"
	genDir := filepath.Join(root, generation)
	a := newSnapshotBuildAdapter(genDir)
	a.nixVarSnapshotRoot = root
	a.nixVarGeneration = generation
	a.nixGCRootsRoot = t.TempDir()
	held, err := lockRealize(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnlockSnapshot(held) })

	errc := make(chan error, 1)
	out := runnertest.CaptureStdoutDuring(t, func() {
		go func() { errc <- a.EnsureReady() }()
		select {
		case err := <-errc:
			t.Errorf("EnsureReady returned %v while the realize lock was held", err)
			return
		case <-time.After(300 * time.Millisecond):
		}
		seedSnapshot(t, versionedDBPath(genDir), "sibling's snapshot")
		UnlockSnapshot(held)
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("EnsureReady: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("EnsureReady did not resume after the realize lock was released")
		}
	})
	if !strings.Contains(out, "a sibling launcher is writing a nix-var snapshot under "+root) {
		t.Errorf("output lacks the waiting line:\n%s", out)
	}
	if got := vacuums.Load(); got != 0 {
		t.Errorf("VACUUM INTO ran %d times, want 0 for a waiter", got)
	}
	got, _ := os.ReadFile(versionedDBPath(genDir))
	if string(got) != "sibling's snapshot" {
		t.Errorf("db.sqlite = %q, want the sibling's", got)
	}
}
