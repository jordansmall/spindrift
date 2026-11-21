package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/report"
)

// pool is the shared state behind Loop's slot goroutines. First halt wins —
// Loop emits exactly one "halt" event per call — and pool cancels the slots'
// shared context the moment it halts, so a sibling asleep in its idle wait or
// blocked in ResolveTip stops promptly.
type pool struct {
	cfg Config
	r   Runner
	em  *Emitter
	clk Clock

	cancel context.CancelFunc

	// abandon is closed when the in-place handoff commits: every slot's wait
	// on its child ends, and the children keep running (see commitHandoffIfClean).
	abandon chan struct{}

	// baton is the discovery token (issue #3684): at most one slot may be
	// between "started" and "announced a Box", or two slots discovering
	// against one tracker snapshot could select the same issue. st.batonSlot
	// is the holder (noBaton when free). nil in a single-slot pool, which has
	// no sibling to stagger against.
	baton chan struct{}

	mu  sync.Mutex
	st  state
	seq uint64 // publish sequence counter; see mutate

	// bg tracks the background self-update prebuild, which Loop waits out so
	// no event follows its return.
	bg sync.WaitGroup

	// rev is the late-merge sweeper's revision source; see revisionFeed.
	rev *revisionFeed

	// demandFlights holds each kind's in-flight Demand probe, so slots that
	// all find the same kind stale share one Runner.Demand call. Guarded by
	// p.mu but kept off state: it is coordination, not published pool state.
	demandFlights map[Kind]*demandFlight

	// onDemandJoin, when set by a test, fires each time a slot joins another
	// slot's Demand flight, so coalescing can be pinned without timing.
	onDemandJoin func()
}

// state is all of the pool's mutable state, in one value: every field here
// is read and written only under p.mu, and only ever changed by mutate.
type state struct {
	halted    bool
	reason    Halt
	slots     []slotState
	awakeShut bool // true once awake_close has fired, until the matching awake_open
	b         breaker
	sched     Schedule
	batonSlot int

	// lastStart is each slot's latest child_start, kept on state rather than
	// slotFlight because finishChild zeroes the flight. It starts empty per
	// process, so a restart that starts a child in the same second as the
	// prior process's last child_start publishes n=0 where the dashboard,
	// counting both Events generations, numbers it 1: an accepted gap.
	lastStart []childStartPin

	// tip is the last revision a resolution observed as moved, and
	// continues counts each kind's children that ended Continue. A child's
	// not_due report is only as fresh as these were when it started: see
	// reportStale.
	tip       string
	continues map[Kind]int

	// self is the latest self-update attempt; zero until a tip's daemon
	// build first differs from the running one. See selfUpdate.
	self SelfUpdate
	// selfSince is when self.Phase last changed; mutate maintains it, so no
	// phase assignment site needs to. Kept off self so self's zero test and
	// equality comparisons ignore it; projectStatus publishes it.
	selfSince time.Time

	// wake is the current wake generation: a parked slot sleeps on a context
	// derived from it, and wakeParked replaces it when a change grows the set
	// of startable kinds. It lives on state because observe and tipMoved run
	// inside mutate, which hands them only *state. A halt wakes slots via pctx.
	wake       context.Context
	wakeCancel context.CancelFunc
}

// childStartPin is a child_start's time string and its ordinal among the
// slot's child_starts sharing that string.
type childStartPin struct {
	time string
	n    int
}

// wakeParked ends the current wake generation, waking every slot parked on
// it, and installs a fresh one for the slots that park next.
func (s *state) wakeParked() {
	s.wakeCancel()
	s.wake, s.wakeCancel = context.WithCancel(context.Background())
}

// observe folds ev into the schedule and wakes every parked slot only when
// Schedule.Observe reports a wake (the startable set grew, or a claim made a
// kind probe-due). A wake never produces a claimed event, so slots never wake
// one another in a ring.
func (s *state) observe(now time.Time, ev schedEvent) {
	var woke bool
	s.sched, woke = s.sched.Observe(now, ev)
	if woke {
		s.wakeParked()
	}
}

// tipMoved ends every jammed kind's gate and every reported kind's tip wait,
// returns the kinds lifted, and wakes parked slots on the same terms as
// observe.
func (s *state) tipMoved(now time.Time) (lifted []Kind) {
	var woke bool
	s.sched, lifted, woke = s.sched.TipMoved(now)
	if woke {
		s.wakeParked()
	}
	return lifted
}

// slotState is one slot's own state. flight is meaningful only while phase
// == PhaseRunning: finishChild zeroes it leaving that phase, and projectStatus
// reads it only for a running slot, so a flight a bare setPhase left behind is
// never published as stale occupancy.
type slotState struct {
	phase Phase
	// since is when phase last changed; mutate maintains it, so no phase
	// assignment site needs to.
	since time.Time
	// sincePinned marks a since the seater chose (an inherited child's real
	// start), which mutate keeps through the phase move and then clears.
	sincePinned bool
	flight      slotFlight
	// handed is the child's runner-level state once the handoff abandoned the
	// slot's wait; non-nil marks the slot as part of the handoff.
	handed *ChildState
}

// mutate is the pool's one path to changing state; nowhere else takes p.mu
// to write. Emitting f's events under the lock (Emitter never reaches back
// into the pool) orders them ahead of any later mutate's by construction, and
// allocating the snapshot's sequence number in the same hold lets
// StatusWriter.Publish drop a stale snapshot rather than needing publishes to
// arrive in order.
//
// f must touch nothing but the *state it is handed: a p.-receiver method
// that itself takes p.mu would deadlock here. Accepted costs: a stalled event
// writer stalls every state change, halt included, and every mutate publishes.
func (p *pool) mutate(f func(s *state) []Event) {
	p.mu.Lock()
	before := make([]slotState, len(p.st.slots))
	copy(before, p.st.slots)
	selfBefore := p.st.self.Phase
	evs := f(&p.st)
	p.commitHandoffIfClean(&p.st)
	// startChild and finishChild replace a whole slotState, zeroing since, and
	// noteAwakeOpen re-sets idle on every iteration; restoring the prior
	// since unless the phase moved keeps both from resetting it. A slot
	// seated with sincePinned keeps its since across the move that seats it.
	now := p.clk.Now()
	if p.st.self.Phase != selfBefore {
		p.st.selfSince = now
	}
	for i := range p.st.slots {
		switch {
		case p.st.slots[i].phase == before[i].phase:
			p.st.slots[i].since = before[i].since
		case !p.st.slots[i].sincePinned:
			p.st.slots[i].since = now
		}
		p.st.slots[i].sincePinned = false
	}
	for _, ev := range evs {
		p.em.Emit(ev)
	}
	p.seq++
	seq := p.seq
	// Clock is read under p.mu: no Clock takes it. One hold keeps the
	// projection from stitching state of two instants together.
	snap := projectStatus(&p.st, p.cfg, p.clk.Now())
	p.mu.Unlock()
	p.publish(seq, snap)
}

