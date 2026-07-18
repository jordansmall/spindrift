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
	// MaxParallel sets the live cap's *starting* value only (1 unless
	// positive, matching the pre-#647 single-slot behaviour) — since #653
	// (ADR 0023) the cap actually enforced during a session lives in
	// l.limiter()'s *waves.Limiter and can move at runtime via
	// Resize/ResizeDelta.
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
	// any test not exercising Rebuild) makes Rebuild a no-op. It returns the
	// rebuild's captured nix output (issue #765) alongside the error, so a
	// background rebuild never writes directly to the Console's own
	// stdout/stderr.
	RebuildFn func() (string, error)
	// RecoverFn adopts an orphaned issue's abandoned PR through the
	// existing settle adoption path (recoverByNumber) — Console startup
	// orphan recovery (issue #651). Wired by cmdConsole in main.go, since
	// console cannot import the main package. Nil (every test not
	// exercising recovery) skips orphan detection entirely.
	RecoverFn func(issueNum string) error

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
	// rebuildOutput is the last rebuild's captured nix output (issue #765) —
	// stdout/stderr merged, in build order — set on every RebuildFn
	// completion regardless of outcome so an operator can retrieve it
	// through StaleStatus without RunNixBuild ever writing to the Console's
	// own stdout/stderr.
	rebuildOutput string
	// pollInterval overrides Run's default background poll cadence — unset
	// (zero) in every production construction site, so only same-package
	// tests reach in to shrink it below defaultPollInterval.
	pollInterval time.Duration
	// terminated is the shared registry Terminate marks and RunContinuous /
	// Settle check at their loop checkpoints (ADR 0024, issue #649). Lazily
	// created by registry() so a bare struct literal (every production and
	// test call site) needs no constructor.
	terminated *terminate.Registry
	// terminatingNums tracks issue numbers with a TerminateAsync goroutine
	// still in flight, guarding against a second confirm firing a duplicate
	// Terminate for the same issue (issue #745): the queue pick stays
	// PickRunning — and so isLive keeps reporting it live — for the whole
	// async call, not just Terminate's old synchronous window. Lazily
	// created by terminating() so a bare struct literal (every production
	// and test call site) needs no constructor.
	terminatingNums map[string]bool
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

// LiveIssues returns the issue numbers of every pick this session currently
// has PickRunning — the quit dialog's live-or-not gate and drain/
// terminate-all's own enumeration (issue #651) all read this one source of
// truth, synchronized through Queue's own lock, rather than the Limiter's
// live count: a settle marks the queue pick Settled before releasing the
// Limiter slot (queueSettler.Settle), so this can never observe "no live
// Dispatches" a moment before the Limiter itself agrees.
func (l *Launcher) LiveIssues() []string {
	var nums []string
	for _, p := range l.Queue.Snapshot() {
		if p.State == PickRunning {
			nums = append(nums, p.Number)
		}
	}
	return nums
}

// OrphanedIssues returns the issue numbers of every sandbox still running
// under the deterministic agent-issue-<N> naming scheme, with nothing in
// this fresh process tracking it — the signature of a hard death (crash,
// dropped SSH) from a prior session (issue #651, ADR 0023). A Launcher built
// without a Factory reports none.
func (l *Launcher) OrphanedIssues() ([]string, error) {
	if l.Factory == nil {
		return nil, nil
	}
	return l.Factory.OrphanedIssues()
}

// Resize adjusts the live parallelism cap by delta (+1/-1 from the
// Console's raise/lower keybinding), clamped to at least 1. Raising it
// takes effect immediately -- a held pick launches into the freed slot
// without waiting for a running Dispatch to settle. Lowering it never
// terminates a running Dispatch; it only gates new launches until the live
// count sinks under the new cap on its own (ADR 0023) -- Terminate remains
// the only way a running Dispatch dies by hand.
func (l *Launcher) Resize(delta int) {
	l.limiter().ResizeDelta(delta)
}

