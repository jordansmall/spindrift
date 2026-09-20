package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Runner is the daemon's one seam onto the outside world: resolve the
// revision to pin the next child to, and run a child of a given Dispatch
// kind at that revision. Both fold behind one seam so a later "tip moved
// between iterations" test needs no second one.
//
// RunChild may return a ChildResult carrying Issues alongside a non-nil
// error: a child can announce Boxes and only then fail the seam itself (a
// wait failure that is no ExitError), and those Boxes are real work already
// in flight, so Loop emits them before it halts.
type Runner interface {
	ResolveRevision(ctx context.Context) (string, error)
	RunChild(ctx context.Context, req ChildRequest) (ChildResult, error)
}

// ChildRequest is one child invocation's parameters. Slot is the daemon
// pool slot the child occupies (0-based): it is how the production Runner
// tracks several concurrent children for signal forwarding, and how the
// event stream says which slot a child filled.
type ChildRequest struct {
	Slot     int
	Kind     Kind
	Revision string
}

// Clock is the loop's time seam: Now for the breaker's window, Sleep for
// the idle and backoff waits. One interface rather than a bare sleep func
// so both read off a single seam a test fakes once.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration)
}

// ChildResult is what one child invocation reports back. Issues holds the
// issue numbers the child announced Boxes for, in announce order, so the
// loop can emit one "box" event per announced issue.
type ChildResult struct {
	Exit   int
	Issues []string
}

// Config is the loop's tuning: which Dispatch kind to drive, how many
// slots (concurrent single-Box children) the pool runs, and the
// idle-backoff/failure-backoff/breaker knobs below. Per-kind backoff and
// the instance lock are later tickets.
type Config struct {
	Kind  Kind
	Slots int

	// Awake gates when a slot may start a new child: nil means no window
	// is configured, so the daemon is always awake. When set, it only
	// gates the moment a slot is about to start a child — a child already
	// running when the window closes is never touched, however long it
	// outlasts the close.
	Awake *Window

	// IdleFloor and IdleCap bound the pool-wide idle backoff: the first
	// no-work check (exit 2 always, exit 3 when the pool is otherwise
	// idle) waits IdleFloor, and each further consecutive no-work check
	// doubles the wait, capped at IdleCap. Any check that actually
	// dispatches or refreshes an image resets the wait back to IdleFloor.
	// IdleFloor must be positive; IdleCap must be >= IdleFloor.
	IdleFloor time.Duration
	IdleCap   time.Duration

	// FailureBackoff is how long a slot sleeps before refilling itself
	// after an unclassified failure (an unrecognised exit code, a
	// RunChild seam error, or a ResolveRevision error): the bad
	// slot backs off and retries alone, rather than the whole pool
	// stopping over one issue. Must be non-negative.
	FailureBackoff time.Duration
	// BreakerThreshold and BreakerWindow bound the pool-wide breaker: if
	// BreakerThreshold unclassified failures land (from any slot, in any
	// mix) within a trailing BreakerWindow, the pool halts instead of
	// every slot backing off forever — the signature of a systemic fault
	// (an expired token, a forge outage) that no per-slot retry clears.
	// Both must be positive.
	BreakerThreshold int
	BreakerWindow    time.Duration
}

