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
// #1637): a ticker goroutine, symmetric to the Grown() listener below,
// retries drainRefill() on this interval so a transient refill miss -- an
// eventually-consistent discover() result that doesn't yet show a
// just-merged blocker's child as ready, a blocker resolving while every Box
// is busy, a touch-overlap deferral, or a transient DepsOf hiccup -- gets
// retried without waiting for an unrelated Box to finish. Hardcoded for now;
// a follow-up issue (#1638) makes it an operator knob. cfg.pollInterval
// overrides it for tests that can't wait out a real interval.
const defaultPollInterval = 30 * time.Second

// ErrImageStale is returned by RunContinuous when the freshness checker
// reports the loaded image would be rebuilt against the current
// base-branch tip: no further Boxes are launched, in-flight ones are left
// to finish on the image they started with, and RunContinuous returns once
// they do, so the driving loop can rebuild and re-invoke (exit code 4, see
// main.go's runExitCode).
var ErrImageStale = errors.New("image stale; rebuild and re-invoke")

// Discoverer re-queries the dispatchable batch, its blocker edges, the
// source (native relationship vs body-text parsing) each blocker was
// resolved from, and the set of issues whose own BuildEdges/DepsOf call
// errored (#752, #1103) — a transient tracker hiccup that looks identical to
// a confirmed zero-blocker issue in edges alone unless a caller checks
// failed explicitly. RunContinuous calls it once at startup and again before
// every slot refill, so a blocker that merges mid-run is picked up without a
// fresh invocation.
type Discoverer func() (issues []Issue, edges map[string][]string, sources Sources, failed map[string]bool, err error)

// FreshnessChecker answers whether a refill may launch a new Box.
// Applicable is false for a runtime with no loaded image to compare
// (bwrap) — such a refill always proceeds. Fresh is meaningless when
// applicable is false.
type FreshnessChecker func() (applicable, fresh bool, message string)

// nextReady scans issues in order for the first one ready to dispatch,
// applying the same selection drainMaxJobs does for a whole batch —
// blocker-failed cascade, blocked skip, touch-overlap defer — but returns
// after the first match rather than collecting a whole wave, since a
// refill only ever needs to fill one freed slot. sources carries each
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
		var failed, unready []string
		if !cfg.PreResolved {
			_, failed, unready = blockerStatus(cfg, it, cf, iss.Number, edges)
		}
		switch {
		case !cfg.PreResolved && !cfg.IgnoreBlockers && depsOfFailed[iss.Number]:
			// Own DepsOf call failed (#752, #1103) -- edges[iss.Number] is
			// unreliable, not a confirmed zero-blocker result. Hold rather
			// than launch or cascade-fail; the next refill retries.
			skip(iss.Number, fmt.Sprintf("    ~~ #%s blocker check failed; will retry\n", iss.Number))
		case len(failed) > 0:
			skip(iss.Number, fmt.Sprintf("    !! #%s  status=blocker-failed  note=#%s failed; skipping\n", iss.Number, strings.Join(failed, ", #")))
			transitionState(it, iss.Number, forge.Dispatchable, forge.Failed)
		case len(unready) > 0:
			skip(iss.Number, fmt.Sprintf("    ~~ #%s blocked by #%s; skipping\n", iss.Number, strings.Join(unready, ", #")))
		default:
			if collider, overlapped := checkOverlap(iss.Number); overlapped {
				skip(iss.Number, fmt.Sprintf("    ~~ #%s touches overlap in-progress #%s; deferring\n", iss.Number, collider))
				continue
			}
			return iss, true
		}
	}
	return Issue{}, false
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

// RunContinuous runs the opt-in slot-refill dispatch mode (#527): it fills
// up to cfg.MaxParallel slots from discover's result, then, as each Box
// finishes, consults fresh before refilling the slot it freed. A fresh
// result re-runs discover (blocker readiness, touch overlap, and cascade
// failure — the same selection nextReady applies) and claims and launches
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
	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		return err
	}

	var limiter *Limiter
	if session != nil {
		limiter = session.Limiter
	}
	if limiter == nil {
		// Headless (CONTINUOUS_DISPATCH) and every nil-Session call site: a
		// fixed cap for this invocation only, never resized -- behaviour is
		// unchanged from the plain int cfg.MaxParallel this replaces.
		limiter = NewLimiter(cfg.MaxParallel)
	}
	var terminated *terminate.Registry
	if session != nil {
		terminated = session.Terminated
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
			return false
		}
		issues, edges, sources, failed, err := discover()
		if err != nil {
			fmt.Fprintf(os.Stderr, "continuous: re-discover: %v\n", err)
			return false
		}
		if len(edges) > 0 {
			if node, cycle := detectCycle(edges, issueNums(issues)); cycle {
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
				fmt.Printf("    !! #%s FAILED (logs/issue-%s.log)\n", iss.Number, iss.Number)
				transitionState(it, iss.Number, forge.InProgress, forge.Failed)
				s.Fail(iss.Number, iss.Generation, result)
			default:
				fmt.Printf("    <- #%s done  (logs/issue-%s.log)\n", iss.Number, iss.Number)
				s.Settle(d, iss.Number, iss.Generation, result)
			}
			limiter.Release()
			mu.Lock()
			outstanding--
			drainRefill()
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

	// growDone stops the grow listener once this call is finished; done
	// confirms it has actually exited before RunContinuous returns, so no
	// call ever leaks a goroutine watching a Limiter shared across a whole
	// Console session.
	growDone := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-limiter.Grown():
				// A Console "+" mid-drain (ADR 0023, issue #653): the
				// operator's raise should launch a held pick right away, not
				// wait for an unrelated Box to settle or a background poll.
				// Loop rather than a single refill() call: per Grown's
				// signal-loss contract, one signal only means "at least one
				// unit freed" (issue #766) — draining until refill does
				// nothing catches every slot the raise actually freed.
				mu.Lock()
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
