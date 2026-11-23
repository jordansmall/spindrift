package conformance_test

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
	"spindrift.dev/launcher/internal/golden"
)

const (
	connectorWasmEnv   = "SPINDRIFT_DRIVER_CONNECTOR_WASM"
	connectorCorpusEnv = "SPINDRIFT_DRIVER_CONNECTOR_CORPUS"
)

// A Driver Connector's conformance check sets both variables, for a Driver
// named after no core Driver and so with no corpus in core; core's own go test
// leaves them unset and skips.
func TestConnectorCorpus(t *testing.T) {
	wasm := os.Getenv(connectorWasmEnv)
	if wasm == "" {
		t.Skipf("%s unset: no compiled Driver Connector to test; its connector-<name>-conformance check runs it", connectorWasmEnv)
	}
	corpus := os.Getenv(connectorCorpusEnv)
	if corpus == "" {
		t.Fatalf("%s must be set with %s", connectorCorpusEnv, connectorWasmEnv)
	}
	if golden.Update() {
		t.Fatal("refusing to update goldens from the Connector; unset UPDATE_GOLDENS")
	}
	conformance.RunConnector(t, wasm, corpus)
}
