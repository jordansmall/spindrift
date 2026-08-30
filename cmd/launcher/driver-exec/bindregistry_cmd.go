package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/bindregistry"
)

// isBindRegistryInvocation reports whether args (os.Args[1:]) selects the
// bind-registry subcommand: a distinct verb, not a top-level flag,
// mirroring isReadonlyGuardsInvocation/isAssemblePromptInvocation.
func isBindRegistryInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "bind-registry"
}

// runBindRegistry is the `bind-registry` subcommand's thin CLI wrapper
// (ADR 0007's thin-exec-glue tier, ADR 0036 amendment #6, issue #2930): it
// classifies -work-dir's lockfiles via bindregistry.Classify -- the same
// ecosystem table registryproxy's path-allowlist uses -- and writes the
// result as a sourceable NUDGE_ECOSYSTEM env file. Returns the process exit
// code.
func runBindRegistry(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("bind-registry", flag.ContinueOnError)
	fs.SetOutput(stdout)
	workDir := fs.String("work-dir", "", "the cloned Target repo to scan for lockfiles (required)")
	ecosystemEnvOutput := fs.String("ecosystem-env-output", "", "path to write the sourceable NUDGE_ECOSYSTEM env file to (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *workDir == "" || *ecosystemEnvOutput == "" {
		fmt.Fprintln(fs.Output(), "driver-exec bind-registry: -work-dir and -ecosystem-env-output are required")
		return 1
	}

	classification := bindregistry.Classify(*workDir)

	// %q emits Go quoting, not shell quoting -- safe here only because
	// classification is always one of bindregistry's own fixed constant
	// strings, never attacker- or repo-controlled input.
	env := fmt.Sprintf("NUDGE_ECOSYSTEM=%q\n", classification)
	if err := os.WriteFile(*ecosystemEnvOutput, []byte(env), 0o644); err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec bind-registry: write ecosystem env output:", err)
		return 1
	}

	return 0
}
