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

	"spindrift.dev/launcher/internal/report"
)

// statusFileName is the well-known status file name within a checkout's git
// dir, beside checkoutLockFileName. It is advisory data, not liveness truth
// — see ReadStatus. The Dashboard mirrors it; the contract fixture
// (dashboard/testdata/contract) pins it.
const statusFileName = "spindrift-daemon.status"

// statusSchema is the status file's top-level "schema". Bump it only on a
// breaking change — a rename, a removal, or a changed meaning; adding a
// field never bumps it (issue #4713). The Dashboard mirrors this value; bump
// dashboard/status.go with it; the contract fixture pins it.
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
	// SelfUpdate is the latest self-update attempt (issue #5012): nil until
	// a fetched tip's daemon build first differs from the running one.
	SelfUpdate *SelfUpdate `json:"self_update,omitempty"`
}

// SelfUpdate is the prebuild of the daemon build a newer tip would load.
type SelfUpdate struct {
	// Phase is SelfUpdateBuilding while the prebuild runs, SelfUpdateBuilt
	// once the build passed its smoke test (the pool is about to halt),
	// SelfUpdateBlocked when it failed and the pool kept its running build,
	// and, in the in-place mode, SelfUpdateWaiting while the pool holds off
	// new children for a clean moment to hand off, then SelfUpdateRestarting.
	// A stop while waiting drops the phase back to SelfUpdateBuilt.
	Phase SelfUpdatePhase `json:"phase"`
	// Revision is the tip the build was made at; a blocked Revision is not
	// retried until the tip moves past it.
	Revision string `json:"revision"`
	// Reason is the failure, set only when Phase is SelfUpdateBlocked.
	Reason string `json:"reason,omitempty"`
	// Since is RFC3339 UTC: when the Daemon entered Phase. Absent from status
	// files written before it existed.
	Since string `json:"since,omitempty"`
}

// SelfUpdatePhase is a SelfUpdate's stage.
type SelfUpdatePhase string

// SelfUpdate phases.
const (
	SelfUpdateBuilding SelfUpdatePhase = "building"
	SelfUpdateBuilt    SelfUpdatePhase = "built"
	SelfUpdateBlocked  SelfUpdatePhase = "blocked"
	// SelfUpdateWaiting: in-place mode, build passed, no slot starts a child.
	SelfUpdateWaiting SelfUpdatePhase = "waiting"
	// SelfUpdateRestarting: the handoff is committed and cannot be cancelled.
	SelfUpdateRestarting SelfUpdatePhase = "restarting"
	// SelfUpdateHandoff: event only, never a status phase — the successor
	// records that it took over from an in-place restart.
	SelfUpdateHandoff SelfUpdatePhase = "handoff"
	// SelfUpdateRolledBack: event only, like SelfUpdateHandoff — the image
	// resumed from a rollback records that the new build refused to start.
	SelfUpdateRolledBack SelfUpdatePhase = "rolled-back"
)

