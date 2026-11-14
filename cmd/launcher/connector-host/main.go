// Command connector-host runs one Connector's module out of process: it reads
// frames from stdin and answers on stdout until stdin closes (issue #7013).
// It is a separate cgo-off binary so a sandbox can bind its single static file.
// stdout carries frames and nothing else; everything it says goes to stderr.
package main

import (
	"context"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/connectorhost"
)

func main() {
	if err := connectorhost.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "connector-host:", err)
		os.Exit(1)
	}
}
