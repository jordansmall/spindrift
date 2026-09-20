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
	cfg  Config
	r    Runner
	em   *Emitter
	clk  Clock
	b    *breaker
	idle *idleBackoff

	cancel context.CancelFunc

	mu        sync.Mutex
	halted    bool
	reason    string
	occupied  map[int]struct{}
	awakeShut bool // true once awake_close has fired, until the matching awake_open
}

// newPool derives ctx into a context pool.cancel can stop independently of
// the caller, and returns both the pool and that derived context — every
// slot runs against the derived one, never the caller's directly, so
// RunChild is already contractually drain-safe under a cancelled ctx (see
// loop.go's own doc), and hostRunner.RunChild uses exec.Command rather than
// CommandContext, so cancelling it can never kill a running child. r, clk,
// the breaker, and the idle backoff are all pool-wide policy, not
// slot-tracking state, but they live here so backoffOrHalt and runSlot stop
// threading them as parameters.
func newPool(ctx context.Context, cfg Config, r Runner, em *Emitter, clk Clock) (*pool, context.Context) {
	pctx, cancel := context.WithCancel(ctx)
	p := &pool{
		cfg:      cfg,
		r:        r,
		em:       em,
		clk:      clk,
		b:        newBreaker(cfg.BreakerThreshold, cfg.BreakerWindow),
		idle:     newIdleBackoff(cfg.IdleFloor, cfg.IdleCap),
		cancel:   cancel,
		occupied: make(map[int]struct{}),
	}
	return p, pctx
}

// occupy marks slot as having a child running right now. A slot calls this
// immediately before RunChild, and unoccupy immediately after RunChild
// returns — before the result is interpreted. That ordering is the whole
// point: a slot must clear its own occupancy before it ever asks
// siblingsOccupied, or it would count itself as a running sibling and "none
// dispatchable, pool otherwise idle" could never be true for a lone slot.
func (p *pool) occupy(slot int) {
	p.mu.Lock()
	p.occupied[slot] = struct{}{}
	p.mu.Unlock()
}

// unoccupy clears slot's occupancy (see occupy's doc for the ordering).
func (p *pool) unoccupy(slot int) {
	p.mu.Lock()
	delete(p.occupied, slot)
	p.mu.Unlock()
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

// noteAwakeClose records the pool-wide transition into a shut window and
// emits awake_close, but only for the caller that actually observes the
// transition: with several slots parking on the same close, only the first
// to flip awakeShut reports it, so the stream carries exactly one
// awake_close per closing however many slots are waiting on it. The emit
// happens while p.mu is still held so the flag flip and the emit are one
// atomic step -- otherwise two slots whose clk.Now() calls straddle the
// opening instant could publish awake_open before awake_close.
func (p *pool) noteAwakeClose(slot int, wait time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	first := !p.awakeShut
	p.awakeShut = true

	if first {
		p.em.Emit(Event{Event: "awake_close", Kind: p.cfg.Kind, Slot: intPtr(slot), Wait: wait.String(), Reason: "outside the Awake window"})
	}
}

// noteAwakeOpen is noteAwakeClose's counterpart: it fires awake_open only
// when a close was already reported, so a daemon that starts (or every
// slot merely finds the window already open) never emits an open with no
// matching close. As in noteAwakeClose, the emit happens under p.mu so the
// flag flip and the emit stay one atomic step: an awake_open therefore
// never reaches the stream ahead of the awake_close whose flag flip it
// observed.
func (p *pool) noteAwakeOpen(slot int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	was := p.awakeShut
	p.awakeShut = false

	if was {
		p.em.Emit(Event{Event: "awake_open", Kind: p.cfg.Kind, Slot: intPtr(slot), Reason: "the Awake window reopened"})
	}
}

// stopped reports whether the pool has already recorded a halt reason —
// deliberately not a raw ctx.Err() check; see stopOnCancel (loop.go) for why.
func (p *pool) stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.halted
}

// halt records reason as the pool's halt reason if none is recorded yet,
// emits the single pool-level halt event, and cancels the derived context.
// Idempotent: once the pool has halted, later calls (racing siblings, or a
// slot that merely rediscovers the same cancellation) are no-ops — the
// first reason wins and no second halt event is ever emitted.
func (p *pool) halt(reason, revision string) {
	p.mu.Lock()
	if p.halted {
		p.mu.Unlock()
		return
	}
	p.halted = true
	p.reason = reason
	p.mu.Unlock()

	p.em.Emit(Event{Event: "halt", Kind: p.cfg.Kind, Revision: revision, Reason: reason})
	p.cancel()
}

// backoffOrHalt is what a slot calls on an unclassified failure (a
// ResolveRevision error, a RunChild seam error, or an unrecognised exit
// code): it records the failure in the pool-wide breaker and either trips
// the pool (enough failures landed across the pool within the window to
// look systemic — no per-slot retry clears that) or backs this one slot
// off and lets it retry alone. Returns true if the pool halted (the caller
// must stop), false if the caller should sleep out the backoff and
// continue its own loop.
func (p *pool) backoffOrHalt(ctx context.Context, slot int, revision, reason string) bool {
	count, crossed := p.b.recordAndCheck(p.clk.Now())
	if crossed {
		haltReason := fmt.Sprintf("breaker: %d failures within %s reached threshold %d", count, p.cfg.BreakerWindow, p.cfg.BreakerThreshold)
		p.em.Emit(Event{Event: "breaker_trip", Kind: p.cfg.Kind, Slot: intPtr(slot), Failures: &count, Wait: p.cfg.BreakerWindow.String()})
		p.halt(haltReason, revision)
		return true
	}

	// This call did not cross the threshold itself, but a sibling racing
	// concurrently through recordAndCheck might already have — check
	// before backing off so a slot never sleeps out a fresh backoff
	// against a pool that is already stopping.
	if p.stopped() {
		return true
	}

	p.em.Emit(Event{Event: "backoff", Kind: p.cfg.Kind, Revision: revision, Slot: intPtr(slot), Wait: p.cfg.FailureBackoff.String(), Reason: reason})
	p.clk.Sleep(ctx, p.cfg.FailureBackoff)
	return false
}

// idleWait sleeps out a none-dispatchable wait in IdleFloor-sized slices,
// polling ResolveRevision between slices so a merge that unblocks a jammed
// queue is noticed instead of riding out the rest of a long backoff. A
// queue-empty wait (exit 2) never comes here: it stays the plain, single
// p.clk.Sleep it always was, since a merge cannot create new work in an
// empty queue and polling there would only spend a query for nothing.
//
// revision is the revision this slot's child just ran at; a poll result
// that differs from it is the "tip moved" signal. On that signal, idleWait
// emits tip_moved, resets the idle backoff (the observed change ends the
// no-work streak same as real work would), and returns immediately so the
// slot starts its next iteration at once rather than sleeping out the rest
// of the wait.
func (p *pool) idleWait(ctx context.Context, slot int, wait time.Duration, revision string) {
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
			p.em.Emit(Event{Event: "tip_moved", Kind: p.cfg.Kind, Slot: intPtr(slot), Revision: newRevision, Reason: "a merge can unblock a jammed queue"})
			p.idle.reset()
			return
		}
	}
}

// haltReason returns the pool's recorded halt reason. Only meaningful after
// every slot goroutine has returned (Loop calls it after its WaitGroup
// drains), so no lock is strictly required at that point, but the pool's
// own mutex is reused anyway rather than trusting a second, easy-to-miss
// synchronisation argument.
func (p *pool) haltReason() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reason
}
