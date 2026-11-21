package waves

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/panicguard"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/shutdown"
	"spindrift.dev/launcher/internal/terminate"
)

// transitionState logs a failed dispatch-state transition rather than
// propagating the error, and returns the Settlement it recorded. note is the
// call site's most specific reason, or "". Unlike settle.Settle's method of
// the same name it always settles d's Record directly rather than latching.
func transitionState(d dispatch.Dispatcher, it forge.IssueTracker, num string, v settle.Verdict, note string) settle.Settlement {
	v, note, err := settle.Transition(it, num, forge.InProgress, v, note)
	if err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %s: %v\n", num, v.State(), err)
	}
	// Records even on a failed write (issue #3627): the record is what the
	// host decided, not whether the tracker write landed.
	return settle.SettledBy(d, dispatchkey.Issue(num), v, note)
}

// blockedMarker is the file under .spindrift/logs/ that tells the dispatching
// pipeline to release a claimed issue that cannot run yet. The release
// workflow posts its content after "released this issue back to
// `ready-for-agent`: ", so it starts lowercase and names how to re-trigger.
// It is one line: Forgejo passes it through a $GITHUB_OUTPUT heredoc.
const blockedMarker = "blocked.txt"

// writeBlockedMarker names the unmet blockers as "#a (native), #b (body)".
func writeBlockedMarker(pwd string, blockers []string, sources map[string]forge.DepSource) error {
	refs := make([]string, len(blockers))
	for i, b := range blockers {
		refs[i] = forge.Ref(b, sources[b])
	}
	resolved := "the blocker is resolved."
	if len(blockers) > 1 {
		resolved = "the blockers are resolved."
	}
	return writeMarker(pwd, "it is blocked by "+strings.Join(refs, ", ")+
		", not yet `agent-complete` or closed. Re-trigger once "+resolved)
}

// writeDepsOfFailedMarker records a transient DepsOf failure (#1103) in place
// of a named blocker, so the workflow's release-the-claim step still fires.
// Nothing retries in CI; the operator re-triggers.
func writeDepsOfFailedMarker(pwd string) error {
	return writeMarker(pwd, "the blocker check failed transiently. Re-trigger to run it again.")
}

// writeOverlapMarker names the in-progress collider that deferred a claimed
// issue. The collider's touch-set is its declared touches plus its open PR's
// changed files.
func writeOverlapMarker(pwd, collider string) error {
	return writeMarker(pwd, "its touches overlap in-progress #"+collider+
		". Re-trigger once #"+collider+" is resolved.")
}

func writeMarker(pwd, text string) error {
	path := filepath.Join(dispatch.HostLogDirFor(pwd), blockedMarker)
	return os.WriteFile(path, []byte(text), 0o644)
}

