package waves

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
)

// defaultPollInterval is the background refill poll's fixed cadence (issue
// #1637): a ticker goroutine, symmetric to the Resized() listener below,
// retries drainRefill() on this interval so a transient refill miss -- an
// eventually-consistent discover() result that doesn't yet show a
// just-merged blocker's child as ready, a blocker resolving while every Box
// is busy, a touch-overlap deferral, or a transient DepsOf hiccup -- gets
// retried without waiting for an unrelated Box to finish. Hardcoded for now;
// a follow-up issue (#1638) makes it an operator knob. cfg.pollInterval
// overrides it for tests that can't wait out a real interval.
const defaultPollInterval = 30 * time.Second

// drainMarker is the file emitDrainReport appends each stale-drain report's
// HostLog line to, under .spindrift/logs/ (#2678) -- named like engine.go's
// blockedMarker rather than left as a scattered string literal.
const drainMarker = "drain.log"

// ErrImageStale is returned by RunContinuous when the freshness checker
// reports the loaded image would be rebuilt against the current
// base-branch tip: no further Boxes are launched, in-flight ones are left
// to finish on the image they started with, and RunContinuous returns once
// they do, so the driving loop can rebuild and re-invoke (exit code 4, see
// main.go's runExitCode).
var ErrImageStale = errors.New("image stale; rebuild and re-invoke")

// Discoverer re-queries the dispatchable batch, its blocker edges, the
// source (native relationship vs body-text parsing) each blocker was
// resolved from, and the set of issues whose own NewReadiness/DepsOf call
// errored (#752, #1103) — a transient tracker hiccup that looks identical to
// a confirmed zero-blocker issue in edges alone unless a caller checks
// failed explicitly. RunContinuous calls it once at startup and again before
// every slot refill, so a blocker that merges mid-run is picked up without a
// fresh invocation. Config.DiscoverReporting (#2777) is a second,
// independently-settable value of this same type: RunContinuous never calls
// it for regular discovery, only for the stale-transition heldBack
// computation, where it stands in for the discover param when the caller
// needs that call to stay side-effect free.
type Discoverer func() (issues []Issue, edges map[string][]string, sources Sources, failed map[string]bool, err error)

// FreshnessChecker answers whether a refill may launch a new Box.
// Applicable is false for a runtime with no loaded image to compare
// (bwrap) — such a refill always proceeds. Fresh is meaningless when
// applicable is false.
type FreshnessChecker func() (applicable, fresh bool, message string)

