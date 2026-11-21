package daemon

import (
	"time"
)

// idleBackoff grows an idle wait on each consecutive no-work check and
// resets it the moment any check sees real work. One is embedded per kind
// (kindBackoff, issue #3541), bounding that kind's own rate-limit spend
// against the forge.
//
// A plain value (issue #3623): the pool holds it under p.mu, so next() and
// reset() return the updated value for the caller to store back.
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
// the wait for this check: the current value, before it doubles. The clamp
// only keeps cur from overshooting cap; it is not an int64 overflow guard.
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
// be tried. It is per kind because an empty work queue must not slow research
// down (issue #3541).
//
// A plain value (issue #3623): the Schedule holds one per kind under p.mu,
// and every method returns the updated value for the caller to store back.
type kindBackoff struct {
	b      idleBackoff
	until  time.Time
	jammed bool
	// gen counts the gates markNoWork has set. Never reset: a child that
	// started under an earlier gen is told apart from one started under the
	// current gate, even across a reset in between.
	gen int
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
// Deliberately exit-3-alone: sibling occupancy plays no part. jammed is also
// idleSleep's tip-poll gate (park.TipPoll), and a sibling that drains the
// queue clears it through reset(); occupancy belongs only to the jam
// *event*'s narrower predicate (state.siblingsEngaged).
func (k kindBackoff) markNoWork(now time.Time, jammed bool) (kindBackoff, time.Duration) {
	b, wait := k.b.next()
	k.b = b
	k.until = now.Add(wait)
	k.jammed = jammed
	k.gen++
	return k, wait
}

// markNoWorkUnder records a no-work result from a child that started when the
// kind's gate generation was gen (issue #4618). If a sibling's result has
// gated the kind since then and nothing has reset it, this result is the same
// observation as that sibling's — one burst of concurrent children polling one
// queue — so it neither doubles the backoff nor moves the deadline; it only
// refreshes jammed, last-wins as markNoWork does. A result arriving once the
// gate has lapsed is a fresh observation and marks normally: absorbing it
// would leave the kind runnable at once, or jammed with no live gate.
func (k kindBackoff) markNoWorkUnder(now time.Time, jammed bool, gen int) kindBackoff {
	if gen != k.gen && !k.runnable(now) {
		k.jammed = jammed
		return k
	}
	k, _ = k.markNoWork(now, jammed)
	return k
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
