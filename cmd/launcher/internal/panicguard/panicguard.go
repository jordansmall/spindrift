// Package panicguard restores the terminal when a spindrift goroutine
// panics. bubbletea recovers panics only on its own goroutines, so a panic
// elsewhere kills the process with the terminal still in alt-screen and the
// cursor hidden. Only goroutines started through Go are covered; goroutines
// owned by third-party code are not.
package panicguard

import "sync/atomic"

var restore atomic.Pointer[func()]

// SetRestore registers fn as the terminal-restore hook and returns a func
// that unsets it. The returned unset only removes the hook it registered, so
// a stale unset never wipes a newer registration.
func SetRestore(fn func()) (unset func()) {
	p := &fn
	restore.Store(p)
	return func() { restore.CompareAndSwap(p, nil) }
}

// Go runs fn on a new goroutine. If fn panics while a restore hook is set, Go
// runs the hook and re-panics with the same value, so the panic stays fatal
// and its trace prints after the terminal is back. With no hook set it
// behaves exactly like `go fn()`.
func Go(fn func()) {
	go func() {
		// The hook is read at panic time, not spawn time, and recover is only
		// called when one is set so the headless path is untouched.
		defer func() {
			if h := restore.Load(); h != nil {
				if r := recover(); r != nil {
					(*h)()
					panic(r)
				}
			}
		}()
		fn()
	}()
}
