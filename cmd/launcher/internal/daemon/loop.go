package daemon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/report"
)

// Tip is one resolution of the base branch's tip: the revision to pin the
// next child to, plus what that same fetch found out about the daemon's own
// build.
type Tip struct {
	Revision string
	SelfPath string // the daemon attribute's store path at Revision; empty when the self check is off (Runner has no self attr configured)
	Moved    bool   // Revision differs from the last one this Runner handed out
}

// SelfEvalError marks the self-path half of a ResolveTip call as the half
// that failed — the fetch itself succeeded (Tip.Revision is still valid),
// but evaluating the daemon attribute at that revision did not. Error()
// returns the wrapped error's text unchanged, so a self-build halt reason
// built from it carries the eval error verbatim.
type SelfEvalError struct{ Err error }

func (e *SelfEvalError) Error() string { return e.Err.Error() }
func (e *SelfEvalError) Unwrap() error { return e.Err }

// PrebuildStage names the stage of Runner.Prebuild that failed.
type PrebuildStage string

const (
	PrebuildStageBuild     PrebuildStage = "build"      // realising the new daemon
	PrebuildStageSmokeTest PrebuildStage = "smoke-test" // running it with --check-only
)

// PrebuildError marks which stage of Runner.Prebuild failed. Error() leads
// with the stage so the self_update blocked event's reason says which.
type PrebuildError struct {
	Stage PrebuildStage
	Err   error
}

func (e *PrebuildError) Error() string { return string(e.Stage) + ": " + e.Err.Error() }
func (e *PrebuildError) Unwrap() error { return e.Err }

// FeatureBranchGoneError is what Runner.ResolveTip returns on a definitive
// "absent" answer from the remote for the daemon's --feature-branch, not a
// fetch that merely failed (see HaltFeatureBranchGone).
type FeatureBranchGoneError struct{ Remote, Branch string }

func (e *FeatureBranchGoneError) Error() string {
	return fmt.Sprintf("feature branch %q no longer exists on %s — likely merged and deleted (or never pushed); restart the daemon without --feature-branch, or with a new branch", e.Branch, e.Remote)
}

// Runner is the daemon's one seam onto the outside world: resolve the
// current tip (the revision to pin the next child to, and what the
// daemon's own build would be at that revision) and run a child of a given
// Dispatch kind at a resolved revision.
type Runner interface {
	// ResolveTip fetches the base branch's current tip. An error that
	// unwraps to *SelfEvalError leaves Tip.Revision valid; every other error
	// (including *FeatureBranchGoneError) means the fetch failed and the
	// returned Tip is the zero value.
	ResolveTip(ctx context.Context) (Tip, error)
	// Demand counts the startable items a child of kind could claim. Only
	// asked for a kind with a Config.ProbeIntervals entry (ADR 0059). fresh
	// tells an adapter that caches to skip its cache and re-read. inFlight is
	// the keys the pool's own children hold at probe time: a source whose work
	// those children can hold (the host outbox) leaves them out of its count,
	// a tracker counter ignores it.
	Demand(ctx context.Context, kind Kind, fresh bool, inFlight dispatchkey.Set) (Demand, error)
	StartChild(ctx context.Context, req ChildRequest) (ChildHandle, error)
	// InheritChild rebuilds the handle of a child an earlier daemon image
	// started and handed off (ADR 0063); the handle behaves exactly as
	// StartChild's. ctx is never cancelled by a pool halt: the child is
	// already running, so a halt must not fail the re-attach.
	InheritChild(ctx context.Context, req ChildRequest, child ChildState) (ChildHandle, error)
	// Prebuild realises tip.SelfPath with a GC root and smoke-tests it
	// (--check-only), so a self-change halt only ever hands the service
	// manager a build already known to start. Only called when
	// tip.SelfPath differs from Config.SelfProgram. A failure unwraps to
	// *PrebuildError; ctx cancellation aborts it and is not a verdict on the
	// build.
	Prebuild(ctx context.Context, tip Tip) error
	// SweepLateMerges runs the throttled late-merge sweep at revision
	// (issue #7342), off the pool's slots. A failure only warns. ctx
	// outlives a pool halt; a hard abort, a committed in-place handoff, or
	// the caller's own cancellation ends it.
	SweepLateMerges(ctx context.Context, revision string) error
}

