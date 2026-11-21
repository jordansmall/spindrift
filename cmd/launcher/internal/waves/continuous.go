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
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/panicguard"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/shutdown"
	"spindrift.dev/launcher/internal/terminate"
)

// defaultPollInterval is the background refill poll's cadence (issue #1637),
// retrying a transient refill miss without waiting for a Box to finish. Three
// minutes, not 30s (issue #2874): each tick spends tracker rate-limit budget.
// Issue #1638 tracks making it an operator knob.
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
// precedence over ErrImageStale and ErrOpenNoneDispatchable, so an explicit
// wind-down is never masked by a verdict found in the same drain (#3520).
// Dispatch's one-shot wave returns it too, once its shutdown.Gate reports
// signalled (#3522).
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
	// Pruning keeps logged bounded over a long session, and lets an issue that
	// leaves and returns re-log its state.
	pruneAbsent(logged, issues)
	// skip drops a line unchanged since the issue's last one: refill re-walks
	// this list on every completion and ~3m poll tick (#1637). A nil logged
	// (unit tests) disables it.
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

// pruneAbsent deletes entries of m whose issue is not in issues. A nil m is a
// no-op.
func pruneAbsent(m map[string]string, issues []Issue) {
	if m == nil {
		return
	}
	present := make(map[string]bool, len(issues))
	for _, iss := range issues {
		present[iss.Number] = true
	}
	for num := range m {
		if !present[num] {
			delete(m, num)
		}
	}
}

