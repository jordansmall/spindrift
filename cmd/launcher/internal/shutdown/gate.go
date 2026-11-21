// Package shutdown carries the launcher's two-stage operator-shutdown latch
// (issue #3522) for every Box-launching path except RunContinuous, which keeps
// its own hand-rolled copy (waves/continuous.go) and shares only AbortInFlight,
// the loop that talks to the tracker and reaper.
package shutdown

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"sync"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/panicguard"
	"spindrift.dev/launcher/internal/terminate"
)

// Gate is the two-stage stop/abort latch: stop means "launch nothing further,
// let in-flight work drain"; abort means "reap every in-flight Box now".
// Allowed, Launch, Leave and Signalled are safe to call concurrently; Watch and
// Settle are lifecycle calls made by the Gate's builder, before the first and
// after the last of those.
type Gate struct {
	stop  <-chan struct{}
	abort <-chan struct{}

	it forge.IssueTracker
	cf forge.CodeForge
	// branchPrefix is BRANCH_PREFIX, naming the agent branch Reclaim resolves.
	branchPrefix string
	reaper       terminate.Reaper
	reg          *terminate.Registry
	// settled is the kind's forge.SettledLabels; empty disables the
	// unsettled check.
	settled []string

	// mu also guards signalled, aborted, aborting, inflight, and claimed.
	mu sync.Mutex
	// idle lets Settle block until an in-progress reclaim (aborting) clears.
	idle *sync.Cond

	signalled bool
	aborted   bool
	// aborting is true only for the span of one reclaimInFlight call: a Settle
	// that returned mid-reclaim could report a false Signalled while an issue
	// sits between Reclaim's Kill and its InProgress->Dispatchable transition.
	aborting bool
	// inflight holds issues Launch registered and Leave has not forgotten.
	inflight map[string]bool
	// claimed holds the issues whose in-progress claim this process took, so a
	// decline knows whether releasing one is its business. An issue is claimed
	// from before its Box starts until Leave; recover, which inherits its
	// claim, never appears here.
	claimed map[string]bool

	watchDone    chan struct{}
	watchExited  chan struct{}
	watchStarted sync.Once
	watchSettled sync.Once
}

// NewGate returns a Gate reading stop/abort. Either channel may be nil,
// meaning the caller offers no such request. settled is the kind's
// forge.SettledLabels; nil skips the already-settled check on abort.
func NewGate(stop, abort <-chan struct{}, it forge.IssueTracker, cf forge.CodeForge, branchPrefix string, reaper terminate.Reaper, reg *terminate.Registry, settled []string) *Gate {
	if reg == nil {
		reg = terminate.NewRegistry()
	}
	g := &Gate{
		stop:         stop,
		abort:        abort,
		it:           it,
		cf:           cf,
		branchPrefix: branchPrefix,
		reaper:       reaper,
		reg:          reg,
		settled:      settled,
		inflight:     map[string]bool{},
		claimed:      map[string]bool{},
	}
	g.idle = sync.NewCond(&g.mu)
	return g
}

// observeStop is a non-blocking latch on stop. Caller holds mu. announce is
// false only for Settle's final post-join re-check, where any drain is
// already over and a drain line would mislead.
func (g *Gate) observeStop(announce bool) {
	if g.signalled || g.stop == nil {
		return
	}
	select {
	case <-g.stop:
		g.signalled = true
		if !announce {
			return
		}
		if len(g.inflight) > 0 {
			fmt.Println("==> stop requested; draining outstanding work")
		} else {
			fmt.Println("==> stop requested; nothing in flight")
		}
	default:
	}
}

// observeAbort is a non-blocking latch on abort. Whichever call site notices
// abort first (the watcher, Allowed, Launch, or Settle) runs reclaimInFlight;
// every later call is a no-op. Caller holds mu.
func (g *Gate) observeAbort() {
	if g.aborted || g.abort == nil {
		return
	}
	select {
	case <-g.abort:
		g.aborted = true
		g.reclaimInFlight()
	default:
	}
}

// reclaimInFlight snapshots inflight and drops mu for the reclaim I/O, which
// talks to the tracker and reaper and so must not run under a held lock.
// Caller holds mu on entry and exit.
func (g *Gate) reclaimInFlight() {
	nums := make([]string, 0, len(g.inflight))
	for num := range g.inflight {
		nums = append(nums, num)
	}
	g.aborting = true
	g.mu.Unlock()
	AbortInFlight(g.it, g.cf, g.branchPrefix, g.reaper, g.reg, g.settled, nums)
	g.mu.Lock()
	g.aborting = false
	g.idle.Broadcast()
}

// Hold records that this process is responsible for num's in-progress claim,
// so a later decline releases it rather than stranding it in-progress. Call it
// right after taking the claim, or up front for a workflow-pre-claimed issue.
// recover, whose claim the agent-recover workflow holds and whose PR may still
// be open, must never call it. That governs decline only: an abort reaps every
// in-flight issue, recover's included, since the Box is killed either way.
func (g *Gate) Hold(num string) {
	g.mu.Lock()
	g.claimed[num] = true
	g.mu.Unlock()
}

// releaseClaim hands num back to Dispatchable only if Hold recorded this
// process's claim: releasing someone else's claim strands an open PR's issue
// as re-dispatchable. The entry is forgotten first, so two declines release it
// once. Both callers decline before arm has run, so there is no Box to kill,
// hence the nil reaper. Caller holds mu on entry and exit; it is dropped
// across the reclaim I/O.
func (g *Gate) releaseClaim(num string) {
	if !g.claimed[num] {
		return
	}
	delete(g.claimed, num)
	g.mu.Unlock()
	reclaimOne(g.it, g.cf, g.branchPrefix, nil, g.reg, num, terminate.DrainDecline)
	g.mu.Lock()
}

