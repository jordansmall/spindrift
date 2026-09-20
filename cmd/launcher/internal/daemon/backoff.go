package daemon

import (
	"sync"
	"time"
)

// idleBackoff grows an idle wait on each consecutive no-work check and
// resets it the moment any check sees real work. It was pool-wide (the same
// shape as breaker, see breaker.go:8-13) before issue #3541 split the pool
// across two Dispatch kinds; now one idleBackoff is embedded per kind, inside
// kindBackoff below, so the quantity each embedding bounds is that one kind's
// own rate-limit spend against the forge, and a slot observing work resets
// only the kind it was working, not its sibling's.
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

// kindBackoff is one Dispatch kind's own no-work state: the idle backoff it
// grows across consecutive empty results, plus the instant that kind may next
// be tried. Issue #3541 splits idleBackoff (pool-wide) into one of these per
// kind because the two kinds no longer share a queue: an empty work queue
// does not slow research down (and vice versa), so each kind's streak — and
// the growing wait it earns — has to live and reset independently of the
// other's.
type kindBackoff struct {
	mu     sync.Mutex
	b      *idleBackoff
	until  time.Time
	jammed bool
}

// newKindBackoff builds a kindBackoff whose underlying idleBackoff starts at
// floor, doubling toward cap on each no-work result, same as idleBackoff.
func newKindBackoff(floor, cap time.Duration) *kindBackoff {
	return &kindBackoff{b: newIdleBackoff(floor, cap)}
}

// markNoWork records one no-work result for this kind and gates it until the
// wait it returns has elapsed. jammed records whether the gating result was
// "none dispatchable" (exit 3) rather than "queue empty" (exit 2): only a jam
// is worth polling a moved tip for.
func (k *kindBackoff) markNoWork(now time.Time, jammed bool) time.Duration {
	k.mu.Lock()
	defer k.mu.Unlock()
	wait := k.b.next()
	k.until = now.Add(wait)
	k.jammed = jammed
	return wait
}

// runnable reports whether this kind may be tried at now.
func (k *kindBackoff) runnable(now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	// !Before, not After: a fake clock that advances by exactly the slept
	// duration must land on the deadline as runnable, not one tick short.
	return k.until.IsZero() || !now.Before(k.until)
}

// readyAt returns the instant this kind may next be tried, and whether it is
// currently gated at all.
func (k *kindBackoff) readyAt() (time.Time, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.until, !k.until.IsZero()
}

// jammedNow reports whether this kind is currently gated by a
// none-dispatchable result.
func (k *kindBackoff) jammedNow() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.jammed
}

// reset clears the gate and returns the backoff to its floor: this kind
// produced real work (or the tip moved under a jam), so the streak is over.
func (k *kindBackoff) reset() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.b.reset()
	k.until = time.Time{}
	k.jammed = false
}