// nextReady scans issues in order for the first one ready to dispatch,
// applying the same selection drainMaxJobs does for a whole batch —
// blocked skip, touch-overlap defer — but returns after the first match
// rather than collecting a whole wave, since a refill only ever needs to
// fill one freed slot. sources carries each
// blocker's provenance alongside edges, mirroring drainMaxJobs' own
// parameter (engine.go) — like that function's general blocked-skip line,
// nextReady's does not render it: the only current Sources consumer,
// writeBlockedMarker, fires for OriginClaimed only, a mode continuous
// dispatch never uses (issue #662).
func nextReady(cfg Config, it forge.IssueTracker, cf forge.CodeForge, checkOverlap func(string) (string, bool), issues []Issue, edges map[string][]string, sources Sources, depsOfFailed map[string]bool, logged map[string]string) (Issue, bool) {
	// Drop dedup entries for issues no longer in the candidate batch: keeps
	// logged from growing unbounded across a long Console session, and lets an
	// issue that left and later returns re-log its state afresh.
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
	// skip logs a non-dispatch outcome for an issue at most once per distinct
	// line: refill re-walks this list on every completion and the background
	// poll re-walks it every ~30s (#1637), so an unchanged blocked/deferred
	// reason would otherwise reprint on every tick. logged carries the last
	// line emitted per issue across those re-walks; a nil map (direct unit
	// tests) disables dedup. The line re-prints when it changes -- a new
	// blocker appears, one of several resolves -- so a real state change still
	// surfaces.
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

// issueReadiness classifies iss the same way nextReady's selection loop
// does -- own DepsOf failure, unresolved blocker, touch-overlap -- but as a
// pure query with no printing/dedup side effect, so it can be reused by a
// caller that only wants to know ready-vs-not (the stale-drain heldBack
// count, #2678) without nextReady's own skip()/logged bookkeeping. line is
// the same non-dispatch message nextReady would otherwise print for a
// not-ready result; it is meaningless when ready is true.
func issueReadiness(cfg Config, it forge.IssueTracker, cf forge.CodeForge, checkOverlap func(string) (string, bool), iss Issue, edges map[string][]string, depsOfFailed map[string]bool) (ready bool, line string) {
	var unready []string
	if !cfg.PreResolved {
		unready = unreadyBlockers(it, cf, iss.Number, edges, cfg.SeedScopeOf)
	}
	switch {
	case !cfg.PreResolved && !cfg.IgnoreBlockers && depsOfFailed[iss.Number]:
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

// dropClaimed filters a refill's discover result against the in-run claimed
// set before nextReady scans it (issue #1646). GitHub's search-backed issue
// listing is eventually consistent, so a refill soon after a claim can still
// see the just-claimed issue as dispatchable; dropping it here rather than
// inside nextReady's loop keeps the no-double-dispatch guarantee intact
// while avoiding a per-issue skip line every refill re-walks — with N slots
// claimed that line would otherwise repeat O(N^2) times over a run.
func dropClaimed(issues []Issue, claimed map[string]bool) []Issue {
	unclaimed := make([]Issue, 0, len(issues))
	for _, iss := range issues {
		if !claimed[iss.Number] {
			unclaimed = append(unclaimed, iss)
		}
	}
	return unclaimed
}

// countReady counts how many issues in the batch are actually ready to
// dispatch -- applying nextReady's own blocked/touch-overlap/failed-check
// filtering without its print/dedup side effects -- so the stale-drain
// report's heldBack number (#2678) reflects issues that would genuinely
// have launched, not every unclaimed issue sitting in the discovered batch
// regardless of readiness.
func countReady(cfg Config, it forge.IssueTracker, cf forge.CodeForge, checkOverlap func(string) (string, bool), issues []Issue, edges map[string][]string, depsOfFailed map[string]bool) int {
	n := 0
	for _, iss := range issues {
		if ready, _ := issueReadiness(cfg, it, cf, checkOverlap, iss, edges, depsOfFailed); ready {
			n++
		}
	}
	return n
}

// emitDrainReport prints report to stdout and appends its HostLog line to
// pwd's drain.log, swallowing any file error to stderr (#2678). Shared by
// both emission sites -- the stale-transition branch's zero-outstanding
// case (refill, below) and the in-flight completion goroutine's
// drain-finished case -- so the open/write/close pattern is written once.
// Also forwards report to cfg.OnDrainReport when set, so a Console session
// -- which never sees this function's raw stdout write, since it runs under
// tea.WithAltScreen() -- learns the same report through that callback
// instead (#2678).
func emitDrainReport(cfg Config, pwd string, report DrainReport) {
	fmt.Print(report.Console())
	if cfg.OnDrainReport != nil {
		cfg.OnDrainReport(report)
	}
	logPath := filepath.Join(dispatch.HostLogDirFor(pwd), drainMarker)
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "continuous: open %s: %v\n", logPath, err)
		return
	}
	defer logFile.Close()
	if _, err := logFile.WriteString(report.HostLog()); err != nil {
		fmt.Fprintf(os.Stderr, "continuous: write %s: %v\n", logPath, err)
	}
}

// RunContinuous runs the opt-in slot-refill dispatch mode (#527): it fills
// up to cfg.MaxParallel slots from discover's result, then, as each Box
// finishes, consults fresh before refilling the slot it freed. A fresh
// result re-runs discover (blocker readiness, touch overlap — the same
// selection nextReady applies) and claims and launches
// the next unblocked issue; a rebuild-needed result stops refilling — the
// slot stays empty and in-flight Boxes still run to completion — and
// RunContinuous returns ErrImageStale once every Box has finished. Claim
// stays in-process: every issue claimed during this invocation is recorded
// in a claimed set guarded by the same mutex as discovery, and every
// refill's discovery result is filtered against it before selection. The
// forge's label swap is not the authority here — GitHub's search-backed
// issue listing is eventually consistent, so a refill soon after a claim
// can still see the just-claimed issue as dispatchable; the in-run record
// is what actually stops a second Box from launching for it.
func RunContinuous(cfg Config, session *Session, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, discover Discoverer, fresh FreshnessChecker) error {
	if err := os.MkdirAll(dispatch.HostLogDirFor(pwd), 0o755); err != nil {
		return err
	}

	var limiter *Limiter
	var terminated *terminate.Registry
	if session != nil {
		limiter, terminated = session.Limiter, session.Terminated
	}
	if limiter == nil {
		// Headless (CONTINUOUS_DISPATCH) and every nil-Session call site: a
		// fixed cap for this invocation only, never resized -- behaviour is
		// unchanged from the plain int cfg.MaxParallel this replaces.
		limiter = NewLimiter(cfg.MaxParallel)
	}

	// mu also guards stale/dispatchedAny/claimed/outstanding below, exactly
	// as it guarded refill's shared state before #653 -- every refill call,
	// whether from the bootstrap loop, a completing Box, or the grow
	// listener below, runs under this one lock, so they never interleave.
	var mu sync.Mutex
	idle := sync.NewCond(&mu)
	stale := false
	dispatchedAny := false
	claimed := make(map[string]bool)
	// logged dedups nextReady's non-dispatch skip/defer lines across the
	// refill re-walks that share mu -- every completion, grow, and ~30s poll
	// tick (#1637) re-walks the same candidates, so an unchanged
	// blocked/deferred reason would otherwise reprint on every tick. Keyed by
	// issue number to the last line emitted for it; nextReady re-prints only
	// when that line changes and prunes issues that leave the batch.
	logged := make(map[string]string)
	// outstanding counts in-flight Boxes. A plain sync.WaitGroup can't
	// coordinate safely here: the grow listener below can call refill --
	// and so wg.Add -- from a goroutine with no causal link to any counted
	// Box, which risks the documented WaitGroup race (Add landing after a
	// concurrent Wait has already committed to returning). Tracking the
	// count under mu instead makes "is anything still outstanding" and "am
	// I about to add more" the same critical section, so the two can never
	// race.
	outstanding := 0
	closed := false
	// drain consolidates the stale-drain report state (#2678, #2774): see
	// drainTracker in drain_tracker.go for the per-field rationale.
	var drain drainTracker
	// now is cfg's test override (issue #2678, so the stale-drain report's
	// freeSlotSecs accumulation is exactly assertable from a deterministic
	// clock sequence rather than only >=0-checkable against real wall
	// time), or the production clock.
	now := cfg.now
	if now == nil {
		now = time.Now
	}

	// refill reports whether it launched a Box, so a caller filling more
	// than one freed slot from a single trigger (the grow listener below,
	// or a completing Box) can loop it until a call finally does nothing,
	// rather than assuming one trigger is worth exactly one launch.
	var refill func() bool
	// drainRefill is predeclared here, like refill above, so refill's
	// completion-handler goroutine can call it before its body is assigned.
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
			drain.begin(now(), limiter.Cap())
			// heldBack only calls discover() for callers whose Discoverer is
			// a pure query. Console's Queue.Discover (cfg.PreResolved, the
			// only PreResolved caller today) claims the ready pick it
			// returns as an inseparable side effect of discovering it
			// (queue.go) -- a call here, whose result is otherwise
			// discarded, would claim a pick this drain then never
			// dispatches, orphaning it at InProgress. cfg.PendingCount, when
			// set, is a pure alternative such a caller can supply instead
			// (Console's Queue.PendingCount, #2678) -- checked first since a
			// caller that has it available never needs the discover()
			// fallback, PreResolved or not. When neither applies (PreResolved
			// with no PendingCount -- no caller does this today), heldBack is
			// reported unknown rather than risk the claim or fabricate a
			// confirmed-looking 0 (#2678 review finding). Within this case,
			// cfg.DiscoverReporting, when set, is preferred over the discover
			// param itself (#2777): the discover param may carry a
			// caller-side side effect -- the CLI's log-on-poll behavior --
			// that a reporting-only heldBack call must not trigger, so a
			// caller with such a side effect supplies a pure
			// DiscoverReporting closure instead.
			switch {
			case cfg.PendingCount != nil:
				drain.heldBack = cfg.PendingCount()
			case !cfg.PreResolved:
				reportDiscover := discover
				if cfg.DiscoverReporting != nil {
					reportDiscover = cfg.DiscoverReporting
				}
				if issues, edges, _, failed, err := reportDiscover(); err != nil {
					fmt.Fprintf(os.Stderr, "continuous: re-discover for drain report: %v\n", err)
					drain.heldBackUnknown = true
				} else {
					unclaimed := dropClaimed(issues, claimed)
					checkOverlap := waveOverlapCheck(cfg, it, cf)
					drain.heldBack = countReady(cfg, it, cf, checkOverlap, unclaimed, edges, failed)
				}
			default:
				drain.heldBackUnknown = true
			}
			if outstanding == 0 {
				// Nothing in flight -- the drain is already over. Report it
				// now rather than leaving it to the completion goroutine
				// (slice 3), which never runs when nothing is outstanding.
				// drainEnd is set to drainStart itself, not a fresh
				// time.Now(), so Duration() is exactly zero rather than a
				// near-zero timing artifact.
				emitDrainReport(cfg, pwd, drain.finish(drain.drainStart))
			}
			return false
		}
		issues, edges, sources, failed, err := discover()
		if err != nil {
			fmt.Fprintf(os.Stderr, "continuous: re-discover: %v\n", err)
			return false
		}
		// Continuous refill has no Origin concept — it is always the
		// discovered pool, never a hand-picked selective list — so, unlike
		// NewPlan, sort unconditionally (ADR 0040).
		forge.SortByPriority(issues, func(i Issue) forge.Priority { return i.Priority })
		if len(edges) > 0 {
			if node, cycle := detectCycle(edges, forge.Numbers(issues, func(i Issue) string { return i.Number })); cycle {
				fmt.Fprintf(os.Stderr, "==> ERROR: dependency cycle detected (issue #%s is in the cycle); skipping this refill\n", node)
				return false
			}
		}
		checkOverlap := waveOverlapCheck(cfg, it, cf)
		unclaimed := dropClaimed(issues, claimed)
		iss, ok := nextReady(cfg, it, cf, checkOverlap, unclaimed, edges, sources, failed, logged)
		if !ok {
			return false
		}
		dispatchedAny = true
		claimed[iss.Number] = true
		claimIssue(cfg, it, iss.Number)
		launched = true
		outstanding++
		go func() {
			d := f.New(iss.Number, iss.Title)
			defer d.Close()
			result := d.Run()
			switch {
			case terminated.Marked(iss.Number, iss.Generation):
				// Terminate (ADR 0024, issue #649) already reaped this Box,
				// transitioned the issue back to Dispatchable, and recorded
				// its own comment/log line -- neither a Failed transition
				// nor a Settle call belongs here now.
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
			// A real drain, if in progress and not yet finished, integrates
			// the idle slot-time that elapsed since the last checkpoint here,
			// using the pre-decrement outstanding count -- the busy-slot
			// count over the interval that just ended -- to derive how many
			// slots sat free across it. checkpointDrain uses drainCap, the
			// cap actually in effect over that interval, not a live
			// limiter.Cap() read (#2678 review finding); checkpointIfDraining
			// is a no-op outside a drain.
			drain.checkpointIfDraining(now(), limiter.Cap(), outstanding)
			outstanding--
			drainRefill()
			if drain.inProgress() && outstanding == 0 {
				emitDrainReport(cfg, pwd, drain.finish(drain.drainSlotAt))
			}
			if outstanding == 0 {
				idle.Broadcast()
			}
			mu.Unlock()
		}()
		return true
	}

	// drainRefill fills every currently-free slot that has ready work,
	// looping refill until a call finally does nothing rather than assuming
	// one trigger is worth exactly one launch, and reports how many it
	// launched so the poll ticker below can log only the ticks that actually
	// did something. All four refill triggers -- bootstrap, the grow
	// listener, a completing Box, and the poll ticker -- share this: a
	// single free slot the moment of the call is not the only thing that
	// may be fillable, since a slot freed by an earlier transient refill
	// miss (a not-yet-visible discover result, an unresolved blocker, a
	// touch-overlap deferral, or a DepsOf hiccup) stays free at the limiter
	// level until some later refill call successfully claims it (#1587).
	drainRefill = func() int {
		n := 0
		for refill() {
			n++
		}
		return n
	}

	// growDone stops the resize listener once this call is finished; done
	// confirms it has actually exited before RunContinuous returns, so no
	// call ever leaks a goroutine watching a Limiter shared across a whole
	// Console session.
	growDone := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-limiter.Resized():
				// A Console "+" or "-" mid-drain (ADR 0023, issue #653):
				// listen on Resized, not Grown, because a checkpoint is
				// needed on EITHER direction, not just a raise -- a lower
				// mid-drain is exactly as mis-attributing as a raise if the
				// stale drainCap is left to bridge across it (#2678 review
				// finding: the raise-only fix left the mirror-image
				// over-crediting bug on a lower). Loop drainRefill rather
				// than a single refill() call: per Resized's signal-loss
				// contract, one signal only means "at least one resize
				// happened" (issue #766's coalescing, extended) — draining
				// until refill does nothing catches every slot a raise
				// actually freed, and is a no-op for a lower (refill()
				// short-circuits on stale, which always holds during a
				// drain).
				mu.Lock()
				// The resize that just woke this listener, if a drain is in
				// progress, is exactly the moment the live cap changed:
				// close out the interval that just ended at drainCap, the
				// cap that held during it, before refreshing drainCap to
				// the new value for the interval that starts now (#2678
				// review finding -- this is what stops the new cap from
				// being retroactively credited to the whole preceding
				// interval at the next checkpoint). checkpointIfDraining is
				// a no-op outside a drain.
				drain.checkpointIfDraining(now(), limiter.Cap(), outstanding)
				drainRefill()
				mu.Unlock()
			case <-growDone:
				return
			}
		}
	}()

	// pollInterval is cfg's test override, or the production default.
	// pollDone/pollExited mirror growDone/done exactly: pollDone stops the
	// ticker once this call is finished, pollExited confirms it has actually
	// exited before RunContinuous returns.
	pollInterval := cfg.pollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	pollDone := make(chan struct{})
	pollExited := make(chan struct{})
	go func() {
		defer close(pollExited)
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// A tick after closed is set is a no-op: refill()'s own
				// stale||closed guard (above) makes drainRefill() return 0
				// immediately, so no explicit shutdown race-check is needed
				// here beyond the pollDone case below.
				mu.Lock()
				n := drainRefill()
				mu.Unlock()
				if n > 0 {
					// Usually means an event-driven refill missed and the slot
					// sat idle until this tick -- but a tick can also just win
					// the race against a completion/grow trigger, so this
					// isn't proof of a miss, only that the poll did something.
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
