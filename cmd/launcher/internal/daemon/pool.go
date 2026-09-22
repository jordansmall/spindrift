package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// pool is the shared state behind Loop's slot goroutines. It owns the one
// thing every slot must agree on: whether the daemon has decided to stop,
// and why. First halt wins — Loop emits exactly one "halt" event per call,
// whichever slot gets there first — and every slot shares one derived
// context that pool cancels the moment it halts, so a sibling asleep in its
// idle wait or blocked in ResolveRevision stops promptly instead of riding
// out the full interval or fetch.
type pool struct {
	cfg Config
	r   Runner
	em  *Emitter
	clk Clock

	cancel context.CancelFunc

	// baton is the discovery token (issue #3684): at most one slot may be
	// between "started" and "announced a Box" at any moment, on the first
	// wave and on every refill alike — a pool that lets two slots run
	// discovery against the same tracker snapshot at once risks both
	// selecting the same issue. st.batonSlot is the current holder, or
	// noBaton when the token is free; baton itself is the capacity-1
	// channel the holder sends into when it passes. nil means there is no
	// baton to wait on at all — a single-slot pool has no sibling to
	// stagger against (see newPool).
	baton chan struct{}

	mu  sync.Mutex
	st  state
	seq uint64 // publish sequence counter; see mutate
}

// state is all of the pool's mutable state, in one value: every field here
// is read and written only under p.mu, and only ever changed by mutate.
type state struct {
	halted    bool
	reason    Halt
	slots     []slotState
	awakeShut bool // true once awake_close has fired, until the matching awake_open
	b         breaker
	kinds     map[Kind]kindBackoff
	batonSlot int
}

// slotState is one slot's own state: where it is in its iteration, and
// what its child has in flight while it is running. flight is meaningful
// only while phase == PhaseRunning: finishChild zeroes it on the way out
// of that phase, and snapshotLocked reads it for a running slot only, so a
// flight a bare setPhase left behind is never reachable as stale occupancy
// data.
type slotState struct {
	phase  Phase
	flight slotFlight
}

// mutate is the pool's one path to changing anything in state: every
// change — a halt, an occupancy flip, a baton pass, a backoff record — goes
// through here, and nowhere else takes p.mu to write. It holds the lock
// while f applies the change and while the events f returns are emitted:
// Emitter has its own mutex and never reaches back into the pool, so
// emitting under p.mu is safe, and it is exactly what orders this mutate's
// events ahead of any later mutate's without a special case — no other
// mutate's f can run, and so no other mutate's events can reach the
// stream, until this one releases the lock. The snapshot's sequence number
// is allocated in that same hold, right after, which is what lets
// StatusWriter.Publish drop a stale snapshot on disk instead of needing the
// publish calls themselves to arrive in order.
//
// f must touch nothing but the *state it is handed: a p.-receiver method
// that itself takes p.mu would deadlock here.
//
// Two costs come with that, both accepted as the price of ordering by
// construction: the event-stream writer's I/O runs inside this same lock
// hold, so a stalled writer stalls every state change, halt included; and
// every mutate publishes, so status-file write volume now tracks state
// changes rather than the nine hand-placed call sites it replaces.
func (p *pool) mutate(f func(s *state) []Event) {
	p.mu.Lock()
	evs := f(&p.st)
	for _, ev := range evs {
		p.em.Emit(ev)
	}
	p.seq++
	seq := p.seq
	snap := p.snapshotLocked()
	p.mu.Unlock()
	p.publish(seq, snap)
}

// leadSlot is the pool's designated initial baton holder: a fixed slot
// number rather than whichever slot happens to reach awaitBaton first, so
// a cold start's first discovering slot never depends on scheduler luck.
// No longer a "leader" with any lasting powers — once its first round ends,
// the baton is just a token that circulates to whichever slot is holding it.
const leadSlot = 0

// noBaton is batonSlot's value while the discovery baton is free — held by
// no slot, in flight on p.baton's channel (or, before the pool's first
// round, not yet claimed by anyone but the pre-assigned leadSlot).
const noBaton = -1

