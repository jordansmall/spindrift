// Package drivertest holds the assertion a binary's test runs to prove it
// links the Driver bundle, and builders of the Driver sets and rows documents a
// test needs when it has no runtime of its own: the claude Connector is the one
// Driver the bundle does not link, so they supply it from its row.
package drivertest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/fixturebuild"
)

// RequireBundled fails t unless drivers, the Set a binary's runtime offers, holds
// exactly the Drivers the Nix registry declares and refuses any other name: the
// bundle's native Drivers plus claude, from its row.
func RequireBundled(t testing.TB, drivers *driver.Set) {
	t.Helper()
	if got, want := drivers.Names(), driver.DescriptorNames(); !slices.Equal(got, want) {
		t.Errorf("Drivers = %v, want %v: the binary must give its runtime internal/driver/bundle's Drivers", got, want)
	}
	if _, err := drivers.New("no-such-driver"); err == nil {
		t.Error(`New("no-such-driver") succeeded, want an unknown-DRIVER error`)
	}
}

// claudeRows is the one-row Driver rows document for the claude Connector;
// firstParty marks the row as the Nix renders it into the Box's rows file.
func claudeRows(firstParty bool) ([]byte, error) {
	wasm, err := fixturebuild.ClaudeConnectorWasm()
	if err != nil {
		return nil, fmt.Errorf("claude Connector: %w", err)
	}
	desc, ok := driver.NewSet().DescriptorFor("claude")
	if !ok {
		return nil, errors.New("claude Connector: no generated Descriptor for claude")
	}
	return json.Marshal([]driverrows.Row{{Descriptor: desc, Module: wasm, FirstParty: firstParty}})
}

// WithClaudeRow is raw, a Driver rows document, with the claude Connector's row
// added unless raw already has a row for claude: the one Nix renders into the
// document of any real Launcher, which a test document leaves out. Parse the
// result for the runtime under test (connectorsetup.Input.Drivers).
func WithClaudeRow(raw json.RawMessage) (json.RawMessage, error) {
	var rows []driverrows.Row
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("driver rows: %w", err)
		}
	}
	if slices.ContainsFunc(rows, func(r driverrows.Row) bool { return r.Name == "claude" }) {
		return raw, nil
	}
	claude, err := claudeRows(false)
	if err != nil {
		return nil, err
	}
	var row []driverrows.Row
	if err := json.Unmarshal(claude, &row); err != nil {
		return nil, err
	}
	return json.Marshal(append(rows, row...))
}

// ClaudeRowsFile writes the first-party claude Driver row the Nix renders into
// the Box's rows file and returns its path, for a test that runs a built
// binary, which builds its Drivers from this file.
func ClaudeRowsFile(t testing.TB) string {
	t.Helper()
	raw, err := claudeRows(true)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "driver-rows.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Drivers is the Driver set a Launcher built from the claude Connector's row
// plus natives offers, with Connectors loaded in-process: for a test that reads
// a log stamped with a real Driver and has no runtime of its own.
func Drivers(t testing.TB, natives []driverkit.Strategy) *driver.Set {
	t.Helper()
	s, err := NewDrivers(natives)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// NewDrivers is Drivers for a TestMain, which has no testing.TB: the error is
// the caller's to report.
func NewDrivers(natives []driverkit.Strategy) (*driver.Set, error) {
	raw, err := WithClaudeRow(nil)
	if err != nil {
		return nil, err
	}
	rows, err := driverrows.Parse(raw, natives)
	if err != nil {
		return nil, err
	}
	return rows.Hosted(connectorsandboxtest.InProcess().Load), nil
}