// Event is the self_update event recording su's verdict. It carries no Slot:
// the build belongs to the pool, not to the slot that noticed the revision.
func (su SelfUpdate) Event() Event {
	return Event{Event: report.EventSelfUpdate, Phase: string(su.Phase), Revision: su.Revision, Reason: su.Reason}
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
	// issue #3923), kept apart from Issues as the slotFlight type doc
	// explains. Only the name: a Chore's class, allow-list disposition, and promotion
	// budget live in the Box and host-side settle config, never the pool.
	Chore string `json:"chore,omitempty"`
	// Pass is the Pass label (Record.Phase) of the running child's most
	// recent box record.
	Pass string `json:"pass,omitempty"`
	// Model and ModelRole are the exact model id and optional role of the
	// running child's latest model record since its latest box record.
	Model     string `json:"model,omitempty"`
	ModelRole string `json:"model_role,omitempty"`
	// CIWait is set while the running child waits on its PR's CI, and PRURL
	// names that PR, from the child's latest ci_wait record; CIRunURL is that
	// record's CI run URL, empty until the run is known. The next box or
	// settled record clears all three.
	CIWait   bool   `json:"ci_wait,omitempty"`
	PRURL    string `json:"pr_url,omitempty"`
	CIRunURL string `json:"ci_run_url,omitempty"`
	// ImageBuildSince is RFC3339 UTC: when the running child began building
	// its Box image. Set until the child's finished record, its next box
	// record, or its settled record clears it.
	ImageBuildSince string `json:"image_build_since,omitempty"`
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
	// daemon's own self-build, including resolveOpportunistic's mid-idle-wait
	// resolve.
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
	// NextCheck is RFC3339 UTC; empty means runnable now, or waiting only on
	// a tip move (NextDue "on_tip_move"). It is floored on a shut Awake
	// window's reopening, so an asleep daemon never reports a kind runnable
	// now; in the walk-exhausted case (Status.Reason) that is only a re-check.
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
	// Dashboard mirrors it; the contract fixture pins it.
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

// Write publishes s at seq 0, for the pre-pool path (Emitter.PublishHalted)
// and tests. The drop rule is strictly-older, so seq 0 writes until the
// pool's first sequenced Publish.
func (w *StatusWriter) Write(s Status) error {
	return w.Publish(0, s)
}

// Publish writes s unless seq is older than the last seq written, in which
// case it returns nil and leaves the file untouched. The caller (pool.mutate,
// via pool.publish) must allocate seq in the critical section that took the
// snapshot, or two snapshots could land in the opposite order to their seqs;
// w.mu only serializes this compare-and-write.
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
	// Remove the temp file on any failure: a leftover beside the status file
	// could be mistaken for it.
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
// publisher from a dead one's leftover file. The lock is the liveness truth;
// the status file can only corroborate it. The lock probe runs first, so on a
// status parse error the report still carries LockHeld/Holder.
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

	// holderPid > 0: an unparseable holder line and a "{}" status file both
	// zero their pid, and 0 == 0 would read as live. The host comparison
	// keeps a coincidental pid match across hosts from reading a dead remote
	// daemon's file as live; an absent host= yields "", which never equals
	// the always non-empty Status.Host.
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

// probeCheckoutLock reports whether the checkout lock at path is held. It
// never creates the file (O_CREATE would conjure one into a checkout no
// daemon ran in), and takes LOCK_SH, not LOCK_EX: that still conflicts with
// the holder's LOCK_EX, but two probes never refuse each other.
// AcquireCheckoutLock's retry tolerates this probe's momentary LOCK_SH.
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
		// Mirrors flock.TryExclusive's errno discrimination: only
		// EWOULDBLOCK means "held"; anything else is a real error, not a
		// phantom holder.
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return false, "", 0, "", fmt.Errorf("probe checkout lock file %s: %w", path, err)
		}
		holder := readHolderIdentity(file)
		return true, holder, parseHolderPid(holder), holderField(holder, "host="), nil
	}
	// We got the lock, so no daemon holds it; release at once.
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return false, "", 0, "", nil
}

// holderField extracts the value following prefix (e.g. "pid=" or "host=")
// from a "pid=%d host=%s kind=%s started=%s..." identity line, up to the
// next space. Absent input yields "", on which callers fail closed.
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

// parseHolderPid extracts the pid from an identity line, 0 when none parses.
// 0 is also a "{}" file's Status.Pid, so ReadStatus's holderPid > 0 guard,
// not this zero, rejects the unreadable case.
func parseHolderPid(identity string) int {
	pid, err := strconv.Atoi(holderField(identity, "pid="))
	if err != nil {
		return 0
	}
	return pid
}

// handingOff reports whether the in-place self-update has stopped the pool
// starting children: a build is waiting for, or committed to, a handoff.
func (su SelfUpdate) handingOff() bool {
	return su.Phase == SelfUpdateWaiting || su.Phase == SelfUpdateRestarting
}

// working reports whether any slot's phase is running: projectStatus's
// StateWorking predicate, derived from the same phases siblingsEngaged reads.
func (s *state) working() bool {
	for _, ss := range s.slots {
		if ss.phase == PhaseRunning {
			return true
		}
	}
	return false
}

