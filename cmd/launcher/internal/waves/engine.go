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

// blockedMarker is the file the launcher drops under .spindrift/logs/ when a claimed
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
	path := filepath.Join(dispatch.HostLogDirFor(pwd), blockedMarker)
	return os.WriteFile(path, []byte(strings.Join(refs, ", ")), 0o644)
}

// writeDepsOfFailedMarker records that the claimed issue's own DepsOf call
// failed transiently (#1103) — the OriginClaimed counterpart of
// writeBlockedMarker for a lookup failure rather than a named blocker, so the
// workflow's release-the-claim step still fires and interpolates a
// human-readable reason into its comment.
func writeDepsOfFailedMarker(pwd string) error {
	path := filepath.Join(dispatch.HostLogDirFor(pwd), blockedMarker)
	return os.WriteFile(path, []byte("a transient blocker check failure (will retry)"), 0o644)
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

// heldIssues returns the issues from the batch that were not selected for
// this wave — the ones a later invocation (or, for OriginSelective, an
// operator re-run) could still dispatch. Order matches issues.
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
func drainMaxJobs(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, issues []Issue, edges map[string][]string, sources Sources, depsOfFailed map[string]bool, origin Origin) error {
	checkOverlap := waveOverlapCheck(cfg, it, cf)
	var selected []Issue
outer:
	for _, iss := range issues {
		// An issue named in depsOfFailed had its own NewReadiness/DepsOf call
		// error — a transient tracker hiccup indistinguishable from
		// "confirmed zero blockers" in edges alone (#752, #1103). Hold it
		// for a later invocation rather than reading the missing edges
		// entry as ready, and never fail it: the failure is the lookup
		// itself, not a dependency.
		if !cfg.PreResolved && !cfg.IgnoreBlockers && depsOfFailed[iss.Number] {
			fmt.Printf("    ~~ #%s blocker check failed; will retry\n", iss.Number)
			continue
		}
		var unready []string
		if !cfg.PreResolved && !cfg.IgnoreBlockers {
			unready = unreadyBlockers(it, cf, iss.Number, edges, cfg.SeedScopeOf)
		}
		switch {
		// A blocker bearing FailedLabel is held here too (unreadyBlockers
		// never treats it as satisfied): agent-failed is a recoverable
		// state (agent-recover retries it), so a dependent must never be
		// cascade-failed as a consequence of a blocker's label or state
		// (#1984, incident #1972) — it waits, the same as any other unmet
		// blocker, until the blocker reaches a satisfied state.
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
		// Claimed single-issue path: the caller already swapped this issue
		// onto the in-progress label, so a bare skip would strand it there.
		// Drop a marker naming the unmet blockers; the dispatching pipeline
		// releases the claim and comments. Give up — no wait, no recovery.
		if origin == OriginClaimed && len(issues) > 0 {
			num := issues[0].Number
			if !cfg.IgnoreBlockers {
				switch {
				case depsOfFailed[num]:
					// The claimed issue's own DepsOf call failed, so
					// edges[num] is unreliable rather than a confirmed
					// zero-blocker result (#1103) -- write the marker
					// anyway so the release workflow reverts the claim and
					// a later re-trigger retries, exactly as a real unmet
					// blocker would, instead of stranding the issue on
					// in-progress with no signal.
					if err := writeDepsOfFailedMarker(pwd); err != nil {
						return err
					}
					fmt.Printf("==> #%s blocker check failed; wrote .spindrift/logs/%s for the pipeline to release the claim\n", num, blockedMarker)
				default:
					if blockers := unreadyBlockers(it, cf, num, edges, cfg.SeedScopeOf); len(blockers) > 0 {
						if err := writeBlockedMarker(pwd, blockers, sources[num]); err != nil {
							return err
						}
						fmt.Printf("==> #%s blocked; wrote .spindrift/logs/%s for the pipeline to release the claim\n", num, blockedMarker)
					}
				}
			}
			fmt.Printf("no unblocked '%s' issues to drain — nothing to do.\n", cfg.Label)
			return nil
		}
		// Unattended drain path: if issues remain held, signal callers with
		// ErrOpenNoneDispatchable so they stop instead of hot-looping.
		held := heldIssues(issues, selected)
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
	if held := heldIssues(issues, selected); len(held) > 0 {
		if origin == OriginSelective {
			printSelectiveRerunHint(cfg, held)
		} else {
			fmt.Printf("==> %d issue(s) remain for a later invocation (blocked, deferred, or past MAX_JOBS); re-run `spindrift dispatch` to continue the drain\n", len(held))
		}
	}
	return nil
}

// run executes plan: the claim/dispatch/settle loop per issue, the
// MAX_PARALLEL semaphore within a wave, MAX_JOBS drain concurrency, and the
// Touches overlap check between concurrent Dispatches. pwd is the working
// directory; run creates its .spindrift/logs subdirectory before dispatching any
// issue.
//
// ModeDrain (ADR 0019) is the only mode NewPlan ever selects, for every
// Origin — drainMaxJobs alone handles blocker edges and the Touches overlap
// check with a single selection pass, one wave, exit. Selective-list
// dispatch (#524) shares this path with the queue: an in-list blocker that
// hasn't reached CompleteLabel holds its dependent for a later invocation
// rather than looping waves in-process.
func run(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, plan Plan) error {
	if err := os.MkdirAll(dispatch.HostLogDirFor(pwd), 0o755); err != nil {
		return err
	}
	return drainMaxJobs(cfg, it, cf, pwd, f, s, plan.Issues, plan.Edges, plan.Sources, plan.Failed, plan.Origin)
}

// Dispatch is the one-shot headless entry point folding the previously
// hand-sequenced plan/run pair into a single call (#1547): it validates in
// as a Plan (a dependency cycle among in.Issues is reported as an error)
// and runs it as one wave. main.go's run() and the operator
// `dispatch <nums>` path (selectiveListDispatch) are its callers; both
// resolve in.Edges/in.Sources via NewReadiness themselves first, since
// selective dispatch's external-blocker eviction pass needs that same
// graph before it decides which issues survive into in — building it again
// inside Dispatch would cost a second DepsOf sweep over issues Dispatch
// already has the graph for. preview stops short of running and uses
// NewReadiness/NewPlan directly since it never launches a Box.
func Dispatch(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, in Input) error {
	plan, err := NewPlan(cfg, in)
	if err != nil {
		return err
	}
	return run(cfg, it, cf, pwd, f, s, plan)
}
