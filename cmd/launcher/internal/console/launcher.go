package console

import (
	"sync"

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

	mu        sync.Mutex
	launching bool
	wg        sync.WaitGroup
}

// tryLaunch starts draining Queue through waves.RunContinuous, single slot
// (MaxParallel=1), in the background, unless a drain is already running —
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

	go func() {
		defer func() {
			l.mu.Lock()
			l.launching = false
			l.mu.Unlock()
			l.wg.Done()
		}()
		discover := func() ([]waves.Issue, map[string][]string, error) { return l.Queue.Discover(tracker) }
		fresh := func() (bool, bool, string) { return false, true, "" }
		_ = waves.RunContinuous(waves.Config{MaxParallel: 1}, tracker, l.CodeForge, pwd, l.Factory, queueSettler{l.Settle, l.Queue}, discover, fresh)
	}()
}

// Wait blocks until any in-flight background drain finishes — Run calls it
// before returning, so quitting the console never races the caller's
// cleanup (e.g. the driver-cache teardown) against a still-running Box.
func (l *Launcher) Wait() {
	l.wg.Wait()
}
