package console

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
	"spindrift.dev/launcher/internal/waves"
)

// Launcher carries the dependencies Run needs to actually drive a picked
// issue through the continuous engine, beyond the IssueTracker seam Run
// already has. A nil Launcher passed to Run disables launching entirely — a
// pick still promotes and queues, but nothing ever runs — for callers (and
// tests) that only exercise the Pick/Unpick bookkeeping.
//
// Every lazily-constructed field below has an accessor (limiter, queueRef,
// registry, terminating) so a bare struct literal needs no constructor;
// methods must go through the accessor, never the raw field.
type Launcher struct {
	CodeForge forge.CodeForge
	Factory   *dispatch.Factory
	Settle    settle.Settler
	// The research-kind (ADR 0022) mirror of CodeForge/Factory/Settle. A
	// KindResearch pick promotes and launches through these because
	// ResearchTracker carries the fixed agent-research label family, which a
	// plain TransitionState call can't select per-call. Nil means no research
	// stack is wired: a KindResearch pick's promotion falls back to the
	// caller-supplied work tracker, but drain's stacks() yields only the work
	// stack, so it sits at PickQueued rather than launching.
	ResearchTracker forge.IssueTracker
	ResearchFactory *dispatch.Factory
	ResearchSettle  settle.Settler
	// MaxParallel sets the live cap's *starting* value only (1 unless
	// positive). The cap actually enforced during a session lives in
	// l.limiter()'s *waves.Limiter and moves at runtime via
	// Resize/ResizeDelta (ADR 0023).
	MaxParallel int
	// FailedLabel is the tracker label that marks a blocker issue Failed —
	// threaded into Queue.Discover's held-pick check so a failed blocker
	// surfaces on the held row instead of silently staying "open".
	FailedLabel string
	// Fresh answers whether the loaded image is stale against the current
	// base-branch tip. Nil falls back to "always fresh, not applicable".
	Fresh waves.FreshnessChecker
	// RebuildFn rebuilds and reloads the image; nil makes Rebuild a no-op.
	// It returns the rebuild's captured nix output and a branch-switch notice
	// ("" when pwd's checkout didn't move off the branch it was on) alongside
	// the error, so a background rebuild never writes directly to the
	// Console's own stdout/stderr.
	RebuildFn func() (string, string, error)
	// RecoverFn adopts an orphaned issue's abandoned PR through the settle
	// adoption path. Wired by cmdConsole in main.go, since console cannot
	// import the main package. Nil skips orphan detection entirely.
	RecoverFn func(issueNum string) error

	mu        sync.Mutex
	launching bool
	wg        sync.WaitGroup
	refresh   chan struct{}
	// pendingSnapshot is the queue snapshot signalRefresh most recently
	// recorded, delivered once a waiter drains refresh. Pairs with hasPending
	// so TakePendingSnapshot can tell "nothing pending yet" apart from a
	// genuine empty queue.
	pendingSnapshot []Pick
	hasPending      bool
	// queue is the session's private operator queue — Pick, Unpick, and Land
	// are its sole outside mutators; every other transition (claim, settle,
	// terminate) is one of Launcher's own methods.
	queue *Queue
	// stale and staleMessage record the last stale verdict a drain saw —
	// read by StaleStatus for the console's banner. staleMessage is updated
	// on every freshnessChecker() call, stale (and rebuilding/rebuildErr)
	// only by drain/Rebuild themselves.
	stale        bool
	staleMessage string
	rebuilding   bool
	rebuildErr   error
	// rebuildOutput is the last rebuild's captured nix output — stdout/stderr
	// merged, in build order, bounded to the tail the runner package's output
	// cap enforces — set on every RebuildFn completion regardless of outcome
	// so an operator can retrieve it through StaleStatus. A failed rebuild's
	// output stays until the *next* rebuild overwrites it, rather than being
	// cleared right away: an operator debugging the failure needs it to
	// survive the error banner.
	rebuildOutput string
	// branchSwitchNotice is "" when pwd's checkout didn't move off the branch
	// it was on. Set on every RebuildFn completion, same as rebuildOutput.
	branchSwitchNotice string
	// lastStaleDrainSummary is the last stale-drain report's rendered one-line
	// summary, "" until a drain is reported and again once a successful
	// Rebuild clears it. Distinct from staleMessage: staleMessage describes
	// the *ongoing* stale gate (why launches are held right now), while this
	// is a retrospective report of what a drain cost in idle slot-time and
	// held-back work — information that itself goes stale once a later
	// rebuild resolves the staleness, unlike rebuildOutput.
	lastStaleDrainSummary string
	// pollInterval overrides Run's default background poll cadence — unset in
	// production, so only same-package tests override defaultPollInterval.
	pollInterval time.Duration
	// terminated is the shared registry Terminate marks and RunContinuous /
	// Settle check at their loop checkpoints (ADR 0024).
	terminated *terminate.Registry
	// terminatingNums tracks issue numbers with a TerminateAsync goroutine
	// still in flight, guarding against a second confirm firing a duplicate
	// Terminate: the queue pick stays PickRunning — and isLive keeps
	// reporting it live — for the whole async call.
	terminatingNums map[string]bool
	// cap is the session's live, resizable parallelism cap (ADR 0023) — one
	// Limiter shared across every drain() this Launcher runs, so a Console
	// "+"/"-" takes effect on the RunContinuous call already in flight, not
	// just the next one.
	cap *waves.Limiter
}

