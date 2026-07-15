package console

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
	"spindrift.dev/launcher/internal/waves"
)

// Launcher carries the dependencies Run needs to actually drive a picked
// issue through the continuous engine, beyond the IssueTracker seam Run
// already has. A nil Launcher passed to Run disables launching entirely — a
// pick still promotes and queues, but nothing ever runs — for callers (and
// tests) that only exercise the Pick/Unpick bookkeeping.
type Launcher struct {
	CodeForge forge.CodeForge
	Factory   *dispatch.Factory
	Settle    settle.Settler
	Queue     *Queue
	// MaxParallel caps how many Dispatches run at once. Zero (the default
	// zero-value struct literal) falls back to 1, matching the pre-#647
	// single-slot behaviour.
	MaxParallel int
	// FailedLabel is the tracker label that marks a blocker issue Failed —
	// threaded into Queue.Discover's held-pick check (#650) so a failed
	// blocker surfaces on the held row instead of silently staying "open".
	FailedLabel string
	// Fresh answers whether the loaded image is stale against the current
	// base-branch tip — the same waves.FreshnessChecker seam the headless
	// exit-4 path already uses (issue #652). Nil (every pre-#652 call site)
	// falls back to "always fresh, not applicable", matching drain's old
	// hardcoded stub.
	Fresh waves.FreshnessChecker
	// RebuildFn actually rebuilds and reloads the image — production wires
	// it to the operator's confirm key; nil (every pre-#652 call site, and
	// any test not exercising Rebuild) makes Rebuild a no-op.
	RebuildFn func() error

	mu        sync.Mutex
	launching bool
	wg        sync.WaitGroup
	refresh   chan struct{}
	// stale and staleMessage record the last stale verdict a drain saw —
	// read by StaleStatus for the console's banner. staleMessage is updated
	// on every freshnessChecker() call, stale (and rebuilding/rebuildErr)
	// only by drain/Rebuild themselves.
	stale        bool
	staleMessage string
	rebuilding   bool
	rebuildErr   error
	// pollInterval overrides Run's default background poll cadence — unset
	// (zero) in every production construction site, so only same-package
	// tests reach in to shrink it below defaultPollInterval.
	pollInterval time.Duration
	// terminated is the shared registry Terminate marks and RunContinuous /
	// Settle check at their loop checkpoints (ADR 0024, issue #649). Lazily
	// created by registry() so a bare struct literal (every production and
	// test call site) needs no constructor.
	terminated *terminate.Registry
	// cap is the session's live, resizable parallelism cap (ADR 0023, issue
	// #653) — one Limiter shared across every drain() this Launcher runs,
	// so a Console "+"/"-" takes effect on the RunContinuous call already
	// in flight, not just the next one. Lazily created by limiter() at the
	// MaxParallel starting cap, the same fallback-to-1 tryLaunch already
	// applied.
	cap *waves.Limiter
}

// limiter lazily constructs l.cap at the MaxParallel starting cap (1 when
// unset), mirroring registry()'s lazy-construction pattern so a bare struct
// literal (every production and test call site) needs no constructor.
func (l *Launcher) limiter() *waves.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cap == nil {
		maxParallel := l.MaxParallel
		if maxParallel <= 0 {
			maxParallel = 1
		}
		l.cap = waves.NewLimiter(maxParallel)
	}
	return l.cap
}

// Cap returns the session's current live parallelism cap.
func (l *Launcher) Cap() int {
	return l.limiter().Cap()
}

// Live returns the number of Dispatches this session currently has running.
func (l *Launcher) Live() int {
	return l.limiter().Live()
}

// Resize adjusts the live parallelism cap by delta (+1/-1 from the
// Console's raise/lower keybinding), clamped to at least 1. Raising it
// takes effect immediately -- a held pick launches into the freed slot
// without waiting for a running Dispatch to settle. Lowering it never
// terminates a running Dispatch; it only gates new launches until the live
// count sinks under the new cap on its own (ADR 0023) -- Terminate remains
// the only way a running Dispatch dies by hand.
func (l *Launcher) Resize(delta int) {
	lim := l.limiter()
	lim.Resize(lim.Cap() + delta)
}

// registry lazily constructs l.terminated and, the first time, wires it into
// l.Settle when that Settle is a concrete *settle.Settle (a settle.Fake has
// no loop to check, so the wiring is skipped harmlessly). Both tryLaunch's
// drain (via waves.Config.Terminated) and Terminate itself share the one
// Registry this returns.
func (l *Launcher) registry() *terminate.Registry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminated == nil {
		l.terminated = terminate.NewRegistry()
		if s, ok := l.Settle.(*settle.Settle); ok {
			s.SetTerminated(l.terminated)
		}
	}
	return l.terminated
}

