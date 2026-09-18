package waves

import "sync"

// Limiter is a resizable concurrency bound (issue #653). Headless callers
// never call ResizeDelta and so keep a fixed cap; the Console holds one
// Limiter across a session and resizes it as the operator raises or lowers
// the live cap (ADR 0023).
type Limiter struct {
	mu   sync.Mutex
	cond *sync.Cond
	cap  int
	live int
	// resized is signaled on any cap change, in either direction.
	// RunContinuous's drain checkpoint needs lowers too: its
	// free-slot-seconds accounting must close the interval at the old cap
	// before the lower one takes effect, or it misattributes the interval
	// retroactively (#2678 review finding).
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

// Acquire blocks until a slot is free, then claims it.
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

// Release frees one slot claimed by a prior Acquire or TryAcquire.
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

// ResizeDelta adjusts the cap by delta, clamped to at least 1, as one
// lock-guarded read-modify-write; reading Cap() and then writing would take
// the lock twice and let a concurrent resize land in between. Lowering the
// cap only changes what future TryAcquire calls see. It never revokes a
// claimed slot (ADR 0023).
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

// signalResized wakes Resized's listener and any Acquire waiters. Call it
// after releasing l.mu, never while holding it.
func (l *Limiter) signalResized() {
	l.cond.Broadcast()
	select {
	case l.resized <- struct{}{}:
	default:
	}
}

// Resized returns a channel signaled (coalesced, buffered 1) on any cap
// change. One signal means at least one resize happened, never exactly one
// and never which direction, so a caller must drain until a receive would
// block, or re-check Cap() and Live(), instead of refilling once per signal.
func (l *Limiter) Resized() <-chan struct{} {
	return l.resized
}
