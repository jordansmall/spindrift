// Package connectorsandbox is where the Launcher loads a Connector it hosts
// into the run's Connector sandbox.
//
// main installs one Loader for the run (Use), the sandbox package's. With none
// installed every load fails with ErrNoConnectorSandbox: the Launcher never
// loads a Connector in its own address space. The Box loads its first-party
// Drivers in-process through driverrows and never through this package. The
// package imports no runner, so the Box's binary does not link one for it.
package connectorsandbox

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
)

// ErrConnectorSandboxStart is why the run's Connector sandbox could not be
// started. It is final: no later load retries, and none falls back to loading
// in-process, which would hand the Connector the Launcher's own address space.
var ErrConnectorSandboxStart = errors.New("the Connector sandbox did not start")

// ErrNoConnectorSandbox is a load with no Loader installed. It is distinct from
// ErrConnectorSandboxStart, which is a sandbox that was installed and failed:
// here none was ever attempted, so the run's wiring, not the runner, is at
// fault.
var ErrNoConnectorSandbox = errors.New("no Connector sandbox is installed")

// Loader is the installed sandbox that every load goes to.
type Loader interface {
	Load(ctx context.Context, wasm []byte) (*connectorhost.Connector, error)
	LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error)
	// Err is the sandbox's start failure, or nil.
	Err() error
	// Stats is what the sandbox cost the run; false when none started or its
	// start failed.
	Stats() (Stats, bool)
}

// Stats is what a run's Connector sandbox cost: Start is the runner start plus
// the host's hello, and the embedded HopStats are the exchanges it carried.
type Stats struct {
	Start time.Duration
	connectorhost.HopStats
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

// Load loads wasm into the installed sandbox, or fails with
// ErrNoConnectorSandbox when none is.
func Load(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
	if l := installed(); l != nil {
		return l.Load(ctx, wasm)
	}
	return nil, ErrNoConnectorSandbox
}

// LoadNetwork loads wasm as a network Connector into the installed sandbox, or
// fails with ErrNoConnectorSandbox when none is.
func LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
	if l := installed(); l != nil {
		return l.LoadNetwork(ctx, wasm, n)
	}
	return nil, ErrNoConnectorSandbox
}

// Failure is the installed sandbox's start failure, which the run reports as
// its own whatever the load site did with the error.
func Failure() error {
	if l := installed(); l != nil {
		return l.Err()
	}
	return nil
}

// Report is the installed sandbox's Stats; false when none is installed, none
// started, or its start failed.
func Report() (Stats, bool) {
	if l := installed(); l != nil {
		return l.Stats()
	}
	return Stats{}, false
}
