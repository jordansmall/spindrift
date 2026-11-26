package driverrows_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/golden"
)

const (
	connectorWasmEnv = "SPINDRIFT_DRIVER_CONNECTOR_WASM"
	// connectorNameEnv names the Driver the wasm implements; lib/connector-checks.nix
	// sets it alongside connectorWasmEnv.
	connectorNameEnv = "SPINDRIFT_DRIVER_CONNECTOR_NAME"
)

// A primary row makes the Connector the Driver on both paths that build a Set,
// the Launcher's (Hosted) and the Box's (InProcess), and the Driver New hands out
// passes the same corpus against the same goldens as the native Strategy on
// the Box's path, where driver-exec runs its stream filter live.
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
	desc := generated(t, name)
	raw := rows(t, []driverrows.Row{{Descriptor: desc, Module: wasm, Primary: true, FirstParty: true}})
	// A primary row replaces a native Strategy, which this binary may not link.
	ns := natives(name)
	for path, build := range map[string]func() *driver.Set{
		"host": func() *driver.Set { return hosted(t, raw, ns...) },
		"box":  func() *driver.Set { return box(t, raw, ns...) },
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			d, err := build().New(name)
			if err != nil {
				t.Fatal(err)
			}
			kd, ok := d.(driverkit.Driver)
			if !ok {
				t.Fatalf("New(%s) = %T, want a driverkit.Driver", name, d)
			}
			// The registered stand-in is an empty Strategy; a Connector-built one
			// has a decoder, so this fails if the row left the stand-in in place.
			if kd.Strategy().NewDecoder == nil {
				t.Fatalf("New(%s) kept the empty stand-in Strategy; the Connector's was not applied", name)
			}
			// Both paths hand out the same Connector Strategy, so the corpus
			// replays once: on the interpreter, its 1-byte stream-filter replay is
			// a guest call a byte, and replaying it twice overran go test's 10m.
			if path == "host" {
				return
			}
			// The Driver's own tests may not import the registry, so its corpus is read from here.
			conformance.Run(t, kd.Strategy(), filepath.Join("..", name, "testdata", "corpus"))
			// Wrapped so its cases and credential guard do not share names with Run's.
			if dir := filepath.Join("..", name, "testdata", "streamfilter"); hasDir(t, dir) {
				t.Run("streamfilter", func(t *testing.T) { conformance.RunStreamFilter(t, kd.Strategy(), dir) })
			}
		})
	}
}

// hasDir reports whether p is a directory. Only a missing one means "absent";
// any other stat error would otherwise skip a Driver's stream-filter goldens unseen.
func hasDir(t *testing.T, p string) bool {
	t.Helper()
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return fi.IsDir()
}
