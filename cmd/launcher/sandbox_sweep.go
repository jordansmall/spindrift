package main

import (
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/connectorsandbox/sandbox"
	"spindrift.dev/launcher/internal/runner"
)

// sweepOrphanedSandboxes is the one owner of when the orphan sweep runs: only
// under CONNECTOR_SANDBOX. Best-effort: a failure warns on stderr and never
// changes the caller's verdict. It builds the configured runner without
// checking IsReady: listing and killing sandboxes needs no Box image or
// snapshot, so the sweep must not wait for a build.
func sweepOrphanedSandboxes(c config, stderr io.Writer) {
	if !c.ConnectorSandbox {
		return
	}
	pwd, err := os.Getwd()
	var r runner.Runner
	if err == nil {
		r, err = runnerForKind(c, runnerConfig(c), pwd)
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: reaping orphaned Connector sandboxes: %v\n", err)
		return
	}
	sandbox.ReapOrphans(r, stderr)
}