// limiter lazily constructs l.cap at the MaxParallel starting cap, 1 when
// unset.
func (l *Launcher) limiter() *waves.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cap == nil {
		maxParallel := l.MaxParallel
		if maxParallel <= 0 {
			maxParallel = 1
		}
		l.cap = waves.NewLimiter(maxParallel)
	}
	return l.cap
}

// queueRef lazily constructs l.queue, so no method can observe a nil Queue.
func (l *Launcher) queueRef() *Queue {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.queue == nil {
		l.queue = NewQueue()
	}
	return l.queue
}

// Snapshot returns the session's current queue state — the tea layer's
// one-time startup bootstrap (Init's initialQueueSyncCmd) and the sole
// legitimate outside read of the private queue's full contents.
func (l *Launcher) Snapshot() []Pick {
	return l.queueRef().Snapshot()
}

// Pick promotes num through PickIssue and lands the result on the private
// queue, returning both the outcome Msg and the queue's fresh snapshot in the
// same call, so the tea side updates Model.Picks in the same Update cycle it
// fired the keypress rather than a render behind.
func (l *Launcher) Pick(tracker forge.IssueTracker, num, title string, kind Kind) (Msg, []Pick) {
	msg := PickIssue(l.trackerFor(kind, tracker), num, title, kind)
	return msg, l.Land(msg)
}

// trackerFor returns l.ResearchTracker for a KindResearch pick when one is
// wired, or workTracker otherwise, so a promotion or claim's TransitionState
// call lands on the tracker instance carrying the matching label family.
func (l *Launcher) trackerFor(kind Kind, workTracker forge.IssueTracker) forge.IssueTracker {
	if kind == KindResearch && l.ResearchTracker != nil {
		return l.ResearchTracker
	}
	return workTracker
}

// Land applies an already-resolved pick-outcome Msg (PickQueuedMsg or
// PickDissolvedMsg) onto the private queue and returns the fresh snapshot.
// It is split out of Pick so a bulk scan, which already resolved every
// issue's tracker transition in one ListIssues round trip, doesn't repeat
// PickIssue's terminal-state checks per issue. A failed promotion lands its
// dissolved row exactly as a queued one does, so the operator's only feedback
// that a pick raced, closed, or got relabeled survives the next snapshot push.
func (l *Launcher) Land(msg Msg) []Pick {
	switch m := msg.(type) {
	case PickQueuedMsg:
		l.queueRef().Add(Pick{Number: m.Number, Title: m.Title, Kind: m.Kind, State: PickQueued})
	case PickDissolvedMsg:
		l.queueRef().Add(Pick{Number: m.Number, Title: m.Title, State: PickDissolved, Reason: m.Reason})
	default:
		return l.queueRef().Snapshot()
	}
	// A promotion attempt is always a tracker write, win or lose, so it
	// triggers the same out-of-band refresh every other session write does.
	l.signalRefresh()
	return l.queueRef().Snapshot()
}

