package main

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/driver/drivertest"
	"spindrift.dev/launcher/internal/inputdoc"
)

// A test binary has no runner to start a sandbox on, so the Loader run installs
// is the in-process engine; useRealConnectorSandbox puts the production one back.
func TestMain(m *testing.M) {
	newConnectorSandboxLoader = func(*inputdoc.Document, string) connectorsandbox.Loader {
		return connectorsandboxtest.InProcess()
	}
	os.Exit(drivertest.RunWithClaudeConnector(m))
}
