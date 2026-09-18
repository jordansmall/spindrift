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

// Launcher carries the dependencies Run needs to drive a picked issue through
// the continuous engine. A nil Launcher passed to Run disables launching: a
// pick still promotes and queues, but nothing ever runs.
type Launcher struct {
	CodeForge forge.CodeForge
	Factory   *dispatch.Factory
	Settle    settle.Settler
	// ResearchTracker, ResearchFactory, and ResearchSettle mirror
	// CodeForge/Factory/Settle for the research dispatch kind (ADR 0022, issue
	// #1708): ResearchTracker carries the fixed agent-research label family a
	// plain TransitionState call cannot select per-call. Nil means stacks()
	// yields no research stack, so a KindResearch pick sits at PickQueued.
	ResearchTracker forge.IssueTracker
	ResearchFactory *dispatch.Factory
	ResearchSettle  settle.Settler
	// MaxParallel sets the live cap's starting value only (1 unless positive).
	// Since #653 (ADR 0023) the cap actually enforced lives in l.limiter() and
	// moves at runtime via Resize/ResizeDelta.
	MaxParallel int
	// FailedLabel is the tracker label marking a blocker issue Failed, threaded
	// into Queue.Discover's held-pick check (#650) so a failed blocker shows on
	// the held row instead of staying "open".
	FailedLabel string
	// Fresh answers whether the loaded image is stale against the current
	// base-branch tip (issue #652). Nil falls back to "always fresh, not
	// applicable".
	Fresh waves.FreshnessChecker
	// RebuildFn rebuilds and reloads the image; nil makes Rebuild a no-op. It
	// returns the rebuild's captured nix output (issue #765) and a branch-switch
	// notice ("" when the checkout did not move off its branch, issue #1141), so
	// a background rebuild never writes to the Console's own stdout/stderr.
	RebuildFn func() (string, string, error)
	// RecoverFn adopts an orphaned issue's abandoned PR through settle's
	// recoverByNumber (issue #651). Wired by cmdConsole in main.go, since
	// console cannot import the main package. Nil skips orphan detection.
	RecoverFn func(issueNum string) error

	mu        sync.Mutex
	launching bool
	wg        sync.WaitGroup
	refresh   chan struct{}
	// pendingSnapshot is the snapshot signalRefresh most recently recorded,
	// delivered once a waiter drains refresh (issue #1542). It pairs with
	// hasPending so TakePendingSnapshot can tell "nothing pending yet" apart
	// from a genuine empty queue.
	pendingSnapshot []Pick
	hasPending      bool
	// queue is the session's private operator queue: Pick, Unpick, and Land are
	// its sole outside mutators. Lazily constructed by queueRef(), so a bare
	// struct literal needs no constructor (issue #1542).
	queue *Queue
	// staleMessage is updated on every freshnessChecker() call; stale (and
	// rebuilding/rebuildErr) only by drain/Rebuild themselves.
	stale        bool
	staleMessage string
	rebuilding   bool
	rebuildErr   error
	// rebuildOutput is the last rebuild's captured nix output (issue #765),
	// stdout and stderr merged in build order, bounded to the tail the runner
	// package's output cap enforces (issue #1130), set on every RebuildFn
	// completion regardless of outcome. A failed rebuild's output stays until
	// the next attempt overwrites it: an operator debugging the failure needs it.
	rebuildOutput string
	// branchSwitchNotice is the last rebuild's branch-switch notice, "" when the
	// checkout did not move off the branch it was on (issue #1141). Set on every
	// RebuildFn completion regardless of outcome.
	branchSwitchNotice string
	// lastStaleDrainSummary is the last stale-drain report's rendered one-line
	// summary, "" until a drain reports one or once the next successful Rebuild
	// clears it (#2678). Distinct from staleMessage: that describes the ongoing
	// stale gate, this reports what a completed drain cost in idle slot-time, so
	// a later rebuild resolving the staleness makes it obsolete.
	lastStaleDrainSummary string
	// pollInterval overrides Run's default poll cadence; only same-package tests
	// set it.
	pollInterval time.Duration
	// terminated is the shared registry Terminate marks and RunContinuous /
	// Settle check at their loop checkpoints (ADR 0024, issue #649). Lazily
	// created by registry().
	terminated *terminate.Registry
	// terminatingNums tracks issue numbers with a TerminateAsync goroutine still
	// in flight, so a second confirm cannot fire a duplicate Terminate (issue
	// #745): the queue pick stays PickRunning, and isLive keeps reporting it
	// live, for the whole async call. Lazily created by terminating().
	terminatingNums map[string]bool
	// cap is the session's live, resizable parallelism cap (ADR 0023, issue
	// #653), one Limiter shared across every drain() this Launcher runs, so a
	// Console "+"/"-" takes effect on the RunContinuous call already in flight.
	// Lazily created by limiter() at the MaxParallel starting cap.
	cap *waves.Limiter
}

