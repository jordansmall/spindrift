package bindregistry

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// ForwarderPort is the fixed TCP port the in-Box Forwarder listens on at
// 127.0.0.1 (ADR 0044/0045, issue #3141). Every caller shares this one
// declaration so the number cannot drift. It is not configurable: it is an
// internal contract between the Forwarder and the ecosystem bindings this
// package computes against it.
const ForwarderPort = 27182

// ProbeFunc reports whether something is already listening on
// 127.0.0.1:port. Injected so EnsureForwarderReady's tests never touch a
// real socket.
type ProbeFunc func(port int) bool

// SpawnFunc starts the Forwarder (socat) detached, bridging socketPath to
// 127.0.0.1:port. Injected so EnsureForwarderReady's tests never launch a
// real process.
type SpawnFunc func(socketPath string, port int) (int, error)

// DialProbe is the production ProbeFunc: a short-timeout TCP dial against
// 127.0.0.1:port. Any dial error means nothing is listening yet.
func DialProbe(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// SpawnSocat starts socat detached, bridging a UNIX socket to a TCP listener
// on 127.0.0.1:port. It returns the exec.LookPath error unwrapped so callers
// can tell "socat is missing" from "socat started but never became ready".
// Side effect: it marks every inherited fd above stderr close-on-exec, which
// is process-wide and outlives this call.
func SpawnSocat(socketPath string, port int) (int, error) {
	path, err := exec.LookPath("socat")
	if err != nil {
		return 0, err
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devNull.Close()

	cmd := exec.Command(path,
		fmt.Sprintf("TCP-LISTEN:%d,bind=127.0.0.1,fork,reuseaddr", port),
		fmt.Sprintf("UNIX-CONNECT:%s", socketPath),
	)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	// Setsid detaches the Forwarder from the caller's session and process
	// group so it outlives the caller. Never call cmd.Wait() on it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := closeOnExecInheritedFDs(); err != nil {
		return 0, err
	}

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

// closeOnExecInheritedFDs marks every open fd above stderr close-on-exec.
// Go's os/exec only manages fds it opened itself, so an fd inherited from
// this process's parent without FD_CLOEXEC survives fork+exec. The detached,
// long-running Forwarder would then hold that pipe or file open forever,
// hanging whatever waits for it to close.
func closeOnExecInheritedFDs() error {
	dir, err := os.Open("/dev/fd")
	if err != nil {
		return err
	}
	defer dir.Close()

	// Readdirnames, not ReadDir: /dev/fd entries report DT_UNKNOWN on darwin,
	// so os.ReadDir falls back to an lstatat per entry that fails against
	// this synthetic fs with "bad file descriptor".
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil || fd <= 2 {
			continue
		}
		syscall.CloseOnExec(fd)
	}
	return nil
}

// EnsureForwarderReady makes sure a Forwarder is listening on 127.0.0.1:port,
// spawning one and polling until ready or timeout elapses. When probe already
// reports ready it never spawns, which keeps a re-apply run from starting a
// second Forwarder. An exhausted timeout is not an error: the caller turns
// "not ready" into a warning and no bindings.
func EnsureForwarderReady(socketPath string, port int, probe ProbeFunc, spawn SpawnFunc, timeout, pollInterval time.Duration) (ready bool, pid int, err error) {
	if probe(port) {
		return true, 0, nil
	}

	pid, err = spawn(socketPath, port)
	if err != nil {
		return false, 0, err
	}

	deadline := time.Now().Add(timeout)
	for {
		if probe(port) {
			return true, pid, nil
		}
		if time.Now().After(deadline) {
			return false, pid, nil
		}
		time.Sleep(pollInterval)
	}
}
