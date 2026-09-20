package waves

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/shutdown"
	"spindrift.dev/launcher/internal/terminate"
)

// defaultPollInterval is the background refill poll's cadence (issue #1637):
// a ticker retries drainRefill() on this interval so a transient refill miss
// gets retried without waiting for an unrelated Box to finish. Three minutes
// rather than the original 30s (issue #2874), because each tick spends a share
// of the tracker's rate-limit budget. Issue #1638 makes it an operator knob.
const defaultPollInterval = 3 * time.Minute

func resolvePollInterval(override time.Duration) time.Duration {
	if override <= 0 {
		return defaultPollInterval
	}
	return override
}

// ErrImageStale is returned by RunContinuous when the loaded image would be
// rebuilt against the current base-branch tip: no further Boxes launch,
// in-flight ones finish on the image they started with, and the driving loop
// can then rebuild and re-invoke (exit code 4, see main.go's runExitCode).
var ErrImageStale = errors.New("image stale; rebuild and re-invoke")

// ErrSignalledStop is returned by RunContinuous once cfg.Stop has been closed:
// no further Boxes launch, in-flight ones finish, and the driving loop exits
// without a rebuild (exit code 7, see main.go's runExitCode). It takes
// precedence over ErrImageStale and ErrOpenNoneDispatchable in the terminal
// switch below, whichever detection races first, since an operator's explicit
// wind-down request should never be masked by a stale-image or empty-queue
// verdict discovered in the same drain (#3520). Dispatch's one-shot wave
// returns it too (#3522), by the same precedence, once its own
// shutdown.Gate reports signalled.
var ErrSignalledStop = errors.New("stop requested; drained outstanding work")

// Discoverer re-queries the dispatchable Batch. RunContinuous calls it at
// startup and again before every slot refill, so a blocker that merges mid-run
// is picked up without a fresh invocation. A caller must check Failed
// explicitly (#752, #1103): an issue whose own DepsOf call errored looks
// identical in Edges to a confirmed zero-blocker issue.
type Discoverer func() (Batch, error)

// FreshnessChecker answers whether a refill may launch a new Box. applicable
// is false for a runtime with no loaded image to compare (bwrap), and such a
// refill always proceeds; fresh is meaningless then.
type FreshnessChecker func() (applicable, fresh bool, message string)

// nextReady returns the first issue ready to dispatch, applying the same
// blocked-skip and touch-overlap-defer selection drainMaxJobs does but
// stopping at the first match, since a refill only fills one freed slot.
// sources goes unrendered here: its only consumer, writeBlockedMarker, fires
// for OriginClaimed, a mode continuous dispatch never uses (#662).
func nextReady(cfg Config, it forge.IssueTracker, cf forge.CodeForge, checkOverlap func(string) (string, bool), issues []Issue, edges map[string][]string, sources Sources, depsOfFailed map[string]bool, logged map[string]string) (Issue, bool) {
	// Drop dedup entries for issues no longer in the batch: keeps logged bounded
	// across a long Console session, and lets an issue that leaves and returns
	// re-log its state afresh.
	if logged != nil {
		present := make(map[string]bool, len(issues))
		for _, iss := range issues {
			present[iss.Number] = true
		}
		for num := range logged {
			if !present[num] {
				delete(logged, num)
			}
		}
	}
	// skip logs a non-dispatch outcome at most once per distinct line: refill
	// re-walks this list on every completion and the poll re-walks it every ~3m
	// (#1637), so an unchanged blocked or deferred reason would otherwise
	// reprint on every tick. A nil logged map (direct unit tests) disables it.
	skip := func(num, line string) {
		if logged != nil && logged[num] == line {
			return
		}
		fmt.Print(line)
		if logged != nil {
			logged[num] = line
		}
	}
	for _, iss := range issues {
		ready, line := issueReadiness(cfg, it, cf, checkOverlap, iss, edges, depsOfFailed)
		if !ready {
			skip(iss.Number, line)
			continue
		}
		return iss, true
	}
	return Issue{}, false
}