// ChildHandle is one started child. Only one Wait may run at a time, and once
// a Wait returns anything other than ErrWaitAbandoned the handle is spent and
// must not be waited on again. Wait delivers the child's records through
// the request's OnRecord and forwards its Stop/Abort latches while it waits;
// it returns the child's exit. Closing abandon stops the wait without
// signalling or reaping the child: Wait returns ErrWaitAbandoned and the
// child keeps running — the in-place restart (ADR 0063) hands the child on
// rather than waiting for it. A nil abandon never abandons.
type ChildHandle interface {
	Wait(abandon <-chan struct{}) (ChildResult, error)
	// State is the runner-level state a successor needs to re-attach this
	// child (ADR 0063). Only meaningful once a Wait has returned
	// ErrWaitAbandoned: the partial report line is stable only then.
	State() ChildState
}

// ErrWaitAbandoned is what ChildHandle.Wait returns when abandon closed first.
var ErrWaitAbandoned = errors.New("child wait abandoned")

// ChildRequest is one child invocation's parameters. Slot is the 0-based
// pool slot the child occupies.
//
// Stop and Abort are the operator-stop/hard-abort latch, carried as data on
// every request so a Runner needs no per-child bookkeeping to know whether
// the operator asked it to stop.
type ChildRequest struct {
	Slot     int
	Kind     Kind
	Revision string
	Stop     <-chan struct{}
	Abort    <-chan struct{}

	// ChildLog is the Child log file, relative to the checkout, that the
	// Runner hands the child as its stdout and stderr; child_start names the
	// same path.
	ChildLog string

	// OnRecord, when non-nil, is called with each Record the child reports
	// over its private pipe (internal/report), live as each line arrives: the
	// status file names a slot's in-flight issue from it (issue #3545), and it
	// is the only channel a "settled" record ever reaches.
	OnRecord func(rec Record)
}

// Clock is the loop's time seam: Now for the breaker's window, Sleep for
// the idle and backoff waits.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration)
}

