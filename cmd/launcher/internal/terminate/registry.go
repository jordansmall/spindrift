// Package terminate is the shared, dependency-free seam an operator's
// Terminate gesture (ADR 0024, issue #649) uses to tell an in-flight
// Dispatch/Settle loop it has been reclaimed. Registry carries no other
// state — the reap, the tracker transition, and the comment are each done
// once, directly by the Terminate call itself; Registry only stops a
// surviving goroutine (still polling CI, mid fix pass, or retrying a merge)
// from later corrupting the issue's state or double-reporting.
package terminate

import "sync"

// Registry tracks, per issue number, which dispatch generation (if any) the
// operator has terminated this session. A nil *Registry is inert: every
// method is safe to call on it and Marked always reports false — every
// headless dispatch path constructs no Registry at all, so it can pass a nil
// one through unchanged.
//
// A plain per-number bool (the pre-#743 design) cannot tell "my own stale
// mark from a prior incarnation" apart from "a still-live settle goroutine
// hasn't checked yet": a re-pick's claim must clear the mark so its own
// fresh settle isn't immediately abandoned, but that same clear can race an
// old, still-polling settle goroutine's next checkpoint and erase the mark
// out from under it before it ever sees the value. Keying on a generation
// counter per number closes that race: Begin starts a new generation
// without touching any earlier generation's mark, so an old goroutine
// holding the generation it was launched under (from waves.Issue.Generation)
// keeps seeing itself as terminated regardless of how many later
// generations have started and cleared their own state since.
//
// dead records every generation ever marked, not just the most recent —
// a second Terminate (the re-pick itself gets terminated too) must not
// forget an earlier generation's own mark, however unlikely a still-live
// goroutine from that earlier incarnation is to still be around.
type Registry struct {
	mu   sync.Mutex
	gen  map[string]uint64
	dead map[string]map[uint64]bool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{gen: map[string]uint64{}, dead: map[string]map[uint64]bool{}}
}

// Begin starts a fresh generation for num — called once, at claim time, for
// every freshly dispatched issue (including a re-pick of a previously
// terminated one) — and returns it. The returned value is the identity the
// caller's dispatch must carry through to every Marked check it makes for
// num (waves.Issue.Generation), so that check reports whether *this*
// generation was terminated, not merely whether *some* generation of num
// once was.
func (r *Registry) Begin(num string) uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen[num]++
	return r.gen[num]
}

// Mark records num's current generation as terminated.
func (r *Registry) Mark(num string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead[num] == nil {
		r.dead[num] = map[uint64]bool{}
	}
	r.dead[num][r.gen[num]] = true
}

// Marked reports whether num was terminated at generation gen specifically —
// not whether some other (earlier or later) generation of num was.
func (r *Registry) Marked(num string, gen uint64) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dead[num][gen]
}
