package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestAcquireCheckoutLock_SucceedsThroughMomentarySharedProbe pins the
// issue #3545 review finding: a status reader's probeCheckoutLock takes
// LOCK_SH for a few syscalls then releases it, and a daemon starting in
// that window must not mistake it for a second daemon. This simulates that
// probe directly (LOCK_SH, not via AcquireCheckoutLock) and releases it
// from a goroutine so AcquireCheckoutLock's retry has to actually win the
// race rather than succeeding trivially on the first attempt.
func TestAcquireCheckoutLock_SucceedsThroughMomentarySharedProbe(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, checkoutLockFileName)

	probeFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock file for simulated probe: %v", err)
	}
	if err := syscall.Flock(int(probeFile.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Fatalf("take LOCK_SH for simulated probe: %v", err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(acquireGraceStep)
		_ = syscall.Flock(int(probeFile.Fd()), syscall.LOCK_UN)
		_ = probeFile.Close()
		close(released)
	}()
	t.Cleanup(func() { <-released })

	lock, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: want success once the simulated probe releases, got error: %v", err)
	}
	defer lock.Release()
}

func TestAcquireCheckoutLock_WritesHolderIdentity(t *testing.T) {
	dir := t.TempDir()

	lock, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: unexpected error: %v", err)
	}
	defer lock.Release()

	got := readHolderIdentity(lock.file)
	want := fmt.Sprintf("pid=%d", os.Getpid())
	if !strings.Contains(got, want) {
		t.Fatalf("identity line %q does not contain %q", got, want)
	}
	if !strings.Contains(got, "kind=dispatch") {
		t.Fatalf("identity line %q does not contain kind=dispatch", got)
	}
}

func TestAcquireCheckoutLock_SecondAcquireInSameProcessRefused(t *testing.T) {
	// flock(2) locks are per open-file-description, not per-process, so a
	// second os.OpenFile of the same lock file in the same process still
	// conflicts. That makes this a cheap, in-process refusal test.
	dir := t.TempDir()

	first, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		t.Fatalf("first AcquireCheckoutLock: unexpected error: %v", err)
	}
	defer first.Release()

	// first genuinely holds LOCK_EX for the rest of the test, so the
	// second acquire must exhaust the retry added for issue #3545 and
	// still refuse — bound the refusal against acquireGraceTotal itself
	// (with slack for scheduling), not a magic duration, so the test
	// keeps tracking whatever the const is tuned to.
	start := time.Now()
	_, err = AcquireCheckoutLock(dir, []Kind{KindResearch})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("second AcquireCheckoutLock: want error, got nil")
	}
	if elapsed > 3*acquireGraceTotal {
		t.Fatalf("second AcquireCheckoutLock took %v, want at most ~%v (3x acquireGraceTotal for scheduling slack)", elapsed, 3*acquireGraceTotal)
	}

	want := fmt.Sprintf("pid=%d", os.Getpid())
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("refusal error %q does not name holder %q", err.Error(), want)
	}
	if !strings.Contains(err.Error(), "pid=") {
		t.Fatalf("refusal error %q does not contain pid=", err.Error())
	}
}

func TestCheckoutLock_ReleaseAllowsFreshAcquireSameProcess(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		t.Fatalf("first AcquireCheckoutLock: unexpected error: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: unexpected error: %v", err)
	}

	second, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		t.Fatalf("second AcquireCheckoutLock after release: unexpected error: %v", err)
	}
	defer second.Release()
}

// checkoutLockHolderHelperEnv, when set, tells TestCheckoutLockHolderHelper
// to act as the standalone lock-holding subprocess rather than a no-op.
const checkoutLockHolderHelperEnv = "SPINDRIFT_LOCK_HOLDER_HELPER_DIR"

// TestCheckoutLockHolderHelper is not a real test: it is re-executed as a
// subprocess (via os.Args[0]) by
// TestAcquireCheckoutLock_ReleasedAfterHolderKilled to hold a CheckoutLock
// until killed. It returns immediately as a no-op under a normal `go test`
// run, since the sentinel env var is unset.
func TestCheckoutLockHolderHelper(t *testing.T) {
	dir := os.Getenv(checkoutLockHolderHelperEnv)
	if dir == "" {
		return
	}

	lock, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: AcquireCheckoutLock: %v\n", err)
		os.Exit(1)
	}
	_ = lock

	fmt.Println("ready")
	time.Sleep(time.Minute)
}

func TestAcquireCheckoutLock_ReleasedAfterHolderKilled(t *testing.T) {
	dir := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckoutLockHolderHelper$", "-test.v")
	cmd.Env = append(os.Environ(), checkoutLockHolderHelperEnv+"="+dir)
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

	// Sanity: helper genuinely holds the lock now.
	if _, err := AcquireCheckoutLock(dir, []Kind{KindDispatch}); err == nil {
		t.Fatalf("AcquireCheckoutLock: want refusal while helper holds lock, got nil error")
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL helper (pid %s): %v", strconv.Itoa(cmd.Process.Pid), err)
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
		lock, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
		if err != nil {
			acquireErr <- err
			return
		}
		defer lock.Release()
		close(acquired)
	}()

	select {
	case <-acquired:
	case err := <-acquireErr:
		t.Fatalf("AcquireCheckoutLock after holder killed: unexpected error: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting to acquire lock after holder killed")
	}
}