// ChildResult is what one child invocation reports back: only the exit
// code, since every record already reached the stream through
// ChildRequest.OnRecord.
type ChildResult struct {
	Exit int
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

	// Reservation is the running-child count of the reserved-priority kind
	// (dispatchkind.PriorityReserved) below which a starting slot tries that
	// kind first. It never preempts a running child and holds no slot open:
	// the reserved kind is preferred only while it has queued work, and
	// either tier bursts into the whole pool when the other has backed off
	// into an empty result. Zero is normal-first with the reserved kind on
	// the leftovers; Slots is reserved-first. Must be 0 <= Reservation <=
	// Slots.
	Reservation int

	Slots int

	// LateMergeSweepInterval is how often the daemon runs the late-merge
	// sweep, outside the pool's slots and so never holding one (issue #7342).
	// Zero or negative runs no sweep.
	LateMergeSweepInterval time.Duration

	// Handoff, when non-nil, is the in-place restart document this pool resumes
	// from (ADR 0063): it occupies the Slots the document names and supervises
	// their inherited children as its own. nil is a cold start.
	Handoff *Handoff

	// RepoURL is the Target repo's web URL, published on every status write;
	// issue links are built from it plus the issue number. "" when the tracker
	// has no web repo.
	RepoURL string

	// Stop and Abort are the operator-stop/hard-abort latch (issue #3626).
	// A closed Stop halts the pool with HaltOperatorStop and cancels its
	// derived context, ending an in-progress sleep or fetch promptly. Abort
	// has no pool-level watcher — escalation is the child's business — and
	// only rides every ChildRequest. Nil opts out of either: a caller that
	// sets neither halts only through ctx.
	Stop  <-chan struct{}
	Abort <-chan struct{}

	// Awake gates when a slot may start a new child; nil is always awake. A
	// child already running when the window closes is never touched.
	Awake *Window

	// SelfProgram is the running daemon's own program store path. Before
	// starting a child, the loop compares it against Tip.SelfPath: a mismatch
	// means a newer daemon has merged, and orchestrating fresh Boxes from
	// this stale build risks running work under code nobody has loaded. The
	// loop then prebuilds and smoke-tests that build (Runner.Prebuild, issue
	// #5012); only a passing build acts, and a failing one is recorded as a
	// blocked self-update while the pool carries on until the tip moves. The
	// loop never re-execs itself: by default a passing build drains the pool
	// and halts self-changed, and the operator's service restart policy loads
	// it; RestartInPlace selects a handoff instead.
	//
	// Empty disables the check: a daemon that cannot know its own build must
	// never halt on a comparison it cannot make.
	SelfProgram string

	// RestartInPlace selects the in-place self-update mode (ADR 0063): a
	// passing prebuild stops new starts and waits for a clean moment to hand
	// off instead of draining; a stop requested meanwhile cancels the handoff
	// and drains as usual. No effect while SelfProgram is empty.
	RestartInPlace bool

	// SelfUpdate is the self-update state the pool starts in; zero is none.
	// An image whose in-place exec failed starts blocked at that revision,
	// so it is not rebuilt until the tip moves (ADR 0063, issue #5016).
	SelfUpdate SelfUpdate

	// IdleFloor and IdleCap bound each kind's own idle backoff: the first
	// no-work check (exit 2 or 3) waits IdleFloor, each further one doubles
	// it up to IdleCap, and a dispatch or image refresh resets it. Sibling
	// occupancy plays no part (kindBackoff.markNoWork); a reported kind's
	// exit 2 carrying a next-due report waits for that instead (kindSched).
	// IdleFloor must be positive; IdleCap must be >= IdleFloor.
	IdleFloor time.Duration
	IdleCap   time.Duration

	// ProbeIntervals names the kinds the pool schedules from tracker Demand
	// rather than child exits (ADR 0059), and how often each is re-probed.
	// A kind absent or at 0 stays exit-driven: its empty/jammed exits back it
	// off exactly as IdleFloor/IdleCap describe. For a probed kind those
	// bounds govern only its jam gate. Every key must be in Kinds and every
	// interval non-negative.
	ProbeIntervals map[Kind]time.Duration

	// Trackers names the tracker each kind's Demand counts against. Kinds
	// sharing a name share one rate-limit pause (ADR 0059); a kind with no
	// entry is its own tracker. Every key must be in Kinds.
	Trackers map[Kind]string

	// FailureBackoff is how long a slot sleeps before refilling itself
	// after an unclassified failure (an unrecognised exit code, a seam
	// error, or a ResolveTip error): the bad slot retries alone rather than
	// the whole pool stopping over one issue. Must be non-negative.
	FailureBackoff time.Duration
	// BreakerThreshold and BreakerWindow bound the pool-wide breaker: that
	// many unclassified failures from any slots within a trailing window
	// halts the pool — the signature of a systemic fault (an expired token,
	// a forge outage) no per-slot retry clears. Both must be positive.
	BreakerThreshold int
	BreakerWindow    time.Duration

	// Status, when non-nil, is where the pool publishes its live Status on
	// every state change (issue #3545). Nil is a deliberate opt-out, not a
	// swallowed error.
	Status *StatusWriter
}

