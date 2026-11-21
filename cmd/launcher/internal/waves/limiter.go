package waves

import "sync"

// Limiter is a fixed concurrency bound.
type Limiter struct {
	mu   sync.Mutex
	cond *sync.Cond
	cap  int
	live int
}

// NewLimiter returns a Limiter bounded at cap, clamped to at least 1.
func NewLimiter(cap int) *Limiter {
	if cap < 1 {
		cap = 1
	}
	l := &Limiter{cap: cap}
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

// Release frees one slot claimed by a prior Acquire or TryAcquire. Every
// Release must match exactly one successful claim; an unmatched one panics.
// Clamping it silently would permanently widen the concurrency bound with no
// visible symptom. The panic crashes the launcher on purpose: an unbalanced
// release means the bound is already wrong.
func (l *Limiter) Release() {
	l.mu.Lock()
	if l.live == 0 {
		// Unlock first so a recovered panic leaves the Limiter usable.
		l.mu.Unlock()
		panic("waves: Limiter.Release with no slot held; release is not matched by an Acquire/TryAcquire")
	}
	l.live--
	l.mu.Unlock()
	l.cond.Broadcast()
}

// Live returns the current number of claimed slots.
func (l *Limiter) Live() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live
}

// Cap returns the cap.
func (l *Limiter) Cap() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cap
}
