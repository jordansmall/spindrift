package connectorforgetest

import (
	"context"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/fixturebuild"
)

// Engine loads a fixture Connector: the one choice point between running it in
// this process and in a child connector-host.
type Engine func(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error)

func inProcess(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
	return connectorhost.LoadNetwork(ctx, wasm, n)
}

// remoteEngine starts a fresh host child per load, since an exec.Cmd runs once
// and a harness may load a second Connector. The host's stderr goes to the test
// log.
func remoteEngine(t *testing.T) Engine {
	return func(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
		cmd := fixturebuild.ConnectorHostCmd(t)
		return connectorhost.LoadNetworkRemote(ctx, wasm, n, cmd)
	}
}

// NewVia is New with the Connector loaded through eng.
func NewVia(t *testing.T, eng Engine) *Harness {
	t.Helper()
	return &Harness{newBase(t, eng, "forge.wasm", pushOnlyMethods)}
}

// NewPRVia is NewPR with the Connector loaded through eng.
func NewPRVia(t *testing.T, eng Engine) *PRHarness {
	t.Helper()
	return &PRHarness{newBase(t, eng, PRWasm, prMethods)}
}

// NewRemote is New with the Connector running in a child connector-host.
func NewRemote(t *testing.T) *Harness {
	t.Helper()
	return NewVia(t, remoteEngine(t))
}

// NewPRRemote is NewPR with the Connector running in a child connector-host.
func NewPRRemote(t *testing.T) *PRHarness {
	t.Helper()
	return NewPRVia(t, remoteEngine(t))
}
