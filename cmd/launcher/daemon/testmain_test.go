package main

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/connectorsetup/connectorsetuptest"
	"spindrift.dev/launcher/internal/driver/drivertest"
)

// A test binary has no runner to start a sandbox on, so a runtime built without
// a Sandbox hosts Connectors in the in-process engine; a test of the real
// loader calls connectorsetup.New itself.
func TestMain(m *testing.M) {
	newConnectorRuntime = connectorsetuptest.WithInProcessSandbox(newConnectorRuntime)
	os.Exit(drivertest.RunWithClaudeConnector(m))
}
