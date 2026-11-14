package sandbox

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"syscall"
	"time"

	"spindrift.dev/launcher/internal/runner"
)

// sandboxName is a sandbox's runtime name. It embeds the owning Launcher's pid
// so a later sweep can tell a live run's sandbox from an orphan; ownerPID reads
// it back.
func sandboxName(pid int, t time.Time) string {
	return fmt.Sprintf("%s%d-%d", runner.ConnectorSandboxPrefix, pid, t.UnixNano())
}

// ownerPID is the pid name embeds, or false for a name that is not a sandbox's.
func ownerPID(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, runner.ConnectorSandboxPrefix)
	if !ok {
		return 0, false
	}
	pidStr, nano, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	if _, err := strconv.ParseInt(nano, 10, 64); err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// Reaper is the part of a runner.Runner ReapOrphans needs.
type Reaper interface {
	ListRunning() ([]string, error)
	Kill(name string) error
}

// ReapOrphans kills the Connector sandboxes whose owning Launcher is gone,
// which only a hard kill of the Launcher or the runtime CLI leaves behind.
// Other Launcher runs may be live at once, so a sandbox whose owner still runs
// is left alone. Liveness is judged in this process's pid namespace, so a
// sandbox owned by a Launcher in another pid namespace sharing the same runtime
// looks orphaned. A reused pid can only leave an orphan unreaped. It is
// best-effort: failures are noted on w, never returned.
func ReapOrphans(r Reaper, w io.Writer) {
	names, err := r.ListRunning()
	if err != nil {
		fmt.Fprintf(w, "warning: listing Connector sandboxes to reap: %v\n", err)
		return
	}
	for _, n := range names {
		pid, ok := ownerPID(n)
		if !ok || pidAlive(pid) {
			continue
		}
		if err := r.Kill(n); err != nil {
			fmt.Fprintf(w, "warning: reaping orphaned Connector sandbox %s: %v\n", n, err)
			continue
		}
		fmt.Fprintf(w, "==> reaped orphaned Connector sandbox: %s\n", n)
	}
}

// pidAlive treats any error but ESRCH as alive; EPERM means the process
// exists but is not ours.
func pidAlive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
