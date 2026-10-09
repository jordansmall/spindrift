package report

import (
	"sync/atomic"

	"spindrift.dev/launcher/internal/dispatchkey"
)

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
func Box(key dispatchkey.Key, phase, passLog, recordID string) {
	Default().Box(key, phase, passLog, recordID)
}

// Settled forwards to Default().Settled.
func Settled(key dispatchkey.Key, state, note, prURL, recordID string) {
	Default().Settled(key, state, note, prURL, recordID)
}

// NotDue forwards to Default().NotDue.
func NotDue(key dispatchkey.Key, next NextDue) {
	Default().NotDue(key, next)
}

// Model forwards to Default().Model.
func Model(key dispatchkey.Key, model, role string) {
	Default().Model(key, model, role)
}

// CIWait forwards to Default().CIWait.
func CIWait(key dispatchkey.Key, prURL, runURL string) {
	Default().CIWait(key, prURL, runURL)
}
