package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/bundle"
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/forge"
)

// loadRecorder is a Connector sandbox that records each wasm it is asked to
// load and refuses it with err.
type loadRecorder struct {
	connectorsandbox.Loader
	err    error
	loaded [][]byte
}

func (l *loadRecorder) Load(_ context.Context, wasm []byte) (*connectorhost.Connector, error) {
	l.loaded = append(l.loaded, wasm)
	return nil, l.err
}

// primaryRows is one primary row for opencode, a native Driver, whose module
// holds wasm.
func primaryRows(t *testing.T, wasm string) driverrows.Rows {
	t.Helper()
	module := filepath.Join(t.TempDir(), "opencode.wasm")
	if err := os.WriteFile(module, []byte(wasm), 0o600); err != nil {
		t.Fatal(err)
	}
	desc, ok := driver.NewSet().DescriptorFor("opencode")
	if !ok {
		t.Fatal("no generated Descriptor for opencode")
	}
	raw, err := json.Marshal([]driverrows.Row{{Descriptor: desc, Module: module, Primary: true}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := driverrows.Parse(raw, bundle.Drivers())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// A primary Connector that fails to load fails the run with its named error:
// there is no native fallback, and no Factory is built around a nil Driver. The
// Connector loads into the runtime's own sandbox.
func TestNewDispatchFactoryPrimaryLoadFailureFailsRun(t *testing.T) {
	errBoom := errors.New("boom")
	sb := &loadRecorder{Loader: connectorsandboxtest.InProcess(), err: errBoom}
	rt := connectorruntime.New(backendrows.NewSet(), nil, sb)
	rt.UseDriverRows(primaryRows(t, "primary-wasm"))
	f, err := newDispatchFactory(config{driver: "opencode", connectors: rt}, "", nil, nil, nil, nil, forge.Capabilities{})
	if f != nil || err == nil {
		t.Fatalf("newDispatchFactory = %v, %v, want a nil Factory and an error", f, err)
	}
	if !strings.Contains(err.Error(), `"opencode"`) || !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want it to name opencode and wrap boom", err)
	}
	if len(sb.loaded) != 1 || string(sb.loaded[0]) != "primary-wasm" {
		t.Errorf("runtime sandbox loads = %q, want the Driver's module once", sb.loaded)
	}
}

// The Launcher's Drivers are the runtime's: a runtime whose rows supply no
// claude Connector offers none.
func TestNewDispatchFactoryTakesItsDriverFromTheRuntime(t *testing.T) {
	rt := connectorruntime.New(backendrows.NewSet(), nil, connectorsandboxtest.InProcess())
	rt.UseDriverRows(primaryRows(t, "unused"))
	f, err := newDispatchFactory(config{driver: "claude", connectors: rt}, "", nil, nil, nil, nil, forge.Capabilities{})
	if f != nil || err == nil || !strings.Contains(err.Error(), "no Driver row supplies its Connector module") {
		t.Fatalf("newDispatchFactory = %v, %v, want the missing-Connector-row error", f, err)
	}
}
