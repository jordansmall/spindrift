package waves

import "sync"

// Limiter is a resizable concurrency bound (issue #653): a mutex-guarded
// cap/live pair, replacing the fixed per-invocation semaphore dispatchWave
// and RunContinuous each used to build fresh from cfg.MaxParallel.
// Headless callers build one and never call ResizeDelta, keeping their
// fixed-cap behaviour unchanged; the Console holds one persistent Limiter
// across a session and calls ResizeDelta as the operator raises or lowers
// the live cap (ADR 0023).
type Limiter struct {
	mu   sync.Mutex
	cond *sync.Cond
	cap  int
	live int
	// resized is signaled (coalesced, buffered 1) every time ResizeDelta
	// actually changes the cap, in either direction. RunContinuous's
	// drain-checkpoint listener (continuous.go) is the one consumer that
	// needs a lower too: a Console operator can drop the live cap
	// mid-drain, and the free-slot-seconds accounting must close out the
	// interval at the OLD cap before the new, lower one takes effect --
	// the same retroactive-misattribution risk a raise has, just in the
	// other direction (#2678 review finding).
	resized chan struct{}
}

// NewLimiter returns a Limiter bounded at cap, clamped to at least 1.
func NewLimiter(cap int) *Limiter {
	if cap < 1 {
		cap = 1
	}
	l := &Limiter{cap: cap, resized: make(chan struct{}, 1)}
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

// ResizeDelta adjusts the cap by delta relative to its current value,
// clamped to at least 1, as a single lock-guarded read-modify-write —
// unlike a separate read of Cap() followed by a write, which would read and
// write under separate lock acquisitions and leave a window for a
// concurrent resize to land in between. Any actual cap change wakes
// Resized's listener and any goroutine blocked in Acquire, so a held pick
// can retry into freed capacity right away; lowering it only changes what
// future TryAcquire calls see — slots already claimed are never revoked,
// matching ADR 0023's "lowering never terminates anything."
func (l *Limiter) ResizeDelta(delta int) {
	l.mu.Lock()
	oldCap := l.cap
	newCap := l.cap + delta
	if newCap < 1 {
		newCap = 1
	}
	l.cap = newCap
	l.mu.Unlock()
	if newCap != oldCap {
		l.signalResized()
	}
}

// signalResized wakes Resized's listener and any Acquire waiters on any
// actual cap change, either direction. Must be called after releasing l.mu,
// never while holding it.
func (l *Limiter) signalResized() {
	l.cond.Broadcast()
	select {
	case l.resized <- struct{}{}:
	default:
	}
}

// Resized signals (coalesced, buffered 1) every time ResizeDelta actually
// changes the cap, in either direction. A received signal means only "at
// least one resize happened," never "exactly one," and never which
// direction: a burst of rapid resizes can coalesce into a single delivered
// signal even though Cap()/Live() already reflect every one of them. A
// caller must drain until a receive would block (or otherwise re-check
// Cap()/Live()) rather than treat one signal as license for one refill. See
// RunContinuous's drain-checkpoint listener (continuous.go) for the pattern
// this contract requires.
func (l *Limiter) Resized() <-chan struct{} {
	return l.resized
}