// Unpick retracts num's queued-but-unlaunched pick from the private queue
// and returns the fresh snapshot synchronously — a pure session-queue edit
// with no tracker interaction (ADR 0023). Queue.Remove refuses to drop
// anything past PickQueued/PickHeld, so this is safe even when num never
// queued or already launched.
func (l *Launcher) Unpick(num string) []Pick {
	l.queueRef().Remove(num)
	return l.queueRef().Snapshot()
}

// Cap returns the session's current live parallelism cap.
func (l *Launcher) Cap() int {
	return l.limiter().Cap()
}

// Live returns the number of Dispatches this session currently has running.
func (l *Launcher) Live() int {
	return l.limiter().Live()
}

// LiveIssues returns the issue numbers of every pick this session currently
// has PickRunning. Callers read this rather than the Limiter's live count: a
// settle marks the queue pick Settled before releasing the Limiter slot, so
// this can never observe "no live Dispatches" a moment before the Limiter
// itself agrees.
func (l *Launcher) LiveIssues() []string {
	var nums []string
	for _, p := range l.queueRef().Snapshot() {
		if p.State == PickRunning {
			nums = append(nums, p.Number)
		}
	}
	return nums
}

// OrphanedIssues returns the issue numbers of every sandbox still running
// under the deterministic agent-issue-<N> naming scheme with nothing in this
// fresh process tracking it — the signature of a hard death (crash, dropped
// SSH) in a prior session. A Launcher built without a Factory reports none.
func (l *Launcher) OrphanedIssues() ([]string, error) {
	if l.Factory == nil {
		return nil, nil
	}
	return l.Factory.OrphanedIssues()
}

// Driver returns the Driver l.Factory was constructed with, or nil when a
// Launcher was built without a Factory.
func (l *Launcher) Driver() driver.Driver {
	if l.Factory == nil {
		return nil
	}
	return l.Factory.Driver()
}

// defaultPollInterval is the background backlog poll's cadence — slow enough
// never to spend the rate-limit window the session's Agents share.
const defaultPollInterval = 3 * time.Minute

// PollInterval returns l.pollInterval when a test has overridden it, or
// defaultPollInterval otherwise.
func (l *Launcher) PollInterval() time.Duration {
	if l.pollInterval > 0 {
		return l.pollInterval
	}
	return defaultPollInterval
}

// Resize adjusts the live parallelism cap by delta, clamped to at least 1.
// Raising it takes effect immediately — a held pick launches into the freed
// slot without waiting for a running Dispatch to settle. Lowering it never
// terminates a running Dispatch; it only gates new launches until the live
// count sinks under the new cap on its own (ADR 0023).
func (l *Launcher) Resize(delta int) {
	l.limiter().ResizeDelta(delta)
}

// registry lazily constructs l.terminated and, the first time, wires it into
// l.Settle when that Settle is a concrete *settle.Settle (a settle.Fake has
// no loop to check, so the wiring is skipped harmlessly). Both tryLaunch's
// drain (via waves.Session.Terminated) and Terminate itself share the one
// Registry this returns.
//
// The Registry keys termination on a per-number *generation* counter rather
// than a bool, because a bool cannot tell "a stale mark from a dead
// incarnation" apart from "a still-live settle goroutine hasn't checked yet".
// Every checkpoint a dispatch makes carries the generation it was launched
// under (waves.Issue.Generation) and checks against that generation
// specifically, so a re-pick's Begin can never erase a live settle's own
// mark, and a stale post-settle setState from an old incarnation can never
// land on the re-pick's row (Queue.setState stops at the newest match on
// number).
func (l *Launcher) registry() *terminate.Registry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminated == nil {
		l.terminated = terminate.NewRegistry()
		if s, ok := l.Settle.(*settle.Settle); ok {
			s.SetTerminated(l.terminated)
		}
	}
	return l.terminated
}

