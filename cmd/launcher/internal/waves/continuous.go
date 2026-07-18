package waves

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
)

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
func nextReady(cfg Config, it forge.IssueTracker, cf forge.CodeForge, checkOverlap func(string) (string, bool), issues []Issue, edges map[string][]string, sources Sources, depsOfFailed map[string]bool, claimed map[string]bool) (Issue, bool) {
	for _, iss := range issues {
		var failed, unready []string
		if !claimed[iss.Number] {
			_, failed, unready = BlockerStatus(cfg, it, cf, iss.Number, edges)
		}
		switch {
		case claimed[iss.Number]:
			fmt.Printf("    ~~ #%s already claimed this run; stale re-discovery, skipping\n", iss.Number)
		case !cfg.IgnoreBlockers && depsOfFailed[iss.Number]:
			// Own DepsOf call failed (#752, #1103) -- edges[iss.Number] is
			// unreliable, not a confirmed zero-blocker result. Hold rather
			// than launch or cascade-fail; the next refill retries.
			fmt.Printf("    ~~ #%s blocker check failed; will retry\n", iss.Number)
		case len(failed) > 0:
			fmt.Printf("    !! #%s  status=blocker-failed  note=#%s failed; skipping\n", iss.Number, strings.Join(failed, ", #"))
			transitionState(it, iss.Number, forge.Dispatchable, forge.Failed)
		case len(unready) > 0:
			fmt.Printf("    ~~ #%s blocked by #%s; skipping\n", iss.Number, strings.Join(unready, ", #"))
		default:
			if collider, overlapped := checkOverlap(iss.Number); overlapped {
				fmt.Printf("    ~~ #%s touches overlap in-progress #%s; deferring\n", iss.Number, collider)
				continue
			}
			return iss, true
		}
	}
	return Issue{}, false
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
func RunContinuous(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, discover Discoverer, fresh FreshnessChecker) error {
	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		return err
	}

	limiter := cfg.Limiter
	if limiter == nil {
		// Headless (CONTINUOUS_DISPATCH) and every pre-#653 call site: a
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
	// than one freed slot from a single trigger (the grow listener below)
	// can loop it until a call finally does nothing, rather than assuming
	// one trigger is worth exactly one launch.
	var refill func() bool
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
		iss, ok := nextReady(cfg, it, cf, checkOverlap, issues, edges, sources, failed, claimed)
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
			case cfg.Terminated.Marked(iss.Number, iss.Generation):
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
			refill()
			if outstanding == 0 {
				idle.Broadcast()
			}
			mu.Unlock()
		}()
		return true
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
				// Loop rather than a single refill() call: Resize's grow
				// channel is buffer-1 and non-blocking, so a burst of rapid
				// raises can coalesce into one delivered signal even though
				// the cap (updated synchronously by every Resize call before
				// it ever touches the channel) already reflects all of them
				// (issue #766) — draining until a call does nothing catches
				// every slot the raise actually freed instead of just one.
				mu.Lock()
				for refill() {
				}
				mu.Unlock()
			case <-growDone:
				return
			}
		}
	}()

	mu.Lock()
	for refill() {
	}
	for outstanding > 0 {
		idle.Wait()
	}
	closed = true
	mu.Unlock()

	close(growDone)
	<-done

	if stale {
		return ErrImageStale
	}
	if !dispatchedAny {
		return ErrOpenNoneDispatchable
	}
	return nil
}
