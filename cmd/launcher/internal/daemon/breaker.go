package daemon

import (
	"slices"
	"time"
)

// breaker counts unclassified failures across the whole pool, not per slot:
// failures piling up across slots inside a short window signal a systemic
// fault (an expired token, a forge outage) no per-slot retry clears.
//
// A plain value (issue #3623): the pool holds it under p.mu, and
// recordAndCheck returns the updated value for the caller to store back.
type breaker struct {
	threshold int
	window    time.Duration

	times []time.Time
}

// newBreaker builds a breaker that trips once threshold failures have
// landed within window of each other.
func newBreaker(threshold int, window time.Duration) breaker {
	return breaker{threshold: threshold, window: window}
}

// recordAndCheck notes a failure at now, evicting failures older than window
// so the breaker never grows unbounded, and returns the updated breaker, the
// in-window count, and whether this call crossed threshold. The caller must
// store it back under the same p.mu hold (pool.backoffOrHalt): that makes
// the read-modify-write atomic, so exactly one caller sees the crossing and
// several slots failing at once report one breaker_trip, not one per slot.
func (b breaker) recordAndCheck(now time.Time) (breaker, int, bool) {
	b.times = b.evict(now)
	before := len(b.times)
	b.times = append(b.times, now)
	count := len(b.times)
	crossed := before < b.threshold && count >= b.threshold
	return b, count, crossed
}

// evict returns b.times with anything older than window dropped. It reuses
// b.times' backing array via times[:0] — safe only because the pool stores
// the result straight back under p.mu, the array's single owner; despite
// the value receiver, this writes through the shared array.
func (b breaker) evict(now time.Time) []time.Time {
	cutoff := now.Add(-b.window)
	kept := b.times[:0]
	for _, t := range b.times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

// restore seeds the breaker with the failure times a handoff carried, copied
// so the document does not share the array evict reuses. Only the newest
// threshold-1 are kept (the tail: snapshot leaves the slice in append
// order): recordAndCheck crosses only from below threshold, so a carried
// count at or above a successor's lowered threshold would never trip. Times
// outside the window fall away only at the next recordAndCheck, so a stale
// one may hold a kept slot; the in-window count stays below threshold
// either way.
func (b breaker) restore(times []time.Time) breaker {
	keep := b.threshold - 1
	if len(times) > keep {
		times = times[len(times)-keep:]
	}
	b.times = slices.Clone(times)
	return b
}

// snapshot is the inverse of restore: a copy of the failure times held, so the
// document does not alias the array evict reuses in place.
func (b breaker) snapshot() []time.Time {
	return slices.Clone(b.times)
}