// leadSlot is the initial baton holder: a fixed slot rather than whichever
// reaches awaitBaton first, so a cold start's first discovering slot never
// depends on scheduler luck. It has no lasting powers after its first round.
const leadSlot = 0

// noBaton is batonSlot's value while the discovery baton is free (in flight
// on p.baton's channel).
const noBaton = -1

// batonReason is a baton_pass reason. A distinct type so passBaton and
// backoffOrHalt, which sit next to a free-form string reason, cannot be
// handed the two swapped.
type batonReason string

// Reasons stamped on baton_hold/baton_pass, in the documented-string
// convention of ShutdownDrain (events.go). Every way a discovery round can
// end without a claim has its own pass reason, so an operator sees why the
// baton moved.
const (
	// batonPassClaimed fires when the holder's child announces a Box while
	// still running: discovery is over, so the next waiting slot starts at
	// once rather than waiting out the whole of the holder's Box run.
	batonPassClaimed batonReason = "the holder's child announced a Box: discovery is over, passing the baton to the next waiting slot"
	// batonPassChildEnded fires when the holder's child returns without
	// announcing a Box and the pool is not halting. A HaltPool exit or a
	// breaker trip passes none, since a woken sibling would start a child
	// before the halt lands (issue #4365).
	batonPassChildEnded batonReason = "the holder's child ended without announcing a Box: passing the baton to the next waiting slot"
	// batonPassFailed fires on an unclassified failure reaching
	// backoffOrHalt before the holder started a child (a ResolveTip error or
	// a self-build evaluation failure), so siblings' discovery does not stall
	// through the holder's own backoff. The baton is acquired only after the
	// resolve (runSlot, loop.go), so only leadSlot's first round reaches this
	// holding it.
	batonPassFailed batonReason = "the holder's round failed before it could start a child: passing the baton rather than holding the pool through its backoff"
	// batonPassWindowClosed fires when the Awake window shuts between the
	// holder's ResolveTip fetch and starting its child, so the holder does not
	// keep the baton through the shut span.
	batonPassWindowClosed batonReason = "the Awake window closed before the holder could start a child: passing the baton rather than holding the pool through the shut span"
	// batonPassIdle fires when Decide finds nothing startable or probe-due for
	// the holder: a slot must never idle-sleep holding the baton. Decide runs
	// before the baton is acquired, so only leadSlot's first round reaches
	// this holding it.
	batonPassIdle batonReason = "no kind is runnable for the holder: passing the baton rather than holding the pool through its idle wait"
	// batonPassOutpaced fires when the holder re-decides at startChild and
	// finds a sibling's result took away its start (e.g. an exit 2 zeroing
	// the count). The holder decides again, so the baton must not stay with
	// it through that round's probe or idle wait.
	batonPassOutpaced batonReason = "a sibling's result left nothing startable for the holder: passing the baton rather than spawning an empty child"
	// batonPassChoreKeyed fires when the holder starts a Chore-keyed child.
	// Such a kind selects no tracker issue, so the baton has nothing to
	// guard for it; the pass happens at the start rather than at the box
	// record so a butler run never holds the baton through its run.
	batonPassChoreKeyed batonReason = "the holder started a Chore-keyed child, which discovers no tracker issue: passing the baton to the next waiting slot"
	// batonPassStopped fires when the holder returns before any of the
	// above resolved (a cancelled ctx, a halt, a self-build mismatch, or a
	// HaltPool child exit or breaker trip, passed after the halt lands), so
	// the next waiting slot is never stranded.
	batonPassStopped batonReason = "the holder stopped before its discovery round resolved: passing the baton so no sibling waits on a slot that has already exited"

	// batonHoldReason is stamped on every baton_hold event, like every other
	// wait event's reason.
	batonHoldReason = "waiting for the discovery baton: another slot's child is still discovering"
)

// outsideWindowReason and walkExhaustedReason are the two Reason strings an
// awake_close event can carry (the ordinary shut-window case, and Until's
// walk exhausting untilHorizon without finding an opening). walkExhaustedReason
// also surfaces on a non-halted Status (asleep or working) via projectStatus,
// since the same degraded walk floors every checks[].nextCheck there too.
const (
	outsideWindowReason = "outside the Awake window"
	walkExhaustedReason = "no Awake window opening found within the walk horizon: re-checking then"
)

// slotFlight is what one occupied slot has in flight. issues is the deduped
// set behind SlotStatus.Issues (issue #3627), in first-seen order. key is
// tracked separately: a repeat-issue box (a fix-pass) must still move
// flightClaim's answer, and a Chore-keyed box (ADR 0056, issue #3878) never
// enters issues, so key alone carries it as SlotStatus.Chore.
type slotFlight struct {
	kind     Kind
	revision string
	issues   []string
	key      dispatchkey.Key
	// nextDue folds every not_due record the child reported: when a
	// child-reported kind next has work. Not a claim, so it never moves key.
	nextDue NextDue
	// continues is state.continues[kind] when the child started.
	continues int
	// gateGen is the kind's Schedule.gateGenOf when the child started.
	gateGen int
	// childStart pins the child_start event; see startChild.
	childStart childStartPin
	// childLog is the child's log path, kept for the handoff.
	childLog string
	// reported is set by the child's first record other than image_build. The
	// in-place handoff waits for it, since a child that has said nothing may
	// be mid-discovery, claiming work the successor could not see.
	reported bool
	// pass is the Pass label (Record.Phase) of the child's most recent box
	// record.
	pass string
	// model and modelRole are the exact model id and optional role of the
	// child's latest model record; a box record clears them, since a new Pass
	// starts on a model of its own.
	model     string
	modelRole string
	// ciWait is the child's latest ci_wait record; a box and a settled record
	// clear it.
	ciWait ciWait
	// imageBuildSince is when the child's Box image build started, zero when
	// none is running; its finished record, a box record and a settled record
	// clear it.
	imageBuildSince time.Time
}

// ciWait is the state of a child's latest ci_wait record. A run_url-less
// record (a new wait) resets runURL.
type ciWait struct {
	// pr is the PR URL; non-empty means the child is waiting on that PR's CI.
	pr string
	// runURL is the CI run URL, empty until the host finds the run.
	runURL string
}

