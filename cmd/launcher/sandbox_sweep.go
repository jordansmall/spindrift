package main

import (
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/connectorsandbox/sandbox"
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
	if err == nil {
		r, rerr := runnerForKind(c, runnerConfig(c), pwd)
		if rerr == nil {
			sandbox.ReapOrphans(r, stderr)
			return
		}
		err = rerr
	}
	fmt.Fprintf(stderr, "warning: reaping orphaned Connector sandboxes: %v\n", err)
}
