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

// Clock is the loop's time seam: Now for timestamping and reasoning about
// elapsed time, Sleep for the idle wait. One interface rather than a bare
// sleep func so a later breaker slice can read Now() off the same seam a
// test already fakes.
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

// Config is the loop's tuning: which Dispatch kind to drive, how long to
// wait on an empty queue, and how many slots (concurrent single-Box
// children) the pool runs. Per-kind backoff, the Awake window and the
// instance lock are later tickets.
type Config struct {
	Kind         Kind
	IdleInterval time.Duration
	Slots        int
}

// Loop runs cfg.Slots slot goroutines, each independently driving Dispatch
// children through the same state machine, until something says halt: a
// resolve failure, a RunChild error, an unrecognised or Halt-mapped exit
// code, or a cancelled ctx. Whichever slot halts first wins — Loop emits
// exactly one halt event per call, and returns that first reason, for a
// caller to log or turn into a process exit code.
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
		reason := fmt.Sprintf("config-invalid: slots must be a positive integer, got %d", cfg.Slots)
		em.Emit(Event{Event: "halt", Kind: cfg.Kind, Reason: reason})
		return reason
	}

	p, pctx := newPool(ctx, cfg, em)
	defer p.cancel()

	var wg sync.WaitGroup
	wg.Add(cfg.Slots)
	for slot := 0; slot < cfg.Slots; slot++ {
		go func(slot int) {
			defer wg.Done()
			runSlot(pctx, slot, cfg, r, em, clk, p)
		}(slot)
	}
	wg.Wait()

	return p.haltReason()
}

// runSlot drives one pool slot's children until the pool halts, either
// because this slot decided to halt it or because a sibling did. ctx is the
// pool's own derived context (not the caller's ctx directly): cancelling it
// is how the pool tells every slot to stop promptly, including one asleep
// in clk.Sleep or blocked inside ResolveRevision.
func runSlot(ctx context.Context, slot int, cfg Config, r Runner, em *Emitter, clk Clock, p *pool) {
	for {
		if stopOnCancel(ctx, p) {
			return
		}

		revision, err := r.ResolveRevision(ctx)
		if err != nil {
			p.halt(fmt.Sprintf("resolve-revision: %v", err), "")
			return
		}

		if stopOnCancel(ctx, p) {
			// ResolveRevision (a git fetch) can outlast a SIGTERM sent
			// while it was in flight; re-check here so that fetch never
			// launches a child that forwardStop never gets a chance to
			// signal.
			return
		}

		em.Emit(Event{Event: "child_start", Kind: cfg.Kind, Revision: revision, Slot: intPtr(slot)})

		result, err := r.RunChild(ctx, ChildRequest{Slot: slot, Kind: cfg.Kind, Revision: revision})
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
			p.halt(fmt.Sprintf("run-child: %v", err), revision)
			return
		}

		for _, issue := range result.Issues {
			em.Emit(Event{Event: "box", Kind: cfg.Kind, Issue: issue, Revision: revision, Slot: intPtr(slot)})
		}

		exit := result.Exit
		outcome, action := Interpret(exit)
		em.Emit(Event{Event: "child_finish", Kind: cfg.Kind, Revision: revision, Exit: &exit, Outcome: outcome, Slot: intPtr(slot)})

		switch action {
		case Continue:
			continue
		case Wait:
			wait := cfg.IdleInterval
			em.Emit(Event{Event: "idle", Kind: cfg.Kind, Wait: wait.String(), Slot: intPtr(slot)})
			clk.Sleep(ctx, wait)
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