// Terminate ends num's live Dispatch by hand (ADR 0024): reaps any running
// Box, marks the shared registry so an in-flight settle loop abandons at its
// next checkpoint, transitions the issue InProgress -> Dispatchable (never
// Failed — the operator decided, there is nothing to triage), comments naming
// the terminate and any dangling branch/PR, appends a terminal line to the
// Box log, and marks the queue pick PickTerminated. Pushed branches and open
// PRs are left untouched; a later re-pick adopts an abandoned PR through the
// settle adoption path. Best-effort throughout except the reap, whose error
// is returned so a caller can surface it — every other step runs regardless.
func (l *Launcher) Terminate(tracker forge.IssueTracker, num string) error {
	l.registry().Mark(num)

	var killErr error
	if l.Factory != nil {
		killErr = l.Factory.Kill(num)
		if killErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: kill: %v\n", num, killErr)
		}
		if err := l.Factory.AppendTerminalLine(num, "terminated by operator; issue returned to Dispatchable"); err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: append log line: %v\n", num, err)
		}
	}

	danglingNote := "no open branch/PR found"
	if l.CodeForge != nil {
		branch := l.CodeForge.AgentBranch(num)
		if res, err := forge.ResolveOpenPR(l.CodeForge, num); err == nil && res.Found {
			danglingNote = res.URL
		} else if branch != "" {
			danglingNote = fmt.Sprintf("no open PR found; branch=%s", branch)
		}
	}

	// Which label the issue actually wears depends on the phase Terminate
	// caught: InProgress for a running Box, a CI watch, or anywhere on the
	// landing path, and Complete if Terminate lands just after settling.
	// TransitionState is an unconditional swap with no compare-and-swap, so
	// both calls run regardless: removing an absent label is a no-op on every
	// adapter, and adding Dispatchable twice is idempotent.
	if err := tracker.TransitionState(num, forge.InProgress, forge.Dispatchable); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: transition to Dispatchable: %v\n", num, err)
	}
	if err := tracker.TransitionState(num, forge.Complete, forge.Dispatchable); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: clear Complete: %v\n", num, err)
	}
	comment := fmt.Sprintf("Terminated by operator: reclaimed back to Dispatchable. %s", danglingNote)
	if err := tracker.Comment(num, comment); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: post comment: %v\n", num, err)
	}

	l.queueRef().setState(num, PickTerminated, "terminated by operator")
	l.signalRefresh()
	return killErr
}

// TerminateAsync runs Terminate for num in the background so the operator's
// confirm key returns immediately instead of blocking the Update loop on
// tracker I/O, returning the queue's snapshot as it stands at initiation. num
// already in flight makes a second call a no-op: the queue pick stays
// PickRunning until Terminate sets PickTerminated at the very end, so isLive
// keeps reporting num live for the whole call — a second confirm on the same
// row would otherwise race a duplicate Kill/Comment/TransitionState. The
// PickTerminated transition itself reaches the Model through Terminate's
// pushed refresh signal, not this call's return value.
func (l *Launcher) TerminateAsync(tracker forge.IssueTracker, num string) []Pick {
	// Two short critical sections, not one: terminating() takes l.mu to
	// lazily construct the map, then this function re-takes it for the
	// check-and-set. Safe because the atomicity that matters is the
	// check-and-set itself, not its adjacency to the lazy-init.
	inFlight := l.terminating()

	l.mu.Lock()
	if inFlight[num] {
		l.mu.Unlock()
		return l.queueRef().Snapshot()
	}
	inFlight[num] = true
	l.wg.Add(1)
	l.mu.Unlock()

	go func() {
		defer l.wg.Done()
		// Return value dropped intentionally: Terminate already logs its kill
		// failure to stderr before returning it.
		l.Terminate(tracker, num)

		l.mu.Lock()
		delete(inFlight, num)
		l.mu.Unlock()
	}()

	return l.queueRef().Snapshot()
}

func (l *Launcher) terminating() map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminatingNums == nil {
		l.terminatingNums = make(map[string]bool)
	}
	return l.terminatingNums
}

// refreshChan lazily constructs l.refresh, buffered to exactly one slot: a
// burst of writes (claim, settle, promotion) coalesces into a single pending
// refresh instead of queuing one per write.
func (l *Launcher) refreshChan() chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refresh == nil {
		l.refresh = make(chan struct{}, 1)
	}
	return l.refresh
}