// limiter lazily constructs l.cap at the MaxParallel starting cap (1 when
// unset), so a bare struct literal needs no constructor.
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

// queueRef lazily constructs l.queue. Every Launcher method that touches the
// queue goes through this accessor, never the raw field, so none can observe a
// nil Queue (issue #1542).
func (l *Launcher) queueRef() *Queue {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.queue == nil {
		l.queue = NewQueue()
	}
	return l.queue
}

// Snapshot returns the session's current queue state for the tea layer's
// one-time startup bootstrap (Init's initialQueueSyncCmd), the sole legitimate
// outside read of the private queue's full contents (issue #1542).
func (l *Launcher) Snapshot() []Pick {
	return l.queueRef().Snapshot()
}

// Pick promotes num through PickIssue and lands the result on the private
// queue, returning the outcome Msg and the fresh snapshot together so the tea
// side updates Model.Picks in the Update cycle that fired the keypress, never a
// render behind (issue #1542, closing the one-frame lag #837 worked around).
func (l *Launcher) Pick(tracker forge.IssueTracker, num, title string, kind Kind) (Msg, []Pick) {
	msg := PickIssue(l.trackerFor(kind, tracker), num, title, kind)
	return msg, l.Land(msg)
}

// trackerFor returns l.ResearchTracker for a KindResearch pick when one is
// wired, so a research promotion or claim lands its TransitionState call on the
// tracker instance carrying the matching label family (issue #1708).
func (l *Launcher) trackerFor(kind Kind, workTracker forge.IssueTracker) forge.IssueTracker {
	if kind == KindResearch && l.ResearchTracker != nil {
		return l.ResearchTracker
	}
	return workTracker
}

// Land applies an already-resolved pick-outcome Msg onto the private queue and
// returns the fresh snapshot. It is PickAllReady's per-issue landing step, split
// out of Pick so a bulk scan does not repeat PickIssue's terminal-state checks
// per issue. A failed promotion lands its dissolved row too, so the operator's
// only feedback that a pick raced survives the next snapshot push (issue #1542).
func (l *Launcher) Land(msg Msg) []Pick {
	switch m := msg.(type) {
	case PickQueuedMsg:
		l.queueRef().Add(Pick{Number: m.Number, Title: m.Title, Kind: m.Kind, State: PickQueued})
	case PickDissolvedMsg:
		l.queueRef().Add(Pick{Number: m.Number, Title: m.Title, State: PickDissolved, Reason: m.Reason})
	default:
		return l.queueRef().Snapshot()
	}
	// A promotion attempt is always a tracker write, win or lose, so it triggers
	// the same out-of-band refresh every other session write does (#647 AC4).
	l.signalRefresh()
	return l.queueRef().Snapshot()
}

// Unpick retracts num's queued-but-unlaunched pick and returns the fresh
// snapshot, with no tracker interaction (ADR 0023). Queue.Remove refuses to drop
// anything past PickQueued/PickHeld, so calling this for a num that never queued
// or already launched is safe.
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

// LiveIssues returns the issue numbers of every pick this session has
// PickRunning (issue #651). It reads Queue rather than the Limiter's live count
// because a settle marks the pick Settled before releasing the Limiter slot
// (queueSettler.Settle), so this can never report "no live Dispatches" a moment
// before the Limiter itself agrees.
func (l *Launcher) LiveIssues() []string {
	var nums []string
	for _, p := range l.queueRef().Snapshot() {
		if p.State == PickRunning {
			nums = append(nums, p.Number)
		}
	}
	return nums
}

