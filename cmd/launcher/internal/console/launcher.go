package console

import (
	"sync"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
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

	mu        sync.Mutex
	launching bool
	wg        sync.WaitGroup
	refresh   chan struct{}
	// pollInterval overrides Run's default background poll cadence — unset
	// (zero) in every production construction site, so only same-package
	// tests reach in to shrink it below defaultPollInterval.
	pollInterval time.Duration
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
		return l.Queue.Discover(tracker)
	}
	fresh := func() (bool, bool, string) { return false, true, "" }
	maxParallel := l.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 1
	}
	for {
		_ = waves.RunContinuous(waves.Config{MaxParallel: maxParallel}, tracker, l.CodeForge, pwd, l.Factory, queueSettler{l.Settle, l.Queue, l.signalRefresh}, discover, fresh)

		l.mu.Lock()
		if !l.Queue.hasQueued() {
			l.launching = false
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
	}
}

// Wait blocks until any in-flight background drain finishes — Run calls it
// before returning, so quitting the console never races the caller's
// cleanup (e.g. the driver-cache teardown) against a still-running Box.
func (l *Launcher) Wait() {
	l.wg.Wait()
}