// newPool returns the pool and a context derived from ctx that pool.cancel
// stops independently of the caller; every slot runs against it. Cancelling
// it never kills a running child: StartChild and Wait are drain-safe under a
// cancelled ctx (loop.go), and hostRunner.StartChild uses exec.Command, not
// CommandContext.
func newPool(ctx context.Context, cfg Config, r Runner, em *Emitter, clk Clock) (*pool, context.Context) {
	pctx, cancel := context.WithCancel(ctx)
	slots := make([]slotState, cfg.Slots)
	for i := range slots {
		slots[i].phase = PhaseIdle
		slots[i].since = clk.Now()
	}
	wake, wakeCancel := context.WithCancel(context.Background())
	p := &pool{
		cfg:     cfg,
		r:       r,
		em:      em,
		clk:     clk,
		cancel:  cancel,
		abandon: make(chan struct{}),
		rev:     newRevisionFeed(),
		st: state{
			slots:     slots,
			lastStart: make([]childStartPin, cfg.Slots),
			b:         newBreaker(cfg.BreakerThreshold, cfg.BreakerWindow),
			sched:     newSchedule(cfg.Kinds, cfg.Reservation, cfg.IdleFloor, cfg.IdleCap, cfg.ProbeIntervals, cfg.Trackers),
			batonSlot: leadSlot,

			wake:       wake,
			wakeCancel: wakeCancel,
		},
		demandFlights: make(map[Kind]*demandFlight),
	}
	p.st.self = cfg.SelfUpdate
	p.st.selfSince = clk.Now()
	// Must precede any slot running, so own starts read the restored gate
	// generation (gateGenOf) and the restored breaker.
	if h := cfg.Handoff; h != nil {
		p.st.sched = p.st.sched.restore(h.Kinds)
		p.st.b = p.st.b.restore(h.Breaker)
	}
	if cfg.Slots > 1 {
		// A single slot takes no wait and emits no baton event. The channel
		// starts empty: leadSlot already holds the token, so it only ever
		// sends, when it passes.
		p.baton = make(chan struct{}, 1)
	}
	return p, pctx
}

// slotOrder returns the kinds a starting slot tries, most preferred first, in
// four tiers: first (recover, issue #4656), reserved (research), normal (e.g.
// work) and idle (the butler, ADR 0056). First kinds go first so stranded
// finished work lands before a new Box starts; idle kinds go last, picked only
// once every other kind has reported no work. preferReserved (from
// Schedule.Decide) puts reserved ahead of normal, else behind it.
func slotOrder(kinds []Kind, preferReserved bool) []Kind {
	if len(kinds) < 2 {
		return kinds
	}
	first := make([]Kind, 0, 1)
	reserved := make([]Kind, 0, 1)
	normal := make([]Kind, 0, len(kinds))
	idle := make([]Kind, 0, 1)
	for _, k := range kinds {
		switch kindPriority(k) {
		case dispatchkind.PriorityFirst:
			first = append(first, k)
		case dispatchkind.PriorityReserved:
			reserved = append(reserved, k)
		case dispatchkind.PriorityIdle:
			idle = append(idle, k)
		default:
			normal = append(normal, k)
		}
	}
	order := make([]Kind, 0, len(kinds))
	order = append(order, first...)
	if preferReserved {
		order = append(order, reserved...)
		order = append(order, normal...)
		return append(order, idle...)
	}
	order = append(order, normal...)
	order = append(order, reserved...)
	return append(order, idle...)
}

// kindPriority is k's daemon priority tier, PriorityNormal for a kind with no
// descriptor — the one tier rule slotOrder's ordering and Schedule.Decide's
// reserved count both read.
func kindPriority(k Kind) dispatchkind.DaemonPriority {
	if d, ok := dispatchkind.ByVerb(string(k)); ok {
		return d.DaemonPriority
	}
	return dispatchkind.PriorityNormal
}

// occupancyOf reads what the slot phases say is running, for Schedule.Decide's
// reservation count and Start budget. Only running children count, so
// reserved-tier children that exit early never lift the count, and the
// reserved kind can keep taking every turnover while normal-tier kinds still
// have queued work. A running slot with no flight key has not reported its
// claim, so it is also starting. The deciding slot is not PhaseRunning, so
// neither count includes it. The caller holds p.mu.
func occupancyOf(s *state) occupancy {
	running := make(map[Kind]int)
	starting := make(map[Kind]int)
	for _, sl := range s.slots {
		if sl.phase == PhaseRunning {
			running[sl.flight.kind]++
			if sl.flight.key.IsZero() {
				starting[sl.flight.kind]++
			}
		}
	}
	return occupancy{Running: running, Starting: starting}
}

// liveKeys is the set of dispatch keys the pool's running children hold. A
// finished slot's key is zeroed by finishChild and its phase leaves
// PhaseRunning, so only live claims appear. The caller holds p.mu.
func liveKeys(s *state) dispatchkey.Set {
	keys := make(dispatchkey.Set)
	for _, sl := range s.slots {
		if sl.phase == PhaseRunning && !sl.flight.key.IsZero() {
			keys[sl.flight.key] = true
		}
	}
	return keys
}

// decide asks the Schedule what a free slot should do at now. A pure read,
// so the answer is provisional: startChild re-decides in the same p.mu hold
// as the PhaseRunning flip, so two slots never both take the last reserved
// seat, and nothing is claimed before then, so no early exit needs cleanup.
func (p *pool) decide(now time.Time) decision {
	d, _ := p.decideWithWake(now)
	return d
}

// decideWithWake is decide plus the wake generation, read in the same p.mu
// hold: a wake landing between the two reads would cancel a generation the
// caller never sleeps on, and the slot would sleep through a startable kind.
func (p *pool) decideWithWake(now time.Time) (decision, context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.sched.Decide(now, occupancyOf(&p.st)), p.st.wake
}

// sleepUntilWoken sleeps d on the Clock, ending early if ctx ends or the wake
// generation is cancelled; woken reports that the generation has been
// cancelled by the time it returns, even by a wake just after the sleep ran
// its full d. The Clock sees only the derived ctx, so its interface stays a
// plain Sleep.
func (p *pool) sleepUntilWoken(ctx, wake context.Context, d time.Duration) (woken bool) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(wake, cancel)()
	p.clk.Sleep(sctx, d)
	return wake.Err() != nil
}

// resetKind folds a Continue outcome (exit 0 or 4) into the schedule: a check
// that answered something other than "nothing to do" ends whatever no-work
// streak this kind's backoff was tracking.
func (p *pool) resetKind(kind Kind) {
	now := p.clk.Now()
	p.mutate(func(s *state) []Event {
		s.observe(now, childDone{Kind: kind, Result: childContinue})
		if s.continues == nil {
			s.continues = map[Kind]int{}
		}
		s.continues[kind]++
		return nil
	})
}