// OrphanedIssues returns the issue numbers of every sandbox still running under
// the deterministic agent-issue-<N> naming scheme with nothing in this process
// tracking it, the signature of a hard death in a prior session (issue #651, ADR
// 0023). A Launcher built without a Factory reports none.
func (l *Launcher) OrphanedIssues() ([]string, error) {
	if l.Factory == nil {
		return nil, nil
	}
	return l.Factory.OrphanedIssues()
}

// Driver returns the Driver l.Factory was constructed with, or nil when the
// Launcher has no Factory (issue #1542).
func (l *Launcher) Driver() driver.Driver {
	if l.Factory == nil {
		return nil
	}
	return l.Factory.Driver()
}

// defaultPollInterval is the background backlog poll's cadence, slow enough to
// never spend the rate-limit window the session's Agents share (#647 AC5).
const defaultPollInterval = 3 * time.Minute

// PollInterval returns l.pollInterval when a test has overridden it, or
// defaultPollInterval otherwise (issue #1542).
func (l *Launcher) PollInterval() time.Duration {
	if l.pollInterval > 0 {
		return l.pollInterval
	}
	return defaultPollInterval
}

// Resize adjusts the live parallelism cap by delta, clamped to at least 1.
// Raising it takes effect immediately, so a held pick launches into the freed
// slot without waiting for a running Dispatch to settle. Lowering it never
// terminates a running Dispatch; it only gates new launches until the live count
// sinks under the new cap on its own (ADR 0023).
func (l *Launcher) Resize(delta int) {
	l.limiter().ResizeDelta(delta)
}

// registry lazily constructs l.terminated and wires it into l.Settle when that
// Settle is a concrete *settle.Settle; a settle.Fake has no loop to check.
// Termination keys on a per-number generation, not a bool (issue #743): a
// re-pick's Begin must not clear a mark an in-flight settle from the terminated
// incarnation has yet to see, or its stale setState lands on the re-pick's row.
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

// Terminate ends num's live Dispatch by hand (ADR 0024, issue #649): reaps any
// running Box, marks the shared registry so an in-flight settle abandons at its
// next checkpoint, transitions the issue back to Dispatchable (never Failed,
// since the operator decided), comments, appends a terminal log line, and marks
// the pick PickTerminated. Best-effort except the reap, whose error is returned.
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

	// The issue's current label depends on which phase Terminate caught:
	// InProgress for a running Box, a CI watch, or anywhere on the landing path,
	// since selfHeal holds the swap to Complete until landing settles (issue
	// #757, ready.go); Complete if Terminate lands just after settling.
	// TransitionState has no compare-and-swap, so both calls run regardless.
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

// TerminateAsync runs Terminate for num in the background (issue #745) so the
// operator's confirm key does not block the Update loop on tracker I/O,
// returning the snapshot as it stands at initiation (issue #1542). A second call
// for a num already in flight is a no-op: the pick stays PickRunning until
// Terminate ends, so a second confirm cannot race a duplicate Kill/Comment.
func (l *Launcher) TerminateAsync(tracker forge.IssueTracker, num string) []Pick {
	// terminating() takes l.mu itself to lazily construct the map, so the
	// check-and-set below re-takes it. Splitting the two is safe: the atomicity
	// that matters is the check-and-set, not its adjacency to the lazy init.
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
		// Return value dropped: Terminate already logs its kill failure to stderr
		// before returning it, so nothing is lost by not handling it here.
		l.Terminate(tracker, num)

		l.mu.Lock()
		delete(inFlight, num)
		l.mu.Unlock()
	}()

	return l.queueRef().Snapshot()
}

// terminating lazily constructs l.terminatingNums, so a bare struct literal
// needs no constructor.
func (l *Launcher) terminating() map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminatingNums == nil {
		l.terminatingNums = make(map[string]bool)
	}
	return l.terminatingNums
}

// refreshChan lazily constructs l.refresh. Buffered to exactly one slot, so a
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

// signalRefresh marks a refresh pending and records the queue's current snapshot
// for TakePendingSnapshot to deliver, called after every write this session
// makes to the tracker or queue (#647 AC4, issue #1542). The wake is a
// non-blocking one-slot send; pendingSnapshot always holds the most recent
// snapshot, so a burst of writes can only deliver the latest state.
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

