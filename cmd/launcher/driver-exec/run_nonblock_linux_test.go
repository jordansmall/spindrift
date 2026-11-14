// The repro needs Linux pipe ioctls, and the bwrap runner whose shared fd 1/fd 2
// pipe it models is Linux-only.

package main

import (
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

const nonblockStderrEnv = "DRIVER_EXEC_TEST_NONBLOCK_STDERR"

// TestHelperNonblockStderrDriver is the fake Driver for
// TestRunSurvivesDriverSettingNonblockOnSharedStderr: like libuv, it flips
// O_NONBLOCK on its own fd 2, then streams one large line and exits.
func TestHelperNonblockStderrDriver(t *testing.T) {
	if os.Getenv(nonblockStderrEnv) == "" {
		t.Skip("helper process only")
	}
	if err := syscall.SetNonblock(2, true); err != nil {
		os.Exit(3)
	}
	line := `{"type":"user","message":"` + strings.Repeat("x", 1<<20) + `"}` + "\n"
	// Ignore SIGPIPE and the write error so a broken stream surfaces from
	// driver-exec's own copy error, as it does for a Driver that exits cleanly.
	signal.Ignore(syscall.SIGPIPE)
	os.Stdout.WriteString(line)
	os.Exit(0)
}

// The bwrap runner gives the Box one pipe as both fd 1 and fd 2, so a Driver
// that sets O_NONBLOCK on its inherited stderr flips driver-exec's stdout to
// non-blocking under Go's feet; a write into a full pipe then fails with EAGAIN
// instead of waiting (issue #6848).
func TestRunSurvivesDriverSettingNonblockOnSharedStderr(t *testing.T) {
	var fds [2]int
	if err := syscall.Pipe2(fds[:], syscall.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	// os.NewFile on a blocking fd yields a non-pollable *os.File, as os.Stdout is.
	r := os.NewFile(uintptr(fds[0]), "box-pipe-r")
	w := os.NewFile(uintptr(fds[1]), "box-pipe-w")
	t.Cleanup(func() { r.Close(); w.Close() })

	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := writeFakeDriver(t, dir, "fake-driver",
		nonblockStderrEnv+"=1 exec "+self+" -test.run='^TestHelperNonblockStderrDriver$'\n")
	cfg := execConfig{
		driverBin:    bin,
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		issue:        "7",
	}

	type result struct {
		rc  int
		err error
	}
	done := make(chan result, 1)
	go func() {
		rc, err := run(cfg, w)
		done <- result{rc, err}
	}()

	// Drain only once the pipe is full: the Driver set O_NONBLOCK before it
	// wrote, so the next driver-exec write hits the full-pipe case.
	waitPipeFull(t, fds[0])
	got := make(chan int, 1)
	go func() {
		n, _ := io.Copy(io.Discard, r)
		got <- int(n)
	}()

	res := <-done
	w.Close()
	if res.err != nil {
		t.Fatalf("run: %v", res.err)
	}
	if res.rc != 0 {
		t.Errorf("exit code = %d, want 0", res.rc)
	}
	if n := <-got; n < 1<<20 {
		t.Errorf("pipe carried %d bytes, want the whole >=1 MiB line", n)
	}
}

// waitPipeFull blocks until the pipe's read end fd holds a full pipe's worth.
func waitPipeFull(t *testing.T, fd int) {
	t.Helper()
	const fGetPipeSz = 1032
	size, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), fGetPipeSz, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var n int32
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCINQ, uintptr(unsafe.Pointer(&n))); errno != 0 {
			t.Fatal(errno)
		}
		if int(n) >= int(size) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("pipe never filled")
}