// Reasons stamped on baton_hold/baton_pass — operator-facing prose in the
// same documented-string convention as ShutdownDrain (events.go). Every
// way a slot's discovery round can end without a claim has its own pass
// reason below, so an operator reading the stream sees why the baton moved
// without cross-referencing code.
const (
	// batonPassClaimed fires when the holder's child announces a Box while
	// still running: discovery is over, so the next waiting slot starts at
	// once rather than waiting out the whole of the holder's Box run.
	batonPassClaimed = "the holder's child announced a Box: discovery is over, passing the baton to the next waiting slot"
	// batonPassChildEnded fires when the holder's child returns without
	// ever announcing a Box (queue empty, none dispatchable, an
	// unrecognised exit, or a RunChild seam error): the holder's round is
	// over either way, so the baton passes regardless of which of those it
	// was.
	batonPassChildEnded = "the holder's child ended without announcing a Box: passing the baton to the next waiting slot"
	// batonPassFailed fires on an unclassified failure that reaches
	// backoffOrHalt before the holder ever started a child — a
	// ResolveRevision error or a self-build evaluation failure: the holder
	// is about to back off alone, and holding the pool through that
	// backoff would stall every sibling's own discovery on a problem
	// backoffOrHalt already handles per-slot.
	batonPassFailed = "the holder's round failed before it could start a child: passing the baton rather than holding the pool through its backoff"
	// batonPassWindowClosed fires when the Awake window shuts between the
	// holder's ResolveRevision fetch and starting its child: the holder is
	// about to loop back around into awaitWindow, and holding the pool
	// through that whole shut span would stall every sibling's own
	// discovery for no reason.
	batonPassWindowClosed = "the Awake window closed before the holder could start a child: passing the baton rather than holding the pool through the shut span"
	// batonPassIdle fires when pickKind finds every configured kind backed
	// off for the holder: the holder is about to idleSleep, and a slot
	// must never sleep out an idle wait holding the baton.
	batonPassIdle = "no kind is runnable for the holder: passing the baton rather than holding the pool through its idle wait"
	// batonPassStopped fires when the holder returns before any of the
	// above resolved (a cancelled ctx, a halt, or a self-build mismatch):
	// a holder that never gets to run discovery must still not strand the
	// next waiting slot on a baton nothing else will now ever pass.
	batonPassStopped = "the holder stopped before its discovery round resolved: passing the baton so no sibling waits on a slot that has already exited"

	// batonHoldReason is stamped on every baton_hold event, matching every
	// other wait event in this stream (awake_close, backoff, idle's own
	// wait): an operator reading a held start must see why without
	// cross-referencing code.
	batonHoldReason = "waiting for the discovery baton: another slot's child is still discovering"
)

// slotFlight is what one occupied slot currently has in flight.
type slotFlight struct {
	kind     Kind
	revision string
	issues   []string
}

// newPool derives ctx into a context pool.cancel can stop independently of
// the caller, and returns both the pool and that derived context — every
// slot runs against the derived one, never the caller's directly, so
// RunChild is already contractually drain-safe under a cancelled ctx (see
// loop.go's own doc), and hostRunner.RunChild uses exec.Command rather than
// CommandContext, so cancelling it can never kill a running child. r, clk,
// the breaker, and the per-kind backoffs are all pool-wide policy, not
// slot-tracking state, but they live here so backoffOrHalt and runSlot stop
// threading them as parameters.
func newPool(ctx context.Context, cfg Config, r Runner, em *Emitter, clk Clock) (*pool, context.Context) {
	pctx, cancel := context.WithCancel(ctx)
	kinds := make(map[Kind]kindBackoff, len(cfg.Kinds))
	for _, k := range cfg.Kinds {
		kinds[k] = newKindBackoff(cfg.IdleFloor, cfg.IdleCap)
	}
	slots := make([]slotState, cfg.Slots)
	for i := range slots {
		slots[i].phase = PhaseIdle
	}
	p := &pool{
		cfg:    cfg,
		r:      r,
		em:     em,
		clk:    clk,
		cancel: cancel,
		st: state{
			slots:     slots,
			b:         newBreaker(cfg.BreakerThreshold, cfg.BreakerWindow),
			kinds:     kinds,
			batonSlot: leadSlot,
		},
	}
	if cfg.Slots > 1 {
		// A single slot has no sibling to race, so it must take no wait
		// and the pool must emit no baton event at all (see baton's own
		// doc). The token channel starts empty: batonSlot is
		// pre-assigned to leadSlot above, so the first holder never
		// receives from it, only ever sends when it passes.
		p.baton = make(chan struct{}, 1)
	}
	return p, pctx
}

// slotOrder returns the kinds slot tries, most preferred first: slots below
// the reservation prefer research, the rest prefer every other kind ahead of
// it. The order is derived from kinds — only research's position moves, so a
// third kind added later keeps its configured place instead of silently
// inheriting one half of a hardcoded pair. Static, decided once from cfg
// rather than a live count of who is running what: the floor is exact without
// lock-step counting, and two slots choosing concurrently can never both claim
// the same reserved slot (each computes its own answer independently, off its
// own slot number, not off shared mutable state).
func slotOrder(kinds []Kind, reservation, slot int) []Kind {
	if len(kinds) < 2 {
		return kinds
	}
	research := make([]Kind, 0, 1)
	rest := make([]Kind, 0, len(kinds))
	for _, k := range kinds {
		if k == KindResearch {
			research = append(research, k)
			continue
		}
		rest = append(rest, k)
	}
	order := make([]Kind, 0, len(kinds))
	if slot < reservation {
		return append(append(order, research...), rest...)
	}
	return append(append(order, rest...), research...)
}