// Loop runs cfg.Slots slot goroutines, each driving children of whichever
// configured kind it picks (Schedule.Decide), until a Halt-mapped exit code,
// the pool-wide breaker, or a cancelled ctx says halt. The first halt wins:
// Loop emits exactly one halt event and returns that reason.
//
// An unclassified failure (a resolve failure, a seam error, an unrecognised
// exit, or exit 7 while Stop is still open: someone else signalled that
// child) backs only its own slot off and reaches a halt only through the
// breaker — unless Stop has closed or ctx is cancelled (an operator SIGTERM
// reached a child before its handler was installed, or raced the seam's
// teardown), when it is an ordinary stop that never reaches the breaker
// (issue #3595; see backoffOrHalt).
//
// It never kills a child it has started: the slot waits on the handle and
// emits child_finish before halting, even after ctx cancellation or a sibling
// halt; the in-place handoff instead abandons the wait and leaves the child
// running. "Stop starting new work" is the admission check at the top of a
// slot's loop plus one re-check after awaitBaton, since a slot can park there
// arbitrarily long and the holder passes the baton only after its halt
// decision. Nothing later re-asserts it: Stop and Abort ride every
// ChildRequest, so the Runner signals a child started after Stop at once.
//
// Invalid config (including Slots <= 0, which would otherwise look like a
// healthy idle daemon) is a halt (see Config.Validate).
func Loop(ctx context.Context, cfg Config, r Runner, em *Emitter, clk Clock) Halt {
	if err := cfg.Validate(); err != nil {
		return invalidConfig(em, cfg, err.Error())
	}
	if cfg.Handoff != nil {
		// A latch the predecessor had already closed stays closed: the
		// closed-Stop path below then halts before any child starts, and
		// childRequest hands the closed latch to every inherited child.
		if cfg.Handoff.Stop {
			cfg.Stop = closedLatch()
		}
		if cfg.Handoff.Abort {
			cfg.Abort = closedLatch()
		}
	}

	p, pctx := newPool(ctx, cfg, r, em, clk)
	defer p.cancel()
	// The first status write already shows the inherited slots, so it stands
	// in for publishInitial.
	p.occupyInherited()
	if cfg.Handoff == nil {
		p.publishInitial()
	}

	// Checked synchronously so an already-closed Stop halts before any slot
	// starts: the watcher goroutine below has no ordering promise against
	// the slot goroutines.
	select {
	case <-cfg.Stop:
		p.haltStopRequested("")
	default:
	}
	// A slot deep in clk.Sleep or ResolveTip only unblocks once p.halt
	// cancels pctx, so a later Stop needs its own watcher. pctx.Done keeps
	// it from outliving this Loop call and firing on a later call's Stop.
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-cfg.Stop:
			p.haltStopRequested("")
		case <-pctx.Done():
		}
	}()

	var wg sync.WaitGroup
	sweeperDone := p.startLateMergeSweeper(pctx, ctx)
	wg.Add(cfg.Slots)
	for slot := 0; slot < cfg.Slots; slot++ {
		go func(slot int) {
			defer wg.Done()
			runSlot(pctx, slot, cfg, p)
		}(slot)
	}
	wg.Wait()
	// A prebuild still running outlives its slots (they return on the halt
	// that cancels its ctx); wait it out so no event follows Loop's return.
	p.bg.Wait()
	// The watcher can wake on cfg.Stop as the slots return, and its halt
	// still publishes a status write; join it so nothing follows Loop's
	// return.
	p.cancel()
	<-watcherDone
	<-sweeperDone

	if h := p.committedHandoff(); h != nil {
		return Halt{Handoff: h}
	}
	return p.haltReason()
}

