package daemon

import (
	"context"
	"fmt"
	"sync"
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
	em  *Emitter
	clk Clock
	b   *breaker

	cancel context.CancelFunc

	mu       sync.Mutex
	halted   bool
	reason   string
	occupied map[int]struct{}
}

// newPool derives ctx into a context pool.cancel can stop independently of
// the caller, and returns both the pool and that derived context — every
// slot runs against the derived one, never the caller's directly, so
// RunChild is already contractually drain-safe under a cancelled ctx (see
// loop.go's own doc), and hostRunner.RunChild uses exec.Command rather than
// CommandContext, so cancelling it can never kill a running child. clk and
// the breaker are both breaker policy, not slot-tracking state, but they
// live here so backoffOrHalt and runSlot stop threading them as parameters.
func newPool(ctx context.Context, cfg Config, em *Emitter, clk Clock) (*pool, context.Context) {
	pctx, cancel := context.WithCancel(ctx)
	p := &pool{
		cfg:      cfg,
		em:       em,
		clk:      clk,
		b:        newBreaker(cfg.BreakerThreshold, cfg.BreakerWindow),
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
