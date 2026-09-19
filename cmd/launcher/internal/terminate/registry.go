// Package terminate carries the signal an operator's Terminate gesture (ADR
// 0024, issue #649) sends to an in-flight Dispatch/Settle loop. Reclaim reaps,
// moves the tracker, and comments (issue #3519); Registry only stops a
// surviving goroutine from corrupting the issue's state afterwards.
package terminate

import "sync"

// Registry tracks, per issue number, which dispatch generation the operator
// has terminated this session. A nil *Registry is inert and Marked always
// reports false, so headless dispatch paths can pass nil through unchanged.
type Registry struct {
	mu sync.Mutex
	// gen keys marks by generation instead of a plain per-number bool (the
	// pre-#743 design): a re-pick's claim had to clear that bool so its own
	// settle was not abandoned, and the clear raced an old still-polling
	// settle goroutine's next checkpoint. Begin starts a new generation
	// without touching any earlier generation's mark, closing that race.
	gen map[string]uint64
	// dead records every generation ever marked, not only the most recent, so
	// a second Terminate does not forget an earlier generation's mark.
	dead map[string]map[uint64]bool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{gen: map[string]uint64{}, dead: map[string]map[uint64]bool{}}
}

// Begin starts a fresh generation for num at claim time and returns it. The
// caller must carry that value through to every Marked check it makes for num
// (waves.Issue.Generation), so the check reports whether this generation was
// terminated rather than whether some generation of num once was.
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

// Marked reports whether num was terminated at generation gen specifically,
// not whether some other generation of num was.
func (r *Registry) Marked(num string, gen uint64) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dead[num][gen]
}
