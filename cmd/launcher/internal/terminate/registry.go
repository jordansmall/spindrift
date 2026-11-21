// Package terminate carries the signal that ends a claimed Dispatch/Settle
// loop by hand (ADR 0024, issue #649). Two triggers send it: the launcher's
// stage-two signalled abort on a second SIGTERM/SIGINT, or the shutdown gate
// declining, once a stop or abort has arrived, to launch an issue the
// launcher claimed but never launched. Reclaim reaps any running Box, moves
// the tracker, and comments (issue #3519); Registry only stops a surviving
// goroutine from corrupting the issue's state afterwards.
package terminate

import "sync"

// Registry tracks, per issue number, which dispatch generation the launcher
// has terminated this session. A nil *Registry is safe to call (Begin returns
// 0, Mark is a no-op, Marked reports false); only tests reach it. On a
// headless path, where no Begin bumped the generation, Reclaim marks
// generation 0.
type Registry struct {
	mu sync.Mutex
	// gen keys marks by generation rather than a per-number bool (#743): a
	// re-pick's claim would have to clear that bool so its own settle was not
	// abandoned, racing an old still-polling settle goroutine's next
	// checkpoint. Begin starts a new generation without touching any earlier
	// generation's mark.
	gen map[string]uint64
	// dead records every generation ever marked, so a later Mark does not
	// forget an earlier generation's.
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
