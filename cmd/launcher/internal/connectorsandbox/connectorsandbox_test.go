package connectorsandbox

import (
	"context"
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

type failingLoader struct{ err error }

func (f failingLoader) Load(context.Context, []byte) (*connectorhost.Connector, error) {
	return nil, f.err
}

func (f failingLoader) LoadNetwork(context.Context, []byte, connectorhost.Network) (*connectorhost.Connector, error) {
	return nil, f.err
}

func (f failingLoader) Err() error { return f.err }

// notWasm fails to compile in-process with an error that is not the loader's,
// so a load that falls back shows.
var notWasm = []byte("not a wasm module")

func TestUseRoutesLoadsToTheInstalledLoader(t *testing.T) {
	if Failure() != nil {
		t.Fatal("Failure with no loader installed")
	}
	restore := Use(failingLoader{ErrConnectorSandboxStart})
	t.Cleanup(restore)
	if _, err := Load(context.Background(), notWasm); !errors.Is(err, ErrConnectorSandboxStart) {
		t.Fatalf("Load = %v, want the installed loader's error", err)
	}
	if _, err := LoadNetwork(context.Background(), notWasm, connectorhost.Network{}); !errors.Is(err, ErrConnectorSandboxStart) {
		t.Fatalf("LoadNetwork = %v, want the installed loader's error", err)
	}
	if !errors.Is(Failure(), ErrConnectorSandboxStart) {
		t.Fatalf("Failure = %v", Failure())
	}
	restore()
	if _, err := Load(context.Background(), notWasm); err == nil || errors.Is(err, ErrConnectorSandboxStart) {
		t.Fatalf("Load after restore = %v, want an in-process compile error", err)
	}
	if Failure() != nil {
		t.Fatal("Failure after restore")
	}
}
