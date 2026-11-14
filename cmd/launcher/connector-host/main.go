// Command connector-host runs a run's Connectors out of process in one sandbox:
// it reads session chunks from stdin, each session carrying one Connector's
// frames, and answers on stdout until stdin closes (issues #7013, #7014).
// It is a separate cgo-off binary so a sandbox can bind its single static file.
// stdout carries chunks and nothing else; everything it says goes to stderr.
package main

import (
	"context"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/connectorhost"
)

func main() {
	if err := connectorhost.ServeSessions(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "connector-host:", err)
		os.Exit(1)
	}
}
