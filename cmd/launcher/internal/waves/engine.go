package waves

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
)

// transitionState is a best-effort dispatch-state transition that logs but
// does not propagate errors, matching the original behaviour.
func transitionState(it forge.IssueTracker, num string, from, to forge.DispatchState) {
	if err := it.TransitionState(num, from, to); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %d\n", num, to)
	}
}

// claimIssue marks an issue in-progress before dispatch. When discovery already
// runs off the in-progress label — the workflow claimed the issue in YAML
// before the launcher started — the transition would be a no-op, so it is
// skipped.
func claimIssue(cfg Config, it forge.IssueTracker, num string) {
	if cfg.Label == cfg.InProgressLabel {
		return
	}
	transitionState(it, num, forge.Dispatchable, forge.InProgress)
}

// blockedMarker is the file the launcher drops under logs/ when a claimed
// single issue cannot start because a blocker is unmet. The dispatching
// pipeline reads it to release the claim and comment; detection stays here so
// the two blocker formats are parsed once, in one place.
const blockedMarker = "blocked.txt"

// writeBlockedMarker records the unmet blockers as a "#a, #b" list for the
// workflow to interpolate into its release comment.
func writeBlockedMarker(pwd string, blockers []string) error {
	refs := make([]string, len(blockers))
	for i, b := range blockers {
		refs[i] = "#" + b
	}
	path := filepath.Join(pwd, "logs", blockedMarker)
	return os.WriteFile(path, []byte(strings.Join(refs, ", ")), 0o644)
}

// dispatchWave dispatches a batch of issues in parallel (up to cfg.MaxParallel
// at once). Each goroutine claims its issue only after acquiring a semaphore
// slot so that at most MaxParallel issues are ever in the in-progress state
// simultaneously.
func dispatchWave(cfg Config, it forge.IssueTracker, f *dispatch.Factory, s settle.Settler, batch []Issue) {
	sem := make(chan struct{}, cfg.MaxParallel)
	var wg sync.WaitGroup
	for _, iss := range batch {
		wg.Add(1)
		iss := iss
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			claimIssue(cfg, it, iss.Number)
			d := f.New(iss.Number, iss.Title)
			defer d.Close()
			result := d.Run()
			if !result.Success {
				fmt.Printf("    !! #%s FAILED (logs/issue-%s.log)\n", iss.Number, iss.Number)
				transitionState(it, iss.Number, forge.InProgress, forge.Failed)
			} else {
				fmt.Printf("    <- #%s done  (logs/issue-%s.log)\n", iss.Number, iss.Number)
				s.Settle(d, iss.Number, result)
			}
		}()
	}
	wg.Wait()
}

// dispatchWaves fans issues out in dependency order. Each wave dispatches the
// currently unblocked set; blocked issues are held and rechecked after
// DepsPollSecs. The deadlock timer resets on any progress; if no issue becomes
// ready within DepsWaitSecs the function returns an error rather than blocking
// forever. Dispatched issues leave the remaining set even when they fail.
func dispatchWaves(cfg Config, it forge.IssueTracker, cf forge.CodeForge, f *dispatch.Factory, s settle.Settler, issues []Issue, edges map[string][]string) error {
	remaining := make([]Issue, len(issues))
	copy(remaining, issues)
	elapsed := 0

	for len(remaining) > 0 {
		checkOverlap := waveOverlapCheck(cfg, it, cf)
		var ready, blockerFailed, held []Issue
		for _, iss := range remaining {
			collider, overlapped := checkOverlap(iss.Number)
			switch {
			case issueIsReady(it, cf, iss.Number, edges) && !overlapped:
				ready = append(ready, iss)
			case hasFailedInBatchBlocker(cfg, it, iss.Number, edges):
				blockerFailed = append(blockerFailed, iss)
			default:
				if overlapped {
					fmt.Printf("    ~~ #%s touches overlap in-progress #%s; deferring\n", iss.Number, collider)
				}
				held = append(held, iss)
			}
		}

		for _, iss := range blockerFailed {
			fmt.Printf("    !! #%s  status=blocker-failed  note=a dependency failed; skipping\n", iss.Number)
			transitionState(it, iss.Number, forge.Dispatchable, forge.Failed)
		}

		if len(ready) == 0 {
			if len(blockerFailed) > 0 {
				elapsed = 0
				remaining = held
				continue
			}
			if elapsed >= cfg.DepsWaitSecs {
				fmt.Fprintf(os.Stderr,
					"ERROR: dependency deadlock — blockers did not reach '%s' after %ds\n",
					cfg.CompleteLabel, cfg.DepsWaitSecs)
				for _, iss := range remaining {
					fmt.Fprintf(os.Stderr, "    #%s %s\n", iss.Number, iss.Title)
				}
				return fmt.Errorf("dependency deadlock")
			}
			fmt.Printf("    .. all remaining issues blocked; retrying in %ds (%ds elapsed)\n",
				cfg.DepsPollSecs, elapsed)
			time.Sleep(time.Duration(cfg.DepsPollSecs) * time.Second)
			elapsed += cfg.DepsPollSecs
			continue
		}

		// Progress: reset the deadlock timer.
		elapsed = 0
		dispatchWave(cfg, it, f, s, ready)
		remaining = held
	}
	return nil
}

