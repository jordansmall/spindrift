// Package fakehook lets a test run a callback inside a named method of a fake,
// the way a signal or termination lands mid-call, without a one-off wrapper
// type per method. A fake embeds Hooks and calls Fire at the top of each method
// a test needs to hook. Firing before the method body rather than after it is
// equivalent for state the fake never reads (a settle-side termination mark),
// which is all these hooks are for.
package fakehook

import (
	"slices"
	"sync"
)

// Hooks counts calls per method name and runs the callbacks registered for it.
// The zero value is ready to use and safe for concurrent use.
type Hooks struct {
	mu    sync.Mutex
	calls map[string]int
	hooks map[string][]func(call int)
}

// On registers fn to run on every Fire(method) with that method's 1-based call
// number. Hooks for one method run in registration order.
func (h *Hooks) On(method string, fn func(call int)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hooks == nil {
		h.hooks = map[string][]func(int){}
	}
	h.hooks[method] = append(h.hooks[method], fn)
}

// Fire counts one call of method and runs its hooks. Call it before taking the
// fake's own lock: hooks run outside every lock here, so one may call back into
// the fake.
func (h *Hooks) Fire(method string) {
	h.mu.Lock()
	if h.calls == nil {
		h.calls = map[string]int{}
	}
	h.calls[method]++
	n := h.calls[method]
	fns := slices.Clone(h.hooks[method])
	h.mu.Unlock()
	for _, fn := range fns {
		fn(n)
	}
}