// pickKind returns the Dispatch kind slot should fill itself with now: the
// first kind in its preference order that is not currently backed off. ok is
// false when every configured kind has backed off into an empty result — the
// daemon is genuinely idle, and the caller sleeps instead of dispatching.
// A pure read, not a mutate: nothing here changes state.
func (p *pool) pickKind(slot int) (Kind, bool) {
	// Sample now before taking p.mu: no Clock implementation takes p.mu
	// itself, but calling one under the lock anyway would hold it for
	// however long that call takes, for no reason — the lock only needs
	// to guard the kinds map read below.
	now := p.clk.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, kind := range slotOrder(p.cfg.Kinds, p.cfg.ResearchReservation, slot) {
		if p.st.kinds[kind].runnable(now) {
			return kind, true
		}
	}
	return "", false
}

// resetKind clears kind's backoff gate: runSlot calls this on a Continue
// outcome (exit 0 or 4), so a check that answered something other than
// "nothing to do" ends whatever no-work streak this kind's backoff was
// tracking.
func (p *pool) resetKind(kind Kind) {
	p.mutate(func(s *state) []Event {
		s.kinds[kind] = s.kinds[kind].reset()
		return nil
	})
}

// markNoWork records one no-work result for kind and returns the wait it
// gates for — see kindBackoff.markNoWork for what jammed means. Kept for
// the package's own tests: noteWaitResult is the one production path to a
// no-work fold, and no production caller reaches for this one.
func (p *pool) markNoWork(kind Kind, now time.Time, jammed bool) time.Duration {
	var wait time.Duration
	p.mutate(func(s *state) []Event {
		s.kinds[kind], wait = s.kinds[kind].markNoWork(now, jammed)
		return nil
	})
	return wait
}

// noteWaitResult is runSlot's Wait-case fold: it records kind's no-work
// result and evaluates the jam predicate in the same mutate, so the two can
// never be read from two different instants the way a separate
// siblingsEngaged call followed by a separate markNoWork call used to
// allow. "none-dispatchable" carries a second axis exit 2 doesn't: whether
// a sibling is doing anything at all. With a sibling genuinely running (or
// fetching, or resolving its own self-build, or sleeping out a failure
// backoff), the issues this slot found "none dispatchable" were claimed or
// overlap-deferred against that very sibling — routine, reported like any
// other idle wait. With every sibling idle or merely parked on the shut
// Awake window too, nothing is running and nothing can start: a jam an
// operator may need to clear. The jam alarm's predicate below is
// deliberately not the same as jammed itself: jammed records the queue
// condition this check saw (see kindBackoff.markNoWork), while the alarm
// only fires when no sibling is doing anything a fetch, a self-build check,
// a child, or a backoff sleep counts as (issue #3571).
func (p *pool) noteWaitResult(slot int, kind Kind, revision string, noneDispatchable bool) {
	now := p.clk.Now()
	p.mutate(func(s *state) []Event {
		var wait time.Duration
		s.kinds[kind], wait = s.kinds[kind].markNoWork(now, noneDispatchable)
		if noneDispatchable && !s.siblingsEngaged(slot) {
			return []Event{{Event: "jam", Kind: kind, Revision: revision, Slot: intPtr(slot), Wait: wait.String(), Reason: "no work is dispatchable and no sibling slot is running"}}
		}
		return []Event{{Event: "idle", Kind: kind, Wait: wait.String(), Slot: intPtr(slot)}}
	})
}

// startChild marks slot running for kind at revision and returns the
// child_start event, together in one mutate: the phase flip is therefore
// always visible to any snapshot that publishes alongside this event, which
// a hand-placed write after a separately-emitted child_start could not
// otherwise guarantee.
func (p *pool) startChild(slot int, kind Kind, revision string) {
	p.mutate(func(s *state) []Event {
		s.slots[slot] = slotState{phase: PhaseRunning, flight: slotFlight{kind: kind, revision: revision}}
		return []Event{{Event: "child_start", Kind: kind, Revision: revision, Slot: intPtr(slot)}}
	})
}

