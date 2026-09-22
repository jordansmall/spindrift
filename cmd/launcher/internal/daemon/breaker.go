package daemon

import (
	"time"
)

// breaker counts unclassified failures across the whole pool (not per
// slot): one slot backing off on its own is routine, but failures piling
// up across several slots inside a short window is the signature of a
// systemic fault (an expired token, a forge outage) that no per-slot retry
// clears.
//
// A plain value, not a mutex-guarded object (issue #3623): the pool holds
// its one breaker as a field under p.mu, so recordAndCheck reads the
// receiver and returns the updated value for the caller to store back,
// rather than mutating in place. That single p.mu hold is also what keeps
// the "exactly one caller sees the threshold crossing" property below
// intact — see recordAndCheck.
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

// recordAndCheck notes a failure at now, evicting any failures older than
// window (relative to now) as it goes — so a breaker that never trips also
// never grows unbounded over a long night — and returns the updated
// breaker alongside the resulting in-window count and whether this call is
// the one that crossed threshold. The caller must store the returned
// breaker back (under p.mu; see pool.backoffOrHalt): that single lock hold
// around the whole read-modify-write is what makes recording and checking
// atomic together, so the count advances by exactly one per call, and
// exactly one caller can ever see it step from under threshold to at or
// over. That is what keeps a concurrent crossing — several slots failing
// at once, the systemic fault this breaker exists for — reported as one
// breaker_trip rather than one per slot.
func (b breaker) recordAndCheck(now time.Time) (breaker, int, bool) {
	b.times = b.evict(now)
	before := len(b.times)
	b.times = append(b.times, now)
	count := len(b.times)
	crossed := before < b.threshold && count >= b.threshold
	return b, count, crossed
}

// evict returns b.times with anything older than window (relative to now)
// dropped. Reuses b.times' backing array in place via times[:0] — safe
// only because the pool stores the breaker value this returns straight
// back into its own state.b field under p.mu, the array's single owner; a
// value receiver otherwise reads as copy-safe, but this one call writes
// through the shared backing array.
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