func closedLatch() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// Validate is Loop's whole config check, pure over cfg: it emits nothing and
// writes no status. cmd/launcher/daemon's mainRun runs it first, so a resumed
// image whose config or handoff no longer fits (MAX_PARALLEL lowered, a kind
// dropped) rolls back to its predecessor before anything emits a halt (ADR
// 0063, issue #5039). Loop calls it again for embedders and superviseLoop's
// re-entry.
func (cfg Config) Validate() error {
	if cfg.Slots <= 0 {
		return fmt.Errorf("slots must be a positive integer, got %d", cfg.Slots)
	}
	if len(cfg.Kinds) == 0 {
		return errors.New("kinds must be non-empty")
	}
	seenKinds := make(map[Kind]bool, len(cfg.Kinds))
	for _, k := range cfg.Kinds {
		if _, ok := dispatchkind.ByVerb(string(k)); !ok {
			return fmt.Errorf("unknown kind %q", k)
		}
		if seenKinds[k] {
			return fmt.Errorf("duplicate kind %q", k)
		}
		seenKinds[k] = true
	}
	for _, k := range sortedKeys(cfg.ProbeIntervals) {
		if !seenKinds[k] {
			return fmt.Errorf("probe interval for kind %q, which is not in kinds", k)
		}
		if d := cfg.ProbeIntervals[k]; d < 0 {
			return fmt.Errorf("probe interval for kind %q must be non-negative, got %s", k, d)
		}
	}
	for _, k := range sortedKeys(cfg.Trackers) {
		if !seenKinds[k] {
			return fmt.Errorf("tracker for kind %q, which is not in kinds", k)
		}
	}
	if cfg.Reservation < 0 || cfg.Reservation > cfg.Slots {
		return fmt.Errorf("research reservation must be between 0 and slots (%d), got %d", cfg.Slots, cfg.Reservation)
	}
	if cfg.IdleFloor <= 0 {
		return fmt.Errorf("idle floor must be positive, got %s", cfg.IdleFloor)
	}
	if cfg.IdleCap < cfg.IdleFloor {
		return fmt.Errorf("idle cap must be >= idle floor, got cap %s < floor %s", cfg.IdleCap, cfg.IdleFloor)
	}
	if cfg.FailureBackoff < 0 {
		return fmt.Errorf("failure backoff must be non-negative, got %s", cfg.FailureBackoff)
	}
	if cfg.BreakerThreshold <= 0 {
		return fmt.Errorf("breaker threshold must be a positive integer, got %d", cfg.BreakerThreshold)
	}
	if cfg.BreakerWindow <= 0 {
		return fmt.Errorf("breaker window must be positive, got %s", cfg.BreakerWindow)
	}
	if cfg.Handoff != nil {
		if detail := validateHandoff(cfg); detail != "" {
			return errors.New(detail)
		}
	}
	return nil
}

// validateHandoff names the first defect in cfg.Handoff's slots and kinds, ""
// when it fits the pool: each slot inside [0, cfg.Slots), named once, of a
// configured kind, and each kind entry configured and named once. A slot's
// gate generation may not pass its kind's carried one (0 when the kind has no
// entry, since its counter restarts there): it cannot have started under a
// generation its kind never reached. A failure is a startup refusal: it runs
// before any InheritChild.
func validateHandoff(cfg Config) string {
	seenKind := make(map[Kind]bool, len(cfg.Handoff.Kinds))
	kindGen := make(map[Kind]int, len(cfg.Handoff.Kinds))
	for _, hk := range cfg.Handoff.Kinds {
		switch {
		case !slices.Contains(cfg.Kinds, hk.Kind):
			return fmt.Sprintf("handoff schedule state names kind %q, which is not in kinds", hk.Kind)
		case seenKind[hk.Kind]:
			return fmt.Sprintf("handoff names kind %q twice", hk.Kind)
		}
		seenKind[hk.Kind] = true
		kindGen[hk.Kind] = hk.Gen
	}
	seen := make(map[int]bool, len(cfg.Handoff.Slots))
	for _, hs := range cfg.Handoff.Slots {
		switch {
		case hs.Slot < 0 || hs.Slot >= cfg.Slots:
			return fmt.Sprintf("handoff slot %d is outside the pool's %d slots", hs.Slot, cfg.Slots)
		case seen[hs.Slot]:
			return fmt.Sprintf("handoff names slot %d twice", hs.Slot)
		case !slices.Contains(cfg.Kinds, hs.Kind):
			return fmt.Sprintf("handoff slot %d runs kind %q, which is not in kinds", hs.Slot, hs.Kind)
		case hs.GateGen > kindGen[hs.Kind]:
			return fmt.Sprintf("handoff slot %d started under gate generation %d, past kind %q's %d", hs.Slot, hs.GateGen, hs.Kind, kindGen[hs.Kind])
		}
		seen[hs.Slot] = true
	}
	return ""
}