// noteWaitResult records kind's no-work result and evaluates the jam
// predicate in one mutate, so the two are never read from different instants.
// With a sibling genuinely running, "none dispatchable" means the issues were
// claimed or overlap-deferred against it: routine, reported as idle. With
// every sibling idle, resolving, backing off, parked on the shut Awake window,
// or running only a butler child (siblingsEngaged), nothing that could unblock
// it is running: a jam an operator may need to clear. The alarm is narrower
// than jammed itself, which records the queue condition the check saw
// (kindBackoff.markNoWork; issues #3571, #3735, #4205).
//
// f is the child's flight snapshot (flightReport): its not_due report,
// Continue count, and the gate generation that lets a burst of results from
// one gate raise the backoff once. A stale report (reportStale) is replaced by
// "due now": one spurious start just re-reports, where parking on a stale
// report can idle the kind indefinitely.
func (p *pool) noteWaitResult(slot int, kind Kind, revision string, noneDispatchable bool, f slotFlight) {
	now := p.clk.Now()
	nextDue := f.nextDue
	result := childEmpty
	if noneDispatchable {
		result = childJammed
	}
	p.mutate(func(s *state) []Event {
		if s.reportStale(kind, revision, f.continues, nextDue) {
			nextDue = NextDue{At: now}
		}
		s.observe(now, childDone{Kind: kind, Result: result, NextDue: nextDue, GateGen: f.gateGen})
		// What the kind now waits for: its backoff, or for a probed kind the
		// interval to its next probe.
		var wait time.Duration
		if v := s.sched.View(kind, now); v.Gated && !v.Until.IsZero() {
			wait = v.Until.Sub(now)
		}
		if noneDispatchable && !s.siblingsEngaged(slot) {
			return []Event{{Event: report.EventJam, Kind: kind, Revision: revision, Slot: intPtr(slot), Wait: wait.String(), Reason: "no work is dispatchable and no sibling slot is doing anything that could unblock it"}}
		}
		return []Event{{Event: report.EventIdle, Kind: kind, Wait: wait.String(), Slot: intPtr(slot)}}
	})
}

// reportStale reports that a not_due report from a kind's child, run at
// revision having seen continues Continue exits, no longer holds. A sibling
// child's Continue clears the kind's due state and may have released the very
// claim the report waits on. A tip that moved since revision already spent
// the move an on_tip_move report waits for, and noteTipMoved had no parked
// kind to lift; comparing revisions rather than lifts is what catches a move
// landing anywhere between the child's resolve and its exit.
func (s *state) reportStale(kind Kind, revision string, continues int, nd NextDue) bool {
	if nd.IsZero() {
		return false
	}
	return s.continues[kind] != continues || (nd.OnTipMove && s.tip != "" && s.tip != revision)
}

// startChild marks slot running at revision and returns the kind it chose,
// emitting child_start in the same mutate so any snapshot published alongside
// it shows the phase flip. The kind is re-decided here (Schedule.Decide). When
// that is not a start (or is an issue-keyed start refused to a non-holder
// deciding on a Chore-keyed provisional), a probed provisional kind is not
// started and ok is false: its count is what said there was work, and a
// sibling's exit 2 may have zeroed it, so the caller decides again rather than
// spawn an empty child (ADR 0059). An exit-driven kind has only its backoff to
// go on and a child is how it finds work, so it starts as provisional.
// child_start carries no dispatch key: the child's key is unknown until its
// "box" record, where the slot↔key binding first appears (slotFlight.fold).
// Deferring child_start until then would hide a started child for its whole
// queue scan, or forever if it never claims.
func (p *pool) startChild(slot int, provisional Kind, revision string) (kind Kind, childLog string, ok bool) {
	now := p.clk.Now()
	p.mutate(func(s *state) []Event {
		// A halt can land between the slot's last admission check and here,
		// notably the self-update goroutine's, which no slot's loop orders
		// against; no child may start once it has.
		if s.halted || s.self.handingOff() {
			return nil
		}
		// A slot not holding the baton, deciding on a Chore-keyed
		// provisional kind, must not switch to an issue-keyed one: only the
		// holder discovers issues.
		discovers := !provisional.choreKeyed() || p.baton == nil || s.batonSlot == slot
		if st, started := s.sched.Decide(now, occupancyOf(s)).(start); started && (discovers || st.Kind.choreKeyed()) {
			kind = st.Kind
		} else if v := s.sched.View(provisional, now); v.Probed || !v.NextDue.IsZero() {
			return nil
		} else {
			kind = provisional
		}
		ok = true
		// The event's time is stamped here from this one clock read, not by
		// the emitter, so the status's child_start matches it byte for byte;
		// the ordinal follows the dashboard's dispatchOwners rule (prev is the
		// slot's latest child_start), or its link 404s.
		started := p.clk.Now().UTC().Format(time.RFC3339)
		pin := childStartPin{time: started}
		if prev := s.lastStart[slot]; prev.time == started {
			pin.n = prev.n + 1
		}
		s.lastStart[slot] = pin
		s.slots[slot] = slotState{phase: PhaseRunning, flight: slotFlight{kind: kind, revision: revision, continues: s.continues[kind], gateGen: s.sched.gateGenOf(kind), childStart: pin}}
		childLog = childLogPath(now, slot, kind)
		s.slots[slot].flight.childLog = childLog
		return []Event{{Event: report.EventChildStart, Time: started, Kind: kind, Revision: revision, Slot: intPtr(slot), ChildLog: childLog}}
	})
	return kind, childLog, ok
}

// occupyInherited seats the pool's handoff slots in one mutate, so the first
// status write already shows them, and emits a child_inherited per slot. A
// no-op on a cold start.
func (p *pool) occupyInherited() {
	h := p.cfg.Handoff
	if h == nil {
		return
	}
	p.mutate(func(s *state) []Event {
		var evs []Event
		for _, hs := range h.Slots {
			pin := childStartPin{time: hs.ChildStart, n: hs.ChildStartN}
			s.lastStart[hs.Slot] = pin
			since := p.clk.Now()
			if t, err := time.Parse(time.RFC3339, hs.ChildStart); err == nil {
				since = t
			}
			s.slots[hs.Slot] = slotState{phase: PhaseRunning, since: since, sincePinned: true, flight: slotFlight{
				kind: hs.Kind, revision: hs.Revision, issues: slices.Clone(hs.Issues), key: hs.Key,
				pass: hs.Pass, model: hs.Model, modelRole: hs.ModelRole, childStart: pin,
				continues: s.continues[hs.Kind], gateGen: hs.GateGen,
				ciWait: ciWait{pr: hs.CIWaitPRURL, runURL: hs.CIWaitRunURL},
				// Its predecessor required the first report before handing off.
				// imageBuildSince is not carried: image_build is sent in
				// bootstrap, before any record that permits a handoff.
				childLog: hs.ChildLog, reported: true,
			}}
			evs = append(evs, Event{Event: report.EventChildInherited, Kind: hs.Kind, Key: hs.Key, Revision: hs.Revision, Slot: intPtr(hs.Slot), ChildLog: hs.ChildLog})
		}
		// Every inherited child is past discovery (a handoff only happens with
		// the baton free), so the pre-assigned leader's token must not wait on
		// an inherited leadSlot child that may run for hours.
		if p.baton != nil && len(h.Slots) > 0 {
			s.batonSlot = noBaton
			p.baton <- struct{}{}
		}
		return evs
	})
}

