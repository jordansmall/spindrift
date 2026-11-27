package connectorconformance

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorprotocol"
	"spindrift.dev/launcher/internal/connectorprotocol/schematest"
	"spindrift.dev/launcher/internal/connectortest"
	"spindrift.dev/launcher/internal/fixturebuild"
)

// TestConnectorConformance is the entry the Nix connector-conformance check
// runs; it skips, rather than passes, until the check names a Connector.
func TestConnectorConformance(t *testing.T) {
	Run(t, FromEnv(t))
}

func fixture(t *testing.T, wasm, host string, methods ...string) connectortest.Connector {
	t.Helper()
	return connectortest.Connector{Wasm: connectortest.FixtureWasm(t, wasm), Routes: connectortest.RoutePerMethod(host, methods...)}
}

func TestRun_FixtureTrackerPasses(t *testing.T) {
	Run(t, fixture(t, "tracker.wasm", "tracker.example", http.MethodGet))
}

func TestRun_FixtureWriteTrackerPasses(t *testing.T) {
	Run(t, fixture(t, "tracker-writes.wasm", "tracker.example", http.MethodGet, http.MethodPost, http.MethodDelete))
}

func TestRun_FixturePushOnlyForgePasses(t *testing.T) {
	Run(t, fixture(t, "forge.wasm", "forge.example", http.MethodGet, http.MethodDelete))
}

func TestRun_FixturePRForgePasses(t *testing.T) {
	Run(t, fixture(t, connectorforgetest.PRWasm, "forge.example",
		http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete))
}

// conformanceChild runs TestConnectorConformance in a fresh test binary with
// wasmFile as the Connector under test, so a failing contract fails the child
// and not this test. extraEnv is appended to the child's environment.
func conformanceChild(t *testing.T, extraEnv []string, wasmFile, host string, methods ...string) (string, error) {
	t.Helper()
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	routes, err := json.Marshal(connectortest.RoutePerMethod(host, methods...))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestConnectorConformance$", "-test.v")
	cmd.Env = append(os.Environ(), append([]string{
		WasmEnv + "=" + filepath.Join(dir, wasmFile),
		RoutesEnv + "=" + string(routes),
	}, extraEnv...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRun_ViolatedContractFailsNamingIt(t *testing.T) {
	out, err := conformanceChild(t, nil, "tracker-violate.wasm", "tracker.example", http.MethodGet)
	if err == nil {
		t.Fatalf("a violating tracker passed conformance:\n%s", out)
	}
	if want := "--- FAIL: TestConnectorConformance/TrackerReadContract"; !strings.Contains(out, want) {
		t.Errorf("output does not name the violated contract, want %q:\n%s", want, out)
	}
}

func TestRun_WriteTrackerWithBrokenLabelRemovalFailsTrackerContract(t *testing.T) {
	out, err := conformanceChild(t, nil, "tracker-writes-violate.wasm", "tracker.example",
		http.MethodGet, http.MethodPost, http.MethodDelete)
	if err == nil {
		t.Fatalf("a write tracker that ignores label removals passed conformance:\n%s", out)
	}
	if want := "--- FAIL: TestConnectorConformance/TrackerContract"; !strings.Contains(out, want) {
		t.Errorf("output does not name the violated contract, want %q:\n%s", want, out)
	}
}

func TestRun_PRConnectorWithBrokenBranchesFailsBranchContract(t *testing.T) {
	out, err := conformanceChild(t, nil, "forge-pr-broken-branches.wasm", "forge.example",
		http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete)
	if err == nil {
		t.Fatalf("a PR Connector with broken branches passed conformance:\n%s", out)
	}
	if want := "--- FAIL: TestConnectorConformance/BranchContract"; !strings.Contains(out, want) {
		t.Errorf("output does not name the violated contract, want %q:\n%s", want, out)
	}
}

func TestRun_NoCapabilityFails(t *testing.T) {
	out, err := conformanceChild(t, nil, "current.wasm", "tracker.example", http.MethodGet)
	if err == nil {
		t.Fatalf("a Connector registering no contract-selecting capability passed conformance:\n%s", out)
	}
	if want := "registers none of the capabilities that select a conformance contract"; !strings.Contains(out, want) {
		t.Errorf("output does not say conformance attests nothing, want %q:\n%s", want, out)
	}
}

// A dialect Connector answers only when the backend serves its script, so the
// same wasm fails without ScriptEnv and passes with it: that proves FromEnv
// parses the script and the tracker backend serves it.
func TestRun_ScriptedDialectConnectorPassesOnlyWithItsScript(t *testing.T) {
	script, err := json.Marshal([]connectortest.Scripted{
		{Method: http.MethodGet, Path: "/api/v1/repos/o/r/dialect", Status: http.StatusOK, Body: "{}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := conformanceChild(t, nil, "tracker-dialect.wasm", "tracker.example", http.MethodGet); err == nil {
		t.Fatalf("a dialect Connector passed conformance with no script:\n%s", out)
	}
	out, err := conformanceChild(t, []string{ScriptEnv + "=" + string(script)}, "tracker-dialect.wasm", "tracker.example", http.MethodGet)
	if err != nil {
		t.Fatalf("a dialect Connector failed conformance with its script:\n%s", out)
	}
	if want := "--- PASS: TestConnectorConformance/TrackerReadContract"; !strings.Contains(out, want) {
		t.Errorf("output does not show the read contract passing, want %q:\n%s", want, out)
	}
}

// A Connector that declares a capability it registers no handler for fails the
// declared-capability check, naming the capability.
func TestRun_UndeclaredCapabilityFailsDeclaredCapabilities(t *testing.T) {
	out, err := conformanceChild(t, nil, "tracker-undeclared.wasm", "tracker.example", http.MethodGet)
	if err == nil {
		t.Fatalf("a tracker declaring a capability it does not serve passed conformance:\n%s", out)
	}
	for _, want := range []string{
		"--- FAIL: TestConnectorConformance/DeclaredCapabilities",
		`"tracker.blocks"`,
		`"tracker.blocks" did not answer`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
	if want := "--- PASS: TestConnectorConformance/TrackerReadContract"; !strings.Contains(out, want) {
		t.Errorf("the read contract should still pass, want %q:\n%s", want, out)
	}
}

func TestRun_DeclaredCapabilitiesRunForEveryFixture(t *testing.T) {
	for _, tc := range []struct{ wasm, host string }{
		{"tracker.wasm", "tracker.example"},
		{"tracker-writes.wasm", "tracker.example"},
		{"forge.wasm", "forge.example"},
		{connectorforgetest.PRWasm, "forge.example"},
	} {
		out, err := conformanceChild(t, nil, tc.wasm, tc.host, http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete)
		if err != nil {
			t.Fatalf("%s failed conformance:\n%s", tc.wasm, out)
		}
		if want := "--- PASS: TestConnectorConformance/DeclaredCapabilities"; !strings.Contains(out, want) {
			t.Errorf("%s: want %q:\n%s", tc.wasm, want, out)
		}
	}
}

func TestSampleParamsCoverTheProtocol(t *testing.T) {
	schema, err := schematest.Load()
	if err != nil {
		t.Fatal(err)
	}
	for name, m := range connectorprotocol.Methods {
		if _, ok := declaredAxis(m.Capability); !ok || m.Raw {
			continue
		}
		params, ok := sampleParams(name, "https://backend.example/api/v1", "1")
		if !ok {
			t.Errorf("sampleParams has no sample for %q", name)
			continue
		}
		if err := schema.ValidateParams(name, params); err != nil {
			t.Errorf("sample for %q does not match the protocol: %v", name, err)
		}
	}
}
