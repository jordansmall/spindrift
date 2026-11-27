// Package connectorconformance runs the core contracts a Connector's
// capabilities register, whatever the Connector is named: the tracker read
// contract for "tracker", the whole tracker contract when it also registers
// "tracker.transition" and "tracker.editLabels", the branch contract for
// "forge.branches", the Code Forge contract for a push-only forge and the PR
// Forge contract for "forge.prs". Whatever else it declares, every tracker or
// forge capability it registers must answer each method it serves, as the
// DeclaredCapabilities subtest calls them. A Connector supplies its wasm, its
// declared Route table and any scripted answers its dialect needs.
//
// The backend it runs against is scripted and forgejo-shaped: REST under
// /api/v1 (a Target.API of the backend URL plus /api/v1), with the repository
// at /repos/o/r. A Connector that speaks another dialect scripts the
// responses it needs (connectortest.Scripted) or does not run here.
package connectorconformance

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortest"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/connectortracker/connectortrackertest"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// The environment a Connector's conformance check hands FromEnv.
const (
	// WasmEnv is the path of the compiled Connector.
	WasmEnv = "SPINDRIFT_BACKEND_CONNECTOR_WASM"
	// RoutesEnv is its declared Route table: a JSON array of
	// {method, host, path, followRedirect}.
	RoutesEnv = "SPINDRIFT_BACKEND_CONNECTOR_ROUTES"
	// ScriptEnv is an optional JSON array of connectortest.Scripted.
	ScriptEnv = "SPINDRIFT_BACKEND_CONNECTOR_SCRIPT"
)

// capTracker is the capability that selects the tracker contract; the write and
// forge ones come from connectortracker and connectorforge.
const capTracker = "tracker"

// FromEnv reads the Connector under test from the environment, skipping the
// test when WasmEnv is unset: a skip is not an attestation, so the Nix check
// demands this test's PASS line.
func FromEnv(t *testing.T) connectortest.Connector {
	t.Helper()
	path := os.Getenv(WasmEnv)
	if path == "" {
		t.Skipf("%s unset: no compiled Connector to test; lib/connector-checks.nix's conformance check sets it", WasmEnv)
	}
	wasm, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c := connectortest.Connector{Wasm: wasm}
	if err := json.Unmarshal([]byte(os.Getenv(RoutesEnv)), &c.Routes); err != nil {
		t.Fatalf("%s: %v", RoutesEnv, err)
	}
	if s := os.Getenv(ScriptEnv); s != "" {
		if err := json.Unmarshal([]byte(s), &c.Script); err != nil {
			t.Fatalf("%s: %v", ScriptEnv, err)
		}
	}
	return c
}

// capabilities is c's handshake capabilities, from a load that grants no
// network.
func capabilities(t *testing.T, c connectortest.Connector) []string {
	t.Helper()
	conn, err := connectorhost.LoadNetwork(context.Background(), c.Wasm, connectorhost.Network{
		Name:  "conformance",
		Audit: connectorhost.NewAuditLog(io.Discard),
	})
	if err != nil {
		t.Fatalf("load connector: %v", err)
	}
	defer conn.Close(context.Background())
	return conn.Capabilities
}

// Run runs one subtest per contract c's capabilities register. The subtest
// names are the contract names a failure reports. A Connector registering none
// fails: a pass would attest nothing.
func Run(t *testing.T, c connectortest.Connector) {
	t.Helper()
	caps := capabilities(t, c)
	has := func(name string) bool { return slices.Contains(caps, name) }
	ran := false
	if has(capTracker) {
		ran = true
		if has(connectortracker.CapTransition) && has(connectortracker.CapEditLabels) {
			// The Connector performs core's lifecycle writes, so a broken one
			// must fail here, not pass a read-only run.
			t.Run("TrackerContract", func(t *testing.T) {
				forgetest.RunTrackerContract(t, connectortrackertest.New(t, c))
			})
		} else {
			t.Run("TrackerReadContract", func(t *testing.T) {
				forgetest.RunTrackerReadContract(t, connectortrackertest.New(t, c))
			})
		}
	}
	if has(connectorforge.CapPRs) {
		ran = true
		if has(connectorforge.CapBranches) {
			t.Run("BranchContract", func(t *testing.T) {
				forgetest.RunBranchContract(t, connectorforgetest.NewPRFor(t, c))
			})
		}
		t.Run("PRForgeContract", func(t *testing.T) {
			forgetest.RunPRForgeContract(t, connectorforgetest.NewPRFor(t, c))
		})
	} else if has(connectorforge.CapBranches) || has(connectorforge.CapPushOnly) {
		ran = true
		if has(connectorforge.CapBranches) {
			t.Run("BranchContract", func(t *testing.T) {
				forgetest.RunBranchContract(t, connectorforgetest.NewFor(t, c))
			})
		}
		t.Run("CodeForgeContract", func(t *testing.T) {
			forgetest.RunCodeForgeContract(t, connectorforgetest.NewFor(t, c))
		})
	}
	if hasDeclared(caps) {
		t.Run("DeclaredCapabilities", func(t *testing.T) { declaredCapabilities(t, c, caps) })
	}
	if !ran {
		t.Fatalf("connector registers none of the capabilities that select a conformance contract (%q for TrackerReadContract, %q for BranchContract, %q or %q for CodeForgeContract, %q for PRForgeContract); it registers %v, so conformance attests nothing",
			capTracker, connectorforge.CapBranches, connectorforge.CapBranches, connectorforge.CapPushOnly, connectorforge.CapPRs, caps)
	}
}
