package main

import (
	"flag"
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/dispatchkind"
)

// isAdviseOnlyInvocation reports whether args (os.Args[1:]) selects the
// advise-only subcommand, a distinct verb rather than a top-level flag.
func isAdviseOnlyInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "advise-only"
}

// runAdviseOnly is the one input this Box's advise-only posture reads from
// (issue #3901, replacing the separate ADVISE_ONLY env var): it resolves
// --dispatch-kind through dispatchkind.ByName and prints that Descriptor's
// AdviseOnly bit, never a kind-name literal comparison. An unknown kind
// fails closed (exit 1, no stdout) rather than defaulting to work's posture.
func runAdviseOnly(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("advise-only", flag.ContinueOnError)
	kind := fs.String("dispatch-kind", dispatchkind.Work.Name, "dispatch kind: work | research | ...")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	descriptor, ok := dispatchkind.ByName(*kind)
	if !ok {
		fmt.Fprintf(fs.Output(), "driver-exec advise-only: unknown dispatch kind %q\n", *kind)
		return 1
	}

	if descriptor.AdviseOnly {
		fmt.Fprintln(stdout, "1")
	} else {
		fmt.Fprintln(stdout, "0")
	}
	return 0
}
