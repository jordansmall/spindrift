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

// ProbeFunc reports whether something is already listening on
// 127.0.0.1:port. Injected so EnsureForwarderReady's tests never touch a
// real socket.
type ProbeFunc func(port int) bool

// SpawnFunc starts the Forwarder (socat) detached, bridging socketPath to
// 127.0.0.1:port. Injected so EnsureForwarderReady's tests never launch a
// real process.
type SpawnFunc func(socketPath string, port int) error

// DialProbe is the production ProbeFunc: a short-timeout TCP dial against
// 127.0.0.1:port. Any dial error (refused, timeout, ...) means nothing is
// listening yet. Exported so callers outside this package (driver-exec's
// bind-registry verb) can inject it into EnsureForwarderReady for real use.
func DialProbe(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// SpawnSocat is the production SpawnFunc: it starts socat detached, bridging
// a UNIX socket to a TCP listener on 127.0.0.1:port. The exec.LookPath error
// is returned verbatim (not wrapped) so callers can distinguish "socat is
// missing" from "socat started but never became ready". Exported for the
// same reason as DialProbe. Side effect: before starting socat it marks
// every fd the calling process itself has open above stderr close-on-exec
// (see closeOnExecInheritedFDs) -- process-wide and outliving this call.
func SpawnSocat(socketPath string, port int) error {
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

	if err := closeOnExecInheritedFDs(); err != nil {
		return err
	}

	return cmd.Start()
}

// closeOnExecInheritedFDs marks every open fd above stderr (2) in this
// process as close-on-exec. Go's os/exec only close-on-exec-manages fds it
// opened itself (e.g. devNull above); an fd this process merely inherited
// from its own parent without FD_CLOEXEC (a bash script's own inherited
// pipe or file, say) survives a plain fork+exec into any child -- and since
// the Forwarder is detached and long-running, it would then hold that
// pipe/file open indefinitely, hanging whatever is waiting on it to close.
func closeOnExecInheritedFDs() error {
	dir, err := os.Open("/dev/fd")
	if err != nil {
		return err
	}
	defer dir.Close()

	// Readdirnames, not ReadDir: /dev/fd entries report DT_UNKNOWN on
	// darwin, which sends os.ReadDir down an lstatat-per-entry fallback
	// that fails against this synthetic fs ("bad file descriptor").
	// Names are all this loop needs, and that path never stats.
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
