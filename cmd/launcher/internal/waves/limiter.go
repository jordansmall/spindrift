package waves

import "sync"

// Limiter is a resizable concurrency bound (issue #653): a mutex-guarded
// cap/live pair, replacing the fixed per-invocation semaphore dispatchWave
// and RunContinuous each used to build fresh from cfg.MaxParallel.
// Headless callers build one and never call Resize, keeping their fixed-cap
// behaviour unchanged; the Console holds one persistent Limiter across a
// session and calls Resize as the operator raises or lowers the live cap
// (ADR 0023).
type Limiter struct {
	mu   sync.Mutex
	cond *sync.Cond
	cap  int
	live int
	// grow is signaled (coalesced, buffered 1) every time Resize raises the
	// cap, so a listener blocked waiting for capacity can retry right away
	// instead of waiting for an unrelated Release.
	grow chan struct{}
}

// NewLimiter returns a Limiter bounded at cap, clamped to at least 1.
func NewLimiter(cap int) *Limiter {
	if cap < 1 {
		cap = 1
	}
	l := &Limiter{cap: cap, grow: make(chan struct{}, 1)}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// Acquire blocks until a slot is free, then claims it — the drop-in
// replacement for dispatchWave's buffered-channel semaphore, which also
// blocked the goroutine until a slot freed.
func (l *Limiter) Acquire() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.live >= l.cap {
		l.cond.Wait()
	}
	l.live++
}

// TryAcquire claims one slot and reports success, or reports false without
// side effects when live already meets cap.
func (l *Limiter) TryAcquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.live >= l.cap {
		return false
	}
	l.live++
	return true
}

// Release frees one slot claimed by a prior successful TryAcquire (or
// Acquire).
func (l *Limiter) Release() {
	l.mu.Lock()
	if l.live > 0 {
		l.live--
	}
	l.mu.Unlock()
	l.cond.Broadcast()
}

// Live returns the current number of claimed slots.
func (l *Limiter) Live() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live
}

// Cap returns the current cap.
func (l *Limiter) Cap() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cap
}

// Resize changes the cap, clamped to at least 1. Raising it wakes Grown's
// listener so a held pick can launch into the freed capacity right away;
// lowering it only changes what future TryAcquire calls see — slots already
// claimed are never revoked, matching ADR 0023's "lowering never terminates
// anything."
func (l *Limiter) Resize(newCap int) {
	if newCap < 1 {
		newCap = 1
	}
	l.mu.Lock()
	grew := newCap > l.cap
	l.cap = newCap
	l.mu.Unlock()
	if grew {
		l.cond.Broadcast()
		select {
		case l.grow <- struct{}{}:
		default:
		}
	}
}

// ResizeDelta adjusts the cap by delta relative to its current value,
// clamped to at least 1, as a single lock-guarded read-modify-write —
// unlike calling Cap() then Resize(), which reads and writes under separate
// lock acquisitions and leaves a window for a concurrent Resize to land in
// between. Signals Grown on a raise just like Resize.
func (l *Limiter) ResizeDelta(delta int) {
	l.mu.Lock()
	newCap := l.cap + delta
	if newCap < 1 {
		newCap = 1
	}
	grew := newCap > l.cap
	l.cap = newCap
	l.mu.Unlock()
	if grew {
		l.cond.Broadcast()
		select {
		case l.grow <- struct{}{}:
		default:
		}
	}
}

// Grown signals (coalesced) every time Resize raises the cap.
func (l *Limiter) Grown() <-chan struct{} {
	return l.grow
}