// dispatchWave dispatches a batch of issues in parallel, up to cfg.MaxParallel
// at once. Each goroutine claims its issue only after acquiring a Limiter slot,
// so at most MaxParallel issues are in progress at any moment.
//
// cfg.Stop/cfg.Abort give it RunContinuous's two-stage shutdown via
// shutdown.Gate (#3522): Stop closes off further launches, Abort also
// reclaims in-flight Boxes. A nil registry (tests only) is substituted before
// building the Gate, so the result switch and the Gate share one registry;
// reading the pre-substitution one lost every abort mark. It reports whether
// either stage fired, so the caller can return ErrSignalledStop.
//
// gate.Hold marks a claim as this process's to release: after each Claim, and
// also up front for OriginClaimed, whose issue the workflow claimed before
// the launcher started (Hold is idempotent). Without the up-front Hold, a
// decline at the pre-claim checkpoint strands the issue in-progress (#3522).
func dispatchWave(cfg Config, it forge.IssueTracker, cf forge.CodeForge, f *dispatch.Factory, s settle.Settler, batch []Issue, origin Origin, claimer Claimer, session *Session) bool {
	var terminated *terminate.Registry
	if session != nil {
		terminated = session.Terminated
	}
	if terminated == nil {
		terminated = terminate.NewRegistry()
	}
	limiter := NewLimiter(cfg.MaxParallel)
	reaper := f.AsReaper()
	gate := shutdown.NewGate(cfg.Stop, cfg.Abort, it, cf, cfg.BranchPrefix, reaper, terminated, cfg.SettledLabels)
	gate.Watch()
	var wg sync.WaitGroup
	for _, iss := range batch {
		wg.Add(1)
		iss := iss
		panicguard.Go(func() {
			defer wg.Done()
			limiter.Acquire()
			defer limiter.Release()
			if origin == OriginClaimed {
				gate.Hold(iss.Number)
			}
			if !gate.Allowed(iss.Number) {
				return
			}
			if err := claimer.Claim(iss.Number); err != nil {
				fmt.Print(claimSkipLine(iss.Number, err))
				return
			}
			gate.Hold(iss.Number)
			// New arms the kill latch, so it runs under Gate's lock before
			// in-flight registration, or an abort could snapshot without it
			// (#3522).
			var d *dispatch.Dispatch
			if !gate.Launch(iss.Number, func() { d = f.New(iss.Number, iss.Title) }) {
				// A signal arrived between Allowed and here; Launch already
				// released the claim through its own Reclaim.
				return
			}
			defer gate.Leave(iss.Number)
			defer d.Close()
			disp := d.Run()
			switch {
			case terminated.Marked(iss.Number, iss.Generation):
				// See RunContinuous's identical case.
				fmt.Printf("    ~~ #%s reclaimed; abandoning\n", iss.Number)
			default:
				dispatch.Route(disp,
					func() struct{} {
						// See RunContinuous's identical case (#562, #3633).
						fmt.Printf("    ~~ #%s already in flight; skipping (live run continues)\n", iss.Number)
						return struct{}{}
					},
					func(result dispatch.Result) struct{} {
						fmt.Printf("    !! #%s FAILED (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
						result.ReportFailureReason(iss.Number)
						session.record(iss.Number, transitionState(d, it, iss.Number, settle.ForReason(dispatchrecord.ReasonBoxFailed), result.FailureNote()))
						return struct{}{}
					},
					func(result dispatch.Result) struct{} {
						fmt.Printf("    <- #%s done  (.spindrift/logs/issue-%s.log)\n", iss.Number, iss.Number)
						session.record(iss.Number, s.Settle(d, iss.Number, iss.Generation, result))
						return struct{}{}
					})
			}
		})
	}
	wg.Wait()
	gate.Settle()
	return gate.Signalled()
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
// and the cfg.Verb command that carries them on (ADR 0022). Selective dispatch
// bypasses the label gate (ADR 0011), so re-discovery cannot pick the
// remainder back up (ADR 0019).
func printSelectiveRerunHint(cfg Config, held []Issue) {
	verb := cfg.Verb
	if verb == "" {
		verb = dispatchkind.Default.Verb
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
func drainMaxJobs(cfg Config, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, issues []Issue, edges map[string][]string, sources Sources, depsOfFailed map[string]bool, origin Origin, claimer Claimer, session *Session) error {
	checkOverlap := waveOverlapCheck(cfg, it, cf)
	// Resolved once per drain (#2946). Zero-value descriptors are fine: the
	// blocker gate reads only PRForge and LandingContainmentQuery.
	caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
	var selected []Issue
	// Kept so the claimed-path marker does not re-fetch the touch-set: a
	// failed re-fetch fails open and would strand the claim.
	deferredBy := map[string]string{}
outer:
	for _, iss := range issues {
		// A failed DepsOf looks like zero blockers in edges (#752, #1103), so
		// hold rather than launch. Never fail it: the lookup failed, not a
		// dependency.
		if !cfg.IgnoreBlockers && depsOfFailed[iss.Number] {
			fmt.Printf("    ~~ #%s blocker check failed; will retry\n", iss.Number)
			continue
		}
		var unready []string
		if !cfg.IgnoreBlockers {
			unready = unreadyBlockers(it, caps, cfg.BranchPrefix, iss.Number, edges, cfg.SeedScopeOf)
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
				deferredBy[iss.Number] = collider
				continue
			}
			selected = append(selected, iss)
			if cfg.MaxJobs > 0 && len(selected) >= cfg.MaxJobs {
				break outer
			}
		}
	}
	if len(selected) == 0 {
		// The caller already claimed the issue, so a bare skip would strand it
		// in-progress; the marker makes the pipeline release the claim.
		if origin == OriginClaimed && len(issues) > 0 {
			num := issues[0].Number
			if !cfg.IgnoreBlockers {
				switch {
				case depsOfFailed[num]:
					// edges[num] is unreliable (#1103); the release workflow
					// reverts the claim and the operator re-triggers.
					if err := writeDepsOfFailedMarker(pwd); err != nil {
						return err
					}
					fmt.Printf("==> #%s blocker check failed; wrote .spindrift/logs/%s for the pipeline to release the claim\n", num, blockedMarker)
				default:
					if blockers := unreadyBlockers(it, caps, cfg.BranchPrefix, num, edges, cfg.SeedScopeOf); len(blockers) > 0 {
						if err := writeBlockedMarker(pwd, blockers, sources[num]); err != nil {
							return err
						}
						fmt.Printf("==> #%s blocked; wrote .spindrift/logs/%s for the pipeline to release the claim\n", num, blockedMarker)
					} else if collider, ok := deferredBy[num]; ok {
						if err := writeOverlapMarker(pwd, collider); err != nil {
							return err
						}
						fmt.Printf("==> #%s overlaps in-progress #%s; wrote .spindrift/logs/%s for the pipeline to release the claim\n", num, collider, blockedMarker)
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
	signalled := dispatchWave(cfg, it, cf, f, s, selected, origin, claimer, session)
	if held := heldIssues(issues, selected); len(held) > 0 {
		// A signalled stop still leaves this remainder for a later invocation
		// (#3522); only the return value changes.
		if origin == OriginSelective {
			printSelectiveRerunHint(cfg, held)
		} else {
			fmt.Printf("==> %d issue(s) remain for a later invocation (blocked, deferred, or past MAX_JOBS); re-run `spindrift dispatch` to continue the drain\n", len(held))
		}
	}
	if signalled {
		// Same precedence as RunContinuous (#3520/#3521).
		return ErrSignalledStop
	}
	return nil
}

// run creates pwd's .spindrift/logs subdirectory, then executes plan in a
// single pass for every Origin (ADR 0019), selective-list dispatch (#524)
// included: an in-list blocker holds its dependent for a later invocation
// rather than looping waves in-process.
func run(cfg Config, session *Session, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, plan Plan, claimer Claimer) error {
	if err := os.MkdirAll(dispatch.HostLogDirFor(pwd), 0o755); err != nil {
		return err
	}
	return drainMaxJobs(cfg, it, cf, pwd, f, s, plan.Issues, plan.Edges, plan.Sources, plan.Failed, plan.Origin, claimer, session)
}

// SignalledStopAlready is a non-blocking read of a stop/abort channel pair:
// true if either has already fired. Dispatch and the launcher's pre-Dispatch
// early returns share it so they cannot drift (#3522).
func SignalledStopAlready(stop, abort <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
	}
	select {
	case <-abort:
		return true
	default:
	}
	return false
}

// Dispatch is the one-shot headless entry point (#1547): it validates in as a
// Plan, reporting a dependency cycle among in.Issues as an error, and runs it
// as one wave. Callers resolve in.Edges and in.Sources through NewReadiness
// first because selective dispatch needs that graph to evict externally
// blocked issues; rebuilding it here would cost a second DepsOf sweep.
// session may be nil (#3522).
//
// The cfg.Stop/cfg.Abort override applies once here, at the boundary:
// dispatchWave's Gate only sees a signal once a Box launches, so a cycle
// error, a MkdirAll failure, an all-blocked drain, or a marker write error
// never reaches it (#3522). It replaces only the return value; a marker
// already written stays for the release pipeline.
func Dispatch(cfg Config, session *Session, it forge.IssueTracker, cf forge.CodeForge, pwd string, f *dispatch.Factory, s settle.Settler, in Input, claimer Claimer) error {
	plan, err := NewPlan(cfg, in)
	if err == nil {
		err = run(cfg, session, it, cf, pwd, f, s, plan, claimer)
	}
	if SignalledStopAlready(cfg.Stop, cfg.Abort) {
		if err != nil && !errors.Is(err, ErrSignalledStop) {
			// The override displaces this error as the return value, so exit 7
			// would otherwise be the only trace of a real cycle/MkdirAll failure.
			fmt.Fprintf(os.Stderr, "    ?? dispatch: %v\n", err)
		}
		return ErrSignalledStop
	}
	return err
}
