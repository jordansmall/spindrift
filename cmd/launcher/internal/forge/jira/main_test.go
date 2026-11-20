package jira_test

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
)

// A test binary has no runner to start a sandbox on, so the Connector rows the
// tests apply load into the in-process engine.
func TestMain(m *testing.M) {
	connectorsandbox.Use(connectorsandboxtest.InProcess())
	os.Exit(m.Run())
}