// TakePendingSnapshot returns the most recent snapshot signalRefresh recorded
// and clears the pending flag, reporting whether one was pending. It is the sole
// outside read of the private queue's live state after startup (issue #1542).
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

// tryLaunch starts draining Queue through waves.RunContinuous in the background,
// unless a drain is already running or Queue has nothing left to launch (#754).
// RunContinuous's refill-on-completion picks up any pick added while that drain
// is in flight, so a second concurrent invocation is never needed. See
// Queue.Empty (#650) for why the gate must cover PickHeld as well as PickQueued.
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

// launchStack pairs one Dispatch kind's tracker, dispatch factory, and settle,
// so a KindResearch pick launches and settles through its own instance of each
// rather than the work kind's (issue #1708).
type launchStack struct {
	kind    Kind
	tracker forge.IssueTracker
	factory *dispatch.Factory
	settle  settle.Settler
	// failedLabel is resolved per stack: the work stack uses the
	// operator-configured l.FailedLabel, the research stack the fixed
	// agent-research-failed label. The two families' Failed labels differ, so a
	// research pick's blocker check must never consult the work one.
	failedLabel string
}

// stacks returns the launch stacks drain services this call: the work stack
// first, then the research stack when ResearchFactory and ResearchTracker are
// both wired (issue #1708).
func (l *Launcher) stacks(tracker forge.IssueTracker) []launchStack {
	stacks := []launchStack{{kind: KindWork, tracker: tracker, factory: l.Factory, settle: l.Settle, failedLabel: l.FailedLabel}}
	if l.ResearchFactory != nil && l.ResearchTracker != nil {
		stacks = append(stacks, launchStack{kind: KindResearch, tracker: l.ResearchTracker, factory: l.ResearchFactory, settle: l.ResearchSettle, failedLabel: forge.ResearchDispatchLabels().Failed})
	}
	return stacks
}

// drain runs runStack for every wired launch stack to completion, then, still
// holding l.mu, re-drains if Queue gained a pick the last discover() missed
// (RunContinuous returns as soon as in-flight Boxes hit zero and never listens
// for a later increment), so a racing tryLaunch can never see l.launching==true
// with nothing left to pick it up. A stale image aborts the whole loop.
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
		// Scoped to kinds, not "any pick queued": a pick whose kind has no wired
		// stack is never claimed by the loop above, so counting it as more work
		// would spin drain forever without progress (issue #1708). It is left
		// stranded at PickQueued instead.
		if !q.hasQueuedForKinds(kinds) {
			l.launching = false
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
	}
}

// runContinuousQueue adapts runStack's discover closure, pendingCount, and
// reportStaleDrain plumbing to the waves.Queue seam (issue #2937).
// RunContinuous calls Claim/Pending/ReportStaleDrain through this seam
// exclusively (#2939). Claim is a documented no-op: Queue.Discover already
// claimed via TransitionState before the Batch reaches RunContinuous.
type runContinuousQueue struct {
	discover func() (waves.Batch, error)
	pending  func() int
	report   func(waves.StaleDrainReport)
}

func (q runContinuousQueue) Discover() (waves.Batch, error) { return q.discover() }

func (q runContinuousQueue) Claim(num string) error { return nil }

func (q runContinuousQueue) Pending(map[string]bool) (int, error) { return q.pending(), nil }

func (q runContinuousQueue) ReportStaleDrain(report waves.StaleDrainReport) { q.report(report) }

// EnsureLogDirExists is a no-op: ReportStaleDrain forwards in memory to
// q.report and never touches the filesystem.
func (q runContinuousQueue) EnsureLogDirExists() error { return nil }