// Allowed reports whether the caller may go on to claim and launch num. A
// decline releases num's claim only if Hold recorded it.
func (g *Gate) Allowed(num string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observeStop(true)
	g.observeAbort()
	if g.signalled || g.aborted {
		g.releaseClaim(num)
		return false
	}
	return true
}

// Launch registers num as in-flight and calls arm under the Gate's own lock,
// so the Box's kill latch is armed before any concurrent abort can snapshot
// the in-flight set. False means a signal arrived since the caller's Allowed
// check; num's claim is then released as in Allowed.
func (g *Gate) Launch(num string, arm func()) bool {
	g.mu.Lock()
	g.observeStop(true)
	g.observeAbort()
	if g.signalled || g.aborted {
		g.releaseClaim(num)
		g.mu.Unlock()
		return false
	}
	arm()
	g.inflight[num] = true
	g.mu.Unlock()
	return true
}

// Leave forgets num, both as in-flight and as claimed. Call it the moment its
// Box finishes, or a later abort or decline would drag an already-finished
// issue back to Dispatchable.
func (g *Gate) Leave(num string) {
	g.mu.Lock()
	delete(g.inflight, num)
	delete(g.claimed, num)
	g.mu.Unlock()
}

// Watch starts the abort watcher goroutine, which reclaims in-flight Boxes as
// soon as abort closes rather than at the next Allowed/Launch. Defer Settle
// immediately after Watch so every return path tears the watcher down. A
// no-op when abort is nil; a second call is a no-op too, since Settle's
// one-shot join could never reach a second watcher.
func (g *Gate) Watch() {
	if g.abort == nil {
		return
	}
	g.watchStarted.Do(func() {
		g.watchDone = make(chan struct{})
		g.watchExited = make(chan struct{})
		panicguard.Go(func() {
			defer close(g.watchExited)
			select {
			case <-g.abort:
				g.mu.Lock()
				g.observeAbort()
				g.mu.Unlock()
			case <-g.watchDone:
			}
		})
	})
}

// Settle joins the watcher, waits out any reclaim still in progress, and
// takes the final non-racy latch reading. It is idempotent, so a path may
// both defer it and call it inline.
func (g *Gate) Settle() {
	g.watchSettled.Do(func() {
		if g.watchDone != nil {
			close(g.watchDone)
			<-g.watchExited
		}
	})
	g.mu.Lock()
	// A concurrent Allowed/Launch may have noticed abort first and still be
	// mid-reclaim.
	for g.aborting {
		g.idle.Wait()
	}
	// The watcher's select picks arbitrarily when abort and watchDone are both
	// ready, so a signal closing during teardown could otherwise go unseen.
	g.observeStop(false)
	g.observeAbort()
	g.mu.Unlock()
}

// Signalled reports whether either stage fired. Valid after Settle.
func (g *Gate) Signalled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.signalled || g.aborted
}

// Aborted reports whether the abort stage fired. Valid after Settle.
func (g *Gate) Aborted() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.aborted
}

// reclaimOne runs terminate.Reclaim for one issue, best-effort: a failure is
// logged, not returned.
func reclaimOne(it forge.IssueTracker, cf forge.CodeForge, branchPrefix string, reaper terminate.Reaper, reg *terminate.Registry, num string, trigger terminate.Trigger) {
	if err := terminate.Reclaim(it, cf, branchPrefix, reaper, reg, num, trigger); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: reclaim #%s: %v\n", num, err)
	}
}

// AbortInFlight announces the abort, then reclaims each num in sorted order,
// logging each failure. Exported so RunContinuous's abort path runs the same
// loop (#3522). settled is as for NewGate.
func AbortInFlight(it forge.IssueTracker, cf forge.CodeForge, branchPrefix string, reaper terminate.Reaper, reg *terminate.Registry, settled []string, nums []string) {
	nums = unsettled(it, settled, nums)
	if len(nums) == 0 {
		fmt.Println("==> abort requested; nothing in flight")
		return
	}
	sorted := append([]string(nil), nums...)
	sort.Strings(sorted)
	fmt.Printf("==> abort requested; terminating %d outstanding Box(es)\n", len(sorted))
	for _, num := range sorted {
		reclaimOne(it, cf, branchPrefix, reaper, reg, num, terminate.SignalAbort)
	}
}

// unsettled drops the issues whose settle already wrote a terminal label.
// Both abort paths forget an issue only once its settler has returned, so an
// abort landing between the settler's terminal write and that forgetting
// would otherwise reclaim a finished issue (#3522, #4888). The terminal write
// is the settler's, not the caller's, so the tracker holds the answer before
// the abort is observable. It fails open, keeping an issue in the reap when
// the lookup errors or the tracker carries no dispatch label (Jira's
// statuses): a missed reap strands a live Box, while a redundant one only
// repeats what Reclaim does idempotently.
func unsettled(it forge.IssueTracker, settled []string, nums []string) []string {
	if len(settled) == 0 {
		return nums
	}
	var live []string
	for _, num := range nums {
		if iss, err := it.Issue(num); err == nil && wearsAny(iss.Labels, settled) {
			continue
		}
		live = append(live, num)
	}
	return live
}

func wearsAny(labels, want []string) bool {
	return slices.ContainsFunc(labels, func(l string) bool { return slices.Contains(want, l) })
}
