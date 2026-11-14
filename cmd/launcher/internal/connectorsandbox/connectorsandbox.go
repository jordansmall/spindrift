// Package connectorsandbox is where the Launcher decides whether a Connector it
// hosts loads in its own process or into the run's Connector sandbox.
//
// With CONNECTOR_SANDBOX on, main installs one Loader for the run (Use), the
// sandbox package's. With none installed the loads are the in-process
// connectorhost ones, which is all the Box ever does: it never starts a
// sandbox. The package imports no runner, so the Box's binary does not link one
// for it.
package connectorsandbox

import (
	"context"
	"errors"
	"sync/atomic"

	"spindrift.dev/launcher/internal/connectorhost"
)

// ErrConnectorSandboxStart is why the run's Connector sandbox could not be
// started. It is final: no later load retries, and none falls back to loading
// in-process, which would hand the Connector the Launcher's own address space.
var ErrConnectorSandboxStart = errors.New("the Connector sandbox did not start")

// Loader is the installed sandbox: loads go to it instead of in-process.
type Loader interface {
	Load(ctx context.Context, wasm []byte) (*connectorhost.Connector, error)
	LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error)
	// Err is the sandbox's start failure, or nil.
	Err() error
}

// holder lets the atomic hold a nil Loader.
type holder struct{ l Loader }

var current atomic.Pointer[holder]

// Use installs l as where every Load and LoadNetwork go, and returns the
// function that puts back what was installed before; nil installs none. Called
// once per run, before any load.
func Use(l Loader) (restore func()) {
	prev := current.Swap(&holder{l})
	return func() { current.Store(prev) }
}

func installed() Loader {
	if h := current.Load(); h != nil {
		return h.l
	}
	return nil
}

// Load loads wasm into the installed sandbox, or in-process when none is.
func Load(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
	if l := installed(); l != nil {
		return l.Load(ctx, wasm)
	}
	return connectorhost.Load(ctx, wasm)
}

// LoadNetwork loads wasm as a network Connector into the installed sandbox, or
// in-process when none is.
func LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
	if l := installed(); l != nil {
		return l.LoadNetwork(ctx, wasm, n)
	}
	return connectorhost.LoadNetwork(ctx, wasm, n)
}

// Failure is the installed sandbox's start failure, which the run reports as
// its own whatever the load site did with the error.
func Failure() error {
	if l := installed(); l != nil {
		return l.Err()
	}
	return nil
}