// sortedKeys returns m's kinds in order, so config validation reports the same
// offender every run.
func sortedKeys[V any](m map[Kind]V) []Kind { return slices.Sorted(maps.Keys(m)) }

// invalidConfig emits and returns a HaltInvalidConfig Halt carrying detail.
// No pool exists yet, so the event carries no Kind and the StateHalted status
// is published directly with cfg.Kinds copied as given, unvalidated.
func invalidConfig(em *Emitter, cfg Config, detail string) Halt {
	h := Halt{Class: HaltInvalidConfig, Detail: detail}
	em.Emit(h.Event())
	em.PublishHalted(cfg.Status, cfg.Kinds, h.String())
	return h
}

// runSlot drives one pool slot's children until the pool halts. ctx is the
// pool's derived context: cancelling it is how the pool stops every slot
// promptly, including one in clk.Sleep or ResolveTip.
//
// All waiting lives at the top of the loop, via Decide/idleSleep: with two
// kinds sharing a slot, a Wait on one kind must never sleep in place, since
// "an empty work queue does not slow research down" (issue #3541).
func runSlot(ctx context.Context, slot int, cfg Config, p *pool) {
	// Whatever route a round ends by, the baton must not strand a sibling;
	// passBaton no-ops unless this slot holds it.
	defer p.passBaton(slot, batonPassStopped)
	// A slot returning mid-resolve would otherwise keep reporting
	// "resolving" (issue #3623). Deferred after passBaton so LIFO resets the
	// phase before the baton wakes a sibling.
	defer p.leaveSlot(slot)
	// Set by idleSleep when a park gated by a jammed kind or tip wait
	// outlived its first IdleFloor slice: the next round makes one
	// resolution before Decide. Only the request survives across rounds, not
	// a Tip: awaitWindow and awaitBaton in between can block for hours.
	var wantResolve bool
	// An inherited child is already running, so even a halting pool must
	// wait it out and report its child_finish.
	if h := p.inheritedSlot(slot); h != nil {
		req := p.childRequest(slot, h.Kind, h.Revision, h.ChildLog)
		// A halt that cancelled ctx must not abort the re-attach, or the
		// child's real exit would surface as an error.
		handle, err := p.r.InheritChild(context.WithoutCancel(ctx), req, h.Child)
		if p.superviseChild(ctx, slot, h.Kind, h.Revision, handle, err, seamInheritChild) {
			return
		}
	}
slotLoop:
	for {
		// The pool's admission check (issue #3626), re-asked once after
		// awaitBaton below.
		if p.haltIfStopping(ctx, "") {
			return
		}
		if p.parkIfHandingOff(ctx, slot) {
			return
		}

		p.awaitWindow(ctx, slot)

		// Resolved before Decide so a kind noteTipMoved just unblocked is
		// visible this round. A failure here is swallowed; the post-Decide
		// resolve below reports a genuinely broken fetch.
		var tip Tip
		var haveTip bool
		if wantResolve {
			wantResolve = false
			if t, ok := p.resolveOpportunistic(ctx, slot); ok {
				tip, haveTip = t, true
			}
		}

		// kind is provisional until startChild re-decides it below; the
		// resolve-failure and halt paths in between label this one.
		//
		// The one place a probe may run: the window is open and this slot is
		// free.
		var kind Kind
	decide:
		for {
			switch d := p.decide(p.clk.Now()).(type) {
			case start:
				kind = d.Kind
				break decide
			case probe:
				switch p.probe(ctx, slot, d.Kinds) {
				case probeHalted:
					return
				case probeRetop:
					continue slotLoop
				}
			default:
				// Genuinely idle. A slot must never sleep out an idle wait
				// holding the baton. idleSleep publishes no phase, and
				// resolveOpportunistic above can have left PhaseResolving.
				p.setPhase(slot, PhaseIdle)
				p.passBaton(slot, batonPassIdle)
				wantResolve = p.idleSleep(ctx, slot)
				continue slotLoop
			}
		}

		if !haveTip {
			t, err := p.resolveTip(ctx, slot)
			if err != nil {
				// A gone feature branch halts outright, before any claim,
				// rather than taking the breaker/backoff path.
				var gone *FeatureBranchGoneError
				if errors.As(err, &gone) {
					// Bypasses haltIfStopping: this definitive answer wins
					// over an operator stop already in flight.
					p.halt(Halt{
						Class:  HaltFeatureBranchGone,
						Detail: gone.Error(),
						Kind:   kind,
					})
					return
				}
				// A failed fetch and a failed self-eval are both breaker
				// blips, under separate halt classes.
				revision, reason := resolveFailure(t, err)
				if p.backoffOrHalt(ctx, slot, kind, revision, reason, batonPassFailed) {
					return
				}
				continue
			}
			tip = t
		}
		revision := tip.Revision

		if p.cfg.SelfProgram != "" && tip.SelfPath != p.cfg.SelfProgram {
			// Never re-execs here, so a freshly merged but broken daemon
			// cannot auto-load with nobody awake: a self-change halts (or,
			// in-place, hands off) only after selfUpdate's prebuild passes.
			p.selfUpdate(ctx, kind, tip)
		} else {
			p.clearBlockedSelfUpdate()
		}

		// The baton is a discovery token, acquired only after the resolve:
		// acquiring it earlier would serialize the tip resolutions
		// ResolveTip's single-flight coalesces (issue #3625). Apart from the
		// pre-assigned initial holder's first round, a slot never holds the
		// baton across a resolve: every path back to the top passes it first.
		// A Chore-keyed kind skips it: the Ledger's compare-and-swap settles
		// its races and it selects no tracker issue.
		//
		// The re-check matters because awaitBaton also returns on ctx.Done,
		// and the holder passes only after its halt decision (issue #4365).
		if !kind.choreKeyed() {
			p.awaitBaton(ctx, slot)
		}
		if p.haltIfStopping(ctx, kind) {
			return
		}

		if !cfg.Awake.Open(p.clk.Now()) {
			// ResolveTip can outlast the window's close. No event here:
			// awaitWindow reports the transition on the next iteration.
			// The baton passes now rather than across the whole shut span.
			p.passBaton(slot, batonPassWindowClosed)
			continue
		}

		kind, childLog, started := p.startChild(slot, kind, revision)
		if !started {
			// Outpaced while parked on the baton, or a Chore-keyed non-holder
			// refused an issue-keyed kind.
			p.passBaton(slot, batonPassOutpaced)
			continue
		}
		if kind.choreKeyed() {
			// Held here only by the pre-assigned leader or after startChild
			// switched kinds; a Chore discovers no issue.
			p.passBaton(slot, batonPassChoreKeyed)
		}
		req := p.childRequest(slot, kind, revision, childLog)
		handle, err := p.r.StartChild(ctx, req)
		if p.superviseChild(ctx, slot, kind, revision, handle, err, seamRunChild) {
			return
		}
	}
}