// childLogPath names a child's Child log, relative to the checkout. The
// timestamp carries milliseconds because a fast-exiting child frees its slot
// to start the next one within the same second, and the two must not share a
// file.
func childLogPath(start time.Time, slot int, kind Kind) string {
	name := fmt.Sprintf("%s-slot%d-%s.log", start.UTC().Format(childLogTimeLayout), slot, kind)
	return filepath.Join(hostpaths.LogDir(""), "daemon", name)
}

// childLogTimeLayout is the start-time stamp in a Child log's name, to the
// millisecond.
const childLogTimeLayout = "20060102T150405.000Z"

// finishChild moves slot back to idle and zeroes its flight. A slot calls this
// immediately after the child's Wait returns, before the result is interpreted: it
// must clear its own running phase before it ever asks siblingsEngaged, or
// it would count itself as an engaged sibling and "none dispatchable, pool
// otherwise idle" could never be true for a lone slot.
func (p *pool) finishChild(slot int) {
	p.mutate(func(s *state) []Event {
		s.slots[slot] = slotState{phase: PhaseIdle}
		return nil
	})
}

// noteRecord folds a child-report record into slot's flight under the lock,
// emitting its events and feeding the Schedule in the same mutate as the
// status snapshot.
func (p *pool) noteRecord(slot int, kind Kind, revision string, rec Record) {
	now := p.clk.Now()
	p.mutate(func(s *state) []Event {
		sl := &s.slots[slot]
		var evs []Event
		var sched schedEvent
		sl.flight, evs, sched = sl.flight.fold(rec, slotFacts{slot: slot, kind: kind, revision: revision, now: now, running: sl.phase == PhaseRunning})
		if sched != nil {
			s.observe(now, sched)
		}
		return evs
	})
}

// slotFacts is the slot context slotFlight.fold needs beyond the flight.
// kind and revision come from the caller, not the flight: a settled record
// still emits after the slot cleared and zeroed its flight.
type slotFacts struct {
	slot     int
	kind     Kind
	revision string
	now      time.Time
	running  bool
}

// fold applies one child-report record to f, returning the new flight, the
// events for the Events file, and the Schedule event to observe (nil for
// none). It is pure: f is not written through, so the pool applies the result
// inside one mutate and the event and status snapshot always agree.
//
// A record arriving when the slot is not running is a late record from a child
// whose slot already cleared (Wait returned) — a race, not a state worth
// publishing — so every case but settled drops it.
func (f slotFlight) fold(rec Record, sf slotFacts) (slotFlight, []Event, schedEvent) {
	if !sf.running && rec.Event != report.EventSettled {
		return f, nil, nil
	}
	slot := intPtr(sf.slot)
	var evs []Event
	var sched schedEvent
	switch rec.Event {
	case report.EventBox:
		// Any key change counts as a new claim, so the kind is re-probed
		// before its next start; a repeat of the same key moves no count.
		// A switch back to a key the slot already boxed costs only an extra
		// re-probe.
		if f.key != rec.Key {
			sched = claimed{Kind: sf.kind}
		}
		f.key = rec.Key
		f.pass = rec.Phase
		f.model, f.modelRole = "", ""
		f.clearTransient()
		// A Chore key stays out of the tracker-issue list. Deduped: a fix-pass
		// box for an issue already open here continues the same claim (issue
		// #3627). Clipped so the append never writes the input's backing array.
		if issue, _ := rec.Key.Fields(); issue != "" && !slices.Contains(f.issues, issue) {
			f.issues = append(slices.Clip(f.issues), issue)
		}
		// Emitted even for a deduped issue: only the status-file issue list
		// collapses repeats.
		evs = []Event{{Event: report.EventBox, Kind: sf.kind, Revision: sf.revision, Key: rec.Key, Phase: rec.Phase, PassLog: rec.PassLog, RecordID: rec.RecordID, Slot: slot}}
	case report.EventModel:
		f.model, f.modelRole = rec.Model, rec.ModelRole
		evs = []Event{{Event: report.EventModel, Kind: sf.kind, Revision: sf.revision, Key: rec.Key, Model: rec.Model, ModelRole: rec.ModelRole, Slot: slot}}
	case report.EventCIWait:
		f.ciWait = ciWait{pr: rec.PRURL, runURL: rec.RunURL}
	case report.EventImageBuild:
		// started stamps the build's start, finished clears it. The record is
		// keyless, so the event carries no key.
		f.imageBuildSince = time.Time{}
		if report.ImageBuildState(rec.State) == report.ImageBuildStarted {
			f.imageBuildSince = sf.now
		}
		evs = []Event{{Event: report.EventImageBuild, Kind: sf.kind, Revision: sf.revision, State: rec.State, Slot: slot}}
	case report.EventNotDue:
		// For runSlot to hand the schedule when the child exits empty
		// (flightReport). Neither the claim key nor the baton moves — a
		// not_due Chore was never claimed.
		f.nextDue = f.nextDue.Merge(rec.NextDue)
	case report.EventSettled:
		// Emits even when the slot no longer runs: it is the one event that
		// answers "what happened to #123".
		if sf.running {
			f.clearTransient()
		}
		evs = []Event{{Event: report.EventSettled, Kind: sf.kind, Revision: sf.revision, Key: rec.Key, State: rec.State, Note: rec.Note, PRURL: rec.PRURL, RecordID: rec.RecordID, Slot: slot}}
	}
	// An event fold does not know leaves the flight unchanged and emits
	// nothing, though noteRecord's mutate still publishes the status:
	// ParseRecord already dropped a genuinely unknown one upstream (record.go).
	return f, evs, sched
}

// clearTransient resets the per-child transient indicators that a new Pass or
// a settle ends.
func (f *slotFlight) clearTransient() {
	f.ciWait = ciWait{}
	f.imageBuildSince = time.Time{}
}

// flightClaim returns the key slot's child most recently boxed, repeats
// included. A caller needing it for child_finish must read it before
// finishChild zeroes the flight.
func (p *pool) flightClaim(slot int) dispatchkey.Key {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.slots[slot].flight.key
}

