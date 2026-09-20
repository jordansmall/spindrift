package runner

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// redirectImageLockDir points imageLockDir at a single captured dir for the
// life of the test. Capturing t.TempDir()'s result in a variable matters:
// calling t.TempDir() again inside the closure would hand every acquire a
// fresh directory and defeat the redirect.
func redirectImageLockDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := imageLockDir
	imageLockDir = func() string { return dir }
	t.Cleanup(func() { imageLockDir = orig })
	return dir
}

func TestAcquireImageLock_FreshAcquireSucceedsAndReleases(t *testing.T) {
	redirectImageLockDir(t)

	var onWaitCalled bool
	lock, err := acquireImageLock("agent-image:latest", func() { onWaitCalled = true })
	if err != nil {
		t.Fatalf("acquireImageLock: unexpected error: %v", err)
	}
	if onWaitCalled {
		t.Errorf("onWait called on a fresh (uncontended) acquire, want not called")
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: unexpected error: %v", err)
	}
}

func TestAcquireImageLock_SecondAcquireBlocksUntilReleaseThenOnWaitFired(t *testing.T) {
	redirectImageLockDir(t)

	first, err := acquireImageLock("agent-image:latest", nil)
	if err != nil {
		t.Fatalf("first acquireImageLock: unexpected error: %v", err)
	}

	type result struct {
		lock *imageLock
		err  error
	}
	done := make(chan result, 1)
	onWaitCh := make(chan struct{}, 1)
	go func() {
		lock, err := acquireImageLock("agent-image:latest", func() { onWaitCh <- struct{}{} })
		done <- result{lock: lock, err: err}
	}()

	select {
	case r := <-done:
		t.Fatalf("second acquireImageLock returned early (lock=%v err=%v) while first still held", r.lock, r.err)
	case <-time.After(200 * time.Millisecond):
	}

	select {
	case <-onWaitCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("onWait never fired while the second acquire was blocked on the first")
	}

	if err := first.Release(); err != nil {
		t.Fatalf("first Release: unexpected error: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("second acquireImageLock: unexpected error: %v", r.err)
		}
		if err := r.lock.Release(); err != nil {
			t.Fatalf("second Release: unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for second acquireImageLock after release")
	}
}

func TestAcquireImageLock_DifferentImagesNeverBlockEachOther(t *testing.T) {
	redirectImageLockDir(t)

	first, err := acquireImageLock("agent-image:one", nil)
	if err != nil {
		t.Fatalf("first acquireImageLock: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = first.Release() })

	done := make(chan error, 1)
	go func() {
		second, err := acquireImageLock("agent-image:two", nil)
		if err == nil {
			_ = second.Release()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second acquireImageLock (different image): unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("acquireImageLock for a different image blocked; per-image locks must not contend")
	}
}

func TestImageLockPath_StableInsideDirAndDiffersByImage(t *testing.T) {
	dir := redirectImageLockDir(t)

	a1 := imageLockPath("agent-image:one")
	a2 := imageLockPath("agent-image:one")
	b := imageLockPath("agent-image:two")

	if a1 != a2 {
		t.Errorf("imageLockPath not stable for the same reference: %q != %q", a1, a2)
	}
	if a1 == b {
		t.Errorf("imageLockPath collided for different references: %q", a1)
	}
	if !strings.HasPrefix(a1, dir) {
		t.Errorf("imageLockPath %q not inside imageLockDir() %q", a1, dir)
	}
}

// imageLockHolderHelperEnv, when set, tells TestImageLockHolderHelper to act
// as the standalone lock-holding subprocess rather than a no-op.
const imageLockHolderHelperEnv = "SPINDRIFT_IMAGE_LOCK_HOLDER_DIR"

// TestImageLockHolderHelper is not a real test: it is re-executed as a
// subprocess (via os.Args[0]) by
// TestAcquireImageLock_ReleasedAfterHolderKilled to hold an imageLock until
// killed. It returns immediately as a no-op under a normal `go test` run,
// since the sentinel env var is unset.
func TestImageLockHolderHelper(t *testing.T) {
	dir := os.Getenv(imageLockHolderHelperEnv)
	if dir == "" {
		return
	}
	imageLockDir = func() string { return dir }

	lock, err := acquireImageLock("agent-image:latest", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: acquireImageLock: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("ready")
	time.Sleep(time.Minute)
	// Keep lock (and the fd its flock rides on) reachable across the sleep
	// above — the exact window the parent kills us in. os.OpenFile attaches a
	// close-on-unreachable cleanup to the *os.File, so without this a GC cycle
	// could close the fd and drop the flock mid-sleep, releasing the lock for
	// a reason the test does not mean to measure.
	runtime.KeepAlive(lock)
}

func TestAcquireImageLock_ReleasedAfterHolderKilled(t *testing.T) {
	dir := t.TempDir()
	orig := imageLockDir
	imageLockDir = func() string { return dir }
	t.Cleanup(func() { imageLockDir = orig })

	cmd := exec.Command(os.Args[0], "-test.run=^TestImageLockHolderHelper$", "-test.v")
	cmd.Env = append(os.Environ(), imageLockHolderHelperEnv+"="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	ready := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		total := ""
		for !strings.Contains(total, "ready") {
			n, err := stdout.Read(buf)
			if n > 0 {
				total += string(buf[:n])
			}
			if err != nil {
				readErr <- err
				return
			}
		}
		close(ready)
	}()

	select {
	case <-ready:
	case err := <-readErr:
		t.Fatalf("reading helper stdout before ready marker: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for helper ready marker")
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL helper (pid %d): %v", cmd.Process.Pid, err)
	}

	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for killed helper to exit")
	}

	acquired := make(chan struct{})
	acquireErr := make(chan error, 1)
	go func() {
		var onWaitCalled bool
		lock, err := acquireImageLock("agent-image:latest", func() { onWaitCalled = true })
		if err != nil {
			acquireErr <- err
			return
		}
		if onWaitCalled {
			acquireErr <- fmt.Errorf("onWait called after holder killed, want not called (kernel should have dropped the lock immediately)")
			return
		}
		defer lock.Release()
		close(acquired)
	}()

	select {
	case <-acquired:
	case err := <-acquireErr:
		t.Fatalf("acquireImageLock after holder killed: unexpected error: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting to acquire lock after holder killed")
	}
}
