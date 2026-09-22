package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Runner is the daemon's one seam onto the outside world: resolve the
// revision to pin the next child to, run a child of a given Dispatch kind
// at that revision, and evaluate what the daemon's own program would be if
// rebuilt from that revision. All three fold behind one seam: the self
// check (SelfPath) is the same kind of outside-world question as the other
// two — an evaluation at the fetched tip — so it belongs behind this seam
// rather than a second one a test would need to fake separately.
//
// RunChild may return a ChildResult carrying Issues alongside a non-nil
// error: a child can announce Boxes and only then fail the seam itself (a
// wait failure that is no ExitError), and those Boxes are real work already
// in flight, so Loop emits them before it halts.
type Runner interface {
	ResolveRevision(ctx context.Context) (string, error)
	RunChild(ctx context.Context, req ChildRequest) (ChildResult, error)

	// SelfPath returns the store path the daemon's own app attribute
	// evaluates to at revision — what this daemon's program would be if
	// it were rebuilt from the fetched tip.
	SelfPath(ctx context.Context, revision string) (string, error)
}

// ChildRequest is one child invocation's parameters. Slot is the daemon
// pool slot the child occupies (0-based): it is how the event stream says
// which slot a child filled.
//
// Stop and Abort are the operator-stop/hard-abort latch, carried here as
// data rather than left for the Runner to hold per-child state of its own:
// the pool is what knows which config it was given, and re-handing the same
// two channels on every request (this slot's whole lifetime, not just its
// first child) means a Runner needs no bookkeeping to answer "has the
// operator asked me to stop" — it only ever selects on what this request
// carries.
type ChildRequest struct {
	Slot     int
	Kind     Kind
	Revision string
	Stop     <-chan struct{}
	Abort    <-chan struct{}

	// OnIssue, when non-nil, is called with each issue the child announces a
	// Box for, as the announce line is read rather than after the child
	// exits. ChildResult.Issues remains the authoritative, ordered record for
	// the event stream; this is the live channel the status file needs to name
	// the issue a slot has in flight while the child is still running (issue
	// #3545) — after-the-fact Issues can only ever say what a slot *had*.
	OnIssue func(issue string)
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

// Config is the loop's tuning: which Dispatch kinds to draw from, how many
// slots (concurrent single-Box children) the pool runs, and the
// idle-backoff/failure-backoff/breaker knobs below.
type Config struct {
	// Kinds is the set of Dispatch kinds this pool draws from (issue #3541).
	// One pool rather than one per kind: research runs through the full Box and
	// costs what work costs, so two pools would quietly invalidate the operator's
	// MEMORY_LIMIT x MAX_PARALLEL sizing. Must be non-empty.
	Kinds []Kind

	// ResearchReservation is the number of slots that prefer research over work.
	// It is a floor, not a ceiling: those slots take research only while research
	// has queued work, and either kind bursts into the whole pool when the other
	// has backed off into an empty result. Zero is work-first with research on
	// the leftovers; Slots is research-first. Must be 0 <= ResearchReservation <=
	// Slots.
	ResearchReservation int

	Slots int

	// Stop and Abort are the operator-stop/hard-abort latch (issue #3626):
	// the same two-channel shape the Launcher's own shutdown gate and
	// signal relay already use. A closed Stop halts the pool with
	// HaltOperatorStop and cancels its own derived context, ending an
	// in-progress sleep or fetch promptly. Abort has no pool-level watcher
	// — escalation is the child's business — but is re-exposed on every
	// ChildRequest alongside Stop so a Runner can react to both without
	// tracking per-child state of its own. Nil is the deliberate opt-out
	// for either: a nil channel never fires in a select, so a caller that
	// sets neither — a test, or an embedder that hard-cancels ctx instead —
	// halts only through ctx, exactly as the pre-#3626 host binary did.
	Stop  <-chan struct{}
	Abort <-chan struct{}

	// Awake gates when a slot may start a new child: nil means no window
	// is configured, so the daemon is always awake. When set, it only
	// gates the moment a slot is about to start a child — a child already
	// running when the window closes is never touched, however long it
	// outlasts the close.
	Awake *Window

	// SelfProgram is the running daemon's own program store path — what
	// its build resolved to when it started. At each iteration boundary,
	// before starting a child, the loop compares this against what the
	// daemon attribute evaluates to at the freshly fetched tip
	// (Runner.SelfPath): a mismatch means a newer daemon has already
	// merged, so continuing to orchestrate fresh Boxes from this stale
	// build risks running work under code nobody has actually loaded. On
	// a mismatch the loop finishes what is running and halts at the
	// boundary — it never re-execs itself; a freshly merged but broken
	// daemon must not auto-load with nobody awake. An operator who wants
	// self-update composes that halt with an ordinary service restart
	// policy and gets the behaviour by choice, not by default.
	//
	// Empty disables the check entirely: a daemon that cannot know its
	// own build must not guess, and must never halt on a comparison it
	// cannot make.
	SelfProgram string

	// IdleFloor and IdleCap bound each kind's own idle backoff: the first
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

	// Status, when non-nil, is where the pool publishes its live Status
	// (status.go) on every state change (issue #3545). Nil is the
	// deliberate opt-out, not a swallowed error: a daemon that never
	// located a git dir to publish into never reaches this far, and every
	// existing Config literal simply leaves this nil and keeps working
	// exactly as before.
	Status *StatusWriter
}

// Loop runs cfg.Slots slot goroutines, each independently driving children of
// whichever configured Dispatch kind it currently picks (pickKind, pool.go)
// through the same state machine, until something says halt: a
// Halt-mapped exit code, the pool-wide breaker tripping, or a cancelled
// ctx. Whichever slot halts first wins — Loop emits exactly one halt event
// per call, and returns that first reason, for a caller to log or turn
// into a process exit code.
//
// An unclassified failure is not one of those: a resolve failure, a
// RunChild error, an unrecognised exit code, or exit 7 while Stop is still
// open (someone else signalled that child) backs its own slot off and
// refills it, leaving the siblings working, and only reaches a halt by
// tripping the breaker — unless cfg.Stop has already closed or ctx is
// already cancelled (an operator SIGTERM forwarded to a child that had not
// yet installed its own handler, or that raced the seam's own teardown), in
// which case it is an ordinary stop and never reaches the breaker at all
// (issue #3595, by construction — see backoffOrHalt, pool.go).
//
// It never kills a child it has started: once a slot calls RunChild, it
// always waits for it to return and always emits that child's child_finish
// before halting, even if ctx was cancelled mid-run or a sibling slot
// halted the pool in the meantime. "Stop starting new work" is enforced by
// one admission check, at the top of a slot's loop, before it starts a new
// iteration: cfg.Stop and Abort ride every ChildRequest (see ChildRequest's
// own doc), so a child started after Stop closes is signalled by the Runner
// at once, and nothing between the top of the loop and RunChild needs to
// re-assert the same fact.
//
// cfg.Slots must be positive: a zero or negative pool size would silently
// run no children while looking like a healthy daemon, so Loop rejects it
// as a halt-shaped config error instead.
func Loop(ctx context.Context, cfg Config, r Runner, em *Emitter, clk Clock) Halt {
	if cfg.Slots <= 0 {
		return invalidConfig(em, cfg, fmt.Sprintf("slots must be a positive integer, got %d", cfg.Slots))
	}
	if len(cfg.Kinds) == 0 {
		return invalidConfig(em, cfg, "kinds must be non-empty")
	}
	seenKinds := make(map[Kind]bool, len(cfg.Kinds))
	for _, k := range cfg.Kinds {
		if k != KindDispatch && k != KindResearch {
			return invalidConfig(em, cfg, fmt.Sprintf("unknown kind %q", k))
		}
		if seenKinds[k] {
			return invalidConfig(em, cfg, fmt.Sprintf("duplicate kind %q", k))
		}
		seenKinds[k] = true
	}
	if cfg.ResearchReservation < 0 || cfg.ResearchReservation > cfg.Slots {
		return invalidConfig(em, cfg, fmt.Sprintf("research reservation must be between 0 and slots (%d), got %d", cfg.Slots, cfg.ResearchReservation))
	}
	if cfg.IdleFloor <= 0 {
		return invalidConfig(em, cfg, fmt.Sprintf("idle floor must be positive, got %s", cfg.IdleFloor))
	}
	if cfg.IdleCap < cfg.IdleFloor {
		return invalidConfig(em, cfg, fmt.Sprintf("idle cap must be >= idle floor, got cap %s < floor %s", cfg.IdleCap, cfg.IdleFloor))
	}
	if cfg.FailureBackoff < 0 {
		return invalidConfig(em, cfg, fmt.Sprintf("failure backoff must be non-negative, got %s", cfg.FailureBackoff))
	}
	if cfg.BreakerThreshold <= 0 {
		return invalidConfig(em, cfg, fmt.Sprintf("breaker threshold must be a positive integer, got %d", cfg.BreakerThreshold))
	}
	if cfg.BreakerWindow <= 0 {
		return invalidConfig(em, cfg, fmt.Sprintf("breaker window must be positive, got %s", cfg.BreakerWindow))
	}

	p, pctx := newPool(ctx, cfg, r, em, clk)
	defer p.cancel()
	p.publishInitial()

	// A Stop already closed before Loop was even called must halt before
	// any slot starts, not merely once the background watcher below happens
	// to get scheduled: a bare `go` statement makes no ordering promise
	// against the slot goroutines started just after it. This non-blocking
	// check runs synchronously, in Loop's own goroutine, before any slot
	// exists to race it.
	select {
	case <-cfg.Stop:
		p.haltStopRequested("")
	default:
	}
	// Watches cfg.Stop for the rest of the pool's life, so a later close
	// halts promptly whether or not any slot is between iterations to
	// notice it itself (a slot deep in clk.Sleep or ResolveRevision only
	// unblocks once p.halt cancels pctx). Also selects on pctx.Done so this
	// goroutine never outlives one Loop call — tests drive Loop repeatedly,
	// and a leaked watcher from a previous call must not fire on a later
	// call's cfg.Stop. cfg.Stop is nil-safe: a nil channel never receives,
	// so an unset Stop makes this select equivalent to just waiting on
	// pctx.Done.
	go func() {
		select {
		case <-cfg.Stop:
			p.haltStopRequested("")
		case <-pctx.Done():
		}
	}()

	var wg sync.WaitGroup
	wg.Add(cfg.Slots)
	for slot := 0; slot < cfg.Slots; slot++ {
		go func(slot int) {
			defer wg.Done()
			runSlot(pctx, slot, cfg, p)
		}(slot)
	}
	wg.Wait()

	return p.haltReason()
}

// invalidConfig emits and returns a Halt of class HaltInvalidConfig carrying
// detail, the shape shared by every cfg field Loop rejects before starting a
// pool.
// No Kind is stamped on the halt event: this rejection happens before any
// pool exists to have picked one. It also publishes a StateHalted status
// directly, bypassing pool.snapshot (there is no pool yet to snapshot): cfg
// is otherwise unvalidated at this point, so cfg.Kinds is copied over as
// given rather than assumed well-formed.
func invalidConfig(em *Emitter, cfg Config, detail string) Halt {
	h := Halt{Class: HaltInvalidConfig, Detail: detail}
	em.Emit(h.Event())
	if cfg.Status != nil {
		if err := cfg.Status.Write(Status{Kinds: cfg.Kinds, State: StateHalted, Reason: h.String()}); err != nil {
			fmt.Fprintf(emitErrW, "daemon: status file write failed: %v\n", err)
		}
	}
	return h
}

// runSlot drives one pool slot's children until the pool halts, either
// because this slot decided to halt it or because a sibling did. ctx is the
// pool's own derived context (not the caller's ctx directly): cancelling it
// is how the pool tells every slot to stop promptly, including one asleep
// in clk.Sleep or blocked inside ResolveRevision.
//
// All waiting lives at the top of the loop, via pickKind/idleSleep, rather
// than inline in the Wait case below: with two kinds sharing a slot, a Wait
// on one kind must never sleep in place, since "an empty work queue does not
// slow research down" (issue #3541) — the Wait case only records the no-work
// result and loops back around, and it is the next pickKind that decides
// whether that means switching kinds or genuinely idling.
func runSlot(ctx context.Context, slot int, cfg Config, p *pool) {
	// The catch-all for every return: whatever route a round ends by (a
	// halt, a cancelled ctx, a self-build mismatch), the baton must not
	// strand a sibling on a token this slot will now never pass any other
	// way. passBaton no-ops unless slot actually holds the baton, so this
	// defer needs no guard of its own.
	defer p.passBaton(slot, batonPassStopped)
	// A slot that has left this function is doing nothing at all, whatever
	// phase it last published (issue #3623): one returning mid-resolve would
	// otherwise keep reporting "resolving", and siblingsEngaged would go on
	// counting an exited slot as engaged, suppressing a sibling's real jam.
	// Declared after passBaton so LIFO runs it first — the reset lands before
	// the baton is freed, so the sibling that hand-off wakes never reads this
	// slot as still resolving.
	defer p.setPhase(slot, PhaseIdle)
	var lastRevision string // the revision this slot's last child ran at; the "tip moved" baseline
	for {
		// The pool's one admission check (issue #3626): with cfg.Stop
		// riding every ChildRequest, a child started after Stop closes is
		// signalled by the Runner at once, so nothing between here and
		// RunChild needs to re-assert "stop starting new work".
		if p.haltIfStopping(ctx, "") {
			return
		}

		p.awaitWindow(ctx, slot)

		// Acquired after the Awake window, not before: apart from the
		// pre-assigned initial holder's very first round, a slot never
		// parks in awaitWindow holding the baton — acquisition happens
		// here, after awaitWindow, and every path that loops back to the
		// top passes the baton first (see the release sites below).
		p.awaitBaton(ctx, slot)

		kind, ok := p.pickKind(slot)
		if !ok {
			// Every configured kind has backed off into an empty result:
			// this is the daemon genuinely idling, not merely a kind this
			// slot happens not to prefer right now. A slot must never sleep
			// out an idle wait holding the baton.
			p.passBaton(slot, batonPassIdle)
			p.idleSleep(ctx, slot, lastRevision)
			continue
		}

		// Resolving covers checkSelfBuild's SelfPath call too, below: both
		// are outside-world evaluations at the fetched tip, and a slot
		// inside either is no more idle than one inside a fetch — nothing
		// between here and startChild (or a failure exit) changes phase
		// again, so one setPhase covers the whole span.
		p.setPhase(slot, PhaseResolving)
		revision, err := p.r.ResolveRevision(ctx)
		if err != nil {
			// A failed fetch is exactly the transient blip this slice's
			// breaker exists for: back off and retry alone, unless enough
			// failures have piled up pool-wide to say this is systemic
			// (backoffOrHalt below).
			if p.backoffOrHalt(ctx, slot, kind, "", fmt.Sprintf("resolve-revision: %v", err)) {
				return
			}
			continue
		}
		lastRevision = revision

		switch p.checkSelfBuild(ctx, slot, kind, revision) {
		case selfStop:
			return
		case selfRetry:
			// The evaluation failed and this slot has already backed off.
			// Restart the iteration rather than re-checking against the
			// revision resolved before that wait: every other backoff on
			// this path re-fetches too, and pinning a child to a tip
			// resolved a backoff ago is the staleness the per-iteration
			// fetch exists to avoid.
			continue
		}

		if !cfg.Awake.Open(p.clk.Now()) {
			// ResolveRevision (a git fetch) can outlast the window's own
			// close; re-check here so that fetch never launches a child
			// outside the window. No event here: awaitWindow's
			// edge-triggered noteAwakeClose reports the transition when
			// the slot parks on the next iteration. The holder is about to
			// loop back into awaitWindow for the whole shut span, so the
			// baton passes now rather than holding the pool through it.
			p.passBaton(slot, batonPassWindowClosed)
			continue
		}

		p.startChild(slot, kind, revision)
		req := ChildRequest{
			Slot:     slot,
			Kind:     kind,
			Revision: revision,
			Stop:     cfg.Stop,
			Abort:    cfg.Abort,
			OnIssue: func(issue string) {
				p.noteIssue(slot, issue)
				// A claim settles discovery the moment it happens, live,
				// rather than waiting for this child to exit.
				p.passBaton(slot, batonPassClaimed)
			},
		}
		result, err := p.r.RunChild(ctx, req)
		p.finishChild(slot)
		// One site covers every post-child exit without a claim at once:
		// queue empty, none dispatchable, an unrecognised exit, or a
		// RunChild seam error. A no-op when OnIssue already passed the
		// baton above.
		p.passBaton(slot, batonPassChildEnded)
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
				p.emit(Event{Event: "box", Kind: kind, Issue: issue, Revision: revision, Slot: intPtr(slot)})
			}
			p.emit(Event{Event: "child_finish", Kind: kind, Revision: revision, Outcome: "error", Slot: intPtr(slot)})
			if p.backoffOrHalt(ctx, slot, kind, revision, fmt.Sprintf("run-child: %v", err)) {
				return
			}
			continue
		}

		for _, issue := range result.Issues {
			p.emit(Event{Event: "box", Kind: kind, Issue: issue, Revision: revision, Slot: intPtr(slot)})
		}

		exit := result.Exit
		// Exit 7 means "the operator stopped this child" only while Stop
		// was already closed when the child exited; read the latch once,
		// here, rather than let a later close race Interpret's answer.
		outcome, action, haltClass := Interpret(exit, p.stopClosed())
		p.emit(Event{Event: "child_finish", Kind: kind, Revision: revision, Exit: &exit, Outcome: outcome, Slot: intPtr(slot)})

		switch action {
		case Continue:
			// Exit 0 (dispatched) or exit 4 (image-stale): the check
			// answered something other than "nothing to do", so whatever
			// streak of no-work checks this kind's backoff was tracking is
			// over. The other kind's own timer, if any, is untouched.
			p.resetKind(kind)
			continue
		case Wait:
			if !cfg.Awake.Open(p.clk.Now()) {
				// The window closed while the child ran, so this wait is
				// the window's, not the idle backoff's: recording a no-work
				// result here would spend a backoff step on a span the shut
				// window already covers. awaitWindow, at the top, parks the
				// whole remaining span in one sleep instead, and this kind's
				// idle streak stays untouched so it resumes where it left
				// off.
				continue
			}
			// noteWaitResult records the no-work result and decides the
			// jam-vs-idle predicate together, in one mutate — see its own
			// doc for why the two must not be read from two different
			// instants.
			p.noteWaitResult(slot, kind, revision, outcome == outcomeNoneDispatchable)
			// No sleep here: this kind is now gated until its markNoWork
			// deadline, and the top of the loop's pickKind/idleSleep decides
			// whether that means switching to the other kind at once or
			// genuinely idling.
			continue
		case Backoff:
			if p.backoffOrHalt(ctx, slot, kind, revision, fmt.Sprintf("outcome: %s (exit %d)", outcome, exit)) {
				return
			}
			continue
		default: // HaltPool
			// haltClass is never HaltNone here: Interpret's one table ties
			// action == HaltPool to a real class, pinned by
			// TestInterpret_HaltPoolIffHaltClassSet.
			p.halt(Halt{Class: haltClass, Kind: kind, Revision: revision})
			return
		}
	}
}

// intPtr returns a pointer to v. Event.Slot (like Event.Exit) is a pointer
// so slot/exit 0 — a real value — survives the field's omitempty tag
// instead of being elided as the zero value.
func intPtr(v int) *int {
	return &v
}