// Terminate ends num's live Dispatch by hand (ADR 0024, issue #649): reaps
// any running Box, marks the shared registry so an in-flight settle loop
// abandons at its next checkpoint instead of continuing, transitions the
// issue InProgress -> Dispatchable (never Failed — the operator decided,
// there is nothing to triage), posts an issue comment naming the terminate
// and linking any dangling branch/PR, appends a terminal line to the Box
// log, and marks the matching queue pick PickTerminated. Pushed branches and
// open PRs are left untouched; a later re-pick adopts an abandoned PR
// through the existing settle adoption path. Best-effort throughout except
// the reap, whose error is returned so a caller can surface it — every other
// step (transition, comment, log line) still runs regardless.
func (l *Launcher) Terminate(tracker forge.IssueTracker, num string) error {
	l.registry().Mark(num)

	var killErr error
	if l.Factory != nil {
		killErr = l.Factory.Kill(num)
		if killErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: kill: %v\n", num, killErr)
		}
		if err := l.Factory.AppendTerminalLine(num, "terminated by operator; issue returned to Dispatchable"); err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: append log line: %v\n", num, err)
		}
	}

	danglingNote := "no open branch/PR found"
	if l.CodeForge != nil {
		branch := l.CodeForge.AgentBranch(num)
		if res, err := forge.ResolveOpenPR(l.CodeForge, num); err == nil && res.Found {
			danglingNote = res.URL
		} else if branch != "" {
			danglingNote = fmt.Sprintf("no open PR found; branch=%s", branch)
		}
	}

	// The issue's actual current label depends on which phase Terminate
	// caught: still InProgress for a running Box or CI watch, but already
	// swapped to Complete if it landed during the merge gate --
	// gateToGreen swaps to Complete as soon as CI confirms green, before
	// selfHeal ever attempts the merge itself (ready.go). TransitionState is
	// an unconditional label swap with no compare-and-swap, so both calls
	// run regardless of which (if either) label is actually present: a
	// remove of an absent label is a no-op on every adapter, and the second
	// call's add of Dispatchable is idempotent.
	if err := tracker.TransitionState(num, forge.InProgress, forge.Dispatchable); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: transition to Dispatchable: %v\n", num, err)
	}
	if err := tracker.TransitionState(num, forge.Complete, forge.Dispatchable); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: clear Complete: %v\n", num, err)
	}
	comment := fmt.Sprintf("Terminated by operator: reclaimed back to Dispatchable. %s", danglingNote)
	if err := tracker.Comment(num, comment); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: post comment: %v\n", num, err)
	}

	l.Queue.setState(num, PickTerminated, "terminated by operator")
	l.signalRefresh()
	return killErr
}

// refreshChan lazily constructs l.refresh, so a bare struct literal (every
// production and test call site) needs no constructor to use it. Buffered
// to exactly one slot: a burst of writes (claim, settle, promotion) coalesces
// into a single pending refresh instead of queuing one per write.
func (l *Launcher) refreshChan() chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refresh == nil {
		l.refresh = make(chan struct{}, 1)
	}
	return l.refresh
}

// signalRefresh marks a refresh pending — called after every write this
// session makes to the tracker (a claim, a settle, a promotion), so Run's
// select loop re-queries the backlog without the operator asking (#647 AC4).
// Non-blocking: a refresh already pending is left alone.
func (l *Launcher) signalRefresh() {
	select {
	case l.refreshChan() <- struct{}{}:
	default:
	}
}

// Refreshes returns the channel Run selects on for background-write-triggered
// refreshes.
func (l *Launcher) Refreshes() <-chan struct{} {
	return l.refreshChan()
}

// tryLaunch starts draining Queue through waves.RunContinuous, up to
// MaxParallel slots (1 when unset), in the background, unless a drain is
// already running —
// RunContinuous's own refill-on-completion picks up any pick Add()ed to
// Queue while that drain is in flight, so a second concurrent invocation is
// never needed, only a fresh one once the queue has gone idle.
func (l *Launcher) tryLaunch(tracker forge.IssueTracker, pwd string) {
	l.mu.Lock()
	if l.launching {
		l.mu.Unlock()
		return
	}
	l.launching = true
	l.wg.Add(1)
	l.mu.Unlock()

	go l.drain(tracker, pwd)
}