// flightReport returns slot's flight snapshot for noteWaitResult: the folded
// not_due reports, and the Continue count and gate generation at child start.
// Like flightClaim, it must be read before finishChild.
func (p *pool) flightReport(slot int) slotFlight {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.slots[slot].flight
}

// siblingsEngaged reports whether any slot other than slot could release a
// claim the none-dispatchable read found blocked (#3922): a sibling
// PhaseRunning a non-butler child. No other phase holds anything releasable,
// since the claim is taken inside the child; a new phase defaults to not
// engaged. PhaseBackingOff (#4205) follows a ResolveTip failure that took no
// claim or a child that already exited, whose claim was cleared or orphaned
// as agent-in-progress until `spindrift recover`; the overlap gate reads
// tracker labels, not slot phase, so the backoff sleep releases nothing.
//
// The butler skip ignores the reporting kind, which is safe only while the
// butler never exits 3 (exitCodeFor maps 3 solely from
// waves.ErrOpenNoneDispatchable) and so never reports a jam itself (#3922).
func (s *state) siblingsEngaged(slot int) bool {
	for sl, ss := range s.slots {
		if sl == slot {
			continue
		}
		if ss.phase == PhaseRunning && !ss.flight.kind.choreKeyed() {
			return true
		}
	}
	return false
}

// awaitWindow parks slot until the Awake window is open. It sleeps the
// whole remaining span in one clk.Sleep rather than polling: nothing about
// a fixed daily window can change before its own clock-computed opening
// arrives, so a shut window costs one sleep, not a poll loop through it.
func (p *pool) awaitWindow(ctx context.Context, slot int) {
	for {
		wait, exhausted := p.cfg.Awake.Until(p.clk.Now())
		if wait <= 0 {
			p.noteAwakeOpen(slot)
			return
		}
		p.noteAwakeClose(slot, wait, exhausted)
		p.clk.Sleep(ctx, wait)
		if p.stopped() || ctx.Err() != nil {
			return
		}
	}
}

// noteAwakeClose records the pool-wide move into a shut window and returns
// awake_close only for the slot that flips awakeShut, so the stream carries
// one awake_close per closing however many slots park on it. Every parking
// slot records its own phase regardless.
//
// Being edge-triggered, the event's Reason reflects only the first park's
// walk: a later re-park in the same shut span whose walk exhausts is not
// re-reported. Status.Reason recomputes on every publish, so it still is.
func (p *pool) noteAwakeClose(slot int, wait time.Duration, exhausted bool) {
	p.mutate(func(s *state) []Event {
		first := !s.awakeShut
		s.awakeShut = true
		s.slots[slot].phase = PhaseAwaitingWindow
		if !first {
			return nil
		}
		reason := outsideWindowReason
		if exhausted {
			reason = walkExhaustedReason
		}
		return []Event{{Event: report.EventAwakeClose, Slot: intPtr(slot), Wait: wait.String(), Reason: reason}}
	})
}

// noteAwakeOpen returns awake_open only when a close was reported, so the
// stream never carries an open with no matching close. It is also the
// top-of-iteration phase reset: every path back to the top of runSlot passes
// through awaitWindow and lands here, clearing whatever phase it left behind.
func (p *pool) noteAwakeOpen(slot int) {
	p.mutate(func(s *state) []Event {
		was := s.awakeShut
		s.awakeShut = false
		s.slots[slot].phase = PhaseIdle
		if !was {
			return nil
		}
		return []Event{{Event: report.EventAwakeOpen, Slot: intPtr(slot), Reason: "the Awake window reopened"}}
	})
}

// awaitBaton parks slot until it holds the discovery baton. Returns at once
// with no baton (a single-slot pool) or when slot already holds it (leadSlot's
// first call). Otherwise it tries a non-blocking receive, and only on a miss
// emits baton_hold and blocks on a select that also watches ctx, so a pool
// cancelled while a slot is held never deadlocks it.
func (p *pool) awaitBaton(ctx context.Context, slot int) {
	if p.baton == nil {
		return
	}
	p.mu.Lock()
	held := p.st.batonSlot == slot
	p.mu.Unlock()
	if held {
		return
	}
	select {
	case <-p.baton:
		p.takeBaton(slot)
		return
	default:
	}
	// A slot waiting its turn reads as PhaseIdle, not the "resolving" it
	// entered with; set in baton_hold's mutate so its snapshot shows it.
	p.mutate(func(s *state) []Event {
		s.slots[slot].phase = PhaseIdle
		return []Event{{Event: report.EventBatonHold, Slot: intPtr(slot), Reason: batonHoldReason}}
	})
	select {
	case <-ctx.Done():
	case <-p.baton:
		p.takeBaton(slot)
	}
}

// takeBaton records slot as the current baton holder.
func (p *pool) takeBaton(slot int) {
	p.mutate(func(s *state) []Event {
		s.batonSlot = slot
		return nil
	})
}

// passBaton hands the baton on. A no-op unless slot holds it, so it is
// idempotent per hold and safe at every site a round can end: several release
// sites in runSlot overlap, and only the first one's reason reaches the
// stream.
//
// The holder check needs no mutate: only the holder clears its own hold, so
// the read cannot go stale. The send follows the baton_pass mutate: sending
// first could let a blocked awaitBaton wake and publish before baton_pass,
// so the record would show a sibling acting before the event that allowed
// it. The send cannot block: only the holder sends, at most once per hold.
func (p *pool) passBaton(slot int, reason batonReason) {
	if p.baton == nil {
		return
	}
	p.mu.Lock()
	held := p.st.batonSlot == slot
	p.mu.Unlock()
	if !held {
		return
	}
	p.mutate(func(s *state) []Event {
		s.batonSlot = noBaton
		return []Event{{Event: report.EventBatonPass, Slot: intPtr(slot), Reason: string(reason)}}
	})
	p.baton <- struct{}{}
}

// parkIfHandingOff holds slot while the in-place self-update waits for its
// clean moment: no child may start, yet the slot must not return, or a pool
// with every slot free would end without a halt or a handoff. It parks until
// the pool's context is cancelled (a stop's halt does that) and reports true
// so the caller returns. The baton is passed first so a sibling parked on it
// is not stranded behind a slot that will not discover.
func (p *pool) parkIfHandingOff(ctx context.Context, slot int) bool {
	p.mu.Lock()
	handingOff := p.st.self.handingOff()
	p.mu.Unlock()
	if !handingOff {
		return false
	}
	p.setPhase(slot, PhaseIdle)
	p.passBaton(slot, batonPassStopped)
	<-ctx.Done()
	return true
}

