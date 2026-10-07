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
// — see ReadStatus. The Dashboard mirrors it
// (nix/checks/dashboard-constant-parity.nix).
const statusFileName = "spindrift-daemon.status"

// statusSchema is the status file's top-level "schema". Bump it only on a
// breaking change — a rename, a removal, or a changed meaning; adding a
// field never bumps it (issue #4713). The Dashboard mirrors this value; bump
// dashboard/status.go with it (nix/checks/dashboard-constant-parity.nix).
const statusSchema = 1

// Status is the daemon's published state, written by StatusWriter and read
// by ReadStatus (issue #3545). It is a point-in-time snapshot: a reader gets
// whatever was last published, not a live stream.
type Status struct {
	Schema  int    `json:"schema"`
	Pid     int    `json:"pid"`
	Host    string `json:"host"`
	Started string `json:"started"` // RFC3339 UTC, daemon start
	Time    string `json:"time"`    // RFC3339 UTC, this write
	Kinds   []Kind `json:"kinds"`   // the configured kind set
	State   State  `json:"state"`
	// Reason is the halt reason when State is StateHalted, even if Until's
	// walk also exhausted its horizon. Under any other state it is set only
	// when the walk exhausted, marking NextCheck as a re-check rather than
	// a real reopening — in practice StateAsleep or StateWorking, since an
	// exhausted walk always yields a positive wait. Empty otherwise.
	Reason string       `json:"reason,omitempty"`
	Slots  []SlotStatus `json:"slots"`
	Checks []KindCheck  `json:"checks"`
	// Trackers has one entry per distinct tracker the probed kinds count
	// against, in configured kind order (ADR 0059).
	Trackers []TrackerCheck `json:"trackers,omitempty"`
	// RepoURL is the Target repo's web URL, so a reader can link an issue
	// number without the checkout's config; empty when the tracker has no
	// web repo.
	RepoURL string `json:"repo_url,omitempty"`
}

// TrackerCheck is one tracker's rate-limit standing.
type TrackerCheck struct {
	Tracker string `json:"tracker"`
	// RateLimitedUntil is RFC3339 UTC, set only while a pause holds.
	RateLimitedUntil string `json:"rate_limited_until,omitempty"`
}

// SlotStatus is one pool slot's occupancy at the moment of publish.
type SlotStatus struct {
	Slot int `json:"slot"`
	// Phase is the slot's own position in its iteration (issue #3623).
	// No omitempty: idle is a real, reportable value, and eliding it would
	// make a slot that has never run look identical to a missing field.
	Phase Phase `json:"phase"`
	Busy  bool  `json:"busy"`
	// Since is RFC3339 UTC: when the slot entered its current phase. No
	// omitempty, for the same reason as Phase: every slot always has one.
	Since    string   `json:"since"`
	Kind     Kind     `json:"kind,omitempty"`
	Revision string   `json:"revision,omitempty"`
	Issues   []string `json:"issues,omitempty"`
	// Chore names the Chore a butler slot's child last boxed (ADR 0056,
	// issue #3923), kept apart from Issues as slotFlight.chore explains.
	// Only the name: a Chore's class, allow-list disposition, and promotion
	// budget live in the Box and host-side settle config, never the pool.
	Chore string `json:"chore,omitempty"`
	// Pass is the Pass label (Record.Phase) of the running child's most
	// recent box record.
	Pass string `json:"pass,omitempty"`
	// Model and ModelRole are the exact model id and optional role of the
	// running child's latest model record since its latest box record.
	Model     string `json:"model,omitempty"`
	ModelRole string `json:"model_role,omitempty"`
	// ChildStart and ChildStartN pin the running child's Dispatch as the
	// dashboard names it: the time string of its child_start event, and how
	// many earlier child_starts on this slot share that exact string.
	ChildStart  string `json:"child_start,omitempty"`
	ChildStartN int    `json:"child_start_n,omitempty"`
}

// Phase is one slot's own position in its iteration, published per slot
// alongside busy (issue #3623). busy is phase == PhaseRunning and nothing
// more, so a reader that only knows busy is unaffected by this field.
type Phase string

const (
	// PhaseIdle means the slot holds nothing: parked between iterations,
	// or returned for good, by whatever route it left runSlot.
	PhaseIdle Phase = "idle"
	// PhaseAwaitingWindow means the slot is parked because the Awake
	// window is shut.
	PhaseAwaitingWindow Phase = "awaiting_window"
	// PhaseResolving means the slot is fetching the tip or evaluating the
	// daemon's own self-build, ahead of a child of its own; this covers
	// resolveOpportunistic's own mid-idle-wait resolve too, not just the
	// per-iteration one — both are outside-world evaluations at the fetched
	// tip.
	PhaseResolving Phase = "resolving"
	// PhaseRunning means the slot has a child in flight; this is the one
	// phase Busy reports.
	PhaseRunning Phase = "running"
	// PhaseBackingOff means the slot is sleeping out a failure backoff.
	PhaseBackingOff Phase = "backing_off"
)