// issueReadiness classifies iss the same way nextReady's selection loop does,
// but as a pure query with no printing or dedup, so a caller that only wants
// ready-vs-not can reuse it (the stale-drain heldBack count, #2678). line is
// the message nextReady would print for a not-ready result and is meaningless
// when ready is true.
func issueReadiness(cfg Config, it forge.IssueTracker, cf forge.CodeForge, checkOverlap func(string) (string, bool), iss Issue, edges map[string][]string, depsOfFailed map[string]bool) (ready bool, line string) {
	var unready []string
	if !cfg.IgnoreBlockers {
		// caps is resolved fresh per call rather than threaded in as a parameter
		// (issue #2946): it and cf never vary within a single Dispatch or
		// RunContinuous invocation.
		caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		unready = unreadyBlockers(it, cf, caps, iss.Number, edges, cfg.SeedScopeOf)
	}
	switch {
	case !cfg.IgnoreBlockers && depsOfFailed[iss.Number]:
		// Own DepsOf call failed (#752, #1103), so edges[iss.Number] is
		// unreliable rather than a confirmed zero-blocker result. Hold rather
		// than launch; the next refill retries.
		return false, fmt.Sprintf("    ~~ #%s blocker check failed; will retry\n", iss.Number)
	case len(unready) > 0:
		return false, fmt.Sprintf("    ~~ #%s blocked by #%s; skipping\n", iss.Number, strings.Join(unready, ", #"))
	default:
		if collider, overlapped := checkOverlap(iss.Number); overlapped {
			return false, fmt.Sprintf("    ~~ #%s touches overlap in-progress #%s; deferring\n", iss.Number, collider)
		}
		return true, ""
	}
}

// CountReady counts issues in batch that issueReadiness calls ready, minus
// those this run already claimed (a listing taken after an in-run claim is
// eventually consistent). Its one caller is the headless stale-drain heldBack
// query (issue #2939). Only an unresolved blocker edge excludes an issue
// durably; a touch overlap or a failed DepsOf check is transient.
func CountReady(cfg Config, it forge.IssueTracker, cf forge.CodeForge, batch Batch, claimed map[string]bool) int {
	checkOverlap := waveOverlapCheck(cfg, it, cf)
	n := 0
	for _, iss := range dropClaimed(batch.Issues, claimed) {
		if ready, _ := issueReadiness(cfg, it, cf, checkOverlap, iss, batch.Edges, batch.Failed); ready {
			n++
		}
	}
	return n
}

// dropClaimed filters a refill's discover result against the in-run claimed
// set before nextReady scans it (issue #1646), since an eventually-consistent
// listing can still show a just-claimed issue as dispatchable. Dropping here
// rather than inside nextReady's loop avoids a per-issue skip line that, with
// N slots claimed, would repeat O(N^2) times over a run.
func dropClaimed(issues []Issue, claimed map[string]bool) []Issue {
	unclaimed := make([]Issue, 0, len(issues))
	for _, iss := range issues {
		if !claimed[iss.Number] {
			unclaimed = append(unclaimed, iss)
		}
	}
	return unclaimed
}

// reportStaleDrainReleasingMu runs queue.ReportStaleDrain's blocking I/O with
// mu released, then re-acquires it: mu is held on entry and on exit (#2775).
// Both call sites have already run staleDrain.finish under mu, the write that
// makes inProgress() false, so the single-emit invariant holds before mu drops
// and no second drain report can race in behind this one.
func reportStaleDrainReleasingMu(mu *sync.Mutex, queue Queue, report StaleDrainReport) {
	mu.Unlock()
	queue.ReportStaleDrain(report)
	mu.Lock()
}

