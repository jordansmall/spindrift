// Package drivertest holds the assertion a binary's test runs to prove it
// links the Driver bundle, and the TestMain hook that registers the claude
// Connector, the one Driver the bundle does not link.
package drivertest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/fixturebuild"
)

// RequireBundled fails t unless the calling binary registers exactly the
// Drivers the Nix registry declares and refuses any other name: the bundle's
// native Drivers plus claude, registered by UseClaudeConnector.
func RequireBundled(t testing.TB) {
	t.Helper()
	if got, want := driver.Names(), driver.DescriptorNames(); !slices.Equal(got, want) {
		t.Errorf("registered Drivers = %v, want %v: the binary must import internal/driver/bundle", got, want)
	}
	if _, err := driver.New("no-such-driver"); err == nil {
		t.Error(`driver.New("no-such-driver") succeeded, want an unknown-DRIVER error`)
	}
}

// Isolate restores the Driver registry and Descriptors when t ends, so a test
// may register Drivers and apply Driver rows without leaking them to the tests
// that follow.
func Isolate(t testing.TB) {
	t.Helper()
	t.Cleanup(driver.Snapshot())
}

// InProcessSandbox installs the in-process Connector engine as the Connector
// sandbox until t ends, for a test that has the Launcher's path (driverrows.Use)
// load a real Connector. Without it that load fails with
// connectorsandbox.ErrNoConnectorSandbox.
func InProcessSandbox(t testing.TB) {
	t.Helper()
	t.Cleanup(connectorsandbox.Use(connectorsandboxtest.InProcess()))
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

// UseClaudeConnector registers the claude Driver from the compiled claude
// Connector through driverrows.Use, the production path; claude has no native
// Strategy, so a test binary that resolves it needs this. It also installs the
// in-process engine as the Connector sandbox for the rest of the process, as
// the Launcher's path refuses the load without one. An unresolvable wasm is an
// error, never a skip (ADR 0067); the Connector loads lazily, on the first
// New.
func UseClaudeConnector() error {
	raw, err := claudeRows(false)
	if err != nil {
		return err
	}
	connectorsandbox.Use(connectorsandboxtest.InProcess())
	return driverrows.Use(raw)
}

// ClaudeRowsFile writes the first-party claude Driver row the Nix renders into
// the Box's rows file and returns its path, for a test that runs a built
// binary: the in-process registration of UseClaudeConnector does not reach a
// subprocess, which opens its registry from this file.
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

// RunWithClaudeConnector is TestMain's body: it registers the claude Connector
// and runs the tests, or reports why it could not and fails the binary.
//
//	func TestMain(m *testing.M) { os.Exit(drivertest.RunWithClaudeConnector(m)) }
func RunWithClaudeConnector(m *testing.M) int {
	if err := UseClaudeConnector(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}