// childRequest builds the request for a child of kind pinned to revision on
// slot, wiring its live records into the pool's bookkeeping.
func (p *pool) childRequest(slot int, kind Kind, revision, childLog string) ChildRequest {
	cfg := p.cfg
	return ChildRequest{
		Slot:     slot,
		Kind:     kind,
		Revision: revision,
		Stop:     cfg.Stop,
		Abort:    cfg.Abort,
		ChildLog: childLog,
		OnRecord: func(rec Record) {
			// image_build comes from bootstrap, before discovery, so it must not
			// count as the report that permits an in-place handoff.
			if rec.Event != report.EventImageBuild {
				p.noteReported(slot)
			}
			p.noteRecord(slot, kind, revision, rec)
			if rec.Event == report.EventBox {
				// A claim settles discovery the moment it happens.
				p.passBaton(slot, batonPassClaimed)
			}
		},
	}
}

// childSeam names the Runner seam a supervised child came through; it prefixes
// the failure reason when that seam errors.
type childSeam string

const (
	seamRunChild     childSeam = "run-child"
	seamInheritChild childSeam = "inherit-child"
)

// superviseChild waits out a child on slot and carries its outcome through the
// pool's bookkeeping: the flight, the baton, the kind's backoff state and any
// halt. handle and err are StartChild's (or InheritChild's) results; seam
// names which one for the failure reason. It reports whether the slot must
// stop (true) or loop around to its next round.
func (p *pool) superviseChild(ctx context.Context, slot int, kind Kind, revision string, handle ChildHandle, err error, seam childSeam) bool {
	cfg := p.cfg
	var result ChildResult
	if err == nil {
		result, err = handle.Wait(p.abandon)
		if errors.Is(err, ErrWaitAbandoned) {
			// The child is still running and its flight stays in place for
			// the handoff: no child_finish, and not a failure.
			p.noteAbandoned(slot, handle.State())
			return true
		}
	}
	// Read before finishChild zeroes the flight, the last point the claimed
	// key is still available.
	key := p.flightClaim(slot)
	flight := p.flightReport(slot)
	p.finishChild(slot)
	// Every post-child exit passes the baton only after the round's halt
	// decision, or a parked sibling could start a child on a pool about to
	// stop (issue #4365), and only after folding the result into the kind's
	// backoff, or a woken sibling's Decide sees an earned gate as still open
	// (issue #4380). HaltPool relies on the deferred batonPassStopped.
	if err != nil {
		// The seam failed, not the child, so there is no exit code — but
		// child_start/child_inherited was already emitted and every such
		// child gets a matching child_finish.
		p.emit(Event{Event: report.EventChildFinish, Kind: kind, Key: key, Revision: revision, Outcome: "error", Slot: intPtr(slot)})
		return p.backoffOrHalt(ctx, slot, kind, revision, fmt.Sprintf("%s: %v", seam, err), batonPassChildEnded)
	}

	exit := result.Exit
	// Exit 7 means "the operator stopped this child" only while Stop was
	// already closed; read the latch once so a later close cannot race it.
	outcome, action, haltClass := Interpret(exit, p.stopClosed())
	p.emit(Event{Event: report.EventChildFinish, Kind: kind, Key: key, Revision: revision, Exit: &exit, Outcome: outcome, Slot: intPtr(slot)})

	switch action {
	case Continue:
		p.resetKind(kind)
		p.passBaton(slot, batonPassChildEnded)
		return false
	case Wait:
		if !cfg.Awake.Open(p.clk.Now()) {
			// The window closed while the child ran: this wait is the
			// window's, so it spends no backoff step and the kind's idle
			// streak resumes where it left off.
			p.passBaton(slot, batonPassChildEnded)
			return false
		}
		p.noteWaitResult(slot, kind, revision, outcome == outcomeNoneDispatchable, flight)
		p.passBaton(slot, batonPassChildEnded)
		return false
	case Backoff:
		return p.backoffOrHalt(ctx, slot, kind, revision, fmt.Sprintf("outcome: %s (exit %d)", outcome, exit), batonPassChildEnded)
	default: // HaltPool
		// haltClass is never HaltNone here
		// (TestInterpret_HaltPoolIffHaltClassSet).
		p.halt(Halt{Class: haltClass, Kind: kind, Revision: revision})
		return true
	}
}

// intPtr returns a pointer to v, so slot/exit 0 survives omitempty on
// Event.Slot and Event.Exit.
func intPtr(v int) *int {
	return &v
}
