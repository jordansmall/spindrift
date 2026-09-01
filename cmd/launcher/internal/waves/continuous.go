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
	"spindrift.dev/launcher/internal/terminate"
)

// defaultPollInterval is the background refill poll's fixed cadence (#1637):
// a ticker goroutine retries drainRefill() on this interval so a transient
// refill miss -- a not-yet-visible discover result, a blocker resolving while
// every Box is busy, a touch-overlap deferral, a DepsOf hiccup -- is not left
// waiting on an unrelated Box to finish. Minutes rather than seconds because
// each tick spends a share of the tracker's rate-limit budget (#2874).
// cfg.pollInterval overrides it for tests that can't wait out a real
// interval.
const defaultPollInterval = 3 * time.Minute

// resolvePollInterval is cfg's test override, or defaultPollInterval.
func resolvePollInterval(override time.Duration) time.Duration {
	if override <= 0 {
		return defaultPollInterval
	}
	return override
}

// ErrImageStale is returned by RunContinuous when the freshness checker
// reports the loaded image would be rebuilt against the current base-branch
// tip: no further Boxes are launched, in-flight ones finish on the image they
// started with, and RunContinuous returns once they do (exit code 4, see
// main.go's runExitCode).
var ErrImageStale = errors.New("image stale; rebuild and re-invoke")

// Discoverer re-queries the dispatchable Batch. Callers must check Failed:
// an issue whose own NewReadiness/DepsOf call errored is indistinguishable in
// Edges alone from a confirmed zero-blocker issue (#752, #1103).
// RunContinuous calls it at startup and again before every slot refill, so a
// blocker that merges mid-run is picked up without a fresh invocation.
type Discoverer func() (Batch, error)

// FreshnessChecker answers whether a refill may launch a new Box.
// Applicable is false for a runtime with no loaded image to compare
// (bwrap) — such a refill always proceeds. Fresh is meaningless when
// applicable is false.
type FreshnessChecker func() (applicable, fresh bool, message string)

