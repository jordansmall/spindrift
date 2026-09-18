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

// transitionState logs a failed dispatch-state transition rather than
// propagating the error.
func transitionState(it forge.IssueTracker, num string, from, to forge.DispatchState) {
	if err := it.TransitionState(num, from, to); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %d\n", num, to)
	}
}

// blockedMarker is the file under .spindrift/logs/ that tells the dispatching
// pipeline to release a claimed issue whose blocker is unmet.
const blockedMarker = "blocked.txt"

// writeBlockedMarker records the unmet blockers as a "#a (native), #b (body)"
// list the workflow interpolates into its release comment.
func writeBlockedMarker(pwd string, blockers []string, sources map[string]forge.DepSource) error {
	refs := make([]string, len(blockers))
	for i, b := range blockers {
		refs[i] = forge.Ref(b, sources[b])
	}
	path := filepath.Join(dispatch.HostLogDirFor(pwd), blockedMarker)
	return os.WriteFile(path, []byte(strings.Join(refs, ", ")), 0o644)
}

// writeDepsOfFailedMarker records a transient DepsOf failure (#1103) in place
// of a named blocker, so the workflow's release-the-claim step still fires.
func writeDepsOfFailedMarker(pwd string) error {
	path := filepath.Join(dispatch.HostLogDirFor(pwd), blockedMarker)
	return os.WriteFile(path, []byte("a transient blocker check failure (will retry)"), 0o644)
}

// dispatchWave dispatches a batch of issues in parallel, up to cfg.MaxParallel
// at once. Each goroutine claims its issue only after acquiring a Limiter slot,
// so at most MaxParallel issues sit in the in-progress state at any moment. A
// one-shot wave's cap never resizes; the live cap (issue #653) belongs to
// RunContinuous and Console.
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
				// A live run, possibly orphaned by a killed launcher, still owns
				// this issue's container, so skip without a dispatch-state
				// transition and leave its in-progress claim untouched (#562).
				fmt.Printf("    ~~ #%s already in flight; skipping (live run continues)\n", iss.Number)
			case !result.Success:
				fmt.Printf("    !! #%s FAILED (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
				result.ReportFailureReason(iss.Number)
				transitionState(it, iss.Number, forge.InProgress, forge.Failed)
			default:
				fmt.Printf("    <- #%s done  (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
				s.Settle(d, iss.Number, iss.Generation, result)
			}
		}()
	}
	wg.Wait()
}

// heldIssues returns the unselected issues a later invocation could dispatch,
// in the order they appear in issues.
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
// and the command that carries them into the next invocation, under cfg.Verb's
// subcommand (ADR 0022). Selective dispatch bypasses the label gate (ADR 0011),
// so re-discovery cannot pick the remainder back up and the operator must carry
// it (ADR 0019).
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

// drainMaxJobs drains up to cfg.MaxJobs currently-unblocked issues and exits;
// cfg.MaxJobs == 0 is uncapped. Blocked issues are skipped rather than waited
// on, so no slot goes to a dependency that hasn't merged yet. NewPlan has
// already cycle-checked the in-batch dependency graph.
func drainMaxJobs(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, issues []Issue, edges map[string][]string, sources Sources, depsOfFailed map[string]bool, origin Origin, claimer Claimer) error {
	checkOverlap := waveOverlapCheck(cfg, it, cf)
	// Resolved once for the whole drain so unreadyBlockers does not re-derive
	// it on every blocker check (#2946). Zero-value backend.Descriptor rows are
	// fine: the blocker gate reads only PRForge and LandingContainmentQuery,
	// never the descriptor fields.
	caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
	var selected []Issue
outer:
	for _, iss := range issues {
		// A transient DepsOf error is indistinguishable from confirmed zero
		// blockers in edges alone (#752, #1103), so hold the issue for a later
		// invocation instead of reading the missing entry as ready. Never fail
		// it: the lookup failed, not a dependency.
		if !cfg.IgnoreBlockers && depsOfFailed[iss.Number] {
			fmt.Printf("    ~~ #%s blocker check failed; will retry\n", iss.Number)
			continue
		}
		var unready []string
		if !cfg.IgnoreBlockers {
			unready = unreadyBlockers(it, cf, caps, iss.Number, edges, cfg.SeedScopeOf)
		}
		switch {
		// agent-failed is recoverable (agent-recover retries it), so a blocker
		// wearing FailedLabel holds its dependent like any other unmet blocker
		// instead of cascade-failing it (#1984, incident #1972).
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
		// The caller already swapped a claimed issue onto the in-progress
		// label, so a bare skip would strand it there. The marker naming the
		// unmet blockers is what makes the pipeline release the claim.
		if origin == OriginClaimed && len(issues) > 0 {
			num := issues[0].Number
			if !cfg.IgnoreBlockers {
				switch {
				case depsOfFailed[num]:
					// edges[num] is unreliable, not a confirmed zero-blocker
					// result (#1103), so write the marker anyway and let the
					// release workflow revert the claim for a later retry.
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
		// ErrOpenNoneDispatchable stops an unattended caller instead of letting
		// it hot-loop over issues that are all still held.
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

// run creates pwd's .spindrift/logs subdirectory, then executes plan. Every
// Origin takes the same single pass: drainMaxJobs selects once, runs one wave,
// and exits (ADR 0019). Selective-list dispatch (#524) shares that path, so an
// in-list blocker holds its dependent for a later invocation rather than
// looping waves in-process.
func run(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, plan Plan, claimer Claimer) error {
	if err := os.MkdirAll(dispatch.HostLogDirFor(pwd), 0o755); err != nil {
		return err
	}
	return drainMaxJobs(cfg, it, cf, pwd, f, s, plan.Issues, plan.Edges, plan.Sources, plan.Failed, plan.Origin, claimer)
}

// Dispatch is the one-shot headless entry point (#1547): it validates in as a
// Plan, reporting a dependency cycle among in.Issues as an error, and runs it
// as one wave. Callers resolve in.Edges and in.Sources through NewReadiness
// first because selective dispatch needs that graph to evict externally
// blocked issues; rebuilding it here would cost a second DepsOf sweep.
func Dispatch(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, in Input, claimer Claimer) error {
	plan, err := NewPlan(cfg, in)
	if err != nil {
		return err
	}
	return run(cfg, it, cf, pwd, f, s, plan, claimer)
}
