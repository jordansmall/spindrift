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
	cfg   Config
	r     Runner
	em    *Emitter
	clk   Clock
	b     *breaker
	kinds map[Kind]*kindBackoff

	cancel context.CancelFunc

	mu        sync.Mutex
	halted    bool
	reason    Halt
	occupied  map[int]slotFlight
	awakeShut bool // true once awake_close has fired, until the matching awake_open

	// baton is the discovery token (issue #3684): at most one slot may be
	// between "started" and "announced a Box" at any moment, on the first
	// wave and on every refill alike — a pool that lets two slots run
	// discovery against the same tracker snapshot at once risks both
	// selecting the same issue. batonSlot (below, guarded by p.mu) is the
	// current holder, or noBaton when the token is free; baton itself is
	// the capacity-1 channel the holder sends into when it passes. nil
	// means there is no baton to wait on at all — a single-slot pool has
	// no sibling to stagger against (see newPool).
	baton     chan struct{}
	batonSlot int
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
	kinds := make(map[Kind]*kindBackoff, len(cfg.Kinds))
	for _, k := range cfg.Kinds {
		kinds[k] = newKindBackoff(cfg.IdleFloor, cfg.IdleCap)
	}
	p := &pool{
		cfg:       cfg,
		r:         r,
		em:        em,
		clk:       clk,
		b:         newBreaker(cfg.BreakerThreshold, cfg.BreakerWindow),
		kinds:     kinds,
		cancel:    cancel,
		occupied:  make(map[int]slotFlight),
		batonSlot: leadSlot,
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
func (p *pool) pickKind(slot int) (Kind, bool) {
	now := p.clk.Now()
	for _, kind := range slotOrder(p.cfg.Kinds, p.cfg.ResearchReservation, slot) {
		if p.kinds[kind].runnable(now) {
			return kind, true
		}
	}
	return "", false
}

// occupy marks slot as having a child running right now. A slot calls this
// immediately before RunChild, and unoccupy immediately after RunChild
// returns — before the result is interpreted. That ordering is the whole
// point: a slot must clear its own occupancy before it ever asks
// siblingsOccupied, or it would count itself as a running sibling and "none
// dispatchable, pool otherwise idle" could never be true for a lone slot.
func (p *pool) occupy(slot int, kind Kind, revision string) {
	p.mu.Lock()
	p.occupied[slot] = slotFlight{kind: kind, revision: revision}
	p.mu.Unlock()
}

// unoccupy clears slot's occupancy (see occupy's doc for the ordering).
func (p *pool) unoccupy(slot int) {
	p.mu.Lock()
	delete(p.occupied, slot)
	p.mu.Unlock()
}

// noteIssue appends issue to slot's in-flight issue list, for a snapshot to
// report while the child is still running. A no-op if slot is not occupied:
// the child announced after the slot cleared, which can only be a race
// (RunChild already returned), not a state worth publishing. Dedupe is
// already done by the runner, so this never re-dedupes.
func (p *pool) noteIssue(slot int, issue string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fl, ok := p.occupied[slot]
	if !ok {
		return
	}
	fl.issues = append(fl.issues, issue)
	p.occupied[slot] = fl
}

// siblingsOccupied reports whether any slot other than slot currently has a
// child running. Callers use it only after their own unoccupy(slot) has
// already run, so any entry found here is a genuine sibling, never the
// caller counting itself.
func (p *pool) siblingsOccupied(slot int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for s := range p.occupied {
		if s != slot {
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
			if p.noteAwakeOpen(slot) {
				// noteAwakeOpen emits under p.mu (see its own doc), so it
				// can never call publish itself; publish the transition
				// here, a hair after the emit — the status file is
				// advisory, the event stream is the ordered record. Gated
				// on the transition itself: a slot that finds the window
				// already open (or every sibling racing the same open)
				// must not repeat a publish nothing changed.
				p.publish()
			}
			return
		}
		if p.noteAwakeClose(slot, wait) {
			p.publish()
		}
		p.clk.Sleep(ctx, wait)
		if p.stopped() || ctx.Err() != nil {
			return
		}
	}
}

// noteAwakeClose records the pool-wide transition into a shut window and
// emits awake_close, but only for the caller that actually observes the
// transition: with several slots parking on the same close, only the first
// to flip awakeShut reports it, so the stream carries exactly one
// awake_close per closing however many slots are waiting on it. The emit
// happens while p.mu is still held so the flag flip and the emit are one
// atomic step — otherwise two slots whose clk.Now() calls straddle the
// opening instant could publish awake_open before awake_close. Returns
// whether this call was the one that observed the transition, so
// awaitWindow can skip the publish too on every iteration that finds the
// window still shut with nothing new to report.
func (p *pool) noteAwakeClose(slot int, wait time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	first := !p.awakeShut
	p.awakeShut = true

	if first {
		p.em.Emit(Event{Event: "awake_close", Slot: intPtr(slot), Wait: wait.String(), Reason: "outside the Awake window"})
	}
	return first
}

// noteAwakeOpen is noteAwakeClose's counterpart: it fires awake_open only
// when a close was already reported, so a daemon that starts (or every
// slot merely finds the window already open) never emits an open with no
// matching close. As in noteAwakeClose, the emit happens under p.mu so the
// flag flip and the emit stay one atomic step: an awake_open therefore
// never reaches the stream ahead of the awake_close whose flag flip it
// observed. Returns whether this call was the one that observed the
// transition (see noteAwakeClose).
func (p *pool) noteAwakeOpen(slot int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	was := p.awakeShut
	p.awakeShut = false

	if was {
		p.em.Emit(Event{Event: "awake_open", Slot: intPtr(slot), Reason: "the Awake window reopened"})
	}
	return was
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
	held := p.batonSlot == slot
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
	p.mu.Lock()
	p.batonSlot = slot
	p.mu.Unlock()
}

// passBaton hands the baton on. A no-op unless slot actually holds it,
// which makes it idempotent per hold and safe to call unconditionally from
// every site a round can end — several release sites in runSlot legitimately
// overlap (see the batonPass* reasons' own doc), and only the first to
// actually find itself the holder is the one whose reason reaches the
// stream.
//
// The baton_pass emit happens before the send, not after: sending first
// could let an awaitBaton call already blocked on <-p.baton wake and
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
	if p.batonSlot != slot {
		p.mu.Unlock()
		return
	}
	p.batonSlot = noBaton
	p.mu.Unlock()
	p.emit(Event{Event: "baton_pass", Slot: intPtr(slot), Reason: reason})
	p.baton <- struct{}{}
}