// signalRefresh marks a refresh pending and records the queue's current
// snapshot for TakePendingSnapshot to deliver — called after every write this
// session makes to the tracker or queue, so Run's select loop re-queries the
// backlog and the tea side lands the latest transition without pulling Queue
// itself. The wake is a non-blocking one-slot send, but pendingSnapshot
// always holds the most recent snapshot whether or not a wake was already
// pending, so a burst of writes delivers the latest state, never a stale
// intermediate one.
func (l *Launcher) signalRefresh() {
	picks := l.queueRef().Snapshot()
	l.mu.Lock()
	l.pendingSnapshot = picks
	l.hasPending = true
	l.mu.Unlock()

	select {
	case l.refreshChan() <- struct{}{}:
	default:
	}
}

// TakePendingSnapshot returns the most recent queue snapshot signalRefresh
// recorded and clears the pending flag, reporting whether one was actually
// pending. It is the sole outside read of the private queue's live state
// after startup.
func (l *Launcher) TakePendingSnapshot() ([]Pick, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	picks := l.pendingSnapshot
	had := l.hasPending
	l.pendingSnapshot = nil
	l.hasPending = false
	return picks, had
}

// Refreshes returns the channel Run selects on for background-write-triggered
// refreshes.
func (l *Launcher) Refreshes() <-chan struct{} {
	return l.refreshChan()
}

// tryLaunch starts draining Queue through waves.RunContinuous in the
// background, unless a drain is already running or Queue has nothing left to
// launch. RunContinuous's refill-on-completion picks up any pick Add()ed
// while that drain is in flight, so a second concurrent invocation is never
// needed, only a fresh one once the queue has gone idle. The background poll
// tick calls this every interval regardless of queue state — see Queue.Empty
// for why the gate must cover PickHeld as well as PickQueued.
func (l *Launcher) tryLaunch(tracker forge.IssueTracker, pwd string) {
	if l.queueRef().Empty() {
		return
	}

	l.mu.Lock()
	if l.launching {
		l.mu.Unlock()
		return
	}
	l.launching = true
	l.wg.Add(1)
	l.mu.Unlock()

	go l.drain(tracker, pwd)
}

// launchStack pairs one Dispatch kind's tracker, dispatch factory, and
// settle, so a KindResearch pick launches and settles through its own
// instance of each rather than the work kind's.
type launchStack struct {
	kind    Kind
	tracker forge.IssueTracker
	factory *dispatch.Factory
	settle  settle.Settler
	// failedLabel is resolved per stack: the two label families' Failed
	// labels differ, so a research pick's blocker check must never consult
	// the operator-configured work one.
	failedLabel string
}

// stacks returns the launch stacks drain services this call, work first,
// then research when both ResearchFactory and ResearchTracker are wired.
func (l *Launcher) stacks(tracker forge.IssueTracker) []launchStack {
	stacks := []launchStack{{kind: KindWork, tracker: tracker, factory: l.Factory, settle: l.Settle, failedLabel: l.FailedLabel}}
	if l.ResearchFactory != nil && l.ResearchTracker != nil {
		stacks = append(stacks, launchStack{kind: KindResearch, tracker: l.ResearchTracker, factory: l.ResearchFactory, settle: l.ResearchSettle, failedLabel: forge.ResearchDispatchLabels().Failed})
	}
	return stacks
}

