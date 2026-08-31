package main

import (
	"flag"
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/readonlyguards"
)

// isReadonlyGuardsInvocation reports whether args (os.Args[1:]) selects the
// readonly-guards subcommand: a distinct verb, not a top-level flag (issue
// #2509), mirroring isAssemblePromptInvocation/isOutcomeBackstopInvocation.
func isReadonlyGuardsInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "readonly-guards"
}

// readonlyGuardsFlags holds the parsed (or default, pre-Parse) values of
// every readonly-guards flag, keyed by the flag.FlagSet that owns them.
type readonlyGuardsFlags struct {
	forbiddenMarkersRegistryPath *string
	repoDir                      *string
	extraRepoDir                 *string
	shimDir                      *string
	skipGitHook                  *bool
}

// newReadonlyGuardsFlagSet builds the readonly-guards subcommand's
// flag.FlagSet and registers every flag, without parsing it against any
// args. Split out from runReadonlyGuards so a test can inspect a flag's
// default value without ever invoking readonlyguards.Install or touching a
// real file on disk, mirroring newOutcomeBackstopFlagSet.
func newReadonlyGuardsFlagSet() (*flag.FlagSet, *readonlyGuardsFlags) {
	fs := flag.NewFlagSet("readonly-guards", flag.ContinueOnError)
	flags := &readonlyGuardsFlags{
		forbiddenMarkersRegistryPath: fs.String("forbidden-markers-registry", "", "path to the prompt-contract forbiddenMarkers registry JSON file (required)"),
		repoDir:                      fs.String("repo-dir", "", "the git repository (or bare/decoy repository) whose .git/hooks directory receives the rendered git-hook guard; required when the registry has at least one git-hook row"),
		extraRepoDir:                 fs.String("extra-repo-dir", "", "an additional git repository whose .git/hooks directory also receives the rendered git-hook guard, identical to -repo-dir's (issue #2509 Finding 1: entrypoint.sh passes $WORK_DIR here alongside the decoy repo at -repo-dir); optional"),
		shimDir:                      fs.String("shim-dir", "", "directory to install command-shim guards into; required when the registry has at least one command-shim row"),
		skipGitHook:                  fs.Bool("skip-git-hook", false, "skip the git-hook guard entirely (no error even with -repo-dir empty); command-shim rows still install (issue #2509: a Box whose hand-off is a real git push -- no backend registered today, issue #2927 closed the last such gap -- must never get that push blocked locally)"),
	}
	return fs, flags
}

// runReadonlyGuards is the `readonly-guards` subcommand's thin CLI wrapper
// (ADR 0007's thin-exec-glue tier, issue #2509): it parses args, loads the
// forbiddenMarkers registry via promptassembly.LoadForbiddenMarkersFile --
// the same loader assemble-prompt already uses -- and delegates to
// readonlyguards.Install, the Go successor to agent/entrypoint.sh's
// install_readonly_push_hook and install_readonly_gh_shim. Returns the
// process exit code.
func runReadonlyGuards(args []string, stdout io.Writer) int {
	fs, flags := newReadonlyGuardsFlagSet()
	fs.SetOutput(stdout)
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *flags.forbiddenMarkersRegistryPath == "" {
		fmt.Fprintln(fs.Output(), "driver-exec readonly-guards: -forbidden-markers-registry is required")
		return 1
	}

	rows, err := promptassembly.LoadForbiddenMarkersFile(*flags.forbiddenMarkersRegistryPath)
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec readonly-guards:", err)
		return 1
	}

	var extraRepoDirs []string
	if *flags.extraRepoDir != "" {
		extraRepoDirs = []string{*flags.extraRepoDir}
	}

	result, err := readonlyguards.Install(rows, readonlyguards.Config{
		RepoDir:       *flags.repoDir,
		ExtraRepoDirs: extraRepoDirs,
		ShimDir:       *flags.shimDir,
		SkipGitHook:   *flags.skipGitHook,
	}, stdout)
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec readonly-guards:", err)
		return 1
	}

	fmt.Fprintf(stdout, "driver-exec readonly-guards: installed %d command-shim(s) (%v), hook installed=%v\n", len(result.Shims), result.Shims, result.HookInstalled)
	return 0
}