// stopped reports whether the pool has already recorded a halt reason, or has
// committed its in-place handoff (whose cancelled context must not read as a
// fresh halt to the slots it wakes) — deliberately not a raw ctx.Err() check;
// see haltIfStopping for why.
func (p *pool) stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.halted || p.st.self.Phase == SelfUpdateRestarting
}

// stopClosed reports whether cfg.Stop has been closed, without blocking. A
// nil Stop (an embedder that hard-cancels ctx instead) always reads open.
func (p *pool) stopClosed() bool {
	select {
	case <-p.cfg.Stop:
		return true
	default:
		return false
	}
}

// haltStopRequested records the operator-stop halt a closed cfg.Stop
// demands, shared by Loop's Stop watcher and haltIfStopping so the class and
// detail cannot drift. Its "context-cancelled" rendering (halt.go's
// haltRenderings) is documented grammar in docs/reference.md: an operator
// stop and a caller ctx cancellation render identically on purpose. kind is
// "" from Loop's watcher, which runs no kind.
func (p *pool) haltStopRequested(kind Kind) {
	p.halt(Halt{Class: HaltOperatorStop, Detail: "stop requested", Kind: kind})
}

// haltIfStopping reports whether the slot should stop starting new work,
// checking in order: a sibling already halted (return quietly); cfg.Stop
// closed (an operator stop this call is first to notice); ctx freshly
// cancelled (a caller's hard cancel, as in tests). p.stopped() goes first
// because p.halt's own cancel() also cancels ctx, so ctx.Err() alone cannot
// tell a real cancellation from a sibling's halt. kind is "" from the
// top-of-loop call, which has not picked one yet.
func (p *pool) haltIfStopping(ctx context.Context, kind Kind) bool {
	if p.stopped() {
		return true
	}
	if p.stopClosed() {
		p.haltStopRequested(kind)
		return true
	}
	if err := ctx.Err(); err != nil {
		p.halt(Halt{Class: HaltOperatorStop, Detail: err.Error(), Kind: kind})
		return true
	}
	return false
}

// recordHalt latches h as the halt reason and returns its event, or nil when
// the pool had already halted. It runs inside mutate so a caller can fold
// other state changes into the same atomic step.
func (s *state) recordHalt(h Halt) []Event {
	// A committed handoff already ends the pool (Loop returns it, not a halt),
	// so a halt racing it must not emit an event for a pool that did not halt.
	if s.halted || s.self.Phase == SelfUpdateRestarting {
		return nil
	}
	s.halted = true
	s.reason = h
	if s.self.Phase == SelfUpdateWaiting {
		// The handoff is cancelled; the build it waited to hand off stays.
		s.self.Phase = SelfUpdateBuilt
	}
	return []Event{h.Event()}
}

// halt records h as the pool's halt reason if none is recorded yet, emits
// the single pool-level halt event, and cancels the derived context.
// Idempotent: the first h wins and no second halt event is ever emitted.
func (p *pool) halt(h Halt) {
	first := false
	p.mutate(func(s *state) []Event {
		evs := s.recordHalt(h)
		first = evs != nil
		return evs
	})
	if !first {
		return
	}
	p.cancel()
}

// backoffOrHalt is what a slot calls on an unclassified failure (a
// ResolveTip error, a StartChild/Wait seam error, an unrecognised exit code,
// or exit 7 while Stop is open — someone else signalled that child): it
// records the failure in the pool-wide breaker and either trips the pool
// (failures across the pool look systemic) or backs this slot off alone.
// Returns true if the pool halted. Before backing off it passes the baton
// with passReason, so a holder sleeping through FailureBackoff does not stall
// siblings' discovery; the pass follows the breaker and p.stopped() checks,
// since a sibling woken before a trip lands would start a child on a halting
// pool (issue #4365).
//
// The breaker's one carve-out (issue #3595): a failure landing once Stop has
// closed, the pool has halted, or ctx is cancelled is an ordinary shutdown,
// never counted. It lives here, not at call sites, because backoffOrHalt is
// the breaker's one gate. It reads the latch after the child exited, not at
// exit time as exit 7 does (loop.go, at Interpret), so a close in between also
// carves out an unrecognised exit; the pool halts as an operator stop either
// way, so only the breaker count differs.
func (p *pool) backoffOrHalt(ctx context.Context, slot int, kind Kind, revision, reason string, passReason batonReason) bool {
	// A halt return passes no baton here: runSlot's deferred
	// passBaton(slot, batonPassStopped) (loop.go) releases it after the
	// halt, so an operator stop or breaker trip reads as such in the
	// baton_pass event stream.
	if p.haltIfStopping(ctx, kind) {
		return true
	}

	now := p.clk.Now()
	var count int
	var crossed bool
	p.mutate(func(s *state) []Event {
		s.b, count, crossed = s.b.recordAndCheck(now)
		if !crossed {
			return nil
		}
		return []Event{{Event: report.EventBreakerTrip, Kind: kind, Slot: intPtr(slot), Failures: &count, Wait: p.cfg.BreakerWindow.String()}}
	})
	if crossed {
		detail := fmt.Sprintf("%d failures within %s reached threshold %d", count, p.cfg.BreakerWindow, p.cfg.BreakerThreshold)
		p.halt(Halt{Class: HaltBreaker, Detail: detail, Kind: kind, Revision: revision})
		return true
	}

	// This call did not cross the threshold itself, but a sibling racing
	// concurrently through recordAndCheck might already have — check
	// before backing off so a slot never sleeps out a fresh backoff
	// against a pool that is already stopping.
	if p.stopped() {
		return true
	}

	// passBaton is a no-op unless slot actually holds it, so callers need
	// not track whether they acquired it.
	p.passBaton(slot, passReason)

	p.mutate(func(s *state) []Event {
		s.slots[slot].phase = PhaseBackingOff
		return []Event{{Event: report.EventBackoff, Kind: kind, Revision: revision, Slot: intPtr(slot), Wait: p.cfg.FailureBackoff.String(), Reason: reason}}
	})
	p.clk.Sleep(ctx, p.cfg.FailureBackoff)
	// Set idle here: on a shut window awaitWindow would overwrite
	// backing-off with awaiting-window, never showing the idle in between.
	p.setPhase(slot, PhaseIdle)
	return false
}

// setPhase moves slot to phase with no event of its own — for transitions
// that ride no operator-visible event: resolving (the fetch and self-build
// check share it, since both are the same kind of outside-world evaluation)
// and the idle reset once a backoff sleep ends.
func (p *pool) setPhase(slot int, phase Phase) {
	p.mutate(func(s *state) []Event {
		s.slots[slot].phase = phase
		return nil
	})
}

