package waves

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
)

// transitionState is a best-effort dispatch-state transition: it logs but does
// not propagate errors.
func transitionState(it forge.IssueTracker, num string, from, to forge.DispatchState) {
	if err := it.TransitionState(num, from, to); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %d\n", num, to)
	}
}

// blockedMarker is the file the launcher drops under .spindrift/logs/ when a
// claimed single issue cannot start because a blocker is unmet. The dispatching
// pipeline reads it to release the claim and comment.
const blockedMarker = "blocked.txt"

// writeBlockedMarker records the unmet blockers as a "#a (native), #b (body)"
// list for the workflow to interpolate into its release comment, annotating each
// with the source it was resolved from.
func writeBlockedMarker(pwd string, blockers []string, sources map[string]forge.DepSource) error {
	refs := make([]string, len(blockers))
	for i, b := range blockers {
		refs[i] = forge.Ref(b, sources[b])
	}
	path := filepath.Join(dispatch.HostLogDirFor(pwd), blockedMarker)
	return os.WriteFile(path, []byte(strings.Join(refs, ", ")), 0o644)
}

// writeDepsOfFailedMarker records that the claimed issue's own DepsOf call
// failed transiently — writeBlockedMarker's counterpart for a lookup failure
// rather than a named blocker, so the workflow's release-the-claim step still
// fires.
func writeDepsOfFailedMarker(pwd string) error {
	path := filepath.Join(dispatch.HostLogDirFor(pwd), blockedMarker)
	return os.WriteFile(path, []byte("a transient blocker check failure (will retry)"), 0o644)
}

