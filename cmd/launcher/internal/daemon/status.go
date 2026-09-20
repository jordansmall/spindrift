package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// statusFileName is the well-known status file name within a checkout's git
// dir, beside checkoutLockFileName. It is advisory data, not liveness truth
// — see ReadStatus.
const statusFileName = "spindrift-daemon.status"

// Status is the daemon's published state, written by StatusWriter and read
// by ReadStatus (issue #3545). It is a point-in-time snapshot: a reader gets
// whatever was last published, not a live stream.
type Status struct {
	Pid     int    `json:"pid"`
	Host    string `json:"host"`
	Started string `json:"started"` // RFC3339 UTC, daemon start
	Time    string `json:"time"`    // RFC3339 UTC, this write
	Kinds   []Kind `json:"kinds"`   // the configured kind set
	State   State  `json:"state"`
	// Reason is the halt reason, populated only when State is StateHalted.
	Reason string       `json:"reason,omitempty"`
	Slots  []SlotStatus `json:"slots"`
	Checks []KindCheck  `json:"checks"`
}

// SlotStatus is one pool slot's occupancy at the moment of publish.
type SlotStatus struct {
	Slot     int      `json:"slot"`
	Busy     bool     `json:"busy"`
	Kind     Kind     `json:"kind,omitempty"`
	Revision string   `json:"revision,omitempty"`
	Issues   []string `json:"issues,omitempty"`
}

// KindCheck is one configured kind's next-poll status.
type KindCheck struct {
	Kind Kind `json:"kind"`
	// NextCheck is RFC3339 UTC; empty means the kind is runnable now. It
	// is the later of the kind's own backoff deadline and the instant a
	// shut Awake window reopens, so an asleep daemon never reports a kind
	// as runnable now.
	NextCheck string `json:"nextCheck,omitempty"`
}

// State is the daemon's published operator-facing state; the pool computes
// which one applies (issue #3545).
type State string

const (
	// StateWorking means at least one slot has a child running.
	StateWorking State = "working"
	// StateWaiting means nothing is running, every configured kind is
	// gated, and none of them is gated by a none-dispatchable result:
	// every queue is empty and the daemon is waiting for work. This is
	// the ordinary overnight-quiet state.
	StateWaiting State = "waiting"
	// StateJammed means nothing is running, every configured kind is
	// gated, and at least one of them is gated by a none-dispatchable
	// result: there are open issues and nothing can dispatch them. Looks
	// identical to StateWaiting from outside and means the opposite —
	// only the daemon has the pool occupancy that tells them apart, so
	// this distinction cannot be recovered downstream if the daemon does
	// not report it (issue #3545).
	StateJammed State = "jammed"
	// StateAsleep means nothing is running and the Awake window is shut.
	StateAsleep State = "asleep"
	// StateChecking means nothing is running but at least one kind is
	// runnable: a slot is between iterations, about to check the queue.
	StateChecking State = "checking"
	// StateHalted means the pool has halted; Status.Reason says why.
	StateHalted State = "halted"
)

// StatusWriter publishes a Status to filepath.Join(dir, statusFileName).
type StatusWriter struct {
	dir     string
	pid     int
	host    string
	started string
	now     func() time.Time

	mu sync.Mutex
}

// NewStatusWriter builds a StatusWriter for dir, capturing the process
// identity and started instant once at construction — every later Write
// stamps the same Pid/Host/Started, only Time moves. now is injected so
// tests get a deterministic timestamp, matching NewEmitter's seam.
func NewStatusWriter(dir string, now func() time.Time) *StatusWriter {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return &StatusWriter{
		dir:     dir,
		pid:     os.Getpid(),
		host:    host,
		started: now().UTC().Format(time.RFC3339),
		now:     now,
	}
}

// Write publishes the constant s via Publish, for a caller that already has
// a Status in hand rather than one sampled fresh under the lock — the
// pre-pool halt paths (invalidConfig, and the daemon's preflight refusal,
// both before any pool exists to snapshot) and tests. See Publish for the
// write mechanics.
func (w *StatusWriter) Write(s Status) error {
	return w.Publish(func() Status { return s })
}

// Publish samples snap() under w.mu and publishes the result, so sampling
// and writing are one atomic unit: two concurrent publishers can otherwise
// each sample a consistent Status but interleave their writes, leaving the
// file holding the older sample — a slot reported busy on an idle daemon,
// which nothing then corrects, since every publish is driven by a state
// change rather than a timer. Write is the constant-snap special case.
func (w *StatusWriter) Publish(snap func() Status) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	s := snap()
	s.Pid = w.pid
	s.Host = w.host
	s.Started = w.started
	s.Time = w.now().UTC().Format(time.RFC3339)

	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal daemon status: %w", err)
	}
	data = append(data, '\n')

	finalPath := filepath.Join(w.dir, statusFileName)
	tmp, err := os.CreateTemp(w.dir, statusFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp status file in %s: %w", w.dir, err)
	}
	tmpPath := tmp.Name()
	// Any failure past this point must not leave tmpPath behind — a
	// leftover temp file next to the real status file would be at best
	// litter and at worst mistaken for the published one by hand.
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp status file %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp status file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp status file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpPath, finalPath, err)
	}
	cleanup = false
	return nil
}