// stopped reports whether the pool has already recorded a halt reason —
// deliberately not a raw ctx.Err() check; see stopOnCancel (loop.go) for why.
func (p *pool) stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.halted
}

// halt records h as the pool's halt reason if none is recorded yet, emits
// the single pool-level halt event, and cancels the derived context.
// Idempotent: once the pool has halted, later calls (racing siblings, or a
// slot that merely rediscovers the same cancellation) are no-ops — the
// first h wins and no second halt event is ever emitted.
func (p *pool) halt(h Halt) {
	p.mu.Lock()
	if p.halted {
		p.mu.Unlock()
		return
	}
	p.halted = true
	p.reason = h
	p.mu.Unlock()

	p.emit(h.Event())
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

	count, crossed := p.b.recordAndCheck(p.clk.Now())
	if crossed {
		detail := fmt.Sprintf("%d failures within %s reached threshold %d", count, p.cfg.BreakerWindow, p.cfg.BreakerThreshold)
		p.emit(Event{Event: "breaker_trip", Kind: kind, Slot: intPtr(slot), Failures: &count, Wait: p.cfg.BreakerWindow.String()})
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

	p.emit(Event{Event: "backoff", Kind: kind, Revision: revision, Slot: intPtr(slot), Wait: p.cfg.FailureBackoff.String(), Reason: reason})
	p.clk.Sleep(ctx, p.cfg.FailureBackoff)
	return false
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
	now := p.clk.Now()
	var earliest time.Time
	jammedGate := false
	for _, k := range p.kinds {
		at, gated := k.readyAt(now)
		if !gated {
			// This kind is runnable at now — a sibling's reset() raced in
			// between pickKind's failed pass and this call, or its own
			// deadline has already elapsed. Either way there is nothing to
			// sleep for; the caller loops back around to pickKind at once.
			return
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
		if k.jammedNow() {
			jammedGate = true
		}
	}
	if earliest.IsZero() {
		// p.kinds is empty, so the loop above never ran at all — Loop's own
		// validation (len(cfg.Kinds) == 0) rejects this before a pool is
		// ever built, but newPool itself doesn't enforce that, so this
		// guards a caller (a test, say) that builds a pool directly.
		return
	}

	wait := earliest.Sub(now)

	if jammedGate && lastRevision != "" {
		p.pollSlices(ctx, slot, wait, lastRevision)
		return
	}
	p.clk.Sleep(ctx, wait)
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
			// p.kinds map keeps that set's order deterministic.
			var reset []Kind
			for _, kind := range p.cfg.Kinds {
				if k := p.kinds[kind]; k.jammedNow() {
					k.reset()
					reset = append(reset, kind)
				}
			}
			p.emit(Event{Event: "tip_moved", Slot: intPtr(slot), Revision: newRevision, Reason: "a merge can unblock a jammed queue", Kinds: reset})
			return
		}
	}
}

