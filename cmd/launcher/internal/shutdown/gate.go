// Package shutdown carries the launcher's two-stage operator-shutdown latch
// (issue #3522) for every Box-launching path that is not RunContinuous: the
// one-shot dispatch wave, selective dispatch, research, and the recover gate.
// RunContinuous keeps its own hand-rolled version of this same shape
// (waves/continuous.go's observeStop/observeAbort/reclaimInFlight) rather than
// switching to Gate, since untangling that call site is out of scope here;
// AbortInFlight is exported so its abort path can at least share the one loop
// that actually talks to the tracker and reaper.
package shutdown

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"sync"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/terminate"
)

// Gate is the two-stage stop/abort latch: a first signal on stop means
// "launch nothing further, let in-flight work drain"; a second on abort means
// "reap every in-flight Box now". Either channel may be nil, meaning the
// caller offers no such request. Allowed, Launch, Leave and Signalled are safe
// to call concurrently -- every wave goroutine does. Watch and Settle are
// lifecycle calls the goroutine that built the Gate makes, before the first
// and after the last of those.
type Gate struct {
	stop  <-chan struct{}
	abort <-chan struct{}

	it     forge.IssueTracker
	cf     forge.CodeForge
	reaper terminate.Reaper
	reg    *terminate.Registry
	// completeLabel is the label a settle writes once it has merged and
	// landed, the one unsettled below reads. Empty disables that check.
	completeLabel string

	// mu also guards signalled, aborted, aborting, inflight, and claimed
	// below.
	mu sync.Mutex
	// idle lets Settle block until an in-progress reclaim (aborting) clears,
	// mirroring waves/continuous.go's idle/outstanding wait.
	idle *sync.Cond

	signalled bool
	aborted   bool
	// aborting is true only for the span of one reclaimInFlight call, the same
	// invariant as waves/continuous.go's own aborting flag in reclaimInFlight: a
	// Settle that returned mid-reclaim could report a false Signalled while an
	// issue is still stuck between Reclaim's Kill and its
	// InProgress->Dispatchable transition.
	aborting bool
	// inflight holds only issues Launch has registered and Leave has not yet
	// forgotten. It must lose an entry the instant that issue's Box finishes,
	// or a later abort would drag an already-finished issue back to
	// Dispatchable.
	inflight map[string]bool
	// claimed holds the issues whose in-progress claim this process took, so a
	// decline knows whether releasing one is its business. It is not the
	// inverse of inflight: an issue is claimed from before its Box starts
	// until Leave, while a caller that inherited an already-claimed issue
	// without taking the claim itself -- recover -- never appears here at all.
	claimed map[string]bool

	watchDone    chan struct{}
	watchExited  chan struct{}
	watchStarted sync.Once
	watchSettled sync.Once
}

