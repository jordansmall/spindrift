// Package terminate is the shared, dependency-free seam an operator's
// Terminate gesture (ADR 0024, issue #649) uses to tell an in-flight
// Dispatch/Settle loop it has been reclaimed. Registry carries no other
// state — the reap, the tracker transition, and the comment are each done
// once, directly by the Terminate call itself; Registry only stops a
// surviving goroutine (still polling CI, mid fix pass, or retrying a merge)
// from later corrupting the issue's state or double-reporting.
package terminate

import "sync"

// Registry tracks issue numbers the operator has terminated this session. A
// nil *Registry is inert: every method is safe to call on it and Marked
// always reports false — every headless dispatch path constructs no
// Registry at all, so it can pass a nil one through unchanged.
type Registry struct {
	mu   sync.Mutex
	dead map[string]bool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{dead: map[string]bool{}}
}

// Mark records num as terminated.
func (r *Registry) Mark(num string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dead[num] = true
}

// Unmark clears num's terminated mark, if any — a fresh claim (re-pick) of a
// previously terminated issue starts a new Dispatch that settle must treat
// normally, not one still flagged abandoned from the prior run.
func (r *Registry) Unmark(num string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.dead, num)
}

// Marked reports whether num was terminated.
func (r *Registry) Marked(num string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dead[num]
}
