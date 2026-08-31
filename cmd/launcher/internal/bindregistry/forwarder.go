package bindregistry

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ProbeFunc reports whether something is already listening on
// 127.0.0.1:port. Injected so EnsureForwarderReady's tests never touch a
// real socket.
type ProbeFunc func(port int) bool

// SpawnFunc starts the Forwarder (socat) detached, bridging socketPath to
// 127.0.0.1:port. Injected so EnsureForwarderReady's tests never launch a
// real process.
type SpawnFunc func(socketPath string, port int) error

// dialProbe is the production ProbeFunc: a short-timeout TCP dial against
// 127.0.0.1:port. Any dial error (refused, timeout, ...) means nothing is
// listening yet.
func dialProbe(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// spawnSocat is the production SpawnFunc: it starts socat detached, bridging
// a UNIX socket to a TCP listener on 127.0.0.1:port. The exec.LookPath error
// is returned verbatim (not wrapped) so callers can distinguish "socat is
// missing" from "socat started but never became ready".
func spawnSocat(socketPath string, port int) error {
	path, err := exec.LookPath("socat")
	if err != nil {
		return err
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(path,
		fmt.Sprintf("TCP-LISTEN:%d,bind=127.0.0.1,fork,reuseaddr", port),
		fmt.Sprintf("UNIX-CONNECT:%s", socketPath),
	)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	// Setsid detaches the Forwarder from the caller's session/process
	// group so it outlives the caller process; cmd.Wait() is deliberately
	// never called -- this must stay a detached, long-running process.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	return cmd.Start()
}

// EnsureForwarderReady makes sure a Forwarder is listening on
// 127.0.0.1:port, spawning one via spawn if probe doesn't already find it
// listening, then polling probe until ready or timeout elapses.
//
// If probe already reports ready, spawn is never called -- this is the
// double-spawn-prevention path a re-apply run depends on. If spawn returns
// an error, EnsureForwarderReady returns immediately without polling: nothing
// was started, so there's nothing to wait for. A timeout that exhausts
// without probe ever reporting ready is not itself a Go error -- it's the
// caller's job to turn "not ready" into a warning and no bindings.
func EnsureForwarderReady(socketPath string, port int, probe ProbeFunc, spawn SpawnFunc, timeout, pollInterval time.Duration) (ready bool, err error) {
	if probe(port) {
		return true, nil
	}

	if err := spawn(socketPath, port); err != nil {
		return false, err
	}

	deadline := time.Now().Add(timeout)
	for {
		if probe(port) {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(pollInterval)
	}
}
