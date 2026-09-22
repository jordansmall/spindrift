package report

import "sync/atomic"

// def holds the process-wide default Reporter. A process-wide default
// rather than threaded config exists because there is exactly one report
// descriptor per process, and the call sites that need it span
// internal/dispatch, internal/settle, internal/waves and main.go — a
// parameter thread through all four would carry the same one value every
// time, for no benefit over a package-level default.
var def atomic.Pointer[Reporter]

// Install sets the process-wide default Reporter and returns a restore
// closure that puts the previous default back, for use with t.Cleanup.
func Install(r *Reporter) (restore func()) {
	prev := def.Swap(r)
	return func() { def.Store(prev) }
}

// Default returns the process-wide default Reporter, or nil if none was
// installed (nil is safe to call methods on).
func Default() *Reporter {
	return def.Load()
}

// Box forwards to Default().Box.
func Box(issue, phase string) {
	Default().Box(issue, phase)
}

// Settled forwards to Default().Settled.
func Settled(issue, state, note string) {
	Default().Settled(issue, state, note)
}
