package driverrows_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/golden"
)

// The claude Connector, built through the production path, passes the
// corpus and goldens that live beside it. The conformance runner is core's and
// internal, so this tree replays a corpus the Connector's module owns.
func TestCorpus(t *testing.T) {
	t.Parallel()
	// The Connector's conformance check owns this replay and requires its PASS
	// line; in core's go test it only adds CPU (issue #6799).
	if os.Getenv(connectorNameEnv) != "claude" {
		t.Skipf("%s is not claude: connector-claude-conformance runs this corpus", connectorNameEnv)
	}
	// conformance.Run would rewrite the goldens from the Connector's output.
	if golden.Update() {
		t.Fatal("refusing to update goldens from the Connector; unset UPDATE_GOLDENS")
	}
	// An unresolvable wasm is an error, never a skip (ADR 0067).
	wasm, err := fixturebuild.ClaudeConnectorWasm()
	if err != nil {
		t.Fatalf("claude Connector: %v", err)
	}
	raw, err := json.Marshal([]driverrows.Row{{Descriptor: generated(t, "claude"), Module: wasm}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := hosted(t, raw).New("claude")
	if err != nil {
		t.Fatal(err)
	}
	kd, ok := d.(driverkit.Driver)
	if !ok {
		t.Fatalf("New(claude) = %T, want a driverkit.Driver", d)
	}
	conformance.Run(t, kd.Strategy(), filepath.Join("..", "..", "..", "..", "..", "connectors", "claude", "testdata", "corpus"))
}
