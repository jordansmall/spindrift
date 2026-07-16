package waves

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

// writeBlockedMarker records the unmet blockers as a "#a (native), #b
// (body)" list for the workflow to interpolate into its release comment,
// annotating each with the source (native relationship vs body-text
// parsing) it was resolved from.
func writeBlockedMarker(pwd string, blockers []string, sources map[string]forge.DepSource) error {
	refs := make([]string, len(blockers))
	for i, b := range blockers {
		refs[i] = forge.Ref(b, sources[b])
	}
	path := filepath.Join(pwd, "logs", blockedMarker)
	return os.WriteFile(path, []byte(strings.Join(refs, ", ")), 0o644)
}

// dispatchWave dispatches a batch of issues in parallel (up to cfg.MaxParallel
// at once). Each goroutine claims its issue only after acquiring a Limiter
// slot so that at most MaxParallel issues are ever in the in-progress state
// simultaneously. The Limiter is built fresh from cfg.MaxParallel and never
// resized — the live, resizable cap (issue #653) is a RunContinuous/Console
// concept; a one-shot wave's cap is fixed for its whole call.
func dispatchWave(cfg Config, it forge.IssueTracker, f *dispatch.Factory, s settle.Settler, batch []Issue) {
	limiter := NewLimiter(cfg.MaxParallel)
	var wg sync.WaitGroup
	for _, iss := range batch {
		wg.Add(1)
		iss := iss
		go func() {
			defer wg.Done()
			limiter.Acquire()
			defer limiter.Release()
			claimIssue(cfg, it, iss.Number)
			d := f.New(iss.Number, iss.Title)
			defer d.Close()
			result := d.Run()
			switch {
			case result.AlreadyInFlight:
				// A live run (possibly orphaned by a killed launcher) still
				// owns this issue's container/sandbox -- skip without any
				// dispatch-state transition, so its in-progress claim stands
				// untouched (issue #562).
				fmt.Printf("    ~~ #%s already in flight; skipping (live run continues)\n", iss.Number)
			case !result.Success:
				fmt.Printf("    !! #%s FAILED (logs/issue-%s.log)\n", iss.Number, iss.Number)
				transitionState(it, iss.Number, forge.InProgress, forge.Failed)
			default:
				fmt.Printf("    <- #%s done  (logs/issue-%s.log)\n", iss.Number, iss.Number)
				s.Settle(d, iss.Number, iss.Generation, result)
			}
		}()
	}
	wg.Wait()
}

// heldIssues returns the issues from the batch that were neither selected
// for this wave nor cascade-failed — the ones a later invocation (or, for
// OriginSelective, an operator re-run) could still dispatch. Order matches
// issues.
func heldIssues(issues, selected, blockerFailed []Issue) []Issue {
	dispatched := make(map[string]bool, len(selected)+len(blockerFailed))
	for _, iss := range selected {
		dispatched[iss.Number] = true
	}
	for _, iss := range blockerFailed {
		dispatched[iss.Number] = true
	}
	var held []Issue
	for _, iss := range issues {
		if !dispatched[iss.Number] {
			held = append(held, iss)
		}
	}
	return held
}

// printSelectiveRerunHint names the issues a selective-list wave left behind
// and the exact command that carries them into the next invocation. cfg.Verb
// names the subcommand (dispatch or research, ADR 0022) so the hint carries
// the remainder back into the same kind that produced it; empty defaults to
// "dispatch". Selective dispatch bypasses the label gate (ADR 0011), so
// re-discovery cannot pick the remainder back up the way the queue path does
// — the operator carries it instead (ADR 0019).
func printSelectiveRerunHint(cfg Config, held []Issue) {
	verb := cfg.Verb
	if verb == "" {
		verb = "dispatch"
	}
	nums := make([]string, len(held))
	for i, iss := range held {
		nums[i] = iss.Number
	}
	fmt.Printf("==> %d issue(s) remain: #%s\n", len(held), strings.Join(nums, ", #"))
	fmt.Printf("==> re-run to continue: spindrift %s --yes %s\n", verb, strings.Join(nums, " "))
}

// drainMaxJobs drains up to cfg.MaxJobs currently-unblocked issues from the
// batch and exits; cfg.MaxJobs == 0 is uncapped and drains every unblocked
// issue in the batch. Blocked issues are skipped so no slot is wasted on a
// dependency that hasn't merged yet; they wait for the next invocation. The
// in-batch dependency graph is assumed already cycle-checked by NewPlan.
func drainMaxJobs(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, issues []Issue, edges map[string][]string, sources Sources, origin Origin) error {
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
		case !cfg.IgnoreBlockers && origin != OriginClaimed && hasFailedInBatchBlocker(cfg, it, iss.Number, edges):
			blockerFailed = append(blockerFailed, iss)
		case !cfg.IgnoreBlockers && !issueIsReady(it, cf, iss.Number, edges):
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
			if !cfg.IgnoreBlockers {
				if blockers := unreadyBlockers(it, cf, num, edges); len(blockers) > 0 {
					if err := writeBlockedMarker(pwd, blockers, sources[num]); err != nil {
						return err
					}
					fmt.Printf("==> #%s blocked; wrote logs/%s for the pipeline to release the claim\n", num, blockedMarker)
				}
			}
			fmt.Printf("no unblocked '%s' issues to drain — nothing to do.\n", cfg.Label)
			return nil
		}
		// Unattended drain path: if issues remain after cascade-failing blockers,
		// signal callers with ErrOpenNoneDispatchable so they stop instead of
		// hot-looping.
		held := heldIssues(issues, selected, blockerFailed)
		if len(held) > 0 {
			if origin == OriginSelective {
				printSelectiveRerunHint(cfg, held)
			} else {
				fmt.Printf("no unblocked '%s' issues to drain — %d remain blocked or deferred.\n", cfg.Label, len(held))
			}
			return ErrOpenNoneDispatchable
		}
		fmt.Printf("no unblocked '%s' issues to drain — nothing to do.\n", cfg.Label)
		return nil
	}
	fmt.Printf("==> draining %d unblocked issue(s) (MAX_JOBS=%d)\n", len(selected), cfg.MaxJobs)
	dispatchWave(cfg, it, f, s, selected)
	if held := heldIssues(issues, selected, blockerFailed); len(held) > 0 {
		if origin == OriginSelective {
			printSelectiveRerunHint(cfg, held)
		} else {
			fmt.Printf("==> %d issue(s) remain for a later invocation (blocked, deferred, or past MAX_JOBS); re-run `spindrift dispatch` to continue the drain\n", len(held))
		}
	}
	return nil
}

// Run executes plan: the claim/dispatch/settle loop per issue, the
// MAX_PARALLEL semaphore within a wave, MAX_JOBS drain concurrency, and the
// Touches overlap check between concurrent Dispatches. pwd is the working
// directory; Run creates its logs/ subdirectory before dispatching any
// issue.
//
// ModeDrain (ADR 0019) is the only mode NewPlan ever selects, for every
// Origin — drainMaxJobs alone handles blocker edges and the Touches overlap
// check with a single selection pass, one wave, exit. Selective-list
// dispatch (#524) shares this path with the queue: an in-list blocker that
// hasn't reached CompleteLabel holds its dependent for a later invocation
// rather than looping waves in-process.
func Run(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, plan Plan) error {
	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		return err
	}
	return drainMaxJobs(cfg, it, cf, pwd, f, s, plan.Issues, plan.Edges, plan.Sources, plan.Origin)
}