// drain runs waves.RunContinuous to completion, then — still holding
// l.mu — checks Queue for a pick that landed too late for that run's last
// discover() to see (RunContinuous returns as soon as its wg count hits
// zero, with no listener for a subsequent Add). Finding one re-drains
// immediately instead of clearing l.launching, so a concurrent tryLaunch
// call racing this same window can never observe l.launching==true with
// nothing left to pick it up — either this loop sees the new pick, or its
// Add()+tryLaunch happens-after this critical section releases l.mu and
// starts a fresh drain itself.
func (l *Launcher) drain(tracker forge.IssueTracker, pwd string) {
	defer l.wg.Done()
	discover := func() ([]waves.Issue, map[string][]string, error) {
		defer l.signalRefresh() // a claim attempt is always a tracker write, win or lose
		issues, edges, err := l.Queue.Discover(tracker, l.CodeForge, l.FailedLabel)
		// A successful claim here is a fresh Dispatch starting for issues,
		// so any earlier Terminate mark for these numbers must not carry
		// over — otherwise a re-pick's own settle would abandon on its very
		// first checkpoint instead of running the adoption path normally
		// (ADR 0024, issue #649).
		for _, iss := range issues {
			l.registry().Unmark(iss.Number)
		}
		return issues, edges, err
	}
	for {
		err := waves.RunContinuous(waves.Config{Limiter: l.limiter(), Terminated: l.registry()}, tracker, l.CodeForge, pwd, l.Factory, queueSettler{l.Settle, l.Queue, l.signalRefresh, l.registry()}, discover, l.freshnessChecker())

		if errors.Is(err, waves.ErrImageStale) {
			// Every in-flight Box this run already finished (RunContinuous
			// only returns ErrImageStale after wg.Wait()) — nothing left to
			// race. Park here instead of looping: a tight re-drain would
			// just re-hit the same stale verdict on every refill. Rebuild
			// resumes draining on success by calling tryLaunch again.
			l.mu.Lock()
			l.launching = false
			l.mu.Unlock()
			return
		}

		l.mu.Lock()
		if !l.Queue.hasQueued() {
			l.launching = false
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
	}
}

// freshnessChecker wraps l.Fresh so every call also records the verdict for
// StaleStatus to read — RunContinuous calls the checker directly and never
// sees Launcher, so this is the only place that can capture its result. Nil
// Fresh (every pre-#652 call site) falls back to the always-fresh stub
// drain used to hardcode.
func (l *Launcher) freshnessChecker() waves.FreshnessChecker {
	if l.Fresh == nil {
		return func() (bool, bool, string) { return false, true, "" }
	}
	return func() (bool, bool, string) {
		applicable, fresh, msg := l.Fresh()
		l.mu.Lock()
		l.stale = applicable && !fresh
		l.staleMessage = msg
		l.mu.Unlock()
		return applicable, fresh, msg
	}
}

// Rebuild runs RebuildFn in the background — the operator's confirm key
// (issue #652 AC3) — so the session stays alive and responsive with
// Rebuilding surfaced on the banner while it runs. A rebuild already in
// flight makes a second call a no-op. On success it clears the stale gate
// and resumes draining (tryLaunch), so any pick held at PickQueued through
// the stale window launches without being re-picked; on failure it leaves
// the gate held and records the error for StaleStatus to surface. A nil
// RebuildFn (no production wiring, or a test not exercising Rebuild) is a
// no-op.
func (l *Launcher) Rebuild(tracker forge.IssueTracker, pwd string) {
	if l.RebuildFn == nil {
		return
	}
	l.mu.Lock()
	if l.rebuilding {
		l.mu.Unlock()
		return
	}
	l.rebuilding = true
	l.mu.Unlock()
	l.signalRefresh()

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		err := l.RebuildFn()

		l.mu.Lock()
		l.rebuilding = false
		l.rebuildErr = err
		if err == nil {
			l.stale = false
			l.staleMessage = ""
		}
		l.mu.Unlock()
		l.signalRefresh()

		if err == nil {
			l.tryLaunch(tracker, pwd)
		}
	}()
}

// StaleStatus returns the launcher's live image-freshness/rebuild state —
// the console's per-render sync source for the stale banner (issue #652).
func (l *Launcher) StaleStatus() (stale bool, message string, rebuilding bool, rebuildErr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rebuildErr != nil {
		rebuildErr = l.rebuildErr.Error()
	}
	return l.stale, l.staleMessage, l.rebuilding, rebuildErr
}

// Wait blocks until any in-flight background drain finishes — Run calls it
// before returning, so quitting the console never races the caller's
// cleanup (e.g. the driver-cache teardown) against a still-running Box.
func (l *Launcher) Wait() {
	l.wg.Wait()
}
