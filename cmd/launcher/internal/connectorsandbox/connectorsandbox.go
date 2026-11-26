// Package connectorsandbox is where the Launcher loads a Connector it hosts
// into a Connector sandbox of the run. connectorsetup.New builds the Connector
// runtime with the sandbox package's Loader: the runtime's own Load is the
// sandbox every Driver Connector shares, and an OwningLoader also starts one
// of its own for each credential-holding Connector. A runtime built with none
// fails every load with ErrNoConnectorSandbox, so the Launcher never loads a
// Connector in its own address space. The Box loads its first-party Drivers
// in-process through its InProcess runtime, never through this package, and
// this package imports no runner so the Box's binary does not link one.
package connectorsandbox

import (
	"context"
	"errors"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
)

// ErrConnectorSandboxStart is why the run's Connector sandbox could not be
// started or restarted. It fails that load and the run, though the next load
// tries again; no load falls back to loading in-process, which would hand the
// Connector the Launcher's own address space.
var ErrConnectorSandboxStart = errors.New("the Connector sandbox did not start")

// ErrNoConnectorSandbox is a load through a Connector runtime built with no
// sandbox: unlike ErrConnectorSandboxStart, the run's wiring, not the runner,
// is at fault. The refusal is a property of how the runtime is built, so it
// holds whatever else the process does.
var ErrNoConnectorSandbox = errors.New("the Connector runtime has no Connector sandbox")

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