// registry lazily constructs l.terminated and, the first time, wires it into
// l.Settle when that Settle is a concrete *settle.Settle (a settle.Fake has
// no loop to check, so the wiring is skipped harmlessly). Both tryLaunch's
// drain (via waves.Config.Terminated) and Terminate itself share the one
// Registry this returns.
//
// Re-pick vs. abandoned-settle race (issue #743, found reviewing #649): a
// plain per-number "terminated" bool cannot tell "my own stale mark from a
// dead incarnation" apart from "a still-live settle goroutine hasn't
// checked yet". Terminate marks num terminated (below); discover's own
// claim of a re-pick used to unconditionally clear that same mark so its
// fresh settle wasn't instantly abandoned (ADR 0024, issue #649) — but if
// that unmark landed in the window between Terminate's mark and the old,
// still-in-flight settle goroutine's next checkpoint (settle/ready.go's
// CI-watch/merge-gate loops poll only once per MergePollInterval tick),
// the mark vanished before the old goroutine ever observed it. It then
// proceeded as if never terminated, and queueSettler's post-settle
// setState (settler.go) landed on the re-pick's own row — Queue.setState
// scans back-to-front and stops at the newest match on number, so a stale
// write from the old incarnation corrupted the new one's queue state
// instead of the discarded old row it was meant for.
//
// The fix: terminate.Registry now keys termination on a per-number
// generation counter instead of a bool. Begin (called once, at claim time,
// for every freshly dispatched issue including a re-pick) starts a new
// generation without touching any earlier one's mark; Mark records the
// *current* generation as terminated; Marked reports whether one specific
// generation was marked, not merely whether the number ever was. Every
// checkpoint a dispatch makes — waves/continuous.go's post-Run() check,
// settle's internal CI-watch/merge-gate loops, and queueSettler's
// post-settle check — carries the generation it was launched under
// (waves.Issue.Generation, threaded through the whole Settle/Fail call) and
// checks against that generation specifically. A re-pick's Begin can no
// longer erase a live settle's own mark: the two incarnations now hold
// distinct identities that never collide.
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
	// caught: still InProgress for a running Box, a CI watch, or anywhere on
	// the landing path (rebase-retry, conflict-resolve, post-force-push-wait)
	// -- selfHeal holds the swap to Complete until the landing path settles
	// (issue #757, ready.go), so InProgress is the common case here. Complete
	// is still possible if Terminate lands just after settling. TransitionState
	// is an unconditional label swap with no compare-and-swap, so both calls
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

// TerminateAsync runs Terminate for num in the background (issue #745),
// mirroring tryLaunch/Rebuild's pattern so the operator's confirm key
// returns immediately instead of blocking the Update loop on tracker I/O.
// num already in flight makes a second call a no-op: the queue pick stays
// PickRunning until Terminate itself sets PickTerminated at the very end, so
// isLive keeps reporting num live for the whole call, not just its old
// synchronous window — a second confirm on the same row would otherwise
// race a duplicate Kill/Comment/TransitionState.
func (l *Launcher) TerminateAsync(tracker forge.IssueTracker, num string) {
	// Two short critical sections, not one: terminating() takes l.mu itself
	// to lazily construct the map, then this function re-takes it right
	// after for the check-and-set below. Splitting them is safe because
	// every read/write of the map is still mutex-guarded throughout — the
	// atomicity that matters is the check-and-set itself, not its adjacency
	// to the lazy-init.
	inFlight := l.terminating()

	l.mu.Lock()
	if inFlight[num] {
		l.mu.Unlock()
		return
	}
	inFlight[num] = true
	l.wg.Add(1)
	l.mu.Unlock()

	go func() {
		defer l.wg.Done()
		// Return value dropped intentionally: Terminate already logs its own
		// kill failure to stderr, via Factory.Kill above, before returning it,
		// so nothing is lost by not handling it here too. This goroutine is the
		// sole call site of Terminate since #745 folded the prior synchronous
		// caller into this async path — there is no second call site left to
		// stay consistent with (see tea.go's handleTerminateConfirmKey for the
		// matching rationale at the caller).
		l.Terminate(tracker, num)

		l.mu.Lock()
		delete(inFlight, num)
		l.mu.Unlock()
	}()
}

// terminating lazily constructs l.terminatingNums, mirroring registry()'s
// lazy-construction pattern so a bare struct literal (every production and
// test call site) needs no constructor.
func (l *Launcher) terminating() map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminatingNums == nil {
		l.terminatingNums = make(map[string]bool)
	}
	return l.terminatingNums
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