// drain runs runStack for every wired launch stack to completion, then —
// still holding l.mu — checks Queue for a pick that landed too late for that
// pass's last discover() to see (RunContinuous returns as soon as its
// in-flight count hits zero, with no listener for a subsequent increment).
// Finding one re-drains immediately instead of clearing l.launching, so a
// concurrent tryLaunch racing this window can never observe l.launching==true
// with nothing left to pick it up: either this loop sees the new pick, or its
// Add()+tryLaunch happens-after this critical section releases l.mu. A stale
// image aborts the whole loop, not just the stack that hit it — a later stack
// would need the same rebuild anyway.
func (l *Launcher) drain(tracker forge.IssueTracker, pwd string) {
	defer l.wg.Done()
	stacks := l.stacks(tracker)
	kinds := make([]Kind, len(stacks))
	for i, st := range stacks {
		kinds[i] = st.kind
	}
	for {
		for _, st := range stacks {
			if l.runStack(st, pwd) {
				return
			}
		}

		q := l.queueRef()
		l.mu.Lock()
		// Scoped to kinds, not "any pick queued": a pick whose kind has no
		// wired stack is never claimed by the loop above and never will be,
		// so counting it as "more work to do" would spin drain forever. It is
		// left stranded at PickQueued instead.
		if !q.hasQueuedForKinds(kinds) {
			l.launching = false
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
	}
}

// runContinuousQueue adapts runStack's discover closure, pending count, and
// stale-drain reporting to the waves.Queue seam. Claim is a deliberate no-op:
// Queue.Discover already claimed via TransitionState before the Batch ever
// reaches RunContinuous.
type runContinuousQueue struct {
	discover func() (waves.Batch, error)
	pending  func() int
	report   func(waves.StaleDrainReport)
}

func (q runContinuousQueue) Discover() (waves.Batch, error) { return q.discover() }

func (q runContinuousQueue) Claim(num string) error { return nil }

func (q runContinuousQueue) Pending() (int, error) { return q.pending(), nil }

func (q runContinuousQueue) ReportStaleDrain(report waves.StaleDrainReport) { q.report(report) }

// runStack drives waves.RunContinuous once for st's kind, filling up to the
// session's shared parallelism cap with st's ready picks before returning.
// Reports whether the image went stale and the caller must abort the whole
// drain rather than try the next stack.
func (l *Launcher) runStack(st launchStack, pwd string) bool {
	discover := func() (waves.Batch, error) {
		defer l.signalRefresh() // a claim attempt is always a tracker write, win or lose
		batch, err := l.queueRef().Discover(st.tracker, l.CodeForge, st.failedLabel, st.kind)
		// A successful claim is a fresh Dispatch starting, so an earlier
		// Terminate mark must not carry over or a re-pick's settle would
		// abandon on its very first checkpoint. Begin starts a *new*
		// generation rather than clearing the old mark, so an in-flight
		// settle goroutine from the terminated incarnation keeps seeing
		// itself as terminated however many re-picks follow (see registry()).
		for i, iss := range batch.Issues {
			batch.Issues[i].Generation = l.registry().Begin(iss.Number)
		}
		// Queue.Discover already held any DepsOf-failure pick internally, so
		// Batch.Failed is always nil here.
		return batch, err
	}
	// OverlapGate is deliberately left zero-value, which makes the
	// touch-overlap gate a no-op: Console picks are operator-directed, not
	// batch-discovered, so they're exempt from deferring on another
	// in-progress issue's touched files.
	err := waves.RunContinuous(waves.Config{}, &waves.Session{Limiter: l.limiter(), Terminated: l.registry()}, st.tracker, l.CodeForge, pwd, st.factory, queueSettler{st.settle, l.queueRef(), l.signalRefresh, l.registry()}, runContinuousQueue{
		discover: discover,
		pending:  func() int { return l.queueRef().PendingCount(st.kind) },
		report:   l.recordStaleDrainReport,
	}, l.freshnessChecker())

	if errors.Is(err, waves.ErrImageStale) {
		// RunContinuous's "stale" flag is a one-shot latch per invocation:
		// once any refill sees a stale verdict, later refills short-circuit
		// without consulting fresh() again. That leaves a window where a
		// concurrent Rebuild finishes — flipping the checker fresh and
		// calling tryLaunch — while this drain still waits on an in-flight
		// Box; that tryLaunch no-ops (l.launching is still true), so without
		// this re-check the loop would park a held pick with no one left to
		// resume it.
		if applicable, fresh, _ := l.freshnessChecker()(); applicable && !fresh {
			l.mu.Lock()
			l.launching = false
			l.mu.Unlock()
			return true
		}
	}
	return false
}

// freshnessChecker wraps l.Fresh so every call also records the verdict for
// StaleStatus to read — RunContinuous calls the checker directly and never
// sees Launcher, so this is the only place that can capture its result. Nil
// Fresh falls back to an always-fresh stub.
func (l *Launcher) freshnessChecker() waves.FreshnessChecker {
	if l.Fresh == nil {
		return func() (bool, bool, string) { return false, true, "" }
	}
	return func() (bool, bool, string) {
		applicable, fresh, msg := l.Fresh()
		l.mu.Lock()
		wasStale := l.stale
		l.stale = applicable && !fresh
		l.staleMessage = msg
		newlyStale := l.stale && !wasStale
		l.mu.Unlock()
		// A stale->fresh transition signals nothing: Rebuild is the sole path
		// that clears staleness, and it already signals its own clear.
		if newlyStale {
			l.signalRefresh()
		}
		return applicable, fresh, msg
	}
}

// recordStaleDrainReport records r's rendered summary for StaleStatus to
// surface and signals a refresh so a live Console session picks it up.
// r.Console() is reused verbatim rather than re-derived, so the console
// banner and the stale-drain.log lines a headless caller sees always agree.
func (l *Launcher) recordStaleDrainReport(r waves.StaleDrainReport) {
	l.mu.Lock()
	l.lastStaleDrainSummary = strings.TrimRight(r.Console(), "\n")
	l.mu.Unlock()
	l.signalRefresh()
}

// Rebuild runs RebuildFn in the background so the session stays alive and
// responsive with Rebuilding surfaced on the banner while it runs. A rebuild
// already in flight makes a second call a no-op. On success it clears the
// stale gate and resumes draining, so any pick held at PickQueued through the
// stale window launches without being re-picked; on failure it leaves the
// gate held and records the error for StaleStatus. A nil RebuildFn is a
// no-op.
func (l *Launcher) Rebuild(tracker forge.IssueTracker, pwd string) {
	if l.RebuildFn == nil {
		return
	}
	l.mu.Lock()
	if l.rebuilding {
		l.mu.Unlock()
		return
	}
	l.rebuilding = true
	l.rebuildErr = nil
	l.mu.Unlock()
	l.signalRefresh()

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		output, notice, err := l.RebuildFn()

		l.mu.Lock()
		l.rebuilding = false
		l.rebuildErr = err
		l.rebuildOutput = output
		l.branchSwitchNotice = notice
		if err == nil {
			l.stale = false
			l.staleMessage = ""
			l.lastStaleDrainSummary = ""
		}
		l.mu.Unlock()
		l.signalRefresh()

		if err == nil {
			l.tryLaunch(tracker, pwd)
		}
	}()
}