// runStack drives waves.RunContinuous once for st's kind, filling up to the
// session's shared parallelism cap with st's ready picks before returning (issue
// #1708). It reports whether the image went stale and the caller must abort the
// whole drain rather than try the next stack.
func (l *Launcher) runStack(st launchStack, pwd string) bool {
	discover := func() (waves.Batch, error) {
		defer l.signalRefresh() // a claim attempt is always a tracker write, win or lose
		batch, err := l.queueRef().Discover(st.tracker, l.CodeForge, st.failedLabel, st.kind)
		// A successful claim is a fresh Dispatch, so an earlier Terminate mark
		// must not carry over or the re-pick's settle would abandon on its first
		// checkpoint (ADR 0024, issue #649). Begin starts a new generation rather
		// than clearing the old one, so an in-flight settle from the terminated
		// incarnation keeps seeing itself terminated (see registry(), issue #743).
		for i, iss := range batch.Issues {
			batch.Issues[i].Generation = l.registry().Begin(iss.Number)
		}
		// Queue.Discover already held this pick's own DepsOf failure internally,
		// so Batch.Failed is always nil here.
		return batch, err
	}
	// OverlapGate is deliberately left zero-value (#706): Console picks are
	// operator-directed, not batch-discovered, so they are exempt from deferring
	// on another in-progress issue's touched files. Queue.Discover already claimed
	// the issue from Dispatchable to InProgress, so runContinuousQueue's no-op
	// Claim avoids a redundant second one (issue #2938).
	err := waves.RunContinuous(waves.Config{}, &waves.Session{Limiter: l.limiter(), Terminated: l.registry()}, st.tracker, l.CodeForge, st.factory, queueSettler{st.settle, l.queueRef(), l.signalRefresh, l.registry()}, runContinuousQueue{
		discover: discover,
		pending:  func() int { return l.queueRef().PendingCount(st.kind) },
		report:   l.recordStaleDrainReport,
	}, l.freshnessChecker())

	if errors.Is(err, waves.ErrImageStale) {
		// RunContinuous latches "stale" for the whole invocation, so a Rebuild
		// finishing while this drain still waits on an in-flight Box flips the
		// checker back to fresh but its tryLaunch no-ops (l.launching is still
		// true), leaving a held pick with no one to resume it. Re-checking
		// freshness here catches that race and re-drains instead of parking.
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
// StaleStatus to read: RunContinuous calls the checker directly and never sees
// Launcher, so this is the only place that can capture its result. Nil Fresh
// falls back to an always-fresh stub.
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
		// A stale-to-fresh transition signals nothing: Rebuild is the sole path
		// that clears staleness and already signals its own clear (issue #1124).
		if newlyStale {
			l.signalRefresh()
		}
		return applicable, fresh, msg
	}
}

// recordStaleDrainReport records r's rendered summary for StaleStatus and
// signals a refresh so a live Console session picks it up (#2678). r.Console()
// is reused verbatim rather than re-derived, so the console banner and the
// stale-drain.log lines a headless caller sees always agree.
func (l *Launcher) recordStaleDrainReport(r waves.StaleDrainReport) {
	l.mu.Lock()
	l.lastStaleDrainSummary = strings.TrimRight(r.Console(), "\n")
	l.mu.Unlock()
	l.signalRefresh()
}

// Rebuild runs RebuildFn in the background (issue #652 AC3) so the session stays
// responsive. A rebuild already in flight, or a nil RebuildFn, makes the call a
// no-op. On success it clears the stale gate and resumes draining, so a pick held
// at PickQueued through the stale window launches without being re-picked; on
// failure it holds the gate and records the error for StaleStatus.
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

// RebuildStatus is the launcher's live image-freshness/rebuild state, carried
// into the pure core by StaleStatusMsg and stored in one Model field the header
// renders from (issue #1541).
type RebuildStatus struct {
	Stale      bool
	Message    string
	Rebuilding bool
	Err        string
	// Output is the last rebuild's captured nix output (issue #765).
	Output string
	// BranchSwitchNotice is "" when the checkout did not move off the branch it
	// was on (issue #1141).
	BranchSwitchNotice string
	// StaleDrainSummary is the last stale-drain report's rendered one-line
	// summary, "" when no drain has been reported this session (#2678). Unlike
	// Stale/Message, it is retrospective: what a completed drain cost.
	StaleDrainSummary string
}

// StaleStatus returns the launcher's live image-freshness/rebuild state, the
// console's per-render sync source for the stale banner (issue #652). Output
// (issue #765) and BranchSwitchNotice (issue #1141) are retrieved here instead
// of ever being streamed to the Console's own stdout/stderr.
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

// Wait blocks until any in-flight background drain finishes. Run calls it before
// returning, so quitting the console never races the caller's cleanup (the
// driver-cache teardown, for one) against a still-running Box.
func (l *Launcher) Wait() {
	l.wg.Wait()
}