// finishChild moves slot back to idle and zeroes its flight. A slot calls this
// immediately after RunChild returns, before the result is interpreted: it
// must clear its own running phase before it ever asks siblingsEngaged, or
// it would count itself as an engaged sibling and "none dispatchable, pool
// otherwise idle" could never be true for a lone slot.
func (p *pool) finishChild(slot int) {
	p.mutate(func(s *state) []Event {
		s.slots[slot] = slotState{phase: PhaseIdle}
		return nil
	})
}

// noteIssue appends issue to slot's in-flight issue list, for a snapshot to
// report while the child is still running. A no-op if slot is not currently
// running: the child announced after the slot cleared, which can only be a
// race (RunChild already returned), not a state worth publishing. Dedupe is
// already done by the runner, so this never re-dedupes.
func (p *pool) noteIssue(slot int, issue string) {
	p.mutate(func(s *state) []Event {
		if s.slots[slot].phase != PhaseRunning {
			return nil
		}
		s.slots[slot].flight.issues = append(s.slots[slot].flight.issues, issue)
		return nil
	})
}

// working reports whether any slot's phase is running — snapshotLocked's
// StateWorking predicate, one bool derived from the same per-slot phase
// siblingsEngaged reads, rather than a second occupancy notion tracked
// alongside it.
func (s *state) working() bool {
	for _, ss := range s.slots {
		if ss.phase == PhaseRunning {
			return true
		}
	}
	return false
}

// siblingsEngaged reports whether any slot other than slot is doing
// anything but waiting for its own turn: any phase but idle or
// awaiting-window counts, since a sibling fetching, resolving its
// self-build, running a child, or sleeping out a failure backoff can still
// be the reason this slot's own queue read nothing dispatchable (issue
// #3571). idle and awaiting-window are the only two phases a slot can sit
// in indefinitely while genuinely doing nothing, which is exactly the
// "every sibling parked" case the jam alarm exists to catch.
func (s *state) siblingsEngaged(slot int) bool {
	for sl, ss := range s.slots {
		if sl == slot {
			continue
		}
		if ss.phase != PhaseIdle && ss.phase != PhaseAwaitingWindow {
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
		wait := p.cfg.Awake.Until(p.clk.Now())
		if wait <= 0 {
			p.noteAwakeOpen(slot)
			return
		}
		p.noteAwakeClose(slot, wait)
		p.clk.Sleep(ctx, wait)
		if p.stopped() || ctx.Err() != nil {
			return
		}
	}
}

// noteAwakeClose records the pool-wide transition into a shut window and,
// only for the caller that actually observes the transition, returns the
// awake_close event: with several slots parking on the same close, only the
// first to flip awakeShut reports it, so the stream carries exactly one
// awake_close per closing however many slots are waiting on it. The phase
// write is unconditional, though, unlike the event: every parking slot is
// about to sleep out the shut window, whether or not it was the one that
// noticed the edge, so each must record its own phase regardless.
func (p *pool) noteAwakeClose(slot int, wait time.Duration) {
	p.mutate(func(s *state) []Event {
		first := !s.awakeShut
		s.awakeShut = true
		s.slots[slot].phase = PhaseAwaitingWindow
		if !first {
			return nil
		}
		return []Event{{Event: "awake_close", Slot: intPtr(slot), Wait: wait.String(), Reason: "outside the Awake window"}}
	})
}

// noteAwakeOpen is noteAwakeClose's counterpart: it returns awake_open only
// when a close was already reported, so a daemon that starts (or every slot
// merely finds the window already open) never emits an open with no
// matching close. It is also the top-of-iteration phase reset: awaitWindow
// returns through here on every open-window pass, so every path that loops
// back to the top of runSlot lands here and clears whatever phase (backing
// off, awaiting window) it left behind, with no publish of its own beyond
// the one this mutate already makes.
func (p *pool) noteAwakeOpen(slot int) {
	p.mutate(func(s *state) []Event {
		was := s.awakeShut
		s.awakeShut = false
		s.slots[slot].phase = PhaseIdle
		if !was {
			return nil
		}
		return []Event{{Event: "awake_open", Slot: intPtr(slot), Reason: "the Awake window reopened"}}
	})
}

// awaitBaton parks slot until it holds the discovery baton. Returns at once
// when there is no baton (a single-slot pool) or when slot already holds
// it — the pre-assigned initial holder's very first call, or any slot that
// re-enters here while still holding from a prior pass (not possible today,
// since every acquisition site is paired with a pass before the next
// acquisition, but checked directly rather than assumed).
//
// The batonSlot check is tried first: this is called on every iteration of
// every slot, and once a slot holds the baton a re-check costs one lock and
// nothing more. Only a slot that does not hold it falls through to the
// non-blocking receive (the token may already be waiting on the channel),
// and only after that misses does it emit baton_hold and actually block, on
// a select that also watches ctx so a pool that is cancelled while a slot
// is held never deadlocks it.
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
	p.emit(Event{Event: "baton_hold", Slot: intPtr(slot), Reason: batonHoldReason})
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

// passBaton hands the baton on. A no-op unless slot actually holds it,
// which makes it idempotent per hold and safe to call unconditionally from
// every site a round can end — several release sites in runSlot legitimately
// overlap (see the batonPass* reasons' own doc), and only the first to
// actually find itself the holder is the one whose reason reaches the
// stream.
//
// The holder check is a fast-path read, not itself a mutate: only the
// holder ever clears its own hold, and only that slot ever sets it, so a
// slot that just observed itself as the holder stays the holder until it
// clears the flag itself a few lines below — nothing else can race that
// read stale. Once confirmed, one mutate clears batonSlot and returns the
// baton_pass event; the channel send happens after, not before: sending
// first could let an awaitBaton call already blocked on <-p.baton wake and
// publish its own next event before baton_pass itself reaches the stream,
// which would make the durable record say a sibling acted before the event
// that explains why it was allowed to. The send itself cannot block: the
// holder is the only slot that ever sends, and the batonSlot flip above
// guarantees it sends at most once per hold, so the capacity-1 channel is
// always empty at this point.
func (p *pool) passBaton(slot int, reason string) {
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
		return []Event{{Event: "baton_pass", Slot: intPtr(slot), Reason: reason}}
	})
	p.baton <- struct{}{}
}

