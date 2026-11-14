package connectorhost

import "context"

// engine runs a Connector's module; the Connector speaks the protocol over it.
// The in-process wazero runtime (wasmEngine) is the one engine today. Another,
// such as an out-of-process host, can stand in without the Connector's callers
// noticing, because a network Connector's Host-call policy (Routes, credentials,
// audit, the response cap) stays in the Launcher behind hostCalls.
//
// An engine is built already instantiated and initialised. Its methods are
// called with the Connector's mu held, except abort.
//
// A failure carries the same sentinels whatever the engine: ErrBudget when the
// call ran past its budget, ErrTrap for a trap or exit, ErrMemoryCap,
// ErrOutputTooLarge, ErrMalformedOutput (output the Launcher cannot take at
// face value, or a Host call with bad arguments), ErrExport, the fatal error a
// Host call failed closed with, and the ctx error when the caller's context cut
// the call off.
type engine interface {
	// handshake returns connector_handshake's result, checked to be UTF-8 JSON
	// within the output cap, for a call under limitsFor(0).
	handshake(ctx context.Context) ([]byte, error)
	// call sends req, an encoded request of at most maxRequest bytes, through
	// connector_alloc and connector_call under limitsFor(len(req)), and returns
	// the result as handshake does. A failure wrapping ErrExport means the module
	// lacks the call exports and is still usable; any other leaves it in an
	// unknown state, and the Connector closes the engine.
	call(ctx context.Context, req []byte) ([]byte, error)
	// abort kills the module so a call in flight fails. It is safe without the
	// Connector's mu and concurrently with a call.
	abort()
	// close releases everything the engine holds; a second close returns nil.
	close(ctx context.Context) error
}
