package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

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

	_, err = AcquireCheckoutLock(dir, []Kind{KindResearch})
	if err == nil {
		t.Fatalf("second AcquireCheckoutLock: want error, got nil")
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