// RunContinuous runs the opt-in slot-refill dispatch mode (#527): it fills up
// to cfg.MaxParallel slots from queue.Discover, then consults fresh before
// refilling each slot a finished Box frees, returning ErrImageStale once a
// stale image has drained. The in-run claimed set, not the forge's label swap,
// is what stops a second Box launching for one issue (issue #3035).
func RunContinuous(cfg Config, session *Session, it forge.IssueTracker, cf forge.CodeForge, f *dispatch.Factory, s settle.Settler, queue Queue, fresh FreshnessChecker) error {
	if err := queue.EnsureLogDirExists(); err != nil {
		return err
	}

	var limiter *Limiter
	var terminated *terminate.Registry
	if session != nil {
		limiter, terminated = session.Limiter, session.Terminated
	}
	if limiter == nil {
		// Headless (CONTINUOUS_DISPATCH) and every nil-Session call site: a fixed
		// cap for this invocation only, never resized.
		limiter = NewLimiter(cfg.MaxParallel)
	}
	if terminated == nil {
		// Reclaim's Mark is what stops a surviving Box goroutine from Failing or
		// Settling an issue the abort path already released back to
		// Dispatchable (#3521): the abort watcher below always calls Reclaim
		// against terminated, and headless dispatch never has a Session to
		// supply one. A fresh Registry with nothing marked yet behaves exactly
		// like the nil one it replaces (Marked reports false until something
		// marks it), so no non-abort path changes.
		terminated = terminate.NewRegistry()
	}
	reaper := f.AsReaper()

	// mu also guards stale, dispatchedAny, claimed, and outstanding below
	// (#653): every refill call, whether from the bootstrap loop, a completing
	// Box, or the grow listener, runs under this one lock, so they never
	// interleave.
	var mu sync.Mutex
	idle := sync.NewCond(&mu)
	stale := false
	// signalled latches an operator wind-down request observed on cfg.Stop
	// (#3520), mu-guarded exactly like stale: refill's guard and the terminal
	// switch below both read it, and the dedicated stop watcher goroutine is
	// the only other writer, so the drain-once invariant holds the same way
	// staleDrain's does.
	signalled := false
	// aborted latches an operator second-signal abort observed on cfg.Abort
	// (#3521), mu-guarded exactly like signalled. It has more than one writer,
	// unlike signalled: observeAbort below runs the one-time reclaim inline, so
	// whichever mu-held site first notices cfg.Abort closed is the site that
	// latches. The latch itself is what keeps that to one reclaim, not a
	// single-writer discipline.
	aborted := false
	// aborting is true only for the span of that one reclaim, keeping the
	// terminal wait below from returning while a Reclaim is still between its
	// Kill and the InProgress->Dispatchable transition that same Kill races:
	// the reclaimed Box's own completion goroutine drops outstanding to 0 and
	// broadcasts from inside that window, which would otherwise exit the
	// process with the aborted issue stranded on InProgress (#3521).
	aborting := false
	dispatchedAny := false
	claimed := make(map[string]bool)
	// inflight holds only issues claimed and not yet finished — unlike claimed,
	// which also remembers a finished issue so it is never re-claimed, this
	// must forget one the moment it finishes, or the abort watcher would drag
	// an already-Complete issue back to Dispatchable (#3521). refill's claim
	// and the abort watcher's snapshot both run under mu, so a Box either
	// launches before the watcher latches aborted (and is therefore in the
	// snapshot) or never launches at all.
	inflight := make(map[string]bool)
	// logged keys an issue number to the last skip line printed for it, so the
	// re-walks on every completion, grow, and ~3m poll tick (#1637) do not
	// reprint an unchanged blocked or deferred reason.
	logged := make(map[string]string)
	// outstanding counts in-flight Boxes. A plain sync.WaitGroup cannot
	// coordinate safely here: the grow listener can call refill, and so wg.Add,
	// from a goroutine with no causal link to any counted Box, risking the
	// documented WaitGroup race. Counting under mu makes "is anything still
	// outstanding" and "am I about to add more" the same critical section.
	outstanding := 0
	closed := false
	// staleDrain consolidates the stale-drain report state (#2678, #2774).
	var staleDrain staleDrainTracker
	// now is cfg's test override, so the report's freeSlotSecs accumulation is
	// assertable from a deterministic clock sequence (issue #2678), or the
	// production clock.
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	// clock is cfg's injectable sleep seam for the rate-limited re-discover
	// backoff (issue #2866); only same-package tests inject a fake Clock, so
	// the backoff sleeps through it instead of a real sleep.
	clock := cfg.Clock
	if clock.Sleep == nil {
		clock = retry.RealClock()
	}

	// observeStop is a non-blocking latch on cfg.Stop (#3520). Caller holds mu.
	// refill calls it on every entry — bootstrap, completion-triggered, grow
	// listener, and the ~3m poll tick all funnel through refill — so a stop
	// closed at any point is picked up on the very next of those triggers. The
	// dedicated watcher goroutine below calls the same helper — see its own
	// comment for the promptness that buys. announce is false only
	// for the final post-drain latch, where the drain is already over and a
	// drain line would mislead.
	observeStop := func(announce bool) {
		if signalled || cfg.Stop == nil {
			return
		}
		select {
		case <-cfg.Stop:
			signalled = true
			if !announce {
				return
			}
			if outstanding > 0 {
				fmt.Println("==> stop requested; draining outstanding work")
			} else {
				fmt.Println("==> stop requested; nothing in flight")
			}
		default:
		}
	}

	// reclaimInFlight snapshots inflight, drops mu for shutdown.AbortInFlight's
	// I/O, and retakes it before returning -- the reportStaleDrainReleasingMu
	// idiom above: mu is held on entry and exit, and observeAbort below is its
	// only caller, guarded so it runs exactly once. AbortInFlight (#3522) is
	// the same sort/print/Reclaim-loop dispatchWave's shutdown.Gate uses for
	// its own abort path, so the two Box-launching paths share one loop that
	// actually talks to the tracker and reaper.
	reclaimInFlight := func() {
		nums := make([]string, 0, len(inflight))
		for num := range inflight {
			nums = append(nums, num)
		}
		aborting = true
		mu.Unlock()
		shutdown.AbortInFlight(it, cf, reaper, terminated, cfg.CompleteLabel, nums)
		mu.Lock()
		aborting = false
		idle.Broadcast()
	}

	// observeAbort is a non-blocking latch on cfg.Abort (#3521), the abort
	// counterpart to observeStop above, called from the same handful of
	// mu-held sites (both refill guards, the terminal re-check, and the
	// dedicated abort watcher below). Unlike observeStop it does the one-time
	// reclaim itself rather than leaving that to its callers: the aborted
	// guard at top means whichever call site notices cfg.Abort closed first
	// runs reclaimInFlight, and every later call is a no-op. Folding the
	// reclaim in here (rather than only in the watcher, mirroring the stop
	// watcher's shape verbatim) closes a real race: relying solely on the
	// watcher goroutine racing the shutdown joins below (close(growDone)/
	// <-done, close(pollDone)/<-pollExited) against its own select can lose
	// that coin toss under scheduler pressure when nothing was ever
	// in-flight, silently dropping the announcement. Caller holds mu.
	observeAbort := func() {
		if aborted || cfg.Abort == nil {
			return
		}
		select {
		case <-cfg.Abort:
			aborted = true
			reclaimInFlight()
		default:
		}
	}

	// refill reports whether it launched a Box, so a caller filling more than
	// one freed slot from a single trigger can loop it until a call finally does
	// nothing, rather than assuming one trigger is worth exactly one launch.
	var refill func() bool
	// drainRefill is predeclared so refill's completion goroutine can call it
	// before its body is assigned.
	var drainRefill func() int
	refill = func() bool {
		observeStop(true)
		observeAbort()
		if stale || closed || signalled || aborted {
			return false
		}
		if !limiter.TryAcquire() {
			return false
		}
		launched := false
		defer func() {
			if !launched {
				limiter.Release()
			}
		}()
		applicable, isFresh, msg := fresh()
		if applicable && !isFresh {
			stale = true
			fmt.Printf("==> %s\n", msg)
			staleDrain.begin(now(), limiter.Cap())
			// heldBack comes from queue.Pending(claimed), a side-effect-free
			// count each Queue supplies its own way, rather than RunContinuous
			// re-discovering or re-filtering (#2939). A Pending error is
			// reported as unknown rather than a fabricated 0 (#2678 review
			// finding).
			if n, err := queue.Pending(claimed); err != nil {
				fmt.Fprintf(os.Stderr, "continuous: query pending for stale-drain report: %v\n", err)
				staleDrain.heldBackUnknown = true
			} else {
				staleDrain.heldBack = n
			}
			if outstanding == 0 {
				// Nothing in flight, so the drain is already over and the
				// completion goroutine below never runs to report it. end is
				// set to start itself, not a fresh time.Now(), so Duration() is
				// exactly zero rather than a near-zero timing artifact.
				reportStaleDrainReleasingMu(&mu, queue, staleDrain.finish(staleDrain.start))
			}
			return false
		}
		var batch Batch
		attempt := 0
		for {
			var err error
			batch, err = queue.Discover()
			if err == nil {
				break
			}
			if !errors.Is(err, forge.ErrRateLimit) {
				fmt.Fprintf(os.Stderr, "continuous: re-discover: %v\n", err)
				return false
			}
			attempt++
			if attempt > cfg.Policy.Max {
				fmt.Fprintf(os.Stderr, "continuous: re-discover: rate limited; retry cap exhausted (%d)\n", cfg.Policy.Max)
				return false
			}
			backoff := cfg.Policy.Backoff(clock).Duration(attempt)
			fmt.Fprintf(os.Stderr, "continuous: re-discover: rate limited; retry %d/%d in %s\n", attempt, cfg.Policy.Max, backoff)
			// mu is dropped across the sleep so the stop-watcher isn't blocked
			// out of the drain line for the whole backoff (#3520 review). A
			// whole other refill (completion, grow, or poll tick) may run to
			// completion in this window; that is safe because this one re-reads
			// every input it decides on — the Discover above, claimed, and the
			// stop/stale flags — after re-taking mu, so it cannot duplicate a
			// launch the other made.
			mu.Unlock()
			clock.Sleep(backoff)
			mu.Lock()
			observeStop(true)
			observeAbort()
			if stale || closed || signalled || aborted {
				return false
			}
		}
		// Continuous refill has no Origin concept, always the discovered pool
		// and never a hand-picked list, so unlike NewPlan it sorts
		// unconditionally (ADR 0040).
		forge.SortByPriority(batch.Issues, func(i Issue) forge.Priority { return i.Priority })
		if len(batch.Edges) > 0 {
			if node, cycle := detectCycle(batch.Edges, forge.Numbers(batch.Issues, func(i Issue) string { return i.Number })); cycle {
				fmt.Fprintf(os.Stderr, "==> ERROR: dependency cycle detected (issue #%s is in the cycle); skipping this refill\n", node)
				return false
			}
		}
		checkOverlap := waveOverlapCheck(cfg, it, cf)
		unclaimed := dropClaimed(batch.Issues, claimed)
		iss, ok := nextReady(cfg, it, cf, checkOverlap, unclaimed, batch.Edges, batch.Sources, batch.Failed, logged)
		if !ok {
			return false
		}
		if err := queue.Claim(iss.Number); err != nil {
			fmt.Printf("    ~~ #%s claim failed; skipping (%v)\n", iss.Number, err)
			return false
		}
		dispatchedAny = true
		claimed[iss.Number] = true
		// New arms this issue's kill latch, so it must happen here, under mu
		// and before the inflight entry below — not as the goroutine's first
		// statement (#3521). reclaimInFlight snapshots inflight under mu, so
		// arming first means every issue it can reap is already armed, and no
		// later New can re-arm the latch its Kill just closed.
		d := f.New(iss.Number, iss.Title)
		inflight[iss.Number] = true
		launched = true
		outstanding++
		go func() {
			defer d.Close()
			result := d.Run()
			switch {
			case terminated.Marked(iss.Number, iss.Generation):
				// Terminate (ADR 0024, issue #649) already reaped this Box,
				// moved the issue back to Dispatchable, and logged its own
				// line, so neither a Failed transition nor a Settle belongs
				// here.
				fmt.Printf("    ~~ #%s terminated by operator; abandoning\n", iss.Number)
			case !result.Success:
				fmt.Printf("    !! #%s FAILED (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
				result.ReportFailureReason(iss.Number)
				transitionState(it, iss.Number, forge.InProgress, forge.Failed)
				s.Fail(iss.Number, iss.Generation, result)
			default:
				fmt.Printf("    <- #%s done  (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
				s.Settle(d, iss.Number, iss.Generation, result)
			}
			limiter.Release()
			mu.Lock()
			// Integrate the idle slot-time since the last checkpoint using the
			// pre-decrement outstanding count, the busy-slot count over the
			// interval that just ended. checkpoint bills that interval at
			// staleDrain.cap, the cap in effect over it, not a live limiter.Cap()
			// read (#2678 review finding); it is a no-op outside a drain.
			staleDrain.checkpointIfNeeded(now(), limiter.Cap(), outstanding)
			outstanding--
			// Must run before the abort watcher can next take mu, or a Box that
			// just finished on its own would still look in-flight to it (#3521).
			delete(inflight, iss.Number)
			drainRefill()
			if staleDrain.inProgress() && outstanding == 0 {
				reportStaleDrainReleasingMu(&mu, queue, staleDrain.finish(staleDrain.slotAt))
			}
			if outstanding == 0 {
				idle.Broadcast()
			}
			mu.Unlock()
		}()
		return true
	}

	// drainRefill fills every free slot that has ready work and reports how many
	// it launched, so the poll ticker below logs only the ticks that did
	// something. A slot freed by an earlier transient refill miss stays free at
	// the limiter level until some later call claims it, so one trigger is not
	// worth exactly one launch (#1587).
	drainRefill = func() int {
		n := 0
		for refill() {
			n++
		}
		return n
	}

	// growDone stops the resize listener; done confirms it has exited before
	// RunContinuous returns, so no call leaks a goroutine watching a Limiter
	// shared across a whole Console session.
	growDone := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-limiter.Resized():
				// A Console "+" or "-" mid-drain (ADR 0023, issue #653). Listen
				// on Resized, not Grown: a lower mid-drain mis-attributes as
				// badly as a raise if the stale staleDrain.cap bridges across it
				// (#2678 review finding). Loop drainRefill, since one signal
				// only means at least one resize happened (issue #766).
				mu.Lock()
				// The resize is the moment the live cap changed: close out the
				// interval that just ended at the old staleDrain.cap before
				// refreshing it, so the new cap is not retroactively credited to
				// the whole preceding interval at the next checkpoint (#2678
				// review finding). A no-op outside a drain.
				staleDrain.checkpointIfNeeded(now(), limiter.Cap(), outstanding)
				drainRefill()
				mu.Unlock()
			case <-growDone:
				return
			}
		}
	}()

	// pollDone stops the ticker once this call is finished; pollExited confirms
	// it has exited before RunContinuous returns.
	pollInterval := resolvePollInterval(cfg.pollInterval)
	pollDone := make(chan struct{})
	pollExited := make(chan struct{})
	go func() {
		defer close(pollExited)
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// A tick after closed is set is a no-op: refill()'s stale||closed
				// guard makes drainRefill() return 0, so no explicit shutdown
				// race-check is needed beyond the pollDone case below.
				mu.Lock()
				n := drainRefill()
				mu.Unlock()
				if n > 0 {
					// Usually an event-driven refill missed and the slot sat idle
					// until this tick, but a tick can also just win the race
					// against a completion or grow trigger, so this is not proof
					// of a miss.
					fmt.Printf("    <- poll: launched %d issue(s)\n", n)
				}
			case <-pollDone:
				return
			}
		}
	}()

	// stopWatchDone stops the watcher once this call is finished, whether or
	// not cfg.Stop was ever closed; stopExited confirms it has exited before
	// RunContinuous returns — the same shutdown shape as growDone/done and
	// pollDone/pollExited above, so signalled has no writer left once this
	// function reads it below (#3520). Only started when cfg.Stop != nil: a
	// nil channel would block select forever, so there is nothing to watch.
	var stopWatchDone, stopExited chan struct{}
	if cfg.Stop != nil {
		stopWatchDone = make(chan struct{})
		stopExited = make(chan struct{})
		go func() {
			defer close(stopExited)
			select {
			case <-cfg.Stop:
				// The promptness seam: refill's own observeStop call only fires
				// on the next completion, grow, or ~3m poll tick. Without this
				// watcher, a stop that arrives while the run is blocked in
				// idle.Wait() below would print nothing until one of those next
				// occurs.
				mu.Lock()
				observeStop(true)
				mu.Unlock()
			case <-stopWatchDone:
			}
		}()
	}

	// abortWatchDone stops the watcher once this call is finished; abortExited
	// confirms exit before RunContinuous returns, the same shutdown shape as
	// stopWatchDone/stopExited above. This is the promptness seam for an
	// abort with nothing else scheduled to call refill again: without it, an
	// abort arriving while the run is blocked in idle.Wait() below with
	// nothing outstanding, or after the last poll tick, would go unserviced
	// until the final observeAbort() re-check after this function's own
	// shutdown joins. observeAbort's own aborted guard (not "only this
	// goroutine calls it") is what keeps reclaimInFlight to exactly one call
	// no matter which of this watcher, refill, or the final re-check notices
	// cfg.Abort closed first (#3521).
	var abortWatchDone, abortExited chan struct{}
	if cfg.Abort != nil {
		abortWatchDone = make(chan struct{})
		abortExited = make(chan struct{})
		go func() {
			defer close(abortExited)
			select {
			case <-cfg.Abort:
				mu.Lock()
				observeAbort()
				mu.Unlock()
			case <-abortWatchDone:
			}
		}()
	}

	mu.Lock()
	drainRefill()
	for outstanding > 0 || aborting {
		idle.Wait()
	}
	closed = true
	mu.Unlock()

	close(growDone)
	<-done
	close(pollDone)
	<-pollExited
	if stopWatchDone != nil {
		close(stopWatchDone)
		<-stopExited
	}
	if abortWatchDone != nil {
		close(abortWatchDone)
		<-abortExited
	}

	// The watcher's select picks arbitrarily when cfg.Stop/cfg.Abort and their
	// own *WatchDone are both ready, so a signal closing as the drain finishes
	// could otherwise go unseen and flip this run's verdict on a coin toss.
	// Reading both once more, after their watchers have exited and left
	// signalled/aborted without a concurrent writer, settles it (#3520,
	// #3521). announce=false: the drain is over, so there is none to
	// announce for Stop. observeAbort() has no such quiet mode — by this
	// point outstanding is already 0, so if this is the call that first
	// notices cfg.Abort closed, reclaimInFlight has nothing left to reclaim
	// and only prints its harmless "nothing in flight" line.
	mu.Lock()
	observeStop(false)
	observeAbort()
	mu.Unlock()

	if signalled || aborted {
		return ErrSignalledStop
	}
	if stale {
		return ErrImageStale
	}
	if !dispatchedAny {
		return ErrOpenNoneDispatchable
	}
	return nil
}