// resolveFailure turns a ResolveTip error into the revision and reason
// backoffOrHalt should record. A *SelfEvalError means the fetch succeeded
// but the self-build evaluation failed; it renders through Halt.String() so
// the documented self-build grammar keeps one renderer. Any other error is a
// failed fetch, with no revision.
func resolveFailure(tip Tip, err error) (revision, reason string) {
	var se *SelfEvalError
	if errors.As(err, &se) {
		return tip.Revision, Halt{Class: HaltSelfBuild, Detail: se.Err.Error()}.String()
	}
	return "", fmt.Sprintf("resolve-revision: %v", err)
}

// idleSleep is what a slot calls when Schedule.Decide finds nothing to start
// or probe. It re-decides rather than taking the caller's park, so a
// sibling's reset or a landed probe is seen; anything but a park with time
// left returns at once. Without a jammed kind or tip wait gating it
// (park.TipPoll false) it sleeps the whole wait: a merge cannot create work in
// a merely queue-empty kind, so resolving early would waste a fetch.
//
// With one, it sleeps one IdleFloor slice (or the shorter whole wait) and
// reports wantResolve when time remained, so runSlot (loop.go) makes one
// opportunistic resolution, the call that observes Tip.Moved, before its next
// Decide.
//
// Either sleep ends early when a sibling's event grows the startable set
// (state.wakeParked); a woken slot reports wantResolve false.
func (p *pool) idleSleep(ctx context.Context, slot int) (wantResolve bool) {
	now := p.clk.Now()
	d, wake := p.decideWithWake(now)
	pk, ok := d.(park)
	if !ok || !pk.Until.After(now) {
		return false
	}
	wait := pk.Until.Sub(now)

	if !pk.TipPoll {
		p.sleepUntilWoken(ctx, wake, wait)
		return false
	}

	// A never-run slot slicing its wait and resolving during a sibling's
	// jam is safe: haveLastRevision makes the first resolution any slot
	// ever makes report Moved == false, whichever slot makes it, so there
	// is no spurious first-ever "moved" to guard against.
	slice := p.cfg.IdleFloor
	if slice > wait {
		slice = wait
	}
	woken := p.sleepUntilWoken(ctx, wake, slice)
	if woken || p.stopped() || ctx.Err() != nil {
		// The caller's next Decide or haltIfStopping handles each of these;
		// no resolution is worth making.
		return false
	}
	return wait > slice
}

// resolveTip is the one path from runSlot's post-Decide fetch and
// resolveOpportunistic to Runner.ResolveTip: it sets PhaseResolving and
// reports a Moved tip via noteTipMoved, so no caller can receive a moved Tip
// and forget to report it. Under coalescing every joiner sees Moved=true;
// noteTipMoved's empty-lift guard keeps a merge to one tip_moved.
//
// The report fires on tip.Moved whatever err is: a *SelfEvalError still
// carries the fetched revision and the runner's baseline has already
// advanced, so a move dropped here is never re-observed. Errors are returned
// unchanged for each caller's own handling.
func (p *pool) resolveTip(ctx context.Context, slot int) (Tip, error) {
	p.setPhase(slot, PhaseResolving)
	tip, err := p.r.ResolveTip(ctx)
	if tip.Revision != "" {
		p.rev.set(tip.Revision)
	}
	if tip.Moved {
		p.noteTipMoved(slot, tip.Revision)
	}
	return tip, err
}

// resolveOpportunistic makes the opportunistic ResolveTip call idleSleep asks
// runSlot for, ahead of its next Decide; resolveTip already reports
// Tip.Moved. A failure counts as no change observed, with no haltIfStopping,
// backoffOrHalt or breaker failure: only the post-Decide fetch reports a
// broken fetch.
func (p *pool) resolveOpportunistic(ctx context.Context, slot int) (Tip, bool) {
	tip, err := p.resolveTip(ctx, slot)
	if err != nil {
		// Includes *FeatureBranchGoneError: no claim is possible from this
		// opportunistic call, so swallowing it here only delays the halt to
		// runSlot's next real resolve, plus one extra ls-remote.
		return Tip{}, false
	}
	return tip, true
}

// noteTipMoved records revision as an observed moved tip and lifts every
// jammed kind's gate (a queue-empty kind's streak is untouched: a moved tip is
// no evidence an empty queue refilled) and every reported kind's on_tip_move
// wait. tip_moved's Kinds is the set lifted, in configured order; it is
// emitted in the same mutate as the lift, so the two always agree.
//
// It is emitted only for a non-empty lift: resolveTip reports every fetch's
// Moved tip here, and without the guard tip_moved would fire on every
// ordinary base-branch advance rather than staying the jam signal. The
// Runner-global baseline means two slots crossing one merge produce one
// tip_moved, which suffices since the lift is pool-wide.
func (p *pool) noteTipMoved(slot int, revision string) {
	now := p.clk.Now()
	p.mutate(func(s *state) []Event {
		s.tip = revision
		lifted := s.tipMoved(now)
		if len(lifted) == 0 {
			return nil
		}
		return []Event{{Event: report.EventTipMoved, Slot: intPtr(slot), Revision: revision, Reason: "a merge can unblock a jammed queue or end a reported kind's on_tip_move wait", Kinds: lifted}}
	})
}

// publish writes s to Config.Status, if configured; mutate is its only
// caller. A nil Status is a deliberate opt-out (no git dir, or the operator
// chose not to publish). A write failure is advisory and never fails the
// daemon.
func (p *pool) publish(seq uint64, s Status) {
	if p.cfg.Status == nil {
		return
	}
	if err := p.cfg.Status.Publish(seq, s); err != nil {
		p.em.warnStatusWrite(err)
	}
}

// publishInitial publishes the startup state before any slot's first change.
// It goes through mutate rather than projectStatus/publish so the snapshot
// and its sequence number come from the one operation that allocates both;
// otherwise it could race a slot's first mutate and overwrite fresher state.
func (p *pool) publishInitial() {
	p.mutate(func(*state) []Event { return nil })
}

// emit is mutate's trivial case, for an event that rides no state change of
// its own; today only child_finish.
func (p *pool) emit(ev Event) {
	p.mutate(func(*state) []Event { return []Event{ev} })
}

// haltReason returns the pool's recorded Halt. Only meaningful once every
// slot goroutine has returned; it locks anyway rather than lean on that.
func (p *pool) haltReason() Halt {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.reason
}

// inheritedSlot is slot's entry in the pool's handoff, nil when it has none.
func (p *pool) inheritedSlot(slot int) *HandoffSlot {
	if p.cfg.Handoff == nil {
		return nil
	}
	for i := range p.cfg.Handoff.Slots {
		if p.cfg.Handoff.Slots[i].Slot == slot {
			return &p.cfg.Handoff.Slots[i]
		}
	}
	return nil
}
