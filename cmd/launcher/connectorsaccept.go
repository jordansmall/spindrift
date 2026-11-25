package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/backend/backendrows"
)

// defaultBaselineFile is the Route baseline's name in the Consumer flake's root.
const defaultBaselineFile = "spindrift-connector-routes.json"

// cmdConnectorsAccept is `connectors accept [<baseline-file>]`: write every
// Connector's current declaration into the baseline file, so accepting a grant
// is a reviewed diff of a generated file rather than hand-typed JSON. rows is
// the input document's raw backend rows, never backendrows.All: Use has
// rewritten the stored rows' {base} hosts to resolved ones.
func cmdConnectorsAccept(rows json.RawMessage, args []string, stdout, stderr io.Writer) int {
	path := defaultBaselineFile
	switch {
	case len(args) > 1, len(args) == 1 && len(args[0]) > 0 && args[0][0] == '-':
		fmt.Fprintln(stderr, subcommandUsage("connectors"))
		return 1
	case len(args) == 1:
		path = args[0]
	}
	out, err := backendrows.Baseline(rows)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s; review its diff, then commit it\n", path)
	return 0
}
