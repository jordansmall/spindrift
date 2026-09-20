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
		cfg:      cfg,
		r:        r,
		em:       em,
		clk:      clk,
		b:        newBreaker(cfg.BreakerThreshold, cfg.BreakerWindow),
		kinds:    kinds,
		cancel:   cancel,
		occupied: make(map[int]struct{}),
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
		p.em.Emit(Event{Event: "awake_close", Slot: intPtr(slot), Wait: wait.String(), Reason: "outside the Awake window"})
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
		p.em.Emit(Event{Event: "awake_open", Slot: intPtr(slot), Reason: "the Awake window reopened"})
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
func (p *pool) halt(kind Kind, reason, revision string) {
	p.mu.Lock()
	if p.halted {
		p.mu.Unlock()
		return
	}
	p.halted = true
	p.reason = reason
	p.mu.Unlock()

	p.em.Emit(Event{Event: "halt", Kind: kind, Revision: revision, Reason: reason})
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
func (p *pool) backoffOrHalt(ctx context.Context, slot int, kind Kind, revision, reason string) bool {
	count, crossed := p.b.recordAndCheck(p.clk.Now())
	if crossed {
		haltReason := fmt.Sprintf("breaker: %d failures within %s reached threshold %d", count, p.cfg.BreakerWindow, p.cfg.BreakerThreshold)
		p.em.Emit(Event{Event: "breaker_trip", Kind: kind, Slot: intPtr(slot), Failures: &count, Wait: p.cfg.BreakerWindow.String()})
		p.halt(kind, haltReason, revision)
		return true
	}

	// This call did not cross the threshold itself, but a sibling racing
	// concurrently through recordAndCheck might already have — check
	// before backing off so a slot never sleeps out a fresh backoff
	// against a pool that is already stopping.
	if p.stopped() {
		return true
	}

	p.em.Emit(Event{Event: "backoff", Kind: kind, Revision: revision, Slot: intPtr(slot), Wait: p.cfg.FailureBackoff.String(), Reason: reason})
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
		if p.backoffOrHalt(ctx, slot, kind, revision, HaltSelfBuildPrefix+err.Error()) {
			return selfStop
		}
		return selfRetry
	}
	if path == p.cfg.SelfProgram {
		return selfOK
	}
	reason := fmt.Sprintf("%s: daemon build at %s is %s, running %s", HaltSelfChanged, revision, path, p.cfg.SelfProgram)
	p.halt(kind, reason, revision)
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
		at, gated := k.readyAt()
		if !gated {
			continue
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
		if k.jammedNow() {
			jammedGate = true
		}
	}
	if earliest.IsZero() {
		// Every kind is runnable after all — a sibling's reset() raced in
		// between pickKind's failed pass and this call. Nothing to sleep
		// for; the caller loops back around to pickKind at once.
		return
	}

	wait := earliest.Sub(now)
	if wait <= 0 {
		return
	}

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
			p.em.Emit(Event{Event: "tip_moved", Slot: intPtr(slot), Revision: newRevision, Reason: "a merge can unblock a jammed queue", Kinds: reset})
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
