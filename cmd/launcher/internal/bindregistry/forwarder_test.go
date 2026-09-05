package bindregistry

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

// TestEnsureForwarderReady_AlreadyReady verifies that when probe reports the
// Forwarder is already listening, EnsureForwarderReady returns success
// without ever calling spawn -- the double-spawn-prevention path a re-apply
// run depends on.
func TestEnsureForwarderReady_AlreadyReady(t *testing.T) {
	spawnCalled := false
	probe := func(port int) bool { return true }
	spawn := func(socketPath string, port int) (int, error) {
		spawnCalled = true
		return 0, nil
	}

	ready, pid, err := EnsureForwarderReady("/tmp/sock", 27182, probe, spawn, 20*time.Millisecond, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("EnsureForwarderReady returned err = %v, want nil", err)
	}
	if !ready {
		t.Errorf("EnsureForwarderReady ready = false, want true")
	}
	if spawnCalled {
		t.Errorf("spawn was called, want it never called when already ready")
	}
	if pid != 0 {
		t.Errorf("EnsureForwarderReady pid = %d, want 0 when already ready (spawn never called)", pid)
	}
}

// TestEnsureForwarderReady_SpawnThenReady verifies that when probe first
// reports not-ready, EnsureForwarderReady spawns exactly once and then polls
// probe until it flips ready.
func TestEnsureForwarderReady_SpawnThenReady(t *testing.T) {
	probeCalls := 0
	probe := func(port int) bool {
		probeCalls++
		// First call: pre-spawn already-ready check (false). Then a few
		// more false polls before flipping ready.
		return probeCalls > 3
	}
	spawnCalls := 0
	const wantPid = 12345
	spawn := func(socketPath string, port int) (int, error) {
		spawnCalls++
		return wantPid, nil
	}

	ready, pid, err := EnsureForwarderReady("/tmp/sock", 27182, probe, spawn, 200*time.Millisecond, 2*time.Millisecond)
	if err != nil {
		t.Fatalf("EnsureForwarderReady returned err = %v, want nil", err)
	}
	if !ready {
		t.Errorf("EnsureForwarderReady ready = false, want true")
	}
	if spawnCalls != 1 {
		t.Errorf("spawn called %d times, want exactly 1", spawnCalls)
	}
	if pid != wantPid {
		t.Errorf("EnsureForwarderReady pid = %d, want %d (from spawn)", pid, wantPid)
	}
}

// TestEnsureForwarderReady_SpawnThenTimeout verifies that when probe never
// flips ready after a successful spawn, EnsureForwarderReady gives up after
// timeout and returns (false, nil) -- a timeout is not itself a Go error.
func TestEnsureForwarderReady_SpawnThenTimeout(t *testing.T) {
	probe := func(port int) bool { return false }
	spawnCalls := 0
	const wantPid = 54321
	spawn := func(socketPath string, port int) (int, error) {
		spawnCalls++
		return wantPid, nil
	}

	ready, pid, err := EnsureForwarderReady("/tmp/sock", 27182, probe, spawn, 20*time.Millisecond, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("EnsureForwarderReady returned err = %v, want nil", err)
	}
	if ready {
		t.Errorf("EnsureForwarderReady ready = true, want false on timeout")
	}
	if spawnCalls != 1 {
		t.Errorf("spawn called %d times, want exactly 1", spawnCalls)
	}
	if pid != wantPid {
		t.Errorf("EnsureForwarderReady pid = %d, want %d (spawn succeeded even though Forwarder never became ready)", pid, wantPid)
	}
}

// TestEnsureForwarderReady_SpawnError verifies that a spawn failure (e.g.
// socat missing from PATH) short-circuits: EnsureForwarderReady returns the
// spawn error verbatim without polling probe again afterward.
func TestEnsureForwarderReady_SpawnError(t *testing.T) {
	wantErr := errors.New("exec: \"socat\": executable file not found in $PATH")
	probeCallsAfterSpawn := 0
	spawnAttempted := false
	probe := func(port int) bool {
		if spawnAttempted {
			probeCallsAfterSpawn++
		}
		return false
	}
	spawn := func(socketPath string, port int) (int, error) {
		spawnAttempted = true
		return 0, wantErr
	}

	ready, pid, err := EnsureForwarderReady("/tmp/sock", 27182, probe, spawn, 20*time.Millisecond, 5*time.Millisecond)
	if !errors.Is(err, wantErr) {
		t.Fatalf("EnsureForwarderReady err = %v, want %v", err, wantErr)
	}
	if ready {
		t.Errorf("EnsureForwarderReady ready = true, want false on spawn error")
	}
	if pid != 0 {
		t.Errorf("EnsureForwarderReady pid = %d, want 0 on spawn error", pid)
	}
	if probeCallsAfterSpawn != 0 {
		t.Errorf("probe called %d times after spawn error, want 0", probeCallsAfterSpawn)
	}
}

// fdCloExec reports whether fd has FD_CLOEXEC set, via a raw fcntl(F_GETFD)
// -- there is no portable stdlib wrapper for this outside golang.org/x/sys,
// which this module only pulls in indirectly.
func fdCloExec(t *testing.T, fd int) bool {
	t.Helper()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFD), 0)
	if errno != 0 {
		t.Fatalf("fcntl(F_GETFD, %d): %v", fd, errno)
	}
	return flags&syscall.FD_CLOEXEC != 0
}

// TestCloseOnExecInheritedFDs_MarksLeakedFD reproduces the bug: a bare fd
// this process holds open without FD_CLOEXEC (standing in for a pipe/file
// inherited from an unwitting shell ancestor, e.g. bats' own pipe) is
// exactly what a plain fork+exec would otherwise hand to a detached child
// like the Forwarder. Before closeOnExecInheritedFDs runs, the fd is
// exec-inheritable; after, it must not be.
func TestCloseOnExecInheritedFDs_MarksLeakedFD(t *testing.T) {
	var fds [2]int
	if err := syscall.Pipe(fds[:]); err != nil {
		t.Fatalf("syscall.Pipe: %v", err)
	}
	readFD, writeFD := fds[0], fds[1]
	t.Cleanup(func() {
		syscall.Close(readFD)
		syscall.Close(writeFD)
	})

	// syscall.Pipe (unlike os.Pipe) does not set O_CLOEXEC, so both ends
	// start out exec-inheritable -- the precondition this test exists to
	// reproduce.
	if fdCloExec(t, readFD) {
		t.Fatalf("readFD %d already close-on-exec before the fix runs, precondition broken", readFD)
	}
	if fdCloExec(t, writeFD) {
		t.Fatalf("writeFD %d already close-on-exec before the fix runs, precondition broken", writeFD)
	}

	if err := closeOnExecInheritedFDs(); err != nil {
		t.Fatalf("closeOnExecInheritedFDs: %v", err)
	}

	if !fdCloExec(t, readFD) {
		t.Errorf("readFD %d not close-on-exec after closeOnExecInheritedFDs, a detached child would inherit it", readFD)
	}
	if !fdCloExec(t, writeFD) {
		t.Errorf("writeFD %d not close-on-exec after closeOnExecInheritedFDs, a detached child would inherit it", writeFD)
	}
}