// nextReady returns the first issue ready to dispatch, applying the same
// selection drainMaxJobs does for a whole batch -- blocked skip,
// touch-overlap defer -- but stopping at the first match, since a refill only
// ever needs to fill one freed slot. sources is taken for parity with
// drainMaxJobs but not rendered: its only consumer, writeBlockedMarker, fires
// for OriginClaimed only, a mode continuous dispatch never uses (#662).
func nextReady(cfg Config, it forge.IssueTracker, cf forge.CodeForge, checkOverlap func(string) (string, bool), issues []Issue, edges map[string][]string, sources Sources, depsOfFailed map[string]bool, logged map[string]string) (Issue, bool) {
	// Prune issues no longer in the candidate batch: keeps logged bounded
	// across a long Console session, and lets a returning issue re-log afresh.
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
	// re-walks this list on every completion and the background poll re-walks
	// it every ~3m, so an unchanged blocked/deferred reason would otherwise
	// reprint on every tick. The line re-prints when it changes, so a real
	// state change still surfaces; a nil logged map (direct unit tests)
	// disables dedup.
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

// issueReadiness classifies iss the same way nextReady's selection loop does
// -- own DepsOf failure, unresolved blocker, touch-overlap -- but as a pure
// query with no printing/dedup side effect, so a caller that only wants
// ready-vs-not can reuse it (#2678). line is the non-dispatch message
// nextReady would print; it is meaningless when ready is true.
func issueReadiness(cfg Config, it forge.IssueTracker, cf forge.CodeForge, checkOverlap func(string) (string, bool), iss Issue, edges map[string][]string, depsOfFailed map[string]bool) (ready bool, line string) {
	var unready []string
	if !cfg.IgnoreBlockers {
		// Resolved fresh per call rather than threaded in as a parameter:
		// it/cf never vary within a single Dispatch/RunContinuous invocation
		// (#2946).
		caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		unready = unreadyBlockers(it, cf, caps, iss.Number, edges, cfg.SeedScopeOf)
	}
	switch {
	case !cfg.IgnoreBlockers && depsOfFailed[iss.Number]:
		// Own DepsOf call failed (#752, #1103) -- edges[iss.Number] is
		// unreliable, not a confirmed zero-blocker result. Hold rather
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

// CountReady counts how many issues in batch pass the same
// blocked/touch-overlap/failed-check filtering issueReadiness applies to a
// single issue. Exported for the headless CLI's stale-drain heldBack query
// (#2939). claimed excludes issues this run already claimed, since
// batch.Issues can come from an eventually-consistent listing taken after an
// in-run claim.
//
// Deliberately conservative: of the three exclusions only an unresolved
// blocker edge is a durable, pre-existing block -- counting the other two
// would conflate the drain's own cost with issues merely incidentally
// delayed by this run's own concurrency.
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
// set (#1646): the tracker's issue listing is eventually consistent, so a
// refill soon after a claim can still see the just-claimed issue as
// dispatchable. Filtering here rather than inside nextReady's loop avoids a
// per-issue skip line that would repeat O(N^2) times over a run.
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
// mu released, then re-acquires it -- held on entry, held on exit (#2775).
//
// Safe because the write the single-emit invariant depends on is already
// committed: both call sites build report via staleDrain.finish(...) under
// mu, and finish is what makes staleDrain.inProgress() false. The report is
// then a private copy no other goroutine can reach, so no second drain report
// can race in behind this one.
func reportStaleDrainReleasingMu(mu *sync.Mutex, queue Queue, report StaleDrainReport) {
	mu.Unlock()
	queue.ReportStaleDrain(report)
	mu.Lock()
}

// RunContinuous runs the opt-in slot-refill dispatch mode (#527): it fills up
// to cfg.MaxParallel slots from queue.Discover's result, then, as each Box
// finishes, consults fresh before refilling the slot it freed. A
// rebuild-needed result stops refilling -- in-flight Boxes still run to
// completion -- and RunContinuous returns ErrImageStale once they have.
//
// Claim stays in-process. The forge's label swap is not the authority here:
// the tracker's issue listing is eventually consistent, so a refill soon
// after a claim can still see the just-claimed issue as dispatchable. The
// in-run claimed set, guarded by the same mutex as discovery, is what stops a
// second Box from launching for it.
func RunContinuous(cfg Config, session *Session, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, queue Queue, fresh FreshnessChecker) error {
	if err := os.MkdirAll(dispatch.HostLogDirFor(pwd), 0o755); err != nil {
		return err
	}

	var limiter *Limiter
	var terminated *terminate.Registry
	if session != nil {
		limiter, terminated = session.Limiter, session.Terminated
	}
	if limiter == nil {
		// Headless (CONTINUOUS_DISPATCH) and every nil-Session call site:
		// a fixed cap for this invocation only, never resized.
		limiter = NewLimiter(cfg.MaxParallel)
	}

	// mu also guards stale/dispatchedAny/claimed/outstanding: every refill
	// call, whatever its trigger, runs under this one lock, so they never
	// interleave.
	var mu sync.Mutex
	idle := sync.NewCond(&mu)
	stale := false
	dispatchedAny := false
	claimed := make(map[string]bool)
	// logged dedups nextReady's non-dispatch skip/defer lines across the
	// refill re-walks that share mu, keyed by issue number to the last line
	// emitted for it.
	logged := make(map[string]string)
	// outstanding counts in-flight Boxes. A plain sync.WaitGroup can't
	// coordinate safely here: the grow listener below can call refill -- and
	// so wg.Add -- from a goroutine with no causal link to any counted Box,
	// risking Add landing after a concurrent Wait has committed to returning.
	// Tracking under mu makes "is anything outstanding" and "am I about to
	// add more" the same critical section.
	outstanding := 0
	closed := false
	// staleDrain consolidates the stale-drain report state (#2678, #2774):
	// see staleDrainTracker in stale_drain_tracker.go for the per-field rationale.
	var staleDrain staleDrainTracker
	// now is cfg's test override (#2678, so the report's freeSlotSecs is
	// assertable from a deterministic clock), or the production clock.
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	// clock is cfg's injectable sleep seam for the rate-limited re-discover
	// backoff (#2866); nil, as in every production construction site, means
	// retry.RealClock().
	clock := cfg.Clock
	if clock.Sleep == nil {
		clock = retry.RealClock()
	}

	// refill reports whether it launched a Box, so a caller filling more than
	// one freed slot from a single trigger can loop it until a call finally
	// does nothing.
	var refill func() bool
	// Predeclared, like refill, so refill's completion goroutine can call it.
	var drainRefill func() int
	refill = func() bool {
		if stale || closed {
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
			// heldBack comes from queue.Pending() -- a quiet,
			// side-effect-free count each Queue supplies its own way --
			// rather than RunContinuous re-discovering to derive it (#2939).
			// An error is reported as unknown, never a fabricated 0.
			if n, err := queue.Pending(); err != nil {
				fmt.Fprintf(os.Stderr, "continuous: query pending for stale-drain report: %v\n", err)
				staleDrain.heldBackUnknown = true
			} else {
				staleDrain.heldBack = n
			}
			if outstanding == 0 {
				// The completion goroutine that would otherwise report the
				// drain never runs when nothing is outstanding. End is
				// staleDrainStart itself, so Duration() is exactly zero
				// rather than a timing artifact.
				reportStaleDrainReleasingMu(&mu, queue, staleDrain.finish(staleDrain.staleDrainStart))
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
			lb := retry.LinearBackoff{Unit: cfg.Policy.Unit, Clock: clock}
			backoff := lb.Duration(attempt)
			fmt.Fprintf(os.Stderr, "continuous: re-discover: rate limited; retry %d/%d in %s\n", attempt, cfg.Policy.Max, backoff)
			clock.Sleep(backoff)
		}
		// Continuous refill has no Origin concept — it is always the
		// discovered pool, never a hand-picked selective list — so, unlike
		// NewPlan, sort unconditionally (ADR 0040).
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
		launched = true
		outstanding++
		go func() {
			d := f.New(iss.Number, iss.Title)
			defer d.Close()
			result := d.Run()
			switch {
			case terminated.Marked(iss.Number, iss.Generation):
				// Terminate (ADR 0024) already reaped this Box, transitioned
				// the issue back to Dispatchable, and logged it -- neither a
				// Failed transition nor a Settle call belongs here now.
				fmt.Printf("    ~~ #%s terminated by operator; abandoning\n", iss.Number)
			case !result.Success:
				fmt.Printf("    !! #%s FAILED (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
				transitionState(it, iss.Number, forge.InProgress, forge.Failed)
				s.Fail(iss.Number, iss.Generation, result)
			default:
				fmt.Printf("    <- #%s done  (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
				s.Settle(d, iss.Number, iss.Generation, result)
			}
			limiter.Release()
			mu.Lock()
			// Integrate the idle slot-time since the last checkpoint using
			// the pre-decrement outstanding count -- the busy-slot count over
			// the interval that just ended, credited at staleDrainCap rather
			// than a live limiter.Cap() read.
			staleDrain.checkpointIfStaleDraining(now(), limiter.Cap(), outstanding)
			outstanding--
			drainRefill()
			if staleDrain.inProgress() && outstanding == 0 {
				reportStaleDrainReleasingMu(&mu, queue, staleDrain.finish(staleDrain.staleDrainSlotAt))
			}
			if outstanding == 0 {
				idle.Broadcast()
			}
			mu.Unlock()
		}()
		return true
	}

	// drainRefill fills every currently-free slot that has ready work and
	// reports how many it launched, so the poll ticker logs only the ticks
	// that did something. One trigger is not worth exactly one launch: a slot
	// freed by an earlier transient refill miss stays free at the limiter
	// level until some later refill call successfully claims it (#1587).
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
				// A Console "+" or "-" mid-drain (ADR 0023): Resized, not
				// Grown, because a lower mid-drain mis-attributes exactly as
				// much as a raise if the stale staleDrainCap bridges across
				// it. Loop drainRefill because one signal only means "at
				// least one resize happened" (#766's coalescing).
				mu.Lock()
				// Close the interval that just ended at the old
				// staleDrainCap before refreshing it, so the new cap is not
				// retroactively credited to the whole preceding interval.
				staleDrain.checkpointIfStaleDraining(now(), limiter.Cap(), outstanding)
				drainRefill()
				mu.Unlock()
			case <-growDone:
				return
			}
		}
	}()

	// pollDone/pollExited mirror growDone/done.
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
				// A tick after closed is a no-op: refill()'s own
				// stale||closed guard makes drainRefill() return 0.
				mu.Lock()
				n := drainRefill()
				mu.Unlock()
				if n > 0 {
					// A tick can also just win the race against another
					// trigger, so this is not proof an event-driven refill
					// missed.
					fmt.Printf("    <- poll: launched %d issue(s)\n", n)
				}
			case <-pollDone:
				return
			}
		}
	}()

	mu.Lock()
	drainRefill()
	for outstanding > 0 {
		idle.Wait()
	}
	closed = true
	mu.Unlock()

	close(growDone)
	<-done
	close(pollDone)
	<-pollExited

	if stale {
		return ErrImageStale
	}
	if !dispatchedAny {
		return ErrOpenNoneDispatchable
	}
	return nil
}
