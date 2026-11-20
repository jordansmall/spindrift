// Package connectorsandboxtest is the in-process Connector engine a test binary
// installs as the Connector sandbox, so its tests load Connectors without
// starting a runner.
//
// Test binaries only: a production binary must never import it, as it is the
// in-process load the Launcher refuses to do.
package connectorsandboxtest

import (
	"context"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox"
)

type inProcess struct{}

func (inProcess) Load(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
	return connectorhost.Load(ctx, wasm)
}

func (inProcess) LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
	return connectorhost.LoadNetwork(ctx, wasm, n)
}

func (inProcess) Err() error { return nil }

func (inProcess) Stats() (connectorsandbox.Stats, bool) { return connectorsandbox.Stats{}, false }

// InProcess is the Loader that loads every Connector in the calling process.
func InProcess() connectorsandbox.Loader { return inProcess{} }
