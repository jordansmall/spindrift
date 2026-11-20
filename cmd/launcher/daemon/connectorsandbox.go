package main

import (
	"context"

	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/sandbox"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
)

// newConnectorSandboxLoader is a var so a test binary, which has no runner to
// start a sandbox on, can install the in-process engine instead.
var newConnectorSandboxLoader = realConnectorSandboxLoader

// realConnectorSandboxLoader starts through the runner its launcher children
// use: the document's RUNNER_KIND and RUNTIME, with its artifacts and the
// knobs resolved as a child resolves them. The sandbox fixes its own network
// and limits, so no other Config field matters to it.
func realConnectorSandboxLoader(doc *inputdoc.Document, pwd string) connectorsandbox.Loader {
	shared := doc.Artifacts.Shared
	return sandbox.ForKind(shared.RunnerKind.String(), runner.Config{Runtime: shared.Runtime.String()},
		doc.Artifacts, childLookup(doc), pwd)
}

// installConnectorSandbox makes every Connector the Daemon loads itself (its
// Demand probe counts through a backend row in this process) load into a
// sandbox, and returns the func that stops it. The sandbox starts on the first
// load, so a Daemon that loads no Connector starts none.
func installConnectorSandbox(doc *inputdoc.Document, pwd string) (closeSandbox func()) {
	l := newConnectorSandboxLoader(doc, pwd)
	connectorsandbox.Use(l)
	return func() {
		if cl, ok := l.(interface{ Close(context.Context) error }); ok {
			_ = cl.Close(context.Background())
		}
	}
}
