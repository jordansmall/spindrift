// Package connectorsetuptest wraps connectorsetup.New for a test binary, which
// has no runner family to start a Connector sandbox on.
//
// Test binaries only, for the reason connectorsandboxtest is.
package connectorsetuptest

import (
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/connectorsetup"
)

// WithInProcessSandbox is build, except a call that names no Sandbox hosts its
// Connectors in the in-process engine. A test of the real loader passes its own
// Sandbox or calls connectorsetup.New itself.
func WithInProcessSandbox(build func(connectorsetup.Input, connectorsetup.Options) (*connectorruntime.Runtime, error)) func(connectorsetup.Input, connectorsetup.Options) (*connectorruntime.Runtime, error) {
	return func(in connectorsetup.Input, opts connectorsetup.Options) (*connectorruntime.Runtime, error) {
		if opts.Sandbox == nil {
			opts.Sandbox = connectorsandboxtest.InProcess()
		}
		return build(in, opts)
	}
}