// dispatchWave dispatches a batch of issues in parallel (up to cfg.MaxParallel
// at once). Each goroutine claims its issue only after acquiring a Limiter slot,
// so at most MaxParallel issues are ever in-progress simultaneously. The Limiter
// is never resized — the live, resizable cap is a RunContinuous/Console concept;
// a one-shot wave's cap is fixed for its whole call.
func dispatchWave(cfg Config, it forge.IssueTracker, f *dispatch.Factory, s settle.Settler, batch []Issue, claimer Claimer) {
	limiter := NewLimiter(cfg.MaxParallel)
	var wg sync.WaitGroup
	for _, iss := range batch {
		wg.Add(1)
		iss := iss
		go func() {
			defer wg.Done()
			limiter.Acquire()
			defer limiter.Release()
			if err := claimer.Claim(iss.Number); err != nil {
				fmt.Printf("    ~~ #%s claim failed; skipping (%v)\n", iss.Number, err)
				return
			}
			d := f.New(iss.Number, iss.Title)
			defer d.Close()
			result := d.Run()
			switch {
			case result.AlreadyInFlight:
				// A live run (possibly orphaned by a killed launcher) still owns
				// this issue's container/sandbox -- skip without any state
				// transition, so its in-progress claim stands untouched.
				fmt.Printf("    ~~ #%s already in flight; skipping (live run continues)\n", iss.Number)
			case !result.Success:
				fmt.Printf("    !! #%s FAILED (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
				transitionState(it, iss.Number, forge.InProgress, forge.Failed)
			default:
				fmt.Printf("    <- #%s done  (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
				s.Settle(d, iss.Number, iss.Generation, result)
			}
		}()
	}
	wg.Wait()
}

// heldIssues returns the issues from the batch that were not selected for this
// wave — the ones a later invocation could still dispatch. Order matches issues.
func heldIssues(issues, selected []Issue) []Issue {
	dispatched := make(map[string]bool, len(selected))
	for _, iss := range selected {
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

// printSelectiveRerunHint names the issues a selective-list wave left behind and
// the exact command that carries them into the next invocation. cfg.Verb names
// the subcommand (dispatch or research, ADR 0022) so the hint stays within the
// kind that produced it; empty defaults to "dispatch". Selective dispatch
// bypasses the label gate (ADR 0011), so re-discovery cannot pick the remainder
// back up the way the queue path does — the operator carries it instead (ADR
// 0019).
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
// batch and exits; cfg.MaxJobs == 0 is uncapped. Blocked issues are skipped so
// no slot is wasted on a dependency that hasn't merged yet. The in-batch
// dependency graph is assumed already cycle-checked by NewPlan.
func drainMaxJobs(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, issues []Issue, edges map[string][]string, sources Sources, depsOfFailed map[string]bool, origin Origin, claimer Claimer) error {
	checkOverlap := waveOverlapCheck(cfg, it, cf)
	// Resolved once for the whole drain rather than re-derived on every blocker
	// check. Zero-value backend.Descriptor rows are fine: the blocker gate reads
	// only the PRForge/LandingContainmentQuery handles, never the descriptors.
	caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
	var selected []Issue
outer:
	for _, iss := range issues {
		// A depsOfFailed entry is a transient tracker hiccup, indistinguishable
		// from "confirmed zero blockers" in edges alone. Hold it for a later
		// invocation rather than reading the missing edges entry as ready, and
		// never fail it: the failure is the lookup, not a dependency.
		if !cfg.IgnoreBlockers && depsOfFailed[iss.Number] {
			fmt.Printf("    ~~ #%s blocker check failed; will retry\n", iss.Number)
			continue
		}
		var unready []string
		if !cfg.IgnoreBlockers {
			unready = unreadyBlockers(it, cf, caps, iss.Number, edges, cfg.SeedScopeOf)
		}
		switch {
		// A blocker bearing FailedLabel is held here too: agent-failed is
		// recoverable (agent-recover retries it), so a dependent must never be
		// cascade-failed by a blocker's state — it waits like any other unmet
		// blocker.
		case len(unready) > 0:
			fmt.Printf("    ~~ #%s blocked by #%s; skipping\n", iss.Number, strings.Join(unready, ", #"))
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
	if len(selected) == 0 {
		// Claimed single-issue path: the caller already swapped this issue onto
		// the in-progress label, so a bare skip would strand it there. Drop a
		// marker naming the unmet blockers and give up — the dispatching
		// pipeline releases the claim and comments.
		if origin == OriginClaimed && len(issues) > 0 {
			num := issues[0].Number
			if !cfg.IgnoreBlockers {
				switch {
				case depsOfFailed[num]:
					// edges[num] is unreliable rather than a confirmed
					// zero-blocker result, so write the marker anyway and
					// let a re-trigger retry, instead of stranding the
					// issue on in-progress with no signal.
					if err := writeDepsOfFailedMarker(pwd); err != nil {
						return err
					}
					fmt.Printf("==> #%s blocker check failed; wrote .spindrift/logs/%s for the pipeline to release the claim\n", num, blockedMarker)
				default:
					if blockers := unreadyBlockers(it, cf, caps, num, edges, cfg.SeedScopeOf); len(blockers) > 0 {
						if err := writeBlockedMarker(pwd, blockers, sources[num]); err != nil {
							return err
						}
						fmt.Printf("==> #%s blocked; wrote .spindrift/logs/%s for the pipeline to release the claim\n", num, blockedMarker)
					}
				}
			}
			fmt.Println("no unblocked issues to drain — nothing to do.")
			return nil
		}
		// Unattended drain path: held issues signal ErrOpenNoneDispatchable so
		// callers stop instead of hot-looping.
		held := heldIssues(issues, selected)
		if len(held) > 0 {
			if origin == OriginSelective {
				printSelectiveRerunHint(cfg, held)
			} else {
				fmt.Printf("no unblocked issues to drain — %d remain blocked or deferred.\n", len(held))
			}
			return ErrOpenNoneDispatchable
		}
		fmt.Println("no unblocked issues to drain — nothing to do.")
		return nil
	}
	fmt.Printf("==> draining %d unblocked issue(s) (MAX_JOBS=%d)\n", len(selected), cfg.MaxJobs)
	dispatchWave(cfg, it, f, s, selected, claimer)
	if held := heldIssues(issues, selected); len(held) > 0 {
		if origin == OriginSelective {
			printSelectiveRerunHint(cfg, held)
		} else {
			fmt.Printf("==> %d issue(s) remain for a later invocation (blocked, deferred, or past MAX_JOBS); re-run `spindrift dispatch` to continue the drain\n", len(held))
		}
	}
	return nil
}

// run executes plan: the claim/dispatch/settle loop per issue, the MAX_PARALLEL
// semaphore within a wave, MAX_JOBS drain concurrency, and the Touches overlap
// check between concurrent Dispatches. run creates pwd's .spindrift/logs
// subdirectory before dispatching any issue.
//
// ModeDrain (ADR 0019) is the only mode NewPlan ever selects, for every Origin —
// one selection pass, one wave, exit. Selective-list dispatch shares this path
// with the queue: an in-list blocker that hasn't reached CompleteLabel holds its
// dependent for a later invocation rather than looping waves in-process.
func run(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, plan Plan, claimer Claimer) error {
	if err := os.MkdirAll(dispatch.HostLogDirFor(pwd), 0o755); err != nil {
		return err
	}
	return drainMaxJobs(cfg, it, cf, pwd, f, s, plan.Issues, plan.Edges, plan.Sources, plan.Failed, plan.Origin, claimer)
}

// Dispatch is the one-shot headless entry point: it validates in as a Plan (a
// dependency cycle among in.Issues is returned as an error) and runs it as one
// wave. Callers resolve in.Edges/in.Sources via NewReadiness themselves first,
// because selective dispatch's external-blocker eviction pass needs that graph
// before it decides which issues survive into in; rebuilding it here would cost
// a second DepsOf sweep.
func Dispatch(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, in Input, claimer Claimer) error {
	plan, err := NewPlan(cfg, in)
	if err != nil {
		return err
	}
	return run(cfg, it, cf, pwd, f, s, plan, claimer)
}
