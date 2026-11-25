// Package connectorsandbox is where the Launcher loads a Connector it hosts
// into a Connector sandbox of the run. Each loading path installs the sandbox
// package's Loader (Use) before its first load: its own Load is the sandbox
// every Driver Connector shares, and an OwningLoader also starts one of its
// own for each credential-holding Connector. With none, every load fails with
// ErrNoConnectorSandbox, so the Launcher never loads a Connector in its own
// address space. The Box loads its first-party Drivers in-process via
// driverrows, never through this package, and this package imports no runner
// so the Box's binary does not link one.
package connectorsandbox

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
)

// ErrConnectorSandboxStart is why the run's Connector sandbox could not be
// started or restarted. It fails that load and the run, though the next load
// tries again; no load falls back to loading in-process, which would hand the
// Connector the Launcher's own address space.
var ErrConnectorSandboxStart = errors.New("the Connector sandbox did not start")

// ErrNoConnectorSandbox is a load with no Loader installed: unlike
// ErrConnectorSandboxStart, the run's wiring, not the runner, is at fault.
var ErrNoConnectorSandbox = errors.New("no Connector sandbox is installed")

// Loader is a sandbox that loads Connectors.
type Loader interface {
	Load(ctx context.Context, wasm []byte) (*connectorhost.Connector, error)
	LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error)
	// Err is the sandbox's latest start failure, kept after a later start
	// succeeds, or nil.
	Err() error
	// Stats is what the sandbox cost the run; false when none served.
	Stats() (Stats, bool)
}

// Sandbox is one sandbox its caller owns and closes.
type Sandbox interface {
	Loader
	Close(ctx context.Context) error
}

// OwningLoader is a Loader whose own Load is the sandbox every Driver Connector
// shares, and which starts a sandbox of its own for each credential-holding
// Connector.
type OwningLoader interface {
	Loader
	// Own is a new sandbox for the named Connector alone, started by its
	// first load; its Close stops it.
	Own(name string) Sandbox
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

// Current is the installed Loader, or nil when none is.
func Current() Loader {
	if h := current.Load(); h != nil {
		return h.l
	}
	return nil
}

// Installed reports whether a Loader is installed, so a caller that never
// installs one can say so before a load fails.
func Installed() bool { return Current() != nil }

// Load loads wasm into the installed sandbox, or fails with
// ErrNoConnectorSandbox when none is.
func Load(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
	if l := Current(); l != nil {
		return l.Load(ctx, wasm)
	}
	return nil, ErrNoConnectorSandbox
}

// LoadNetwork loads wasm as a network Connector into the installed sandbox, or
// fails with ErrNoConnectorSandbox when none is. An OwningLoader refuses it: a
// network load goes through an owned sandbox (Own).
func LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
	if l := Current(); l != nil {
		return l.LoadNetwork(ctx, wasm, n)
	}
	return nil, ErrNoConnectorSandbox
}

// Failure is the installed sandbox's start failure, which the run reports as
// its own whatever the load site did with the error.
func Failure() error {
	if l := Current(); l != nil {
		return l.Err()
	}
	return nil
}

// Report is the installed sandbox's Stats; false when none is installed or none
// served.
func Report() (Stats, bool) {
	if l := Current(); l != nil {
		return l.Stats()
	}
	return Stats{}, false
}
