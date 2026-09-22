package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/report"
)

// pool is the shared state behind Loop's slot goroutines. It owns the one
// thing every slot must agree on: whether the daemon has decided to stop,
// and why. First halt wins — Loop emits exactly one "halt" event per call,
// whichever slot gets there first — and every slot shares one derived
// context that pool cancels the moment it halts, so a sibling asleep in its
// idle wait or blocked in ResolveTip stops promptly instead of riding
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
	// ResolveTip error or a self-build evaluation failure: the holder
	// is about to back off alone, and holding the pool through that
	// backoff would stall every sibling's own discovery on a problem
	// backoffOrHalt already handles per-slot. The baton is acquired only
	// after the resolve completes (see runSlot, loop.go), so a
	// non-holder never reaches this path with the baton to pass — it is
	// only live for the pre-assigned initial holder's (leadSlot) very
	// first round, the one round a slot gets here already holding it.
	batonPassFailed = "the holder's round failed before it could start a child: passing the baton rather than holding the pool through its backoff"
	// batonPassWindowClosed fires when the Awake window shuts between the
	// holder's ResolveTip fetch and starting its child: the holder is
	// about to loop back around into awaitWindow, and holding the pool
	// through that whole shut span would stall every sibling's own
	// discovery for no reason.
	batonPassWindowClosed = "the Awake window closed before the holder could start a child: passing the baton rather than holding the pool through the shut span"
	// batonPassIdle fires when pickKind finds every configured kind backed
	// off for the holder: the holder is about to idleSleep, and a slot
	// must never sleep out an idle wait holding the baton. pickKind runs
	// before the baton is acquired, so a non-holder never reaches this
	// path holding it — only the pre-assigned initial holder (leadSlot)
	// can, on its very first round.
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