// Loop runs cfg.Slots slot goroutines, each independently driving Dispatch
// children through the same state machine, until something says halt: a
// Halt-mapped exit code, the pool-wide breaker tripping, or a cancelled
// ctx. Whichever slot halts first wins — Loop emits exactly one halt event
// per call, and returns that first reason, for a caller to log or turn
// into a process exit code.
//
// An unclassified failure is not one of those: a resolve failure, a
// RunChild error or an unrecognised exit code backs its own slot off and
// refills it, leaving the siblings working, and only reaches a halt by
// tripping the breaker.
//
// It never kills a child it has started: once a slot calls RunChild, it
// always waits for it to return and always emits that child's child_finish
// before halting, even if ctx was cancelled mid-run or a sibling slot
// halted the pool in the meantime. "Stop starting new work" is enforced
// only between iterations and before RunChild: at the top of a slot's loop,
// after ResolveRevision returns, and after Interpret decides to Wait.
//
// cfg.Slots must be positive: a zero or negative pool size would silently
// run no children while looking like a healthy daemon, so Loop rejects it
// as a halt-shaped config error instead.
func Loop(ctx context.Context, cfg Config, r Runner, em *Emitter, clk Clock) string {
	if cfg.Slots <= 0 {
		return invalidConfig(em, cfg.Kind, fmt.Sprintf("slots must be a positive integer, got %d", cfg.Slots))
	}
	if cfg.IdleFloor <= 0 {
		return invalidConfig(em, cfg.Kind, fmt.Sprintf("idle floor must be positive, got %s", cfg.IdleFloor))
	}
	if cfg.IdleCap < cfg.IdleFloor {
		return invalidConfig(em, cfg.Kind, fmt.Sprintf("idle cap must be >= idle floor, got cap %s < floor %s", cfg.IdleCap, cfg.IdleFloor))
	}
	if cfg.FailureBackoff < 0 {
		return invalidConfig(em, cfg.Kind, fmt.Sprintf("failure backoff must be non-negative, got %s", cfg.FailureBackoff))
	}
	if cfg.BreakerThreshold <= 0 {
		return invalidConfig(em, cfg.Kind, fmt.Sprintf("breaker threshold must be a positive integer, got %d", cfg.BreakerThreshold))
	}
	if cfg.BreakerWindow <= 0 {
		return invalidConfig(em, cfg.Kind, fmt.Sprintf("breaker window must be positive, got %s", cfg.BreakerWindow))
	}

	p, pctx := newPool(ctx, cfg, r, em, clk)
	defer p.cancel()

	var wg sync.WaitGroup
	wg.Add(cfg.Slots)
	for slot := 0; slot < cfg.Slots; slot++ {
		go func(slot int) {
			defer wg.Done()
			runSlot(pctx, slot, cfg, em, p)
		}(slot)
	}
	wg.Wait()

	return p.haltReason()
}

// invalidConfig emits and returns a "config-invalid: " + detail halt reason,
// the shape shared by every cfg field Loop rejects before starting a pool.
func invalidConfig(em *Emitter, kind Kind, detail string) string {
	reason := "config-invalid: " + detail
	em.Emit(Event{Event: "halt", Kind: kind, Reason: reason})
	return reason
}

