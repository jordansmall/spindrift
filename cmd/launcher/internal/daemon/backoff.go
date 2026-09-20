package daemon

import (
	"sync"
	"time"
)

// idleBackoff grows the pool's idle wait on each consecutive no-work check
// and resets it the moment any slot sees real work — the same pool-wide
// shape as breaker (see breaker.go:8-13): the quantity being bounded is the
// pool's aggregate rate-limit spend against the forge, and any slot
// observing work is evidence the world changed, so the streak resets for
// every slot at once rather than per slot.
type idleBackoff struct {
	floor time.Duration
	cap   time.Duration

	mu  sync.Mutex
	cur time.Duration
}

// newIdleBackoff builds an idleBackoff starting at floor, doubling toward
// cap on each next() call.
func newIdleBackoff(floor, cap time.Duration) *idleBackoff {
	return &idleBackoff{floor: floor, cap: cap, cur: floor}
}

// next records one no-work check and returns the wait for it: the current
// value, before it doubles. Doubling only while cur is still below cap, and
// clamping after, keeps cur from overshooting cap — not a general int64
// overflow guard.
func (b *idleBackoff) next() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	wait := b.cur
	if b.cur < b.cap {
		b.cur *= 2
		if b.cur > b.cap {
			b.cur = b.cap
		}
	}
	return wait
}

// reset returns the backoff to floor: a check produced real work, so the
// rate-limit pressure this backoff exists to relieve is gone.
func (b *idleBackoff) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cur = b.floor
}