// snapshot builds a Status from pool state alone, for publish to hand to
// StatusWriter.Publish as its snap func. Takes p.mu for the whole build so a
// reader never sees state from two different instants stitched together, and
// calls p.clk.Now() under that lock — safe, since no Clock implementation
// takes p.mu — so readyAt can tell a kind whose deadline has already elapsed
// from one still gated, the same now-aware check idleSleep makes outside the
// lock. It also calls kindBackoff.readyAt/jammedNow, which take kindBackoff's
// own separate mutex — that nesting is safe (nothing under kindBackoff's
// mutex ever takes p.mu), but noteAwakeClose/noteAwakeOpen emit while
// holding p.mu, so they must never call publish (and therefore never call
// snapshot); see awaitWindow, which publishes after they return instead, and
// publish's own doc for the w.mu → p.mu order snapshot also runs under.
func (p *pool) snapshot() Status {
	p.mu.Lock()
	defer p.mu.Unlock()

	slots := make([]SlotStatus, p.cfg.Slots)
	for i := range slots {
		slots[i] = SlotStatus{Slot: i}
		fl, ok := p.occupied[i]
		if !ok {
			continue
		}
		slots[i].Busy = true
		slots[i].Kind = fl.kind
		slots[i].Revision = fl.revision
		if len(fl.issues) > 0 {
			// A snapshot handed to a writer must not alias state this slot
			// keeps appending to.
			issues := make([]string, len(fl.issues))
			copy(issues, fl.issues)
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
		kb := p.kinds[k]
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
	// check nextCheck already floors on above), not p.awakeShut: awakeShut
	// is edge-triggered by whichever slot's awaitWindow observes the
	// close, so a pool built outside its window and snapshotted before any
	// slot parks would still read awakeShut false and disagree with a
	// nextCheck already naming the reopening. p.awakeShut therefore has no
	// reader left outside the note*Awake* pair itself, which still needs
	// it to edge-trigger awake_close/awake_open.
	state := StateChecking
	reason := ""
	switch {
	case p.halted:
		state = StateHalted
		reason = p.reason.String()
	case len(p.occupied) > 0:
		state = StateWorking
	case !windowOpensAt.IsZero():
		state = StateAsleep
	case allGated && anyJammed:
		state = StateJammed
	case allGated:
		state = StateWaiting
	}

	// Copied, not aliased, matching the fl.issues copy above: nothing
	// mutates cfg.Kinds today, but a returned Status must never assume a
	// future writer keeps that true.
	kinds := make([]Kind, len(p.cfg.Kinds))
	copy(kinds, p.cfg.Kinds)

	return Status{
		Kinds:  kinds,
		State:  state,
		Reason: reason,
		Slots:  slots,
		Checks: checks,
	}
}

// publish writes the pool's current snapshot to Config.Status, if
// configured. A nil Status is the deliberate opt-out — a daemon that never
// located a git dir to publish into, or an operator who chose not to
// publish — not a swallowed error, so publish is silently a no-op rather
// than reporting anything. A write failure is advisory-only and must never
// fail the daemon: it is reported to emitErrW and otherwise ignored.
//
// Publish, not Write, so p.snapshot runs under StatusWriter.mu: that is what
// stops two slot goroutines publishing concurrently from sampling in one
// order and writing in the other. Lock order is therefore w.mu → p.mu, which
// is deadlock-free only because nothing calls publish while already holding
// p.mu.
func (p *pool) publish() {
	if p.cfg.Status == nil {
		return
	}
	if err := p.cfg.Status.Publish(p.snapshot); err != nil {
		fmt.Fprintf(emitErrW, "daemon: status file write failed: %v\n", err)
	}
}

// emit writes ev to the event stream and republishes the status file:
// folding the publish in makes "on state change" structural for every event
// routed through emit, rather than one more call site to remember. It does
// not cover every state change, though: the note*Awake* pair emits directly
// under p.mu, where calling emit (and its own publish) would deadlock, and
// hand-places a publish once the lock is released (awaitWindow); occupy,
// unoccupy, noteIssue, and kindBackoff.reset change pool state with no
// event of their own to ride, so their callers hand-place a publish too
// (loop.go).
func (p *pool) emit(ev Event) { p.em.Emit(ev); p.publish() }

// haltReason returns the pool's recorded Halt. Only meaningful after
// every slot goroutine has returned (Loop calls it after its WaitGroup
// drains), so no lock is strictly required at that point, but the pool's
// own mutex is reused anyway rather than trusting a second, easy-to-miss
// synchronisation argument.
func (p *pool) haltReason() Halt {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reason
}