// issueReadiness is nextReady's classification as a pure query, with no
// printing or dedup (the stale-drain heldBack count reuses it, #2678). line
// is what nextReady would print for a not-ready result.
func issueReadiness(cfg Config, it forge.IssueTracker, cf forge.CodeForge, checkOverlap func(string) (string, bool), iss Issue, edges map[string][]string, depsOfFailed map[string]bool) (ready bool, line string) {
	var unready []string
	if !cfg.IgnoreBlockers {
		// Resolved per call rather than threaded in (issue #2946): caps never
		// varies within one Dispatch or RunContinuous invocation.
		caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		unready = unreadyBlockers(it, caps, cfg.BranchPrefix, iss.Number, edges, cfg.SeedScopeOf)
	}
	switch {
	case !cfg.IgnoreBlockers && depsOfFailed[iss.Number]:
		// Own DepsOf call failed (#752, #1103): edges[iss.Number] is not a
		// confirmed zero-blocker result. Hold; the next refill retries.
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
// set (issue #1646): an eventually-consistent listing can still show a
// just-claimed issue. Dropping here, not in nextReady's loop, avoids a skip
// line that would repeat O(N^2) times over a run.
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
// mu released; mu is held on entry and exit (#2775). Both callers run
// staleDrain.finish under mu first, making inProgress() false, so no second
// drain report can race in behind this one.
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

	limiter := NewLimiter(cfg.MaxParallel)
	var terminated *terminate.Registry
	if session != nil {
		terminated = session.Terminated
	}
	if terminated == nil {
		// Only tests reach this (see Session.Terminated). The abort path needs
		// a Registry: Reclaim's Mark stops a surviving Box goroutine Failing or
		// Settling an issue already released to Dispatchable (#3521). An
		// unmarked Registry behaves like nil, so no non-abort path changes.
		terminated = terminate.NewRegistry()
	}
	reaper := f.AsReaper()

	// mu guards every piece of run state below (#653): bootstrap, completion
	// and poll-tick refills all run under it, so they never interleave.
	var mu sync.Mutex
	idle := sync.NewCond(&mu)
	stale := false
	// signalled latches a wind-down request seen on cfg.Stop (#3520).
	signalled := false
	// aborted latches a second-signal abort seen on cfg.Abort (#3521). Any
	// mu-held site may be first to see cfg.Abort closed; the latch, not a
	// single writer, keeps it to one reclaim.
	aborted := false
	// aborting spans that one reclaim, so the terminal wait cannot return
	// while a Reclaim sits between its Kill and the InProgress->Dispatchable
	// transition: the killed Box's completion goroutine empties inflight inside
	// that window, which would strand the issue on InProgress (#3521).
	aborting := false
	dispatchedAny := false
	claimed := make(map[string]bool)
	// inflight, unlike claimed, forgets an issue the moment it finishes, or the
	// abort watcher would drag a Complete issue back to Dispatchable (#3521).
	// Claim and the watcher's snapshot share mu, so a Box is either in the
	// snapshot or never launches. It also stands in for a sync.WaitGroup,
	// unsafe here: the poll tick would call wg.Add with no causal link to a
	// counted Box.
	inflight := make(map[string]bool)
	// logged keys an issue to the last skip line printed for it (#1637).
	logged := make(map[string]string)
	// claimSkips is logged's counterpart for claim-failure skip lines (#4892).
	claimSkips := make(map[string]string)
	closed := false
	var staleDrain staleDrainTracker
	// Tests override now to make freeSlotSecs deterministic (issue #2678).
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	// Tests inject a fake Clock for the rate-limited re-discover backoff
	// (issue #2866).
	clock := cfg.Clock
	if clock.Sleep == nil {
		clock = retry.RealClock()
	}

	// observeStop is a non-blocking latch on cfg.Stop (#3520). Caller holds mu.
	// announce is false only for the final post-drain latch, where a drain
	// line would mislead.
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
			if len(inflight) > 0 {
				fmt.Println("==> stop requested; draining outstanding work")
			} else {
				fmt.Println("==> stop requested; nothing in flight")
			}
		default:
		}
	}

	// reclaimInFlight snapshots inflight and drops mu for
	// shutdown.AbortInFlight's I/O; mu is held on entry and exit. AbortInFlight
	// is the same Reclaim loop dispatchWave's shutdown.Gate uses (#3522).
	reclaimInFlight := func() {
		nums := make([]string, 0, len(inflight))
		for num := range inflight {
			nums = append(nums, num)
		}
		aborting = true
		mu.Unlock()
		shutdown.AbortInFlight(it, cf, cfg.BranchPrefix, reaper, terminated, cfg.SettledLabels, nums)
		mu.Lock()
		aborting = false
		idle.Broadcast()
	}

	// observeAbort is a non-blocking latch on cfg.Abort (#3521). Caller holds
	// mu. Unlike observeStop it runs the one-time reclaim itself, so whichever
	// site first sees cfg.Abort closed does it. Leaving the reclaim to the
	// watcher alone races the shutdown join below, and can lose under
	// scheduler pressure when nothing was in flight, dropping the
	// announcement.
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

	// refill reports whether it launched a Box, so drainRefill can loop it.
	var refill func() bool
	// Predeclared: refill's completion goroutine calls it.
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
			// heldBack comes from the Queue's own side-effect-free count
			// (#2939). A Pending error reports unknown, not a fabricated 0
			// (#2678).
			if n, err := queue.Pending(claimed); err != nil {
				fmt.Fprintf(os.Stderr, "continuous: query pending for stale-drain report: %v\n", err)
				staleDrain.heldBackUnknown = true
			} else {
				staleDrain.heldBack = n
			}
			if len(inflight) == 0 {
				// No completion goroutine will report this drain. end is start
				// itself so Duration() is exactly zero, not a timing artifact.
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
			// mu is dropped so the stop watcher can print its drain line
			// during the backoff (#3520). Another refill may run meanwhile;
			// safe, since this one re-reads Discover, claimed, and the flags
			// after re-taking mu.
			mu.Unlock()
			clock.Sleep(backoff)
			mu.Lock()
			observeStop(true)
			observeAbort()
			if stale || closed || signalled || aborted {
				return false
			}
		}
		// Refill always works the discovered pool, never a hand-picked list, so
		// unlike NewPlan it sorts unconditionally (ADR 0040).
		forge.SortByPriority(batch.Issues, func(i Issue) forge.Priority { return i.Priority })
		if len(batch.Edges) > 0 {
			if node, cycle := detectCycle(batch.Edges, forge.Numbers(batch.Issues, func(i Issue) string { return i.Number })); cycle {
				fmt.Fprintf(os.Stderr, "==> ERROR: dependency cycle detected (issue #%s is in the cycle); skipping this refill\n", node)
				return false
			}
		}
		checkOverlap := waveOverlapCheck(cfg, it, cf)
		unclaimed := dropClaimed(batch.Issues, claimed)
		pruneAbsent(claimSkips, batch.Issues)
		// A failed claim must not strand the free slot (#4892): walk on to the
		// next candidate. The failed issue is retried on the next trigger.
		var iss Issue
		for {
			next, ok := nextReady(cfg, it, cf, checkOverlap, unclaimed, batch.Edges, batch.Sources, batch.Failed, logged)
			if !ok {
				return false
			}
			err := queue.Claim(next.Number)
			if err == nil {
				iss = next
				break
			}
			// Kept apart from logged: nextReady prunes logged against the shrunken
			// slice it gets on the retry, which would drop this entry and reprint
			// the line on every trigger.
			if line := claimSkipLine(next.Number, err); claimSkips[next.Number] != line {
				fmt.Print(line)
				claimSkips[next.Number] = line
			}
			unclaimed = dropClaimed(unclaimed, map[string]bool{next.Number: true})
		}
		dispatchedAny = true
		claimed[iss.Number] = true
		// New arms the kill latch, so it runs under mu before the inflight
		// entry, not in the goroutine (#3521): every issue reclaimInFlight can
		// reap is already armed, and no later New re-arms a latch Kill closed.
		d := f.New(iss.Number, iss.Title)
		inflight[iss.Number] = true
		launched = true
		panicguard.Go(func() {
			defer d.Close()
			disp := d.Run()
			switch {
			case terminated.Marked(iss.Number, iss.Generation):
				// terminate.Reclaim (ADR 0024) already reaped the Box, moved
				// the issue to Dispatchable, and logged, so neither a Failed
				// transition nor a Settle belongs here.
				fmt.Printf("    ~~ #%s reclaimed; abandoning\n", iss.Number)
			default:
				dispatch.Route(disp,
					func() struct{} {
						// A live run, possibly orphaned by a killed launcher, owns
						// the container: leave its claim untouched (#562, #3633).
						fmt.Printf("    ~~ #%s already in flight; skipping (live run continues)\n", iss.Number)
						return struct{}{}
					},
					func(result dispatch.Result) struct{} {
						fmt.Printf("    !! #%s FAILED (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
						result.ReportFailureReason(iss.Number)
						session.record(iss.Number, transitionState(d, it, iss.Number, settle.ForReason(dispatchrecord.ReasonBoxFailed), result.FailureNote()))
						session.boxFailed(iss.Number, iss.Generation)
						return struct{}{}
					},
					func(result dispatch.Result) struct{} {
						fmt.Printf("    <- #%s done  (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
						session.record(iss.Number, s.Settle(d, iss.Number, iss.Generation, result))
						return struct{}{}
					})
			}
			mu.Lock()
			// Needs the pre-delete inflight count (the busy slots over the
			// interval just ended); below the delete it under-bills one Box.
			staleDrain.checkpointIfNeeded(now(), len(inflight))
			// Must run before the abort watcher can next take mu, or a Box that
			// just finished on its own would still look in-flight to it (#3521).
			delete(inflight, iss.Number)
			// Released under mu, not before taking it: a slot freed earlier lets
			// a refill already holding mu claim it ahead of this completion's
			// bookkeeping, so one completion drives an extra refill pass.
			limiter.Release()
			drainRefill()
			if staleDrain.inProgress() && len(inflight) == 0 {
				reportStaleDrainReleasingMu(&mu, queue, staleDrain.finish(staleDrain.slotAt))
			}
			if len(inflight) == 0 {
				idle.Broadcast()
			}
			mu.Unlock()
		})
		return true
	}

	// drainRefill fills every free slot that has ready work and reports how many
	// it launched. A slot an earlier refill missed stays free until a later
	// call claims it, so one trigger is not worth exactly one launch (#1587).
	drainRefill = func() int {
		n := 0
		for refill() {
			n++
		}
		return n
	}

	pollInterval := resolvePollInterval(cfg.pollInterval)
	pollDone := make(chan struct{})
	pollExited := make(chan struct{})
	panicguard.Go(func() {
		defer close(pollExited)
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// After closed is set, refill's guard makes this a no-op.
				mu.Lock()
				n := drainRefill()
				mu.Unlock()
				if n > 0 {
					// Not proof of a missed refill: a tick can win the race
					// against a completion trigger.
					fmt.Printf("    <- poll: launched %d issue(s)\n", n)
				}
			case <-pollDone:
				return
			}
		}
	})

	// The stop watcher is joined before return, so signalled has no writer
	// left when read below (#3520).
	var stopWatchDone, stopExited chan struct{}
	if cfg.Stop != nil {
		stopWatchDone = make(chan struct{})
		stopExited = make(chan struct{})
		panicguard.Go(func() {
			defer close(stopExited)
			select {
			case <-cfg.Stop:
				// refill only observes a stop on the next completion or poll
				// tick; this announces one arriving during idle.Wait().
				mu.Lock()
				observeStop(true)
				mu.Unlock()
			case <-stopWatchDone:
			}
		})
	}

	// The abort watcher services an abort arriving while nothing else will call
	// refill again (blocked in idle.Wait(), or past the last poll tick), which
	// would otherwise wait for the final re-check after shutdown (#3521).
	var abortWatchDone, abortExited chan struct{}
	if cfg.Abort != nil {
		abortWatchDone = make(chan struct{})
		abortExited = make(chan struct{})
		panicguard.Go(func() {
			defer close(abortExited)
			select {
			case <-cfg.Abort:
				mu.Lock()
				observeAbort()
				mu.Unlock()
			case <-abortWatchDone:
			}
		})
	}

	mu.Lock()
	drainRefill()
	for len(inflight) > 0 || aborting {
		idle.Wait()
	}
	closed = true
	mu.Unlock()

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

	// A watcher's select picks arbitrarily when its signal and *WatchDone are
	// both ready, so a signal closing as the drain ends could flip the verdict
	// on a coin toss; re-reading after the joins settles it (#3520, #3521).
	// observeAbort has no quiet mode; with inflight empty it only prints
	// "nothing in flight".
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
