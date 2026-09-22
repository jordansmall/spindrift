package daemon

import (
	"time"
)

// idleBackoff grows an idle wait on each consecutive no-work check and
// resets it the moment any check sees real work. It was pool-wide (the same
// shape as breaker, see breaker.go:8-13) before issue #3541 split the pool
// across two Dispatch kinds; now one idleBackoff is embedded per kind, inside
// kindBackoff below, so the quantity each embedding bounds is that one kind's
// own rate-limit spend against the forge, and a slot observing work resets
// only the kind it was working, not its sibling's.
//
// A plain value, not a mutex-guarded object (issue #3623): the pool holds
// its per-kind idleBackoffs (via kindBackoff, below) as map values under
// p.mu, so next()/reset() read the receiver and return the updated value
// for the caller to store back, rather than mutating in place.
type idleBackoff struct {
	floor time.Duration
	cap   time.Duration

	cur time.Duration
}

// newIdleBackoff builds an idleBackoff starting at floor, doubling toward
// cap on each next() call.
func newIdleBackoff(floor, cap time.Duration) idleBackoff {
	return idleBackoff{floor: floor, cap: cap, cur: floor}
}

// next records one no-work check and returns the updated backoff alongside
// the wait for this check: the current value, before it doubles. Doubling
// only while cur is still below cap, and clamping after, keeps cur from
// overshooting cap — not a general int64 overflow guard.
func (b idleBackoff) next() (idleBackoff, time.Duration) {
	wait := b.cur
	if b.cur < b.cap {
		b.cur *= 2
		if b.cur > b.cap {
			b.cur = b.cap
		}
	}
	return b, wait
}

// reset returns the backoff at floor: a check produced real work, so the
// rate-limit pressure this backoff exists to relieve is gone.
func (b idleBackoff) reset() idleBackoff {
	b.cur = b.floor
	return b
}

// kindBackoff is one Dispatch kind's own no-work state: the idle backoff it
// grows across consecutive empty results, plus the instant that kind may next
// be tried. Issue #3541 splits idleBackoff (pool-wide) into one of these per
// kind because the two kinds no longer share a queue: an empty work queue
// does not slow research down (and vice versa), so each kind's streak — and
// the growing wait it earns — has to live and reset independently of the
// other's.
//
// A plain value (issue #3623): the pool holds its kinds map as
// map[Kind]kindBackoff under p.mu, so every method here reads the receiver
// and returns the updated value for the caller to store back into the map,
// rather than mutating a shared object in place.
type kindBackoff struct {
	b      idleBackoff
	until  time.Time
	jammed bool
}

// newKindBackoff builds a kindBackoff whose underlying idleBackoff starts at
// floor, doubling toward cap on each no-work result, same as idleBackoff.
func newKindBackoff(floor, cap time.Duration) kindBackoff {
	return kindBackoff{b: newIdleBackoff(floor, cap)}
}

// markNoWork records one no-work result for this kind and gates it until the
// wait it returns has elapsed, returning the updated kindBackoff alongside
// that wait. jammed records whether the gating result was "none
// dispatchable" (exit 3) rather than "queue empty" (exit 2): only a jam is
// worth polling a moved tip for.
//
// Deliberately exit-3-alone: sibling occupancy plays no part. jammed doubles
// as idleSleep's tip-poll gate (jammedGate, pool.go) as well as status data,
// and a sibling that actually drains the queue clears it through reset() on
// its own next Continue — so occupancy is the jam *event*'s narrower
// predicate (state.siblingsEngaged, reached from noteWaitResult, an
// operator alarm), not this flag's.
func (k kindBackoff) markNoWork(now time.Time, jammed bool) (kindBackoff, time.Duration) {
	b, wait := k.b.next()
	k.b = b
	k.until = now.Add(wait)
	k.jammed = jammed
	return k, wait
}

// runnable reports whether this kind may be tried at now, shared with
// readyAt so the two can never drift apart on the deadline boundary.
func (k kindBackoff) runnable(now time.Time) bool {
	// !Before, not After: a fake clock that advances by exactly the slept
	// duration must land on the deadline as runnable, not one tick short.
	return k.until.IsZero() || !now.Before(k.until)
}

// readyAt returns the instant this kind may next be tried, and whether it is
// gated at now — false once a deadline that was set has already elapsed,
// matching runnable's own boundary exactly (see runnable). A caller must
// never publish an until from a gated=false result: it may be a stale
// deadline from a since-elapsed gate, not a live one.
func (k kindBackoff) readyAt(now time.Time) (time.Time, bool) {
	return k.until, !k.runnable(now)
}

// jammedNow reports whether this kind is currently gated by a
// none-dispatchable result.
func (k kindBackoff) jammedNow() bool {
	return k.jammed
}

// reset clears the gate and returns the updated kindBackoff at its floor:
// this kind produced real work (or the tip moved under a jam), so the
// streak is over.
func (k kindBackoff) reset() kindBackoff {
	k.b = k.b.reset()
	k.until = time.Time{}
	k.jammed = false
	return k
}