// runSlot drives one pool slot's children until the pool halts, either
// because this slot decided to halt it or because a sibling did. ctx is the
// pool's own derived context (not the caller's ctx directly): cancelling it
// is how the pool tells every slot to stop promptly, including one asleep
// in clk.Sleep or blocked inside ResolveRevision.
func runSlot(ctx context.Context, slot int, cfg Config, em *Emitter, p *pool) {
	for {
		if stopOnCancel(ctx, p) {
			return
		}

		p.awaitWindow(ctx, slot)
		if stopOnCancel(ctx, p) {
			// A signal can arrive while the slot was parked in
			// awaitWindow; nothing else re-checks ctx between there and
			// ResolveRevision, so this is that check.
			return
		}

		revision, err := p.r.ResolveRevision(ctx)
		if err != nil {
			// A failed fetch is exactly the transient blip this slice's
			// breaker exists for: back off and retry alone, unless enough
			// failures have piled up pool-wide to say this is systemic
			// (backoffOrHalt below).
			if p.backoffOrHalt(ctx, slot, "", fmt.Sprintf("resolve-revision: %v", err)) {
				return
			}
			continue
		}

		if stopOnCancel(ctx, p) {
			// ResolveRevision (a git fetch) can outlast a SIGTERM sent
			// while it was in flight; re-check here so that fetch never
			// launches a child that forwardStop never gets a chance to
			// signal.
			return
		}

		if !cfg.Awake.Open(p.clk.Now()) {
			// ResolveRevision (a git fetch) can outlast the window's own
			// close; re-check here so that fetch never launches a child
			// outside the window. No event here: awaitWindow's
			// edge-triggered noteAwakeClose reports the transition when
			// the slot parks on the next iteration.
			continue
		}

		em.Emit(Event{Event: "child_start", Kind: cfg.Kind, Revision: revision, Slot: intPtr(slot)})

		p.occupy(slot)
		result, err := p.r.RunChild(ctx, ChildRequest{Slot: slot, Kind: cfg.Kind, Revision: revision})
		p.unoccupy(slot)
		if err != nil {
			// The seam failed, not the child (e.g. it could not even be
			// started), so there is no exit code to report — but a
			// child_start was already emitted, and every started child
			// gets a matching child_finish so the stream never shows one
			// without the other. RunChild can still return Issues alongside
			// the error (e.g. a non-ExitError wait failure after the child
			// announced boxes), so emit those first: an announced Box must
			// reach the durable stream even when the seam itself failed.
			for _, issue := range result.Issues {
				em.Emit(Event{Event: "box", Kind: cfg.Kind, Issue: issue, Revision: revision, Slot: intPtr(slot)})
			}
			em.Emit(Event{Event: "child_finish", Kind: cfg.Kind, Revision: revision, Outcome: "error", Slot: intPtr(slot)})
			if p.backoffOrHalt(ctx, slot, revision, fmt.Sprintf("run-child: %v", err)) {
				return
			}
			continue
		}

		for _, issue := range result.Issues {
			em.Emit(Event{Event: "box", Kind: cfg.Kind, Issue: issue, Revision: revision, Slot: intPtr(slot)})
		}

		exit := result.Exit
		outcome, action := Interpret(exit)
		em.Emit(Event{Event: "child_finish", Kind: cfg.Kind, Revision: revision, Exit: &exit, Outcome: outcome, Slot: intPtr(slot)})

		switch action {
		case Continue:
			// Exit 0 (dispatched) or exit 4 (image-stale): the check
			// answered something other than "nothing to do", so whatever
			// streak of no-work checks the idle backoff was tracking is
			// over.
			p.idle.reset()
			continue
		case Wait:
			if !cfg.Awake.Open(p.clk.Now()) {
				// The window closed while the child ran, so this wait is
				// the window's, not the idle backoff's: idleWait would
				// consume a backoff step and poll ResolveRevision straight
				// through a closed window that is supposed to cost
				// nothing. awaitWindow, at the top, parks the whole
				// remaining span in one sleep instead, and the idle streak
				// stays untouched so it resumes where it left off.
				continue
			}
			wait := p.idle.next()
			// "none-dispatchable" carries a second axis exit 2 doesn't: pool
			// occupancy. With a sibling genuinely running, the issues this
			// slot found "none dispatchable" were claimed or overlap-deferred
			// against that very sibling — routine, reported like any other
			// idle wait. With every sibling parked too, nothing is running
			// and nothing can start: a jam an operator may need to clear.
			if outcome == "none-dispatchable" && !p.siblingsOccupied(slot) {
				em.Emit(Event{Event: "jam", Kind: cfg.Kind, Revision: revision, Slot: intPtr(slot), Wait: wait.String(), Reason: "no work is dispatchable and no sibling slot is running"})
			} else {
				em.Emit(Event{Event: "idle", Kind: cfg.Kind, Wait: wait.String(), Slot: intPtr(slot)})
			}
			// The short-circuit is keyed on the outcome, not on which event
			// fired above: a none-dispatchable wait polls for a moved tip
			// even when a sibling running makes it report "idle" rather than
			// "jam". A queue-empty wait (exit 2) never polls — a merge
			// cannot put new work in an empty queue, so noticing one mid-wait
			// would only cost a query for nothing.
			if outcome == "none-dispatchable" {
				p.idleWait(ctx, slot, wait, revision)
			} else {
				p.clk.Sleep(ctx, wait)
			}
			continue
		case Backoff:
			if p.backoffOrHalt(ctx, slot, revision, fmt.Sprintf("outcome: %s (exit %d)", outcome, exit)) {
				return
			}
			continue
		default: // Halt
			p.halt("outcome: "+outcome, revision)
			return
		}
	}
}

// stopOnCancel reports whether the slot should stop starting new work: true
// if the pool has already halted (a sibling got there first — this slot
// returns quietly, nothing more to emit) or if ctx is freshly cancelled (the
// caller's own ctx, e.g. an operator signal — this slot is the one that
// discovers it, so it records the reason via p.halt). Checking p.stopped()
// first is what makes the distinction possible: p.halt's own cancel() also
// cancels ctx, so a raw ctx.Err() check alone cannot tell "I am first to
// notice real cancellation" from "a sibling already halted for some other
// reason and cancelled me as a side effect".
func stopOnCancel(ctx context.Context, p *pool) bool {
	if p.stopped() {
		return true
	}
	if err := ctx.Err(); err != nil {
		p.halt("context-cancelled: "+err.Error(), "")
		return true
	}
	return false
}

// intPtr returns a pointer to v. Event.Slot (like Event.Exit) is a pointer
// so slot/exit 0 — a real value — survives the field's omitempty tag
// instead of being elided as the zero value.
func intPtr(v int) *int {
	return &v
}
