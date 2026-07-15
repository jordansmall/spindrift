package waves

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// Discoverer re-queries the dispatchable batch and its blocker edges.
// RunContinuous calls it once at startup and again before every slot
// refill, so a blocker that merges mid-run is picked up without a fresh
// invocation.
type Discoverer func() (issues []Issue, edges map[string][]string, err error)

// FreshnessChecker answers whether a refill may launch a new Box.
// Applicable is false for a runtime with no loaded image to compare
// (bwrap) — such a refill always proceeds. Fresh is meaningless when
// applicable is false.
type FreshnessChecker func() (applicable, fresh bool, message string)

// nextReady scans issues in order for the first one ready to dispatch,
// applying the same selection drainMaxJobs does for a whole batch —
// blocker-failed cascade, blocked skip, touch-overlap defer — but returns
// after the first match rather than collecting a whole wave, since a
// refill only ever needs to fill one freed slot.
func nextReady(cfg Config, it forge.IssueTracker, cf forge.CodeForge, checkOverlap func(string) (string, bool), issues []Issue, edges map[string][]string, claimed map[string]bool) (Issue, bool) {
	for _, iss := range issues {
		switch {
		case claimed[iss.Number]:
			fmt.Printf("    ~~ #%s already claimed this run; stale re-discovery, skipping\n", iss.Number)
		case hasFailedInBatchBlocker(cfg, it, iss.Number, edges):
			fmt.Printf("    !! #%s  status=blocker-failed  note=a dependency failed; skipping\n", iss.Number)
			transitionState(it, iss.Number, forge.Dispatchable, forge.Failed)
		case !issueIsReady(it, cf, iss.Number, edges):
			fmt.Printf("    ~~ #%s blocked (a blocker is not '%s'); skipping\n", iss.Number, cfg.CompleteLabel)
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

	var mu sync.Mutex
	var wg sync.WaitGroup
	stale := false
	dispatchedAny := false
	claimed := make(map[string]bool)

	var refill func()
	refill = func() {
		if stale {
			return
		}
		applicable, isFresh, msg := fresh()
		if applicable && !isFresh {
			stale = true
			fmt.Printf("==> %s\n", msg)
			return
		}
		issues, edges, err := discover()
		if err != nil {
			fmt.Fprintf(os.Stderr, "continuous: re-discover: %v\n", err)
			return
		}
		if len(edges) > 0 {
			if node, cycle := detectCycle(edges, issueNums(issues)); cycle {
				fmt.Fprintf(os.Stderr, "==> ERROR: dependency cycle detected (issue #%s is in the cycle); skipping this refill\n", node)
				return
			}
		}
		checkOverlap := waveOverlapCheck(cfg, it, cf)
		iss, ok := nextReady(cfg, it, cf, checkOverlap, issues, edges, claimed)
		if !ok {
			return
		}
		dispatchedAny = true
		claimed[iss.Number] = true
		claimIssue(cfg, it, iss.Number)
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := f.New(iss.Number, iss.Title)
			defer d.Close()
			result := d.Run()
			switch {
			case cfg.Terminated.Marked(iss.Number):
				// Terminate (ADR 0024, issue #649) already reaped this Box,
				// transitioned the issue back to Dispatchable, and recorded
				// its own comment/log line -- neither a Failed transition
				// nor a Settle call belongs here now.
				fmt.Printf("    ~~ #%s terminated by operator; abandoning\n", iss.Number)
			case !result.Success:
				fmt.Printf("    !! #%s FAILED (logs/issue-%s.log)\n", iss.Number, iss.Number)
				transitionState(it, iss.Number, forge.InProgress, forge.Failed)
			default:
				fmt.Printf("    <- #%s done  (logs/issue-%s.log)\n", iss.Number, iss.Number)
				s.Settle(d, iss.Number, result)
			}
			mu.Lock()
			refill()
			mu.Unlock()
		}()
	}

	mu.Lock()
	for i := 0; i < cfg.MaxParallel; i++ {
		refill()
	}
	mu.Unlock()

	wg.Wait()

	if stale {
		return ErrImageStale
	}
	if !dispatchedAny {
		return ErrOpenNoneDispatchable
	}
	return nil
}