// KindCheck is one configured kind's next-poll status.
type KindCheck struct {
	Kind Kind `json:"kind"`
	// NextCheck is RFC3339 UTC; empty means the kind is runnable now, or
	// that it waits only on a tip move (then NextDue is "on_tip_move"). It
	// is the later of the kind's own backoff deadline and the instant a
	// shut Awake window reopens, so an asleep daemon never reports a kind
	// as runnable now. In the walk-exhausted degraded case (see
	// Status.Reason) that instant is only a re-check, not a real reopening.
	NextCheck string `json:"nextCheck,omitempty"`
	// Jammed is true when this kind's last check found open issues none of
	// which were dispatchable (exit 3), and it is still gated on that
	// result. Without this, which kind is jammed is unrecoverable once
	// State collapses several kinds into one word (issue #3545).
	Jammed bool `json:"jammed,omitempty"`

	// The fields below describe the kind's Demand probe (issue #4573, ADR
	// 0059) and are set only for a probed kind that has been probed and has not
	// since had a child exit 0 or 4; an exit-driven kind never carries them.
	// Ready is a *int so a real 0 survives omitempty.
	Ready *int `json:"ready,omitempty"`
	// ProbedAt and NextProbe are RFC3339 UTC.
	ProbedAt  string `json:"probed_at,omitempty"`
	NextProbe string `json:"next_probe,omitempty"`
	// JamUntil is set only while a jam gate is live; ReadyAtJam is the
	// Ready count the jam froze, so a reader sees how much work the jam is
	// holding back. A jam recorded while a claim or an empty child's exit had
	// moved the count unconfirmed takes the next probe's count instead, and
	// ReadyAtJam is absent until that probe lands.
	JamUntil   string `json:"jam_until,omitempty"`
	ReadyAtJam *int   `json:"ready_at_jam,omitempty"`

	// NextDue is a child-reported kind's (the butler's) answer, set only
	// while its last child's report stands: an RFC3339 UTC instant, or
	// report.NextDueOnTipMove when only a moved tip lifts it.
	NextDue string `json:"next_due,omitempty"`
	// NextDueOnTipMove is set only when NextDue is an instant and a moved
	// tip would also lift the wait early.
	NextDueOnTipMove bool `json:"next_due_on_tip_move,omitempty"`
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
	// StateJammed is derived from the per-kind Jammed flags (KindCheck):
	// nothing is running, every configured kind is gated, and at least one
	// KindCheck carries Jammed true. Looks identical to StateWaiting from
	// outside and means the opposite; see docs/reference.md for the full
	// rationale.
	StateJammed State = "jammed"
	// StateAsleep means nothing is running and the Awake window is shut.
	StateAsleep State = "asleep"
	// StateChecking means nothing is running but at least one kind is
	// runnable: a slot is between iterations, about to check the queue.
	StateChecking State = "checking"
	// StateHalted means the pool has halted; Status.Reason says why. The
	// Dashboard mirrors it (nix/checks/dashboard-constant-parity.nix).
	StateHalted State = "halted"
)

// StatusWriter publishes a Status to filepath.Join(dir, statusFileName).
type StatusWriter struct {
	dir     string
	pid     int
	host    string
	started string
	now     func() time.Time

	mu      sync.Mutex
	lastSeq uint64
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

// Write publishes s at seq 0, the unsequenced pre-pool path: it is called
// only before any pool exists to allocate a real seq (invalidConfig, and
// the daemon's preflight refusal) and by tests. lastSeq starts at 0 and the
// drop rule is strictly-older, so seq 0 always writes until the pool's
// first sequenced Publish call takes over.
func (w *StatusWriter) Write(s Status) error {
	return w.Publish(0, s)
}

// Publish writes s if seq is not older than the last seq this writer has
// written, and drops it (returning nil, untouched file) otherwise. seq is
// the caller's ordering token, not sampled here: the caller (pool.publish)
// must allocate seq in the same critical section it took the snapshot in,
// or two callers could allocate seq 1/2 but snapshot in the opposite order,
// and Publish would have no way to tell the resulting write from a correct
// one. w.mu here only serializes the read-compare-write of lastSeq and the
// file write itself, matching whatever seq order the caller already fixed.
func (w *StatusWriter) Publish(seq uint64, s Status) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if seq < w.lastSeq {
		return nil
	}
	w.lastSeq = seq

	s.Schema = statusSchema
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
	// HolderPidGone is set when the lock holder's line is on this host, its
	// pid no longer exists, and the status file matches that line. That is
	// ordinarily the acquire window of issue #3597 (a fresh daemon that has
	// not yet rewritten the identity line), but it is an observation, not a
	// claim that a daemon is starting.
	HolderPidGone bool `json:"holderPidGone,omitempty"`
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
	//
	// Issue #3597: between a new daemon's Flock and its identity truncate both
	// files are the dead predecessor's and agree, so a same-host holder pid is
	// probed; a remote pid cannot be, so it keeps the content comparison alone.
	correlated := held && status != nil && holderPid > 0 && status.Pid == holderPid && status.Host == holderHost
	if correlated {
		if holderIsLocal(holderHost) && pidGone(holderPid) {
			report.HolderPidGone = true
		} else {
			report.Live = true
			report.Stale = false
		}
	}

	return report, nil
}

// holderIsLocal reports whether host names the machine this process runs on.
// A hostname lookup error reads as not local, so the caller falls back to
// trusting the lock content rather than probing a pid it cannot place.
func holderIsLocal(host string) bool {
	self, err := os.Hostname()
	return err == nil && self == host
}

// pidGone reports whether the OS has no process with this pid. EPERM means
// the process exists but belongs to another user, so it is not gone.
func pidGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
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