// tryLaunch starts draining Queue through waves.RunContinuous, up to the
// live cap l.limiter() holds (ADR 0023, issue #653) — MaxParallel only sets
// that cap's starting value (1 when unset); Resize can move it up or down
// for the life of the session — in the background, unless a drain is
// already running or Queue has nothing left to launch (#754) —
// RunContinuous's own refill-on-completion picks up any pick Add()ed to
// Queue while that drain is in flight, so a second concurrent invocation is
// never needed, only a fresh one once the queue has gone idle. The
// background poll tick (tea.go pollTickMsg) calls this every interval
// regardless of queue state — see Queue.Empty (#650) for why the gate
// must cover PickHeld as well as PickQueued.
func (l *Launcher) tryLaunch(tracker forge.IssueTracker, pwd string) {
	if l.Queue.Empty() {
		return
	}

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
// discover() to see (RunContinuous returns as soon as its outstanding count
// of in-flight Boxes drops to zero and the idle cond wakes it, with no
// listener for a subsequent increment). Finding one re-drains
// immediately instead of clearing l.launching, so a concurrent tryLaunch
// call racing this same window can never observe l.launching==true with
// nothing left to pick it up — either this loop sees the new pick, or its
// Add()+tryLaunch happens-after this critical section releases l.mu and
// starts a fresh drain itself.
func (l *Launcher) drain(tracker forge.IssueTracker, pwd string) {
	defer l.wg.Done()
	discover := func() ([]waves.Issue, map[string][]string, waves.Sources, map[string]bool, error) {
		defer l.signalRefresh() // a claim attempt is always a tracker write, win or lose
		issues, edges, sources, err := l.Queue.Discover(tracker, l.CodeForge, l.FailedLabel)
		// A successful claim here is a fresh Dispatch starting for issues,
		// so any earlier Terminate mark for these numbers must not carry
		// over — otherwise a re-pick's own settle would abandon on its very
		// first checkpoint instead of running the adoption path normally
		// (ADR 0024, issue #649). Begin starts a new registry generation for
		// each rather than blindly clearing the old one (the pre-#743
		// Unmark did, unconditionally) — an in-flight settle goroutine from
		// the terminated incarnation, still holding the generation it was
		// launched under, keeps seeing itself as terminated no matter how
		// many later re-picks start and clear their own fresh generations in
		// the meantime. See the race this closes, documented on registry()
		// below.
		for i, iss := range issues {
			issues[i].Generation = l.registry().Begin(iss.Number)
		}
		// Queue.Discover already resolved this pick's own DepsOf-failure
		// case internally (held it, rather than returning it) -- nil is
		// always correct here, never a set nextReady would need to act on.
		return issues, edges, sources, nil, err
	}
	for {
		// Label, InProgressLabel, and OverlapGate are deliberately left
		// zero-value (#706). Label==InProgressLabel (both "") makes
		// claimIssue (waves/engine.go) skip a second Dispatchable->InProgress
		// transition — Queue.Discover (queue.go, the discover closure above)
		// already performed that claim itself. OverlapGate=="" leaves the
		// touch-overlap gate a no-op, because Console picks are
		// operator-directed, not batch-discovered, so they're exempt from
		// deferring on another in-progress issue's touched files.
		// TestRunContinuous_ConsoleConfig_SkipsRedundantClaim and
		// TestRunContinuous_DivergentLabels_DoubleClaims (launch_test.go)
		// pin this: diverging Label from InProgressLabel double-claims.
		err := waves.RunContinuous(waves.Config{Limiter: l.limiter(), Terminated: l.registry()}, tracker, l.CodeForge, pwd, l.Factory, queueSettler{l.Settle, l.Queue, l.signalRefresh, l.registry()}, discover, l.freshnessChecker())

		if errors.Is(err, waves.ErrImageStale) {
			// RunContinuous's own "stale" flag is a one-shot latch for this
			// single invocation: once any refill sees a stale verdict, every
			// later refill (including one triggered by a Box that was
			// already running when staleness hit) short-circuits without
			// ever consulting fresh() again. That leaves a window where a
			// concurrent Rebuild finishes — flipping the checker back to
			// fresh and calling tryLaunch — while this drain is still
			// waiting on that in-flight Box; tryLaunch no-ops (l.launching
			// is still true), and this loop would otherwise park a held
			// pick with no one left to resume it. Re-checking freshness
			// once more here catches that race: a fresh verdict re-drains
			// immediately instead of parking, exactly as if Rebuild's own
			// tryLaunch call had landed.
			if applicable, fresh, _ := l.freshnessChecker()(); applicable && !fresh {
				l.mu.Lock()
				l.launching = false
				l.mu.Unlock()
				return
			}
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
		l.signalRefresh()
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
	l.rebuildErr = nil
	l.mu.Unlock()
	l.signalRefresh()

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		output, err := l.RebuildFn()

		l.mu.Lock()
		l.rebuilding = false
		l.rebuildErr = err
		l.rebuildOutput = output
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
// rebuildOutput is the last rebuild's captured nix output (issue #765),
// retrievable here instead of ever being streamed to the Console's own
// stdout/stderr.
func (l *Launcher) StaleStatus() (stale bool, message string, rebuilding bool, rebuildErr string, rebuildOutput string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rebuildErr != nil {
		rebuildErr = l.rebuildErr.Error()
	}
	return l.stale, l.staleMessage, l.rebuilding, rebuildErr, l.rebuildOutput
}

// Wait blocks until any in-flight background drain finishes — Run calls it
// before returning, so quitting the console never races the caller's
// cleanup (e.g. the driver-cache teardown) against a still-running Box.
func (l *Launcher) Wait() {
	l.wg.Wait()
}