// slotFlight is what one occupied slot currently has in flight. issues is
// the deduped set behind SlotStatus.Issues (origin/main's "seen" semantics,
// issue #3627's review finding) in first-seen order; last is tracked
// separately since a repeat-issue box (e.g. a fix-pass after the initial
// box) must still move flightIssue's answer even though it adds nothing to
// issues.
type slotFlight struct {
	kind     Kind
	revision string
	issues   []string
	last     string
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
// otherwise guarantee. child_start carries no issue: the daemon cannot know
// which issue a freshly started child will work until it reports a "box"
// record, and waiting to emit child_start until then would either hide a
// started child from the stream for its whole queue scan, or emit nothing
// at all for a child that never claims — the "box" event is where the
// slot↔issue binding first appears (see noteBox).
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

// noteBox records issue as slot's most recently boxed issue and, the first
// time this child run boxes it, appends it to slot's in-flight issue list
// for a snapshot to report while the child is still running — deduped, to
// match origin/main's "seen" semantics, since a fix-pass box for an issue
// already open in this slot is the same claim continuing, not a second one
// (issue #3627's review finding). It emits the box event unconditionally,
// dedup or not: a fix-pass box is a real thing that happened, and only the
// status-file issue list collapses repeats, not the event stream. Both in
// one mutate so the event and the status snapshot it rides alongside always
// agree. A no-op — no state change, no event — if slot is not currently
// running: the child reported after the slot cleared, which can only be a
// race (RunChild already returned), not a state worth publishing.
func (p *pool) noteBox(slot int, kind Kind, revision, issue, phase string) {
	p.mutate(func(s *state) []Event {
		if s.slots[slot].phase != PhaseRunning {
			return nil
		}
		flight := &s.slots[slot].flight
		flight.last = issue
		seen := false
		for _, existing := range flight.issues {
			if existing == issue {
				seen = true
				break
			}
		}
		if !seen {
			flight.issues = append(flight.issues, issue)
		}
		return []Event{{Event: report.EventBox, Kind: kind, Revision: revision, Issue: issue, Phase: phase, Slot: intPtr(slot)}}
	})
}

// noteSettled emits the settled event through the same mutate-ordered path
// as noteBox, so a settled record is never reordered against the status
// snapshot or another pool event. Unlike noteBox it touches no state: a
// settled record names a terminal outcome, not a fact the in-flight
// SlotStatus.Issues list needs to grow by.
// It carries no `phase != PhaseRunning` guard either, though noteBox's race
// applies here too: a settled record names the issue's terminal outcome,
// true whether or not the slot still runs, and dropping it would lose the
// one event that answers "what happened to #123".
func (p *pool) noteSettled(slot int, kind Kind, revision, issue, settledState, note string) {
	p.mutate(func(*state) []Event {
		return []Event{{Event: report.EventSettled, Kind: kind, Revision: revision, Issue: issue, State: settledState, Note: note, Slot: intPtr(slot)}}
	})
}

// flightIssue returns the issue slot's child most recently boxed (the last
// value noteBox recorded, regardless of whether that box was a repeat and
// so never grew the deduped issues list), or "" if it boxed none. Read
// directly under p.mu rather than through mutate, since it changes
// nothing; callers that need it for a child_finish event must call it
// before finishChild zeroes the slot's flight — this is the only place
// that issue is still available at all.
func (p *pool) flightIssue(slot int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.slots[slot].flight.last
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
	// A slot parked here is waiting for its own turn, not doing anything,
	// so it must read as PhaseIdle: siblingsEngaged counts every other
	// phase as engaged, and a slot that parked straight out of its own
	// resolve would otherwise suppress a sibling's real jam for the whole
	// hold. Rides in the same mutate as baton_hold so the phase is always
	// visible to the snapshot that publishes alongside that event.
	p.mutate(func(s *state) []Event {
		s.slots[slot].phase = PhaseIdle
		return []Event{{Event: "baton_hold", Slot: intPtr(slot), Reason: batonHoldReason}}
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
// deliberately not a raw ctx.Err() check; see haltIfStopping for why.
func (p *pool) stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.halted
}

// stopClosed reports whether cfg.Stop has been closed, without blocking. A
// nil Stop (a Config that sets no latch, e.g. an embedder that hard-cancels
// ctx instead) always reads open: a nil channel never receives in a select,
// so this reads the same as "not closed" rather than blocking.
func (p *pool) stopClosed() bool {
	select {
	case <-p.cfg.Stop:
		return true
	default:
		return false
	}
}

// haltStopRequested records the operator-stop halt a closed cfg.Stop
// demands, from one construction site: Loop's own Stop watcher and
// haltIfStopping below both reach it, so neither the class nor the detail
// can drift between them. "context-cancelled" is docs/reference.md's
// documented halt grammar for this class (halt.go's haltRenderings), not a
// name this slice may change — an operator stop and a caller ctx
// cancellation render identically on purpose.
//
// kind is the halt event's Kind if the caller has one, "" for Loop's own
// watcher, which runs no kind of its own.
func (p *pool) haltStopRequested(kind Kind) {
	p.halt(Halt{Class: HaltOperatorStop, Detail: "stop requested", Kind: kind})
}

// haltIfStopping reports whether the slot should stop starting new work,
// checking three things in order: whether a sibling already recorded a halt
// reason (return quietly — nothing new to emit); whether cfg.Stop has
// closed (an operator stop this call is the first to notice); and whether
// ctx itself is freshly cancelled (the caller's own hard cancel — tests and
// any embedder that skips cfg.Stop and cancels ctx directly instead).
// Checking p.stopped() first is what makes the ctx branch meaningful: p.halt's
// own cancel() also cancels ctx, so a raw ctx.Err() check alone cannot tell
// "I am first to notice real cancellation" from "a sibling already halted
// for some other reason and cancelled me as a side effect".
//
// kind is the halt event's Kind, if the caller has picked one yet: the
// top-of-loop call races cancellation against pickKind itself and so has
// none to give (pass ""), while backoffOrHalt already knows which kind this
// iteration is running.
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
// ResolveTip error, a RunChild seam error, an unrecognised exit code,
// or exit 7 while Stop is open — someone else signalled that child): it
// records the failure in the pool-wide breaker and either trips the pool
// (enough failures landed across the pool within the window to look
// systemic — no per-slot retry clears that) or backs this one slot off and
// lets it retry alone. Returns true if the pool halted (the caller must
// stop), false if the caller should sleep out the backoff and continue its
// own loop. When the caller holds the discovery baton and this call is
// about to back off rather than halt, it also passes the baton before the
// backoff sleep below (see batonPassFailed) — a holder about to sleep
// alone through FailureBackoff must not strand its sibling's discovery on
// that sleep.
//
// This is also the breaker's one carve-out (issue #3595, by construction):
// a failure that lands while Stop has already closed — or while the pool
// has already halted, or the caller's ctx is cancelled — is never counted,
// because it is an ordinary shutdown (an operator SIGTERM racing the
// child/fetch/evaluation) rather than evidence of a systemic fault. The
// check belongs here, once, rather than at every call site: the carve-out
// is about the breaker, and backoffOrHalt is the breaker's one gate. It
// therefore reads the latch after the child exited rather than at the
// moment it exited, the way exit 7 does (loop.go reads it once, at
// Interpret, so a later close cannot race that answer): a close landing in
// that window carves out an unrecognised exit that itself landed while Stop
// was still open. The pool halts as an operator stop under either reading,
// so the wider window changes no operator-visible outcome — only which exit
// the breaker declines to count on the way down.
func (p *pool) backoffOrHalt(ctx context.Context, slot int, kind Kind, revision, reason string) bool {
	// Check stopping before touching the baton: a stop closing here means
	// runSlot's own deferred passBaton(slot, batonPassStopped) (loop.go) is
	// the pass that should be observed, not a batonPassFailed pass from
	// this call — an operator stop racing an in-flight fetch must still
	// read as a stop in the baton_pass event stream.
	if p.haltIfStopping(ctx, kind) {
		return true
	}

	// Only an unclassified pre-child failure (a ResolveTip or
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

// resolveFailure turns a ResolveTip error into the revision and reason
// backoffOrHalt should record. A *SelfEvalError means the fetch itself
// succeeded (tip.Revision is valid) but the self-build evaluation at that
// revision failed; it is rendered through Halt rather than a raw
// "resolve-revision: " literal so the documented self-build grammar keeps
// exactly one renderer (Halt.String()) even though this reason only ever
// reaches a halt indirectly, via the breaker. Any other error means the
// fetch itself failed and carries no revision.
func resolveFailure(tip Tip, err error) (revision, reason string) {
	var se *SelfEvalError
	if errors.As(err, &se) {
		return tip.Revision, Halt{Class: HaltSelfBuild, Detail: se.Err.Error()}.String()
	}
	return "", fmt.Sprintf("resolve-revision: %v", err)
}

// idleSleep is what a slot calls when pickKind finds every configured kind
// backed off: a genuine drought, not just a kind this slot doesn't prefer
// right now. With no jammed kind among those gated, it sleeps the whole wait
// in one call, same as ever: a merge can unblock a jammed queue, but it
// cannot create new work in a kind that is merely queue-empty, so resolving
// early there would only spend a fetch for nothing.
//
// With a jammed kind gated, it instead sleeps only one IdleFloor-sized slice
// (or the whole wait, if that is shorter) and reports back, via wantResolve,
// whether time remained afterward — exactly the condition under which
// runSlot (loop.go) should make one opportunistic resolution before its next
// pickKind: that resolution, not a call made here, is what can observe
// Tip.Moved and report it, so the wait's very first IdleFloor slice alone
// (wantResolve false) resolves nothing extra and fires no tip_moved.
func (p *pool) idleSleep(ctx context.Context, slot int) (wantResolve bool) {
	// Sample now before taking p.mu, same reasoning as pickKind. The scan
	// itself runs under one lock hold (idleWait) so it never observes two
	// kinds at different instants; the actual sleep happens after the lock
	// is released — p.clk.Sleep must never run under p.mu.
	now := p.clk.Now()
	wait, jammedGate, ok := p.idleWait(now)
	if !ok {
		return false
	}

	if !jammedGate {
		p.clk.Sleep(ctx, wait)
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
	p.clk.Sleep(ctx, slice)
	if p.stopped() || ctx.Err() != nil {
		// A sibling halted the pool, or the caller's ctx was cancelled,
		// while this slot slept. The slot's own top-of-loop haltIfStopping
		// does the halt bookkeeping next; no resolution is worth making.
		return false
	}
	return wait > slice
}

// resolveTip is the one seam runSlot's post-pickKind fetch and
// resolveOpportunistic (the opportunistic call idleSleep's return asks for)
// both call to reach Runner.ResolveTip: it sets PhaseResolving and, on a
// Moved tip, reports noteTipMoved before returning. Folding the
// noteTipMoved call in here is what makes it impossible for a caller to
// receive a moved Tip and forget to report it — a caller that reads a Tip
// without passing through here loses that observation for good. Under
// coalescing every joiner of a shared flight
// sees the same Moved=true and lands here, so it is noteTipMoved's own
// len(reset) == 0 guard, not this seam, that keeps a merge from firing more
// than one tip_moved.
//
// The report fires on tip.Moved alone, whatever err is: a *SelfEvalError
// (runner.go) still carries the revision the fetch half resolved, and the
// runner's baseline is already advanced by the time that error comes back,
// so a move dropped here because the self-eval half also failed is dropped
// for good — no later resolve can ever re-observe it. Errors themselves are
// returned unchanged: each caller keeps its own error handling
// (resolveOpportunistic's own swallow, versus runSlot's
// haltIfStopping/backoffOrHalt).
func (p *pool) resolveTip(ctx context.Context, slot int) (Tip, error) {
	p.setPhase(slot, PhaseResolving)
	tip, err := p.r.ResolveTip(ctx)
	if tip.Moved {
		p.noteTipMoved(slot, tip.Revision)
	}
	return tip, err
}

// resolveOpportunistic makes the one opportunistic ResolveTip call idleSleep's
// resolveTip return asks runSlot for, ahead of its next pickKind. tip, true
// on success — the underlying resolveTip already reports Tip.Moved, so the
// caller that keeps this tip for the current iteration never has to check
// Moved itself. A failure here is deliberately treated as no change
// observed: it is not the per-iteration fetch's own site, so it carries no
// haltIfStopping guard, no backoffOrHalt, and no breaker failure — only that
// fetch, made after pickKind finds a kind runnable, reports a genuinely
// broken fetch. The phase is reset to idle on failure so a slot that falls
// through into idleSleep right after is not still counted as PhaseResolving
// by a sibling's siblingsEngaged.
func (p *pool) resolveOpportunistic(ctx context.Context, slot int) (Tip, bool) {
	tip, err := p.resolveTip(ctx, slot)
	if err != nil {
		p.setPhase(slot, PhaseIdle)
		return Tip{}, false
	}
	return tip, true
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

// noteTipMoved records that revision is a tip a resolution actually observed
// as moved (Tip.Moved), and resets every currently-jammed kind's backoff —
// the observed change ends each of their no-work streaks same as real work
// would (a queue-empty kind's streak is untouched, since a moved tip is not
// evidence an empty queue refilled). No single Kind names this: several
// kinds can be jammed at once, and the tip that moved is evidence for all of
// them, not whichever this slot happened to be running. Kinds names the set
// actually reset below; iterating cfg.Kinds rather than the kinds map keeps
// that set's order deterministic. The scan, resets, and the tip_moved event
// itself all happen in one mutate, so the set an operator reads on the event
// is exactly the set that was reset, not a snapshot taken a moment either
// side of it.
//
// The event is emitted only when reset is non-empty: resolveTip (pool.go,
// see its own doc) reports every per-iteration fetch's Moved tip here, not
// just the opportunistic call a jammed kind gates. Without the guard,
// tip_moved would fire on every ordinary advance of the base branch rather
// than staying the jam signal it is.
//
// The Runner-global baseline also changes the count: the per-slot baselines
// this replaced fired one tip_moved per idling slot that crossed a merge,
// where two slots crossing the same merge now share one baseline and so
// produce exactly one between them. A single event still does the whole job,
// since the backoff reset below is pool-wide.
func (p *pool) noteTipMoved(slot int, revision string) {
	p.mutate(func(s *state) []Event {
		var reset []Kind
		for _, kind := range p.cfg.Kinds {
			if k := s.kinds[kind]; k.jammedNow() {
				s.kinds[kind] = k.reset()
				reset = append(reset, kind)
			}
		}
		if len(reset) == 0 {
			return nil
		}
		return []Event{{Event: "tip_moved", Slot: intPtr(slot), Revision: revision, Reason: "a merge can unblock a jammed queue", Kinds: reset}}
	})
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
