package daemon

import (
	"context"
	"fmt"
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
	RunChild(ctx context.Context, kind Kind, revision string) (ChildResult, error)
}

// ChildResult is what one child invocation reports back. Issues holds the
// issue numbers the child announced Boxes for, in announce order, so the
// loop can emit one "box" event per announced issue.
type ChildResult struct {
	Exit   int
	Issues []string
}

// Config is the loop's tuning: which Dispatch kind to drive and how long to
// wait on an empty queue. Pool concurrency, per-kind backoff, the Awake
// window and the instance lock are later tickets.
type Config struct {
	Kind         Kind
	IdleInterval time.Duration
}

// Loop drives Dispatch children until something says halt: a resolve
// failure, a RunChild error, an unrecognised or Halt-mapped exit code, or a
// cancelled ctx. It returns the reason it halted, for a caller to log or
// turn into a process exit code.
//
// It never kills a child it has started: once RunChild is called, the loop
// always waits for it to return and always emits that child's child_finish
// before halting, even if ctx was cancelled mid-run. "Stop starting new
// work" is enforced only between iterations and before RunChild: at the top
// of the loop, after ResolveRevision returns, and after Interpret decides
// to Wait.
func Loop(ctx context.Context, cfg Config, r Runner, em *Emitter, sleep func(ctx context.Context, d time.Duration)) string {
	for {
		if err := ctx.Err(); err != nil {
			reason := "context-cancelled: " + err.Error()
			em.Emit(Event{Event: "halt", Kind: cfg.Kind, Reason: reason})
			return reason
		}

		revision, err := r.ResolveRevision(ctx)
		if err != nil {
			reason := fmt.Sprintf("resolve-revision: %v", err)
			em.Emit(Event{Event: "halt", Kind: cfg.Kind, Reason: reason})
			return reason
		}

		if err := ctx.Err(); err != nil {
			// ResolveRevision (a git fetch) can outlast a SIGTERM sent
			// while it was in flight; re-check here so that fetch never
			// launches a child that forwardStop never gets a chance to
			// signal.
			reason := "context-cancelled: " + err.Error()
			em.Emit(Event{Event: "halt", Kind: cfg.Kind, Reason: reason})
			return reason
		}

		em.Emit(Event{Event: "child_start", Kind: cfg.Kind, Revision: revision})

		result, err := r.RunChild(ctx, cfg.Kind, revision)
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
				em.Emit(Event{Event: "box", Kind: cfg.Kind, Issue: issue, Revision: revision})
			}
			reason := fmt.Sprintf("run-child: %v", err)
			em.Emit(Event{Event: "child_finish", Kind: cfg.Kind, Revision: revision, Outcome: "error"})
			em.Emit(Event{Event: "halt", Kind: cfg.Kind, Revision: revision, Reason: reason})
			return reason
		}

		for _, issue := range result.Issues {
			em.Emit(Event{Event: "box", Kind: cfg.Kind, Issue: issue, Revision: revision})
		}

		exit := result.Exit
		outcome, action := Interpret(exit)
		em.Emit(Event{Event: "child_finish", Kind: cfg.Kind, Revision: revision, Exit: &exit, Outcome: outcome})

		switch action {
		case Continue:
			continue
		case Wait:
			wait := cfg.IdleInterval
			em.Emit(Event{Event: "idle", Kind: cfg.Kind, Wait: wait.String()})
			sleep(ctx, wait)
			continue
		default: // Halt
			reason := "outcome: " + outcome
			em.Emit(Event{Event: "halt", Kind: cfg.Kind, Revision: revision, Reason: reason})
			return reason
		}
	}
}
