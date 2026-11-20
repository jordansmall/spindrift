package github

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/seamtest"
)

// A test binary has no runner to start a sandbox on, so the Connector rows the
// tests apply load into the in-process engine.
func TestMain(m *testing.M) {
	seamtest.MainWith(m, func() error {
		connectorsandbox.Use(connectorsandboxtest.InProcess())
		return nil
	})
}
