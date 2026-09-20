package daemon

import (
	"sync"
	"time"
)

// breaker counts unclassified failures across the whole pool (not per
// slot): one slot backing off on its own is routine, but failures piling
// up across several slots inside a short window is the signature of a
// systemic fault (an expired token, a forge outage) that no per-slot retry
// clears. Mutex-guarded: every slot records into the one breaker
// concurrently.
type breaker struct {
	threshold int
	window    time.Duration

	mu    sync.Mutex
	times []time.Time
}

// newBreaker builds a breaker that trips once threshold failures have
// landed within window of each other.
func newBreaker(threshold int, window time.Duration) *breaker {
	return &breaker{threshold: threshold, window: window}
}

// recordAndCheck notes a failure at now, evicting any failures older than
// window (relative to now) as it goes — so a breaker that never trips also
// never grows unbounded over a long night — and reports both the resulting
// in-window count alongside whether this call is the one that crossed
// threshold. Recording and checking share one lock hold rather than being
// separate calls: each call appends exactly one failure, so the count
// advances by exactly one per call, and exactly one caller can ever see it
// step from under threshold to at or over. That is what keeps a concurrent
// crossing — several slots failing at once, the systemic fault this breaker
// exists for — reported as one breaker_trip rather than one per slot.
func (b *breaker) recordAndCheck(now time.Time) (count int, crossed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.times = b.evictLocked(now)
	before := len(b.times)
	b.times = append(b.times, now)
	count = len(b.times)
	crossed = before < b.threshold && count >= b.threshold
	return count, crossed
}

// evictLocked returns b.times with anything older than window (relative to
// now) dropped. Caller must hold b.mu.
func (b *breaker) evictLocked(now time.Time) []time.Time {
	cutoff := now.Add(-b.window)
	kept := b.times[:0]
	for _, t := range b.times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}