// RebuildStatus is the launcher's live image-freshness/rebuild state —
// StaleStatusMsg carries one into the pure core, and Model stores one field
// the header renders from.
type RebuildStatus struct {
	Stale      bool
	Message    string
	Rebuilding bool
	Err        string
	// Output is the last rebuild's captured nix output.
	Output string
	// BranchSwitchNotice is "" when pwd's checkout didn't move off the branch
	// it was on.
	BranchSwitchNotice string
	// StaleDrainSummary is "" until a drain has been reported this session.
	// Unlike Stale/Message, which describe the *ongoing* stale gate, this is
	// a retrospective report of what a drain cost.
	StaleDrainSummary string
}

// StaleStatus returns the launcher's live image-freshness/rebuild state — the
// console's per-render sync source for the stale banner. The rebuild's
// captured output is retrievable here instead of ever being streamed to the
// Console's own stdout/stderr.
func (l *Launcher) StaleStatus() RebuildStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	status := RebuildStatus{
		Stale:              l.stale,
		Message:            l.staleMessage,
		Rebuilding:         l.rebuilding,
		Output:             l.rebuildOutput,
		BranchSwitchNotice: l.branchSwitchNotice,
		StaleDrainSummary:  l.lastStaleDrainSummary,
	}
	if l.rebuildErr != nil {
		status.Err = l.rebuildErr.Error()
	}
	return status
}

// Wait blocks until any in-flight background drain finishes — Run calls it
// before returning, so quitting the console never races the caller's
// cleanup (e.g. the driver-cache teardown) against a still-running Box.
func (l *Launcher) Wait() {
	l.wg.Wait()
}
