package driverrows_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/driver/drivertest"
	"spindrift.dev/launcher/internal/golden"
)

const (
	connectorWasmEnv = "SPINDRIFT_DRIVER_CONNECTOR_WASM"
	// connectorNameEnv names the Driver the wasm implements; lib/connector-checks.nix
	// sets it alongside connectorWasmEnv.
	connectorNameEnv = "SPINDRIFT_DRIVER_CONNECTOR_NAME"
)

// A primary row makes the Connector the Driver on both paths that apply rows,
// the Launcher's (Use) and the Box's (UseFile), and the Driver New hands out
// passes the same corpus against the same goldens as the native Strategy.
func TestPrimaryRowConnectorPassesCorpus(t *testing.T) {
	wasm := os.Getenv(connectorWasmEnv)
	if wasm == "" {
		t.Skipf("%s unset: no compiled Driver Connector to test; its connector-<name>-conformance check runs it", connectorWasmEnv)
	}
	if golden.Update() {
		t.Fatalf("refusing to update goldens from the Connector; unset UPDATE_GOLDENS or %s", connectorWasmEnv)
	}
	name := os.Getenv(connectorNameEnv)
	if name == "" {
		t.Fatalf("%s must be set with %s", connectorNameEnv, connectorWasmEnv)
	}
	if name == "claude" {
		t.Skip("claude has no native Strategy to replace; TestCorpus runs its Connector")
	}
	desc, ok := driver.DescriptorFor(name)
	if !ok {
		t.Fatalf("%s has no Descriptor", name)
	}
	raw := rows(t, []driverrows.Row{{Descriptor: desc, Module: wasm, Primary: true, FirstParty: true}})
	rowsFile := filepath.Join(t.TempDir(), "rows.json")
	if err := os.WriteFile(rowsFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for path, apply := range map[string]func() error{
		"host": func() error { return driverrows.Use(raw) },
		"box":  func() error { return driverrows.UseFile(rowsFile) },
	} {
		t.Run(path, func(t *testing.T) {
			drivertest.Isolate(t)
			drivertest.InProcessSandbox(t)
			// A primary row replaces a native Strategy, which this binary may not link.
			if !driver.HasNative(name) {
				driver.Register(driverkit.Strategy{Name: name})
			}
			if err := apply(); err != nil {
				t.Fatal(err)
			}
			d, err := driver.New(name)
			if err != nil {
				t.Fatal(err)
			}
			kd, ok := d.(driverkit.Driver)
			if !ok {
				t.Fatalf("New(%s) = %T, want a driverkit.Driver", name, d)
			}
			// The Driver's own tests may not import the registry, so its corpus is read from here.
			conformance.Run(t, kd.Strategy(), filepath.Join("..", name, "testdata", "corpus"))
		})
	}
}
