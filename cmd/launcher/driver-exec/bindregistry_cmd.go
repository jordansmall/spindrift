package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/ecosystem"
)

// The Forwarder readiness poll is 50 tries at 100ms. These are not flags,
// because entrypoint.sh never overrode them.
const (
	registryProxyForwarderTimeout      = 5 * time.Second
	registryProxyForwarderPollInterval = 100 * time.Millisecond
)

func isBindRegistryInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "bind-registry"
}

// runBindRegistry is the bind-registry subcommand's entry point (ADR 0007, ADR
// 0036 amendment #6, issues #2930/#2931): it wires the real DialProbe and
// SpawnSocat into runBindRegistryWithDeps and returns the process exit code.
func runBindRegistry(args []string, stdout io.Writer) int {
	return runBindRegistryWithDeps(args, stdout, bindregistry.DialProbe, bindregistry.SpawnSocat, exec.LookPath, registryProxyForwarderTimeout, registryProxyForwarderPollInterval)
}

// spawnHTTPForwarder is an indirection over bindregistry.SpawnHTTPForwarder (issue
// #3111) so tests never invoke the real one: it re-execs os.Executable() detached,
// which under go test is this package's test binary, relaunching the whole suite.
var spawnHTTPForwarder = func(upstreamHost string, upstreamPort int, secret string, port int) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	return bindregistry.SpawnHTTPForwarder(self, upstreamHost, upstreamPort, secret, port)
}

// renderEnvExports renders exports as export NAME='VALUE' lines for later sourcing
// by agent/entrypoint.sh. Each value is single-quoted with embedded quotes escaped,
// because nothing inside single quotes is special to the shell; %q alone left a
// command-injection path for repo-controlled values (issue #3259). Names are
// interpolated raw, safe only because every Name is a constant from this package.
func renderEnvExports(exports []ecosystem.EnvExport) string {
	var rendered string
	for _, e := range exports {
		rendered += fmt.Sprintf("export %s='%s'\n", e.Name, strings.ReplaceAll(e.Value, "'", `'\''`))
	}
	return rendered
}

// runBindRegistryWithDeps parses the flags and runs whichever of three modes they
// select: bindings, in-tree (issue #2932) and the lockfile scan. Any mode's flags may
// be given alone or with another's. probe, spawn, timeout and pollInterval are
// injected so tests can exercise the readiness paths without a real socat or
// listener. Bindings and in-tree apply share one gate, so neither respawns (#3141).
func runBindRegistryWithDeps(args []string, stdout io.Writer, probe bindregistry.ProbeFunc, spawn bindregistry.SpawnFunc, lookPath bindregistry.LookPathFunc, timeout, pollInterval time.Duration) int {
	fs := flag.NewFlagSet("bind-registry", flag.ContinueOnError)
	fs.SetOutput(stdout)
	bindingsEnvOutput := fs.String("bindings-env-output", "", "path to write the sourceable registry-binding env file to (optional; triggers bindings mode alone)")
	intreeWorkDir := fs.String("intree-work-dir", "", "the cloned Target repo root to apply/revert in-tree bindings in (optional, pairs with -intree-action)")
	intreeAction := fs.String("intree-action", "", "in-tree binding operation: \"apply\" or \"revert\" (optional, pairs with -intree-work-dir)")
	intreeBindingsEnvOutput := fs.String("intree-bindings-env-output", "", "path to write the sourceable cargo source-replacement placeholder env file to (optional, pairs with -intree-work-dir/-intree-action=apply)")
	lockfileScanWorkDir := fs.String("lockfile-scan-work-dir", "", "the cloned Target repo to scan for tracked lockfiles still naming the run's Forwarder URL (optional, standalone mode)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if (*intreeWorkDir == "") != (*intreeAction == "") {
		fmt.Fprintln(stdout, "driver-exec bind-registry: -intree-work-dir and -intree-action must be given together")
		return 1
	}
	if *intreeAction != "" && *intreeAction != "apply" && *intreeAction != "revert" {
		fmt.Fprintln(stdout, "driver-exec bind-registry: -intree-action must be \"apply\" or \"revert\", got "+strconv.Quote(*intreeAction))
		return 1
	}
	if *intreeBindingsEnvOutput != "" && *intreeAction != "apply" {
		fmt.Fprintln(stdout, "driver-exec bind-registry: -intree-bindings-env-output requires -intree-action=apply")
		return 1
	}
	// apply's repo-aware home-config render must be the last writer of a
	// repo-aware row's HomeConfig file: bindings mode in the same invocation
	// would re-render every HomeConfig row from the base template afterward and
	// clobber apply's replacement stanzas. revert renders nothing, so revert
	// plus bindings stays legal.
	if *intreeAction == "apply" && *bindingsEnvOutput != "" {
		fmt.Fprintln(stdout, "driver-exec bind-registry: -intree-action=apply and -bindings-env-output cannot be combined in one invocation — bindings mode would re-render the repo-aware rows' home configs from the base template and undo the apply")
		return 1
	}
	if *bindingsEnvOutput == "" && *intreeWorkDir == "" && *intreeAction == "" && *lockfileScanWorkDir == "" {
		fmt.Fprintln(stdout, "driver-exec bind-registry: at least one of -bindings-env-output, -intree-work-dir/-intree-action, or -lockfile-scan-work-dir is required")
		return 1
	}

	if *lockfileScanWorkDir != "" {
		bindregistry.WarnStaleLockfiles(stdout, *lockfileScanWorkDir)
	}

	// Resolved only when a mode that needs a live Forwarder will run, so a
	// revert-only or lockfile-scan-only call never touches
	// REGISTRY_PROXY_MANIFEST or the probe/spawn deps at all (issue #3141).
	var gate bindregistry.Gate
	if *intreeAction == "apply" || *bindingsEnvOutput != "" {
		gate = bindregistry.ResolveGate(stdout, bindregistry.GateDeps{
			Probe:        probe,
			Spawn:        spawn,
			SpawnTCP:     spawnHTTPForwarder,
			LookPath:     lookPath,
			Timeout:      timeout,
			PollInterval: pollInterval,
		})
	}

	switch *intreeAction {
	case "revert":
		if bindregistry.RevertInTreeBindings(*intreeWorkDir, bindregistry.InTreeBindings(), "driver-exec bind-registry", stdout) {
			return 1
		}
	case "apply":
		var publish bindregistry.PublishFunc
		if *intreeBindingsEnvOutput != "" {
			publish = writeEnvFile(stdout, *intreeBindingsEnvOutput, "write intree bindings env output:")
		}
		if !bindregistry.ApplyInTree(stdout, *intreeWorkDir, gate, publish) {
			return 1
		}
	}

	if *bindingsEnvOutput != "" {
		if !bindregistry.BindHomes(stdout, gate, writeEnvFile(stdout, *bindingsEnvOutput, "write bindings env output:")) {
			return 1
		}
	}

	return 0
}

// writeEnvFile is the verb's PublishFunc: it renders the exports to path in the
// sourceable format agent/entrypoint.sh reads, printing failureLabel on error.
func writeEnvFile(stdout io.Writer, path, failureLabel string) bindregistry.PublishFunc {
	return func(exports []ecosystem.EnvExport) bool {
		if err := os.WriteFile(path, []byte(renderEnvExports(exports)), 0o644); err != nil {
			fmt.Fprintln(stdout, "driver-exec bind-registry: "+failureLabel, err)
			return false
		}
		return true
	}
}