// NewGate returns a Gate reading stop/abort. Either channel may be nil,
// meaning the caller offers no such request (the Gate is then inert).
// completeLabel is the caller's configured complete label, which unsettled
// reads; pass "" to leave that check off.
func NewGate(stop, abort <-chan struct{}, it forge.IssueTracker, cf forge.CodeForge, reaper terminate.Reaper, reg *terminate.Registry, completeLabel string) *Gate {
	if reg == nil {
		// A fresh Registry behaves exactly like the nil one it replaces
		// (Marked reports false until something marks it), mirroring
		// continuous.go's own nil-Registry fallback.
		reg = terminate.NewRegistry()
	}
	g := &Gate{
		stop:          stop,
		abort:         abort,
		it:            it,
		cf:            cf,
		reaper:        reaper,
		reg:           reg,
		completeLabel: completeLabel,
		inflight:      map[string]bool{},
		claimed:       map[string]bool{},
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

// observeAbort is a non-blocking latch on abort, the abort counterpart to
// observeStop. Unlike observeStop it runs the one-time reclaim itself: the
// aborted guard means whichever call site notices abort closed first (the
// watcher, Allowed, Launch, or Settle) runs reclaimInFlight, and every later
// call is a no-op. Caller holds mu.
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

// reclaimInFlight snapshots inflight, drops mu for the reclaim I/O, and
// retakes it before returning -- the reportStaleDrainReleasingMu idiom
// waves/continuous.go uses for the same reason: terminate.Reclaim talks to
// the tracker and reaper, and neither belongs inside a held lock. Caller
// holds mu on entry and exit.
func (g *Gate) reclaimInFlight() {
	nums := make([]string, 0, len(g.inflight))
	for num := range g.inflight {
		nums = append(nums, num)
	}
	g.aborting = true
	g.mu.Unlock()
	AbortInFlight(g.it, g.cf, g.reaper, g.reg, g.completeLabel, nums)
	g.mu.Lock()
	g.aborting = false
	g.idle.Broadcast()
}

// Hold records that this process is responsible for num's in-progress claim,
// so a later decline releases it rather than stranding it in-progress with
// nothing left to move it. Call it right after taking the claim, or up front
// for an issue the caller's workflow pre-claimed. A caller that never took
// the claim -- recover, whose claim the agent-recover workflow holds and
// whose PR may still be open -- must never call it, since a decline releases
// only what Hold recorded. That caveat governs decline (releaseClaim) alone:
// a stage-two abort reaps every in-flight issue, recover's included, because
// the Box is being killed either way.
func (g *Gate) Hold(num string) {
	g.mu.Lock()
	g.claimed[num] = true
	g.mu.Unlock()
}

// releaseClaim hands num back to Dispatchable, but only if Hold recorded this
// process's claim; for anything else it is a no-op, since releasing a claim
// someone else took is what strands an open PR's issue as re-dispatchable. The
// entry is forgotten first, so two declines release it once. Both callers
// decline before arm has run, so there is no Box to kill -- hence the nil
// reaper, unlike the abort path's real one. Caller holds mu on entry and exit;
// mu is dropped across the reclaim I/O, the same idiom as reclaimInFlight.
func (g *Gate) releaseClaim(num string) {
	if !g.claimed[num] {
		return
	}
	delete(g.claimed, num)
	g.mu.Unlock()
	reclaimOne(g.it, g.cf, nil, g.reg, num)
	g.mu.Lock()
}

// Allowed reports whether the caller may go on to claim and launch num. It
// latches stop/abort non-blockingly first, announcing the drain/abort the
// first time it observes one. A decline releases num's claim if and only if
// Hold recorded it, leaving an unheld issue untouched.
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
// the in-flight set. It reports whether the caller may proceed: false means a
// signal arrived after the caller's Allowed check but before this call, in
// which case Launch releases num's claim through the same terminate.Reclaim
// the abort path uses -- but only if Hold recorded that this process took
// that claim, since a caller holding no claim has nothing to release and an
// unconditional reclaim there would re-dispatch an issue whose PR is still
// open.
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

// Leave forgets num, both as in-flight and as claimed, which must happen the
// moment its Box finishes, or a later abort or decline would drag an
// already-finished issue back to Dispatchable.
func (g *Gate) Leave(num string) {
	g.mu.Lock()
	delete(g.inflight, num)
	delete(g.claimed, num)
	g.mu.Unlock()
}

// Watch starts the abort watcher goroutine -- the promptness seam that
// reclaims in-flight Boxes as soon as abort closes, rather than waiting for
// some other caller to call Allowed/Launch again. The one Settle call that
// joins it may be call-once or the first of several (see Settle's own doc);
// callers should defer Settle immediately after Watch so every return path,
// not just the happy one, tears the watcher down. A no-op when abort is nil:
// a nil channel would block the select forever, so there is nothing to
// watch. A second call is also a no-op: it would otherwise leave behind a
// watcher goroutine that Settle's own one-shot join could never reach.
func (g *Gate) Watch() {
	if g.abort == nil {
		return
	}
	g.watchStarted.Do(func() {
		g.watchDone = make(chan struct{})
		g.watchExited = make(chan struct{})
		go func() {
			defer close(g.watchExited)
			select {
			case <-g.abort:
				g.mu.Lock()
				g.observeAbort()
				g.mu.Unlock()
			case <-g.watchDone:
			}
		}()
	})
}

// Settle joins the watcher, waits out any reclaim still in progress, and
// takes the final non-racy latch reading. It is idempotent: only the first
// call joins the watcher, and every call (first or later) does the
// wait-out-aborting-plus-final-recheck below, so callers should defer Settle
// immediately after Watch and, on a path that also settles inline before
// returning, call Settle there too -- a repeat call is always safe.
func (g *Gate) Settle() {
	g.watchSettled.Do(func() {
		if g.watchDone != nil {
			close(g.watchDone)
			<-g.watchExited
		}
	})
	g.mu.Lock()
	// The watcher's own select can race a concurrent Allowed/Launch call that
	// notices abort first and is still mid-reclaim when Settle is called; wait
	// it out before the final re-check below, the same invariant as
	// continuous.go's `for outstanding > 0 || aborting { idle.Wait() }`.
	for g.aborting {
		g.idle.Wait()
	}
	// The watcher's select picks arbitrarily when abort and watchDone are both
	// ready, so a signal closing as the watcher is torn down could otherwise
	// go unseen. Reading both once more, now that the watcher has exited and
	// left signalled/aborted without a concurrent writer, settles it.
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

// reclaimOne runs terminate.Reclaim for a single issue, logging a failure to
// stderr rather than returning it -- the same best-effort discipline
// AbortInFlight uses per issue.
func reclaimOne(it forge.IssueTracker, cf forge.CodeForge, reaper terminate.Reaper, reg *terminate.Registry, num string) {
	if err := terminate.Reclaim(it, cf, reaper, reg, num); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: reclaim #%s: %v\n", num, err)
	}
}

// AbortInFlight announces the abort, then calls terminate.Reclaim for each
// num in sorted order, logging each failure to stderr rather than returning
// it. Exported so RunContinuous's own abort path runs the same loop (#3522).
// completeLabel is the caller's configured complete label, which unsettled
// reads; pass "" to leave that check off.
func AbortInFlight(it forge.IssueTracker, cf forge.CodeForge, reaper terminate.Reaper, reg *terminate.Registry, completeLabel string, nums []string) {
	nums = unsettled(it, completeLabel, nums)
	if len(nums) == 0 {
		fmt.Println("==> abort requested; nothing in flight")
		return
	}
	sorted := append([]string(nil), nums...)
	sort.Strings(sorted)
	fmt.Printf("==> abort requested; terminating %d outstanding Box(es)\n", len(sorted))
	for _, num := range sorted {
		reclaimOne(it, cf, reaper, reg, num)
	}
}

// unsettled drops the issues whose settle already wrote completeLabel. Both
// abort paths forget an issue only once its settler has returned, so an abort
// landing in the window between that settler's own terminal write and the
// forgetting still finds a finished issue in the in-flight snapshot -- and
// reclaiming one undoes a merge that already landed (#3522). Deciding it from
// the tracker is what closes the race: the write is the settler's, not the
// caller's, so the tracker holds the answer before the abort is even
// observable. It fails open, keeping an issue in the reap when the lookup
// errors or when the tracker does not carry dispatch state as a label at all
// (Jira's statuses), since a missed reap strands a live Box while a redundant
// one only repeats what Reclaim already does idempotently.
func unsettled(it forge.IssueTracker, completeLabel string, nums []string) []string {
	if completeLabel == "" {
		return nums
	}
	var live []string
	for _, num := range nums {
		if iss, err := it.Issue(num); err == nil && slices.Contains(iss.Labels, completeLabel) {
			continue
		}
		live = append(live, num)
	}
	return live
}