// projectStatus builds the Status a pool publishes from its state and the
// instant now alone; it reads nothing else and writes nothing. Publish stamps
// the envelope (Schema, Pid, Host, Started, Time); the projection leaves those
// zero.
func projectStatus(st *state, cfg Config, now time.Time) Status {
	slots := make([]SlotStatus, len(st.slots))
	for i, ss := range st.slots {
		slots[i] = SlotStatus{Slot: i, Phase: ss.phase, Busy: ss.phase == PhaseRunning, Since: ss.since.UTC().Format(time.RFC3339)}
		if ss.phase != PhaseRunning {
			continue
		}
		slots[i].Kind = ss.flight.kind
		slots[i].Revision = ss.flight.revision
		slots[i].ChildStart = ss.flight.childStart.time
		slots[i].ChildStartN = ss.flight.childStart.n
		_, slots[i].Chore = ss.flight.key.Fields()
		slots[i].Pass = ss.flight.pass
		slots[i].Model = ss.flight.model
		slots[i].ModelRole = ss.flight.modelRole
		slots[i].CIWait = ss.flight.ciWait.pr != ""
		slots[i].PRURL = ss.flight.ciWait.pr
		slots[i].CIRunURL = ss.flight.ciWait.runURL
		if !ss.flight.imageBuildSince.IsZero() {
			slots[i].ImageBuildSince = ss.flight.imageBuildSince.UTC().Format(time.RFC3339)
		}
		if len(ss.flight.issues) > 0 {
			// A snapshot handed to a writer must not alias state this slot
			// keeps appending to.
			issues := make([]string, len(ss.flight.issues))
			copy(issues, ss.flight.issues)
			slots[i].Issues = issues
		}
	}

	// A shut Awake window gates every kind whatever its own backoff says:
	// no slot starts a child until awaitWindow returns. Until is 0 both
	// while the window is open and for the nil always-awake window, so
	// this stays the zero Time in the ordinary case.
	var windowOpensAt time.Time
	var windowWalkExhausted bool
	if d, exhausted := cfg.Awake.Until(now); d > 0 {
		windowOpensAt = now.Add(d)
		windowWalkExhausted = exhausted
	}
	checks := make([]KindCheck, 0, len(cfg.Kinds))
	var trackers []TrackerCheck
	seenTracker := make(map[string]bool)
	allGated := true
	anyJammed := false
	for _, k := range cfg.Kinds {
		v := st.sched.View(k, now)
		at := v.Until // zero unless Gated, never a stale deadline
		kc := KindCheck{Kind: k}
		if v.Probed && !seenTracker[v.Tracker] {
			seenTracker[v.Tracker] = true
			tc := TrackerCheck{Tracker: v.Tracker}
			if !v.RateLimitedUntil.IsZero() {
				tc.RateLimitedUntil = v.RateLimitedUntil.UTC().Format(time.RFC3339)
			}
			trackers = append(trackers, tc)
		}
		if v.Gated {
			// A probed kind's Jammed flag lingers past its gate; only a live
			// jam gate (JamUntil) counts, not a fresh-empty Demand's wait.
			if v.Jammed && (!v.Probed || !v.JamUntil.IsZero()) {
				anyJammed = true
				kc.Jammed = true
			}
		} else {
			allGated = false
		}
		if v.Probed && !v.ProbedAt.IsZero() {
			// A Continue exit zeroes ProbedAt until the re-probe lands; there
			// is no count to report in that gap.
			ready := v.Ready
			kc.Ready = &ready
			kc.ProbedAt = v.ProbedAt.UTC().Format(time.RFC3339)
			next := v.NextProbe
			if v.RateLimitedUntil.After(next) {
				next = v.RateLimitedUntil
			}
			kc.NextProbe = next.UTC().Format(time.RFC3339)
		}
		if !v.NextDue.IsZero() {
			if v.NextDue.At.IsZero() {
				kc.NextDue = report.NextDueOnTipMove
			} else {
				kc.NextDue = v.NextDue.At.UTC().Format(time.RFC3339)
				kc.NextDueOnTipMove = v.NextDue.OnTipMove
			}
		}
		if !v.JamUntil.IsZero() {
			kc.JamUntil = v.JamUntil.UTC().Format(time.RFC3339)
			if v.Probed && !v.JamBaselinePending {
				readyAtJam := v.ReadyAtJam
				kc.ReadyAtJam = &readyAtJam
			}
		}
		if windowOpensAt.After(at) {
			at = windowOpensAt
		}
		if !at.IsZero() {
			kc.NextCheck = at.UTC().Format(time.RFC3339)
		}
		checks = append(checks, kc)
	}

	// Precedence, most urgent first: halted; working, since a child started
	// before the window closed is genuinely still running; asleep; then
	// jammed vs waiting once every kind is gated (only jammedNow tells them
	// apart); checking otherwise.
	//
	// Asleep keys off windowOpensAt, not st.awakeShut: that flag is
	// edge-triggered by the slot that observes the close, so a pool built
	// outside its window and snapshotted before any slot parks would read it
	// false beside a nextCheck already naming the reopening.
	computedState := StateChecking
	reason := ""
	switch {
	case st.halted:
		computedState = StateHalted
		reason = st.reason.String()
	case st.working():
		computedState = StateWorking
	case !windowOpensAt.IsZero():
		computedState = StateAsleep
	case allGated && anyJammed:
		computedState = StateJammed
	case allGated:
		computedState = StateWaiting
	}
	// Outside the switch so a still-working pool marks its floored
	// nextCheck as a re-check too, not only an asleep one.
	if windowWalkExhausted && computedState != StateHalted {
		reason = walkExhaustedReason
	}

	// Copied: a returned Status must never alias cfg.Kinds.
	kinds := make([]Kind, len(cfg.Kinds))
	copy(kinds, cfg.Kinds)

	var selfUpdate *SelfUpdate
	if st.self != (SelfUpdate{}) {
		su := st.self
		su.Since = st.selfSince.UTC().Format(time.RFC3339)
		selfUpdate = &su
	}

	return Status{
		Kinds:    kinds,
		State:    computedState,
		Reason:   reason,
		Slots:    slots,
		Checks:   checks,
		Trackers: trackers,
		RepoURL:  cfg.RepoURL,

		SelfUpdate: selfUpdate,
	}
}