// stopped reports whether the pool has already recorded a halt reason —
// deliberately not a raw ctx.Err() check; see stopOnCancel (loop.go) for why.
func (p *pool) stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.halted
}

// halt records h as the pool's halt reason if none is recorded yet, emits
// the single pool-level halt event, and cancels the derived context.
// Idempotent: once the pool has halted, later calls (racing siblings, or a
// slot that merely rediscovers the same cancellation) are no-ops — the
// first h wins and no second halt event is ever emitted.
func (p *pool) halt(h Halt) {
	first := false
	p.mutate(func(s *state) []Event {
		if s.halted {
			return nil
		}
		s.halted = true
		s.reason = h
		first = true
		return []Event{h.Event()}
	})
	if !first {
		return
	}
	p.cancel()
}

// backoffOrHalt is what a slot calls on an unclassified failure (a
// ResolveRevision error, a RunChild seam error, or an unrecognised exit
// code): it records the failure in the pool-wide breaker and either trips
// the pool (enough failures landed across the pool within the window to
// look systemic — no per-slot retry clears that) or backs this one slot
// off and lets it retry alone. Returns true if the pool halted (the caller
// must stop), false if the caller should sleep out the backoff and
// continue its own loop. When the caller holds the discovery baton, it
// also passes it before the backoff sleep below (see batonPassFailed) — a
// holder about to sleep alone through FailureBackoff must not strand its
// sibling's discovery on that sleep.
func (p *pool) backoffOrHalt(ctx context.Context, slot int, kind Kind, revision, reason string) bool {
	// Only an unclassified pre-child failure (a ResolveRevision or
	// self-build evaluation error) can still be holding the baton here:
	// loop.go releases it unconditionally right after RunChild returns,
	// before the exit is interpreted, so this call is already a no-op on
	// every post-child failure path. passBaton is a no-op unless slot
	// actually holds it, so it's safe to call unconditionally here too,
	// rather than threading a "did I acquire it" flag through every
	// caller.
	p.passBaton(slot, batonPassFailed)

	// Sample now before taking p.mu, same reasoning as pickKind.
	now := p.clk.Now()
	var count int
	var crossed bool
	p.mutate(func(s *state) []Event {
		s.b, count, crossed = s.b.recordAndCheck(now)
		if !crossed {
			return nil
		}
		return []Event{{Event: "breaker_trip", Kind: kind, Slot: intPtr(slot), Failures: &count, Wait: p.cfg.BreakerWindow.String()}}
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

	p.mutate(func(s *state) []Event {
		s.slots[slot].phase = PhaseBackingOff
		return []Event{{Event: "backoff", Kind: kind, Revision: revision, Slot: intPtr(slot), Wait: p.cfg.FailureBackoff.String(), Reason: reason}}
	})
	p.clk.Sleep(ctx, p.cfg.FailureBackoff)
	// The sleep is the whole backing-off span; once it returns this slot is
	// idle again, not merely about to be — the next loop through
	// awaitWindow would reset it anyway on an open window, but a shut one
	// would instead overwrite backing-off with awaiting-window and never
	// idle in between, understating how this iteration actually ended.
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

// selfVerdict is what checkSelfBuild tells a slot to do next.
type selfVerdict int

const (
	// selfOK: the daemon's own build is unchanged at revision (or the check
	// is disabled), so the slot starts this iteration's child.
	selfOK selfVerdict = iota
	// selfRetry: the evaluation itself failed and this slot has already
	// backed off; the slot restarts its iteration from the fetch.
	selfRetry
	// selfStop: the pool halted (a changed build, or a persistent
	// evaluation failure that tripped the breaker) or the caller's ctx was
	// cancelled; the slot returns.
	selfStop
)

// checkSelfBuild compares the daemon's own build against what the daemon
// attribute evaluates to at revision, the freshly fetched tip, and reports
// what the slot should do next. cfg.SelfProgram == "" skips the check
// entirely — SelfPath is never called.
//
// A SelfPath error is treated as an unclassified per-slot failure, the same
// class as a ResolveRevision or RunChild seam error (backoffOrHalt):
// silently ignoring it would disable this safety property for as long as
// the evaluation stays broken, so instead this one slot backs off and
// retries alone, and only a persistent failure reaches the breaker.
//
// A mismatch never re-execs the daemon: it only records a halt reason and
// cancels the pool's context — the same "stop starting new work, wait out
// whatever is running" halt every other reason already uses (see halt) —
// so a freshly merged but broken daemon cannot auto-load with nobody awake.
func (p *pool) checkSelfBuild(ctx context.Context, slot int, kind Kind, revision string) selfVerdict {
	if p.cfg.SelfProgram == "" {
		return selfOK
	}
	path, err := p.r.SelfPath(ctx, revision)
	if err != nil {
		// A ctx cancelled out from under an in-flight SelfPath (an operator
		// SIGTERM racing this evaluation) is an ordinary stop, not evidence
		// of a broken evaluation — recording it as a breaker failure could
		// trip the breaker on a clean shutdown and turn a 0 exit into a 1.
		if stopOnCancel(ctx, kind, p) {
			return selfStop
		}
		// Rendered through Halt rather than a raw "self-build: " literal so
		// the documented self-build grammar has exactly one renderer
		// (Halt.String()) even though this reason only ever reaches a halt
		// indirectly, via the breaker — backoffOrHalt's own reason param
		// stays a plain string because its other two callers pass
		// non-halt-grammar strings.
		if p.backoffOrHalt(ctx, slot, kind, revision, Halt{Class: HaltSelfBuild, Detail: err.Error()}.String()) {
			return selfStop
		}
		return selfRetry
	}
	if path == p.cfg.SelfProgram {
		return selfOK
	}
	detail := fmt.Sprintf("daemon build at %s is %s, running %s", revision, path, p.cfg.SelfProgram)
	p.halt(Halt{Class: HaltSelfChanged, Detail: detail, Kind: kind, Revision: revision})
	return selfStop
}

// idleSleep is what a slot calls when pickKind finds every configured kind
// backed off: a genuine drought, not just a kind this slot doesn't prefer
// right now. It sleeps until the nearest gated kind's deadline (the pool as
// a whole can move the instant any one kind's gate lifts, even though this
// slot only picks up work once it wakes and re-runs pickKind), polling for a
// moved tip along the way only if a jammed kind is among those gated — a
// merge can unblock a jammed queue, but it cannot create new work in a kind
// that is merely queue-empty, so polling there would only spend a query for
// nothing.
//
// lastRevision is the revision this slot's last child ran at (empty if this
// slot has never yet resolved one); a poll result that differs from it is
// the "tip moved" signal.
func (p *pool) idleSleep(ctx context.Context, slot int, lastRevision string) {
	// Sample now before taking p.mu, same reasoning as pickKind. The scan
	// itself runs under one lock hold (idleWait) so it never observes two
	// kinds at different instants; the actual sleep happens after the lock
	// is released — p.clk.Sleep must never run under p.mu.
	now := p.clk.Now()
	wait, jammedGate, ok := p.idleWait(now)
	if !ok {
		return
	}

	if jammedGate && lastRevision != "" {
		p.pollSlices(ctx, slot, wait, lastRevision)
		return
	}
	p.clk.Sleep(ctx, wait)
}

// idleWait scans every kind's backoff under one p.mu hold and reports the
// wait until the earliest kind's deadline and whether any gated kind is
// currently jammed. ok is false when there is nothing to sleep for at all:
// either a kind turned out runnable already at now (a sibling's reset()
// raced in between pickKind's failed pass and this call, or a deadline has
// already elapsed — the caller loops back around to pickKind at once), or
// p.st.kinds is empty (Loop's own validation, len(cfg.Kinds) == 0, rejects
// that before a pool is ever built, but newPool itself doesn't enforce it,
// so this also guards a caller — a test, say — that builds a pool
// directly). A pure read, not a mutate: nothing here changes state.
func (p *pool) idleWait(now time.Time) (wait time.Duration, jammedGate bool, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var earliest time.Time
	for _, k := range p.st.kinds {
		at, gated := k.readyAt(now)
		if !gated {
			return 0, false, false
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
		if k.jammedNow() {
			jammedGate = true
		}
	}
	if earliest.IsZero() {
		return 0, false, false
	}
	return earliest.Sub(now), jammedGate, true
}

// pollSlices sleeps wait in IdleFloor-sized slices, polling ResolveRevision
// between slices so a merge that unblocks a jammed queue is noticed instead
// of riding out the rest of a long backoff. Only idleSleep's jammed case
// reaches here; a queue-empty gate stays the plain, single p.clk.Sleep in
// idleSleep itself.
//
// revision is idleSleep's lastRevision; a poll result that differs from it
// is the "tip moved" signal. On that signal, pollSlices emits tip_moved,
// resets every currently-jammed kind's backoff (the observed change ends
// each of their no-work streaks same as real work would — a queue-empty
// kind's streak is untouched, since a moved tip is not evidence an empty
// queue refilled), and returns immediately so the slot starts its next
// iteration at once rather than sleeping out the rest of the wait.
func (p *pool) pollSlices(ctx context.Context, slot int, wait time.Duration, revision string) {
	remaining := wait
	for remaining > 0 {
		slice := p.cfg.IdleFloor
		if slice > remaining {
			slice = remaining
		}
		p.clk.Sleep(ctx, slice)
		remaining -= slice

		if p.stopped() || ctx.Err() != nil {
			// A sibling halted the pool, or the caller's ctx was cancelled,
			// while this slot slept. Stop polling and return: the slot's own
			// top-of-loop stopOnCancel does the halt bookkeeping next.
			return
		}
		if remaining <= 0 {
			return
		}

		newRevision, err := p.r.ResolveRevision(ctx)
		if err != nil {
			// This poll is opportunistic, not the loop's own per-iteration
			// fetch: a failure here just means no change was observed, so
			// keep sleeping out the remaining slices rather than treating it
			// as a failure. The next iteration's top-of-loop ResolveRevision
			// is the one that properly reports and backs off on a broken
			// fetch.
			continue
		}
		if newRevision != revision {
			// No single Kind names this: several kinds can be jammed at
			// once, and the tip that moved is evidence for all of them, not
			// whichever this slot happened to be running. Kinds names the
			// set actually reset below; iterating cfg.Kinds rather than the
			// kinds map keeps that set's order deterministic. The scan,
			// resets, and the tip_moved event itself all happen in one
			// mutate, so the set an operator reads on the event is exactly
			// the set that was reset, not a snapshot taken a moment either
			// side of it.
			p.mutate(func(s *state) []Event {
				var reset []Kind
				for _, kind := range p.cfg.Kinds {
					if k := s.kinds[kind]; k.jammedNow() {
						s.kinds[kind] = k.reset()
						reset = append(reset, kind)
					}
				}
				return []Event{{Event: "tip_moved", Slot: intPtr(slot), Revision: newRevision, Reason: "a merge can unblock a jammed queue", Kinds: reset}}
			})
			return
		}
	}
}

// snapshot takes p.mu and builds a Status from pool state alone; see
// snapshotLocked for the build itself. Kept for the package's own tests:
// mutate is the one production path to a fresh snapshot, and no production
// caller reaches for this one.
func (p *pool) snapshot() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshotLocked()
}

// snapshotLocked builds a Status from pool state alone; the caller must
// already hold p.mu. Building the whole Status under one lock hold means a
// reader never sees state from two different instants stitched together,
// and lets it call p.clk.Now() under that same hold — safe, since no Clock
// implementation takes p.mu — so readyAt can tell a kind whose deadline has
// already elapsed from one still gated, the same now-aware check idleSleep
// makes outside the lock.
func (p *pool) snapshotLocked() Status {
	slots := make([]SlotStatus, len(p.st.slots))
	for i, ss := range p.st.slots {
		slots[i] = SlotStatus{Slot: i, Phase: ss.phase, Busy: ss.phase == PhaseRunning}
		if ss.phase != PhaseRunning {
			continue
		}
		slots[i].Kind = ss.flight.kind
		slots[i].Revision = ss.flight.revision
		if len(ss.flight.issues) > 0 {
			// A snapshot handed to a writer must not alias state this slot
			// keeps appending to.
			issues := make([]string, len(ss.flight.issues))
			copy(issues, ss.flight.issues)
			slots[i].Issues = issues
		}
	}

	now := p.clk.Now()
	// A shut Awake window gates every kind whatever its own backoff says:
	// no slot starts a child until awaitWindow returns. Until is 0 both
	// while the window is open and for the nil always-awake window, so
	// this stays the zero Time in the ordinary case.
	var windowOpensAt time.Time
	if d := p.cfg.Awake.Until(now); d > 0 {
		windowOpensAt = now.Add(d)
	}
	checks := make([]KindCheck, 0, len(p.cfg.Kinds))
	allGated := true
	anyJammed := false
	for _, k := range p.cfg.Kinds {
		kb := p.st.kinds[k]
		at, gated := kb.readyAt(now)
		kc := KindCheck{Kind: k}
		if gated {
			if kb.jammedNow() {
				anyJammed = true
				kc.Jammed = true
			}
		} else {
			at = time.Time{} // an ungated at may be a stale deadline; see readyAt
			allGated = false
		}
		if windowOpensAt.After(at) {
			at = windowOpensAt
		}
		if !at.IsZero() {
			kc.NextCheck = at.UTC().Format(time.RFC3339)
		}
		checks = append(checks, kc)
	}

	// Precedence, most urgent first: halted outranks everything, since the
	// pool is already on its way out regardless of what else is true.
	// Working outranks asleep, since a child started before the Awake
	// window closed is still genuinely running even though the window has
	// since shut — "working" is the honest state, not "asleep". Jammed vs
	// waiting only applies once every kind is gated, and only jammedNow (a
	// none-dispatchable result) tells them apart; checking is the default
	// when nothing is gated and nothing is running.
	//
	// Asleep is keyed off windowOpensAt (the same now-aware Awake.Until
	// check nextCheck already floors on above), not p.st.awakeShut:
	// awakeShut is edge-triggered by whichever slot's awaitWindow observes
	// the close, so a pool built outside its window and snapshotted before
	// any slot parks would still read awakeShut false and disagree with a
	// nextCheck already naming the reopening. p.st.awakeShut therefore has
	// no reader left outside the note*Awake* pair itself, which still needs
	// it to edge-trigger awake_close/awake_open.
	computedState := StateChecking
	reason := ""
	switch {
	case p.st.halted:
		computedState = StateHalted
		reason = p.st.reason.String()
	case p.st.working():
		computedState = StateWorking
	case !windowOpensAt.IsZero():
		computedState = StateAsleep
	case allGated && anyJammed:
		computedState = StateJammed
	case allGated:
		computedState = StateWaiting
	}

	// Copied, not aliased, matching the issues copy above: nothing
	// mutates cfg.Kinds today, but a returned Status must never assume a
	// future writer keeps that true.
	kinds := make([]Kind, len(p.cfg.Kinds))
	copy(kinds, p.cfg.Kinds)

	return Status{
		Kinds:  kinds,
		State:  computedState,
		Reason: reason,
		Slots:  slots,
		Checks: checks,
	}
}

// publish writes s (already sequence-numbered by mutate) to Config.Status,
// if configured. Called from mutate alone — there is no other publish call
// site. A nil Status is the deliberate opt-out — a daemon that never
// located a git dir to publish into, or an operator who chose not to
// publish — not a swallowed error, so publish is silently a no-op rather
// than reporting anything. A write failure is advisory-only and must never
// fail the daemon: it is reported to emitErrW and otherwise ignored.
func (p *pool) publish(seq uint64, s Status) {
	if p.cfg.Status == nil {
		return
	}
	if err := p.cfg.Status.Publish(seq, s); err != nil {
		fmt.Fprintf(emitErrW, "daemon: status file write failed: %v\n", err)
	}
}

// publishInitial reports the pool's startup state before any slot has made
// its own first change, so a freshly started daemon's status file says
// something at once instead of only on its first state change. It goes
// through mutate, changing nothing, rather than calling snapshot/publish
// directly: the snapshot and its sequence number must come from the one
// operation that allocates both, or this initial publish could race a
// slot's first real mutate and overwrite that slot's fresher state with
// this stale one.
func (p *pool) publishInitial() {
	p.mutate(func(*state) []Event { return nil })
}

// emit is mutate's trivial case, for the events that ride no state change
// of their own: box, child_finish and baton_hold.
func (p *pool) emit(ev Event) {
	p.mutate(func(*state) []Event { return []Event{ev} })
}

// haltReason returns the pool's recorded Halt. Only meaningful after
// every slot goroutine has returned (Loop calls it after its WaitGroup
// drains), so no lock is strictly required at that point, but the pool's
// own mutex is reused anyway rather than trusting a second, easy-to-miss
// synchronisation argument.
func (p *pool) haltReason() Halt {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.reason
}
