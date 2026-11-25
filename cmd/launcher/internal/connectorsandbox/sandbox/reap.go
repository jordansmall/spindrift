package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"spindrift.dev/launcher/internal/runner"
)

// unowned is the owner token of a sandbox whose Launcher could not read its
// identity. It is never hex, so no sweep matches it.
const unowned = "unowned"

const ownerLen = 12

// ownerID is a short digest of this process's namespaceIdentity. It is a
// variable so tests can replace it.
var ownerID = sync.OnceValues(func() (string, error) {
	id, err := namespaceIdentity()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:ownerLen], nil
})

// lastNano is the latest nanosecond stamp Name issued.
var lastNano atomic.Int64

// Name is a new sandbox's runtime name for the Launcher process pid. A process
// starts several sandboxes and a coarse clock can stamp two in one tick, so the
// stamp is bumped past the last one issued.
func Name(pid int, t time.Time) string {
	owner, err := ownerID()
	if err != nil {
		owner = unowned
	}
	ns := t.UnixNano()
	for {
		last := lastNano.Load()
		if ns <= last {
			ns = last + 1
		}
		if lastNano.CompareAndSwap(last, ns) {
			break
		}
	}
	return sandboxName(owner, pid, time.Unix(0, ns))
}

// sandboxName is a sandbox's runtime name. It embeds the owning Launcher's pid
// namespace identity and pid so a later sweep can tell a live run's sandbox
// from an orphan; parseName reads them back.
func sandboxName(owner string, pid int, t time.Time) string {
	return fmt.Sprintf("%s%s-%d-%d", runner.ConnectorSandboxPrefix, owner, pid, t.UnixNano())
}

// parseName is the owner and pid name embeds, or false for a name that is not
// a sandbox's. An empty owner means the name carries no namespace identity, so
// it must never be reaped.
func parseName(name string) (owner string, pid int, ok bool) {
	rest, ok := strings.CutPrefix(name, runner.ConnectorSandboxPrefix)
	if !ok {
		return "", 0, false
	}
	parts := strings.Split(rest, "-")
	switch len(parts) {
	case 2:
	case 3:
		if parts[0] == "" {
			return "", 0, false
		}
		owner = parts[0]
		parts = parts[1:]
	default:
		return "", 0, false
	}
	if _, err := strconv.ParseInt(parts[1], 10, 64); err != nil {
		return "", 0, false
	}
	pid, err := strconv.Atoi(parts[0])
	if err != nil || pid <= 0 {
		return "", 0, false
	}
	return owner, pid, true
}

// Reaper is the part of a runner.Runner ReapOrphans needs.
type Reaper interface {
	ListRunning() ([]string, error)
	Kill(name string) error
}

// ReapOrphans kills the Connector sandboxes whose owning Launcher is gone,
// which only a hard kill of the Launcher or the runtime CLI leaves behind.
// Other Launchers may be live and a shared runtime daemon lists every pid
// namespace's sandboxes, so it reaps only one whose owner identity matches
// this process's (see namespaceIdentity; none off Linux and macOS) and whose
// pid is dead. A sandbox with no owner identity is skipped with a note; an
// unreadable identity reaps nothing; a reused pid can only leave an orphan
// unreaped. Failures are noted on w, never returned.
func ReapOrphans(r Reaper, w io.Writer) {
	me, err := ownerID()
	if err != nil {
		fmt.Fprintf(w, "warning: reading this Launcher's owner identity, reaping no Connector sandboxes: %v\n", err)
		return
	}
	names, err := r.ListRunning()
	if err != nil {
		fmt.Fprintf(w, "warning: listing Connector sandboxes to reap: %v\n", err)
		return
	}
	for _, n := range names {
		owner, pid, ok := parseName(n)
		if ok && (owner == "" || owner == unowned) {
			fmt.Fprintf(w, "note: skipped Connector sandbox with no owner identity (remove it by hand if orphaned): %s\n", n)
			continue
		}
		if !ok || owner != me || pidAlive(pid) {
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
// exists but is not ours. It is a variable so tests can replace it.
var pidAlive = func(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