// StatusReport is what a reader gets back from ReadStatus: the status file's
// contents plus the lock probe that tells a stale file from a live one.
type StatusReport struct {
	LockHeld bool `json:"lockHeld"`
	// Holder is the lock's identity line, populated only when LockHeld.
	Holder string  `json:"holder,omitempty"`
	Live   bool    `json:"live"`
	Stale  bool    `json:"stale"`
	Status *Status `json:"status,omitempty"`
}

// ReadStatus reads dir's status file and probes dir's lock to tell a live
// publisher from a dead one's leftover file. The lock is the liveness
// truth and the status file is advisory data: a status file can only ever
// corroborate what the lock already says, never override it. The lock
// probe therefore runs first: on error the returned report still carries
// whatever the lock probe established (LockHeld/Holder), even though the
// error itself came from the status file's own parse — a caller must be
// able to report liveness even when the advisory data is garbage.
func ReadStatus(dir string) (StatusReport, error) {
	var report StatusReport

	held, holder, holderPid, holderHost, err := probeCheckoutLock(filepath.Join(dir, checkoutLockFileName))
	if err != nil {
		return report, err
	}
	report.LockHeld = held
	report.Holder = holder

	status, err := readStatusFile(filepath.Join(dir, statusFileName))
	if err != nil {
		return report, err
	}
	report.Status = status
	report.Stale = status != nil

	// holderPid > 0 closes the {}-shaped-file gap: an unparseable holder
	// line and an empty status file both zero-value their pid, and 0 == 0
	// would otherwise read as live. The host comparison closes the
	// analogous shared-checkout gap — a coincidental pid match across two
	// hosts must not read a dead remote daemon's file as live — and is
	// safe unconditionally: an unparseable/absent host= yields "" via
	// holderField, which can never equal a published Status.Host (always
	// non-empty, see NewStatusWriter).
	if held && status != nil && holderPid > 0 && status.Pid == holderPid && status.Host == holderHost {
		report.Live = true
		report.Stale = false
	}

	return report, nil
}

// readStatusFile reads and parses dir's status file. A missing file is not
// an error — nil, nil — but a present, unparseable one is: it means
// something wrote garbage beside the lock, which a caller must surface
// rather than silently treat as "no status".
func readStatusFile(path string) (*Status, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read status file %s: %w", path, err)
	}

	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse status file %s: %w", path, err)
	}
	return &s, nil
}

// probeCheckoutLock reports whether the checkout lock at path is held,
// without ever creating a lock file (O_CREATE would conjure one into a
// checkout no daemon ever ran in) and without excluding a concurrent
// reader (LOCK_SH, not LOCK_EX: it still conflicts with the holder's
// LOCK_EX, so success proves no daemon holds it, but two probes never
// refuse each other). This probe's own momentary LOCK_SH window is in
// turn tolerated by AcquireCheckoutLock's retry — see its doc.
func probeCheckoutLock(path string) (held bool, holder string, holderPid int, holderHost string, err error) {
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", 0, "", nil
		}
		return false, "", 0, "", fmt.Errorf("open checkout lock file %s: %w", path, err)
	}
	defer file.Close()

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		// Mirrors AcquireCheckoutLock's own errno discrimination: only
		// EWOULDBLOCK means "held"; anything else is a real error, not a
		// phantom holder.
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return false, "", 0, "", fmt.Errorf("probe checkout lock file %s: %w", path, err)
		}
		holder := readHolderIdentity(file)
		return true, holder, parseHolderPid(holder), holderField(holder, "host="), nil
	}
	// We took the lock ourselves, proving no daemon holds it — release at
	// once, this was only ever a probe.
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return false, "", 0, "", nil
}

// holderField extracts the value following prefix (e.g. "pid=" or "host=")
// from an identity line shaped like "pid=%d host=%s kind=%s started=%s...",
// stopping at the next space. Absent or malformed input yields "" rather
// than panicking or guessing — parseHolderPid and probeCheckoutLock's host
// extraction both fail closed on that empty value.
func holderField(identity, prefix string) string {
	idx := strings.Index(identity, prefix)
	if idx < 0 {
		return ""
	}
	rest := identity[idx+len(prefix):]
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		rest = rest[:sp]
	}
	return rest
}

// parseHolderPid extracts the pid from an identity line shaped like
// "pid=%d host=... kind=... started=...". A line with no parseable pid=
// yields 0, meaning only "no readable pid" — it is ReadStatus's
// holderPid > 0 guard, not this zero value itself, that rejects the
// unreadable case, since 0 is also a legitimate zero-valued Status.Pid on
// an untrusted, unmarshalled file (e.g. a bare "{}").
func parseHolderPid(identity string) int {
	pid, err := strconv.Atoi(holderField(identity, "pid="))
	if err != nil {
		return 0
	}
	return pid
}