// drainMaxJobs drains up to cfg.MaxJobs currently-unblocked issues from the
// batch and exits; cfg.MaxJobs == 0 is uncapped and drains every unblocked
// issue in the batch. Blocked issues are skipped so no slot is wasted on a
// dependency that hasn't merged yet; they wait for the next invocation. The
// in-batch dependency graph is assumed already cycle-checked by NewPlan.
func drainMaxJobs(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, issues []Issue, edges map[string][]string, origin Origin) error {
	checkOverlap := waveOverlapCheck(cfg, it, cf)
	var selected, blockerFailed []Issue
outer:
	for _, iss := range issues {
		switch {
		// Cascade-fail only in the multi-issue drain path (origin !=
		// OriginClaimed). The claimed single-issue path swaps the issue onto
		// in-progress before calling here; cascading it would add Failed on
		// top of in-progress, leaving the issue double-labeled. That path
		// has its own blocked-marker signaling via the writeBlockedMarker
		// call below.
		case origin != OriginClaimed && hasFailedInBatchBlocker(cfg, it, iss.Number, edges):
			blockerFailed = append(blockerFailed, iss)
		case !issueIsReady(it, cf, iss.Number, edges):
			fmt.Printf("    ~~ #%s blocked (a blocker is not '%s'); skipping\n", iss.Number, cfg.CompleteLabel)
		default:
			if collider, overlapped := checkOverlap(iss.Number); overlapped {
				fmt.Printf("    ~~ #%s touches overlap in-progress #%s; deferring\n", iss.Number, collider)
				continue
			}
			selected = append(selected, iss)
			if cfg.MaxJobs > 0 && len(selected) >= cfg.MaxJobs {
				break outer
			}
		}
	}
	for _, iss := range blockerFailed {
		fmt.Printf("    !! #%s  status=blocker-failed  note=a dependency failed; skipping\n", iss.Number)
		transitionState(it, iss.Number, forge.Dispatchable, forge.Failed)
	}
	if len(selected) == 0 {
		// Claimed single-issue path: the caller already swapped this issue
		// onto the in-progress label, so a bare skip would strand it there.
		// Drop a marker naming the unmet blockers; the dispatching pipeline
		// releases the claim and comments. Give up — no wait, no recovery.
		if origin == OriginClaimed && len(issues) > 0 {
			num := issues[0].Number
			if blockers := unreadyBlockers(it, cf, num, edges); len(blockers) > 0 {
				if err := writeBlockedMarker(pwd, blockers); err != nil {
					return err
				}
				fmt.Printf("==> #%s blocked; wrote logs/%s for the pipeline to release the claim\n", num, blockedMarker)
			}
			fmt.Printf("no unblocked '%s' issues to drain — nothing to do.\n", cfg.Label)
			return nil
		}
		// Unattended drain path: if issues remain after cascade-failing blockers,
		// signal callers with ErrOpenNoneDispatchable so they stop instead of
		// hot-looping.
		remaining := len(issues) - len(blockerFailed)
		if remaining > 0 {
			fmt.Printf("no unblocked '%s' issues to drain — %d remain blocked or deferred.\n", cfg.Label, remaining)
			return ErrOpenNoneDispatchable
		}
		fmt.Printf("no unblocked '%s' issues to drain — nothing to do.\n", cfg.Label)
		return nil
	}
	fmt.Printf("==> draining %d unblocked issue(s) (MAX_JOBS=%d)\n", len(selected), cfg.MaxJobs)
	dispatchWave(cfg, it, f, s, selected)
	return nil
}

// Run executes plan: the claim/dispatch/settle loop per issue, the
// MAX_PARALLEL semaphore within a wave, MAX_JOBS drain concurrency, the
// dependency-wave deadlock timer, and the Touches overlap check between
// concurrent Dispatches. pwd is the working directory; Run creates its
// logs/ subdirectory before dispatching any issue.
//
// A no-edges batch only enters the wave-retry engine (with its ongoing
// per-candidate overlap check and deadlock timer) when an upfront touch-set
// overlap against an in-progress issue is found; that upfront check only
// applies to OriginDiscovered/OriginClaimed (the run() queue-drain path) —
// OriginSelective (operator-specified `dispatch <nums>`) never consulted the
// overlap gate for its mode decision and must not start blocking on it now.
func Run(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, plan Plan) error {
	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		return err
	}
	if plan.Mode == ModeDrain {
		return drainMaxJobs(cfg, it, cf, pwd, f, s, plan.Issues, plan.Edges, plan.Origin)
	}
	hasEdges := len(plan.Edges) > 0
	overlaps := !hasEdges && plan.Origin != OriginSelective && batchHasTouchOverlap(cfg, it, cf, plan.Issues)
	if hasEdges || overlaps {
		if hasEdges {
			fmt.Println("==> dependency edges found; dispatching in waves")
		} else {
			fmt.Println("==> declared touches overlap an in-progress issue; dispatching in waves")
		}
		return dispatchWaves(cfg, it, cf, f, s, plan.Issues, plan.Edges)
	}
	fmt.Printf("==> %d issue(s); launching up to %d container(s) at a time\n", len(plan.Issues), cfg.MaxParallel)
	dispatchWave(cfg, it, f, s, plan.Issues)
	return nil
}
