package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// registryProxyForwarderTimeout/PollInterval mirror the deleted
// entrypoint.sh phase_registry_proxy_forwarder's own readiness-poll
// constants (see git history: 50 tries @ 100ms, ~5s total) verbatim -- not
// exposed as flags, since entrypoint.sh never overrode them either.
const (
	registryProxyForwarderTimeout      = 5 * time.Second
	registryProxyForwarderPollInterval = 100 * time.Millisecond
)

// isBindRegistryInvocation reports whether args (os.Args[1:]) selects the
// bind-registry subcommand: a distinct verb, not a top-level flag,
// mirroring isReadonlyGuardsInvocation/isAssemblePromptInvocation.
func isBindRegistryInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "bind-registry"
}

// runBindRegistry is the `bind-registry` subcommand's public entry point
// (ADR 0007's thin-exec-glue tier, ADR 0036 amendment #6, issue #2930/#2931):
// it wires the real bindregistry.DialProbe/SpawnSocat into
// runBindRegistryWithDeps. Returns the process exit code.
func runBindRegistry(args []string, stdout io.Writer) int {
	return runBindRegistryWithDeps(args, stdout, bindregistry.DialProbe, bindregistry.SpawnSocat, exec.LookPath, registryProxyForwarderTimeout, registryProxyForwarderPollInterval)
}

// lookPathFunc mirrors exec.LookPath's own signature -- resolveRegistryProxyGate's
// injectable seam for the socat-on-PATH check (issue #3141's shared gate),
// so tests can stub PATH lookups the same way probe/spawn are already
// stubbed instead of depending on the ambient sandbox's real PATH (the CI
// failure this seam fixes: the Nix go-test sandbox has no socat build
// input, so an unstubbed real exec.LookPath("socat") call fails there even
// though it would succeed on a developer machine).
type lookPathFunc func(file string) (string, error)

// runBindRegistryWithDeps does the actual flag parsing and orchestration for
// bind-registry, taking probe/spawn as parameters so tests can exercise the
// bindings-mode readiness paths (already-listening, timeout, ready) without
// a real socat process or a real TCP listener. timeout/pollInterval are
// threaded through to resolveRegistryProxyGate so tests can shrink them below
// the real registryProxyForwarderTimeout/PollInterval constants -- production
// callers (runBindRegistry) always pass those two constants unchanged. It
// covers three independent modes, each gated on its own flag(s):
//   - classification mode (-work-dir/-ecosystem-env-output): classifies
//     -work-dir's lockfiles via bindregistry.Classify and writes the result
//     as a sourceable NUDGE_ECOSYSTEM env file (unchanged since #2930).
//   - bindings mode (-bindings-env-output): ensures the Forwarder is
//     listening, then computes and writes the Go/npm-family env bindings
//     plus the cargo config.toml.
//   - in-tree mode (-intree-work-dir/-intree-action, issue #2932): applies or
//     reverts the in-tree config-file rewrite (e.g. npm's .npmrc) that points
//     a tracked ecosystem config file at the local Forwarder instead of the
//     real upstream registry, gated on the same Forwarder readiness bindings
//     mode uses. On -intree-action=apply it also re-renders every row's
//     RepoAwareHomeConfig now that the Target repo is on disk (issue #3201;
//     cargo is the one row today), writing -intree-bindings-env-output as the
//     cargo source-replacement placeholder env file that binding needs.
//
// Bindings mode and in-tree-apply mode no longer take their own transport
// flags (issue #3141): both read REGISTRY_PROXY_MANIFEST (ADR 0045) instead,
// parsed and gated exactly once by resolveRegistryProxyGate below and shared
// by whichever of the two modes actually runs in this invocation, so a
// single call never parses the manifest twice or spawns the Forwarder twice.
//
// Any mode's flag(s) may be given alone, or together with another (the
// entrypoint.sh call sites this verb serves run at different points in
// main() -- see docs/adr/0036 and the coordinator's slice notes -- so a
// single entrypoint.sh invocation only ever needs one mode at a time today,
// but the verb itself doesn't assume that).
func runBindRegistryWithDeps(args []string, stdout io.Writer, probe bindregistry.ProbeFunc, spawn bindregistry.SpawnFunc, lookPath lookPathFunc, timeout, pollInterval time.Duration) int {
	fs := flag.NewFlagSet("bind-registry", flag.ContinueOnError)
	fs.SetOutput(stdout)
	workDir := fs.String("work-dir", "", "the cloned Target repo to scan for lockfiles (optional, pairs with -ecosystem-env-output)")
	ecosystemEnvOutput := fs.String("ecosystem-env-output", "", "path to write the sourceable NUDGE_ECOSYSTEM env file to (optional, pairs with -work-dir)")
	bindingsEnvOutput := fs.String("bindings-env-output", "", "path to write the sourceable registry-binding env file to (optional; triggers bindings mode alone)")
	intreeWorkDir := fs.String("intree-work-dir", "", "the cloned Target repo root to apply/revert in-tree bindings in (optional, pairs with -intree-action)")
	intreeAction := fs.String("intree-action", "", "in-tree binding operation: \"apply\" or \"revert\" (optional, pairs with -intree-work-dir)")
	intreeBindingsEnvOutput := fs.String("intree-bindings-env-output", "", "path to write the sourceable cargo source-replacement placeholder env file to (optional, pairs with -intree-work-dir/-intree-action=apply)")
	lockfileScanWorkDir := fs.String("lockfile-scan-work-dir", "", "the cloned Target repo to scan for tracked lockfiles still naming the run's Forwarder URL (optional, standalone mode)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if (*workDir == "") != (*ecosystemEnvOutput == "") {
		fmt.Fprintln(stdout, "driver-exec bind-registry: -work-dir and -ecosystem-env-output must be given together")
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
	// apply's repo-aware home-config render (line ~147) must be the last
	// writer of a repo-aware row's HomeConfig file -- bindings mode sharing
	// this invocation would re-render every HomeConfig row from the base
	// template afterward and clobber apply's replacement stanzas. revert
	// renders nothing, so revert + bindings stays legal.
	if *intreeAction == "apply" && *bindingsEnvOutput != "" {
		fmt.Fprintln(stdout, "driver-exec bind-registry: -intree-action=apply and -bindings-env-output cannot be combined in one invocation — bindings mode would re-render the repo-aware rows' home configs from the base template and undo the apply")
		return 1
	}
	if *workDir == "" && *ecosystemEnvOutput == "" && *bindingsEnvOutput == "" && *intreeWorkDir == "" && *intreeAction == "" && *lockfileScanWorkDir == "" {
		fmt.Fprintln(stdout, "driver-exec bind-registry: at least one of -work-dir/-ecosystem-env-output, -bindings-env-output, -intree-work-dir/-intree-action, or -lockfile-scan-work-dir is required")
		return 1
	}

	if *workDir != "" {
		if rc := runBindRegistryClassification(stdout, *workDir, *ecosystemEnvOutput); rc != 0 {
			return rc
		}
	}

	if *lockfileScanWorkDir != "" {
		runBindRegistryLockfileScan(stdout, *lockfileScanWorkDir)
	}

	// Resolved once, only when a mode that actually needs a live Forwarder
	// will run this invocation -- a classification-only or intree-revert-only
	// call never touches REGISTRY_PROXY_MANIFEST or the probe/spawn deps at
	// all, matching their pre-#3141 behavior exactly.
	var gate *registryProxyGate
	if *intreeAction == "apply" || *bindingsEnvOutput != "" {
		g := resolveRegistryProxyGate(probe, spawn, lookPath, timeout, pollInterval)
		gate = &g

		// A bats teardown needs a handle on the Setsid-detached Forwarder
		// child to kill it directly (see forwarder.go's own Setsid comment
		// for why it's otherwise unreachable via normal process-group
		// reaping) -- print its PID once, here, since this is the one call
		// site shared by both intree-apply mode and bindings mode. Silent
		// on the already-ready short-circuit (g.pid == 0): nothing was
		// spawned, so there is no process to report.
		if g.outcome == registryProxyReady && g.pid != 0 {
			fmt.Fprintln(stdout, "==> registry proxy Forwarder pid "+strconv.Itoa(g.pid))
		}
	}

	if *intreeAction != "" {
		if rc := runBindRegistryIntree(stdout, *intreeAction, *intreeWorkDir, gate); rc != 0 {
			return rc
		}
		if *intreeAction == "apply" {
			if rc := runBindRegistryRepoAwareHomeConfigs(stdout, *intreeWorkDir, gate, *intreeBindingsEnvOutput); rc != 0 {
				return rc
			}
		}
	}

	if *bindingsEnvOutput != "" {
		return runBindRegistryBindings(stdout, gate, *bindingsEnvOutput)
	}

	return 0
}

// runBindRegistryClassification is the unchanged #2930 classification-mode
// body, extracted verbatim so it can be called conditionally now that
// -work-dir/-ecosystem-env-output are no longer unconditionally required.
func runBindRegistryClassification(stdout io.Writer, workDir, ecosystemEnvOutput string) int {
	classification := bindregistry.Classify(workDir)

	// %q emits Go quoting, not shell quoting -- safe here only because
	// classification is always one of bindregistry's own fixed constant
	// strings, never attacker- or repo-controlled input.
	env := fmt.Sprintf("NUDGE_ECOSYSTEM=%q\n", classification)
	if err := os.WriteFile(ecosystemEnvOutput, []byte(env), 0o644); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: write ecosystem env output:", err)
		return 1
	}

	return 0
}

// runBindRegistryLockfileScan is lockfile-scan mode (issue #3199): at
// settle, after the driver has run, it warns about any git-tracked
// ecosystem lockfile that still names the run's Forwarder URL -- a stale pin
// that would otherwise ship silently in the PR. It parses
// REGISTRY_PROXY_MANIFEST directly, only to decide the registry proxy was on
// for this dispatch at all, and deliberately never calls
// resolveRegistryProxyGate: that gate probes and can spawn the Forwarder via
// socat, which a settle-time scan must never do. Warn-only: every failure
// path here (absent/malformed manifest, a scan error) is silent-or-warn and
// always returns 0, never a non-zero exit for a run that already finished.
func runBindRegistryLockfileScan(stdout io.Writer, workDir string) {
	if _, err := registrymanifest.Parse(os.Getenv(registrymanifest.EnvVar)); err != nil {
		if errors.Is(err, registrymanifest.ErrAbsent) {
			return
		}
		fmt.Fprintln(stdout, "==> WARNING: REGISTRY_PROXY_MANIFEST is malformed, skipping the lockfile Forwarder-URL scan: "+err.Error())
		return
	}

	hits, err := bindregistry.ScanLockfilesForForwarder(workDir, bindregistry.ForwarderPort)
	if err != nil {
		fmt.Fprintln(stdout, "==> WARNING: lockfile Forwarder-URL scan failed, skipping: "+err.Error())
		return
	}

	for _, hit := range hits {
		fmt.Fprintln(stdout, "==> WARNING: "+hit.Ecosystem+" lockfile "+hit.Path+" still names the registry proxy Forwarder URL "+hit.MatchedURL+" — this will ship in the PR (issue #3199)")
	}
}

// isMountedSocket reports whether path exists and is a unix socket -- the
// `[ -S "$path" ]`-equivalent guard resolveRegistryProxyGate uses to confirm
// a manifest's unix endpoint is actually reachable in this Box before ever
// probing/spawning against it.
func isMountedSocket(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

// renderEnvExports renders exports into `export NAME=VALUE\n` lines, one per
// entry, shared by bindings mode's own env output and
// runBindRegistryRepoAwareHomeConfigs' cargo source-replacement placeholder
// env output (issue #3201).
//
// Each value is wrapped in POSIX single quotes, with any embedded single
// quote replaced by the four-byte sequence: close quote, backslash-escaped
// literal quote, reopen quote -- the standard technique for embedding
// arbitrary bytes in a single-quoted shell string. Single quotes are the
// only shell metacharacter-free quoting form: unlike Go's %q (or shell
// double quotes), nothing inside them -- dollar sign, backtick, backslash,
// glob characters -- is special, so the emitted export line is safe for
// this file's later sourcing by agent/entrypoint.sh regardless of what
// e.Value contains, including repo-controlled input (issue #3259 threads a
// Target repo's own committed .npmrc-derived path into an EnvExport.Value;
// %q alone left a command-injection path here since it doesn't escape the
// shell's dollar sign or backtick). This escaping covers e.Value only --
// e.Name is interpolated raw below. That's safe only because every e.Name
// in this codebase is a fixed constant drawn from this package's own
// tables (e.g. "npm_config_registry"), never repo-controlled or otherwise
// external input.
func renderEnvExports(exports []ecosystem.EnvExport) string {
	var rendered string
	for _, e := range exports {
		rendered += fmt.Sprintf("export %s='%s'\n", e.Name, strings.ReplaceAll(e.Value, "'", `'\''`))
	}
	return rendered
}

// spawnHTTPForwarder is a package-level indirection over
// bindregistry.SpawnHTTPForwarder (issue #3111's TCP-fallback transport) so
// tests can substitute a fake without ever invoking the real function: it
// re-execs os.Executable() into a detached "forward-registry-tcp" subprocess,
// and under `go test` that's this package's own test binary, so a real call
// from a test would re-launch (recursively) the whole test suite as a
// detached background process rather than a single fake spawn.
var spawnHTTPForwarder = bindregistry.SpawnHTTPForwarder

// registryProxyGateOutcome classifies how resolveRegistryProxyGate settled
// for this invocation (issue #3141).
type registryProxyGateOutcome int

const (
	// registryProxyAbsent is REGISTRY_PROXY_MANIFEST unset/empty
	// (registrymanifest.ErrAbsent): the registry proxy feature is off for
	// this dispatch, and every mode gated on the manifest stays completely
	// silent -- the same deliberate silence issue #3082 established for an
	// unmounted socket, now keyed on the manifest's absence rather than an
	// empty flag value.
	registryProxyAbsent registryProxyGateOutcome = iota
	// registryProxyUnusable is a manifest present but not deliverable as a
	// live Forwarder: malformed REGISTRY_PROXY_MANIFEST (bad JSON, or an
	// endpoint ParseEndpoint rejects), an unmounted unix socket, missing
	// socat, a missing REGISTRY_PROXY_TCP_SECRET, or EnsureForwarderReady
	// itself failing/timing out. Every mode gated on the manifest warns,
	// naming the endpoint (or the parse error) and its own consequence, then
	// skips its rewrite/binding entirely -- never a partial one (the settled
	// #3112 diagnostic voice).
	registryProxyUnusable
	// registryProxyReady is a confirmed-listening Forwarder at gate.port.
	registryProxyReady
)

// registryProxyGate is resolveRegistryProxyGate's result: computed at most
// once per invocation (one manifest parse, one probe/spawn attempt) and
// shared by whichever of bindings mode/intree-apply mode actually runs, so
// neither mode redoes the other's work or double-spawns the Forwarder.
type registryProxyGate struct {
	outcome  registryProxyGateOutcome
	manifest registrymanifest.Manifest
	port     int
	// pid is the spawned Forwarder's process ID, set only on a
	// registryProxyReady outcome reached via an actual spawn -- zero on
	// every other outcome, including the already-ready short-circuit
	// (EnsureForwarderReady's double-spawn-prevention path), which never
	// spawns and so has no PID to report.
	pid int
	// reason explains a registryProxyUnusable outcome, always naming the
	// endpoint (manifest.Endpoint.String()) or the manifest parse error
	// itself. Callers append their own mode-specific "...skipped" tail.
	reason string
}

// resolveRegistryProxyGate parses REGISTRY_PROXY_MANIFEST (ADR 0045) exactly
// once and, if a manifest is present, ensures the Forwarder it describes is
// listening -- spawning it via spawn only if probe doesn't already find one
// (EnsureForwarderReady's own double-spawn-prevention), so a single
// invocation that runs both intree-apply mode and bindings mode still only
// ever probes/spawns once.
func resolveRegistryProxyGate(probe bindregistry.ProbeFunc, spawn bindregistry.SpawnFunc, lookPath lookPathFunc, timeout, pollInterval time.Duration) registryProxyGate {
	manifest, err := registrymanifest.Parse(os.Getenv(registrymanifest.EnvVar))
	if err != nil {
		if errors.Is(err, registrymanifest.ErrAbsent) {
			return registryProxyGate{outcome: registryProxyAbsent}
		}
		return registryProxyGate{outcome: registryProxyUnusable, reason: "REGISTRY_PROXY_MANIFEST is malformed: " + err.Error()}
	}

	port := bindregistry.ForwarderPort
	endpointName := manifest.Endpoint.String()
	forwarderSocketArg := ""
	effectiveSpawn := spawn

	switch {
	case manifest.Endpoint.IsUnix():
		socketPath := manifest.Endpoint.SocketPath()
		if !isMountedSocket(socketPath) {
			return registryProxyGate{outcome: registryProxyUnusable, manifest: manifest, reason: "registry proxy endpoint " + endpointName + " is not mounted"}
		}
		forwarderSocketArg = socketPath
		// The socat PATH check only gates the *spawn* path:
		// EnsureForwarderReady probes first and only calls spawn if nothing
		// is listening yet, so an already-ready Forwarder needs socat on
		// PATH not at all -- checking unconditionally would wrongly warn and
		// skip everything for a Forwarder that's already up.
		if !probe(port) {
			if _, err := lookPath("socat"); err != nil {
				return registryProxyGate{outcome: registryProxyUnusable, manifest: manifest, reason: "registry proxy endpoint " + endpointName + " is mounted but socat is not on PATH"}
			}
		}
	case manifest.Endpoint.IsTCP():
		// The unix socket can't cross into this Box (issue #3111): the
		// manifest's TCP endpoint means the launcher already decided this
		// runtime needs the TCP-relayed Forwarder instead of socat's socket
		// bridge -- spawnHTTPForwarder re-execs this same binary rather than
		// shelling out, so there's no PATH check to gate here. The launcher
		// only ever mints a TCP endpoint together with REGISTRY_PROXY_TCP_SECRET
		// (see internal/dispatch/box.go), so a missing secret here is a
		// genuine misconfiguration, not an expected shape.
		secret := os.Getenv("REGISTRY_PROXY_TCP_SECRET")
		if secret == "" {
			return registryProxyGate{outcome: registryProxyUnusable, manifest: manifest, reason: "registry proxy endpoint " + endpointName + " requires REGISTRY_PROXY_TCP_SECRET, which is not set"}
		}
		host := manifest.Endpoint.Host()
		upstreamPort, err := strconv.Atoi(manifest.Endpoint.Port())
		if err != nil {
			return registryProxyGate{outcome: registryProxyUnusable, manifest: manifest, reason: "registry proxy endpoint " + endpointName + " has a non-numeric port"}
		}
		effectiveSpawn = func(_ string, listenPort int) (int, error) {
			return spawnHTTPForwarder(host, upstreamPort, secret, listenPort)
		}
	default:
		// The zero Endpoint: registrymanifest.Parse succeeds (valid JSON)
		// but the "endpoint" field was itself absent from the payload, so
		// Endpoint never went through ParseEndpoint/UnmarshalJSON at all --
		// json.Unmarshal has no error to raise over a merely-missing field.
		return registryProxyGate{outcome: registryProxyUnusable, manifest: manifest, reason: "REGISTRY_PROXY_MANIFEST has no usable endpoint"}
	}

	ready, pid, err := bindregistry.EnsureForwarderReady(forwarderSocketArg, port, probe, effectiveSpawn, timeout, pollInterval)
	if err != nil {
		return registryProxyGate{outcome: registryProxyUnusable, manifest: manifest, reason: "registry proxy Forwarder for endpoint " + endpointName + " failed to start: " + err.Error()}
	}
	if !ready {
		return registryProxyGate{outcome: registryProxyUnusable, manifest: manifest, reason: "registry proxy Forwarder for endpoint " + endpointName + " did not start listening on 127.0.0.1:" + strconv.Itoa(port) + " within " + timeout.String()}
	}

	return registryProxyGate{outcome: registryProxyReady, manifest: manifest, port: port, pid: pid}
}

// resolveHomeConfigPath resolves row's HomeConfig to a concrete on-disk
// path and ensures its parent directory exists, shared by bindings mode
// (pre-clone, port/prefix-only render) and runBindRegistryRepoAwareHomeConfigs
// (post-clone, repo-aware render) so the two writes can never drift on home
// resolution (issue #3201). Returns ok == false, having already printed the
// failure, when neither row.HomeConfig.HomeEnvVar nor $HOME is set -- mirrors
// bash's `set -u`: an unset $HOME there would have died on expansion (e.g.
// `${GRADLE_USER_HOME:-$HOME/.gradle}`) rather than let string concatenation
// silently resolve to a relative path under the process's cwd, or -- for
// gradle -- the literal "/.gradle", an absolute root-level path that
// MkdirAll/WriteFile would happily create when running as root, claiming the
// ecosystem is bound at a path nothing will ever read from.
func resolveHomeConfigPath(stdout io.Writer, row ecosystem.Row) (string, bool) {
	hc := row.HomeConfig
	home := os.Getenv(hc.HomeEnvVar)
	if home == "" {
		home = os.Getenv("HOME")
		if home == "" {
			fmt.Fprintf(stdout, "driver-exec bind-registry: %s and HOME are both unset, cannot resolve a %s home\n", hc.HomeEnvVar, row.Name)
			return "", false
		}
		home = filepath.Join(home, hc.HomeRelativeDefault)
	}
	path := filepath.Join(home, hc.ConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(stdout, "driver-exec bind-registry: create %s home directory: %v\n", row.Name, err)
		return "", false
	}
	return path, true
}

// runBindRegistryBindings is bindings mode: it ports the deleted
// entrypoint.sh phase_registry_proxy_forwarder + phase_go_binding (see git
// history) into Go. gate is resolveRegistryProxyGate's shared result (issue
// #3141) -- transport-blind past that point, exactly as before: everything
// downstream of a registryProxyReady gate (the Go/npm/cargo/gradle bindings
// computation, keyed only on gate.port) is unchanged regardless of which
// transport the manifest named, since ecosystem tooling never needs to know.
func runBindRegistryBindings(stdout io.Writer, gate *registryProxyGate, bindingsEnvOutput string) int {
	switch gate.outcome {
	case registryProxyAbsent:
		return 0
	case registryProxyUnusable:
		fmt.Fprintln(stdout, "==> WARNING: "+gate.reason+" — "+ecosystemFallbackNames()+" will fall back to the public registry")
		return 0
	}
	port := gate.port

	// Bindings mode has no per-ecosystem route mapping (issue #3142) -- a
	// GOPROXY value, an npm registry URL, a cargo config.toml, and a gradle
	// init script can each only name ONE upstream, so they all bind to the
	// first manifest route's prefix, preserving the pre-prefix routes[0]
	// fallback semantics Host-header selection used to give any request with
	// Host 127.0.0.1. A missing route or empty prefix here is defensive only
	// -- the launcher always mints at least one route with a non-empty
	// prefix whenever it sets REGISTRY_PROXY_MANIFEST at all (see
	// internal/dispatch/box.go) -- but warn and skip rather than binding
	// every ecosystem to a "http://127.0.0.1:<port>/" URL that 404s at every
	// request under strict prefix routing.
	if len(gate.manifest.Routes) == 0 || gate.manifest.Routes[0].Prefix == "" {
		// Leaves the Forwarder EnsureForwarderReady already spawned running
		// idle for the rest of the dispatch, and never writes
		// bindingsEnvOutput -- both harmless: nothing here needs the
		// Forwarder torn down early, and entrypoint.sh sources an empty
		// mktemp file when the caller never populated it.
		fmt.Fprintln(stdout, "==> WARNING: registry proxy manifest carries no route prefix — "+ecosystemFallbackNames()+" will fall back to the public registry")
		return 0
	}
	prefix := gate.manifest.Routes[0].Prefix

	// Row-generic (rather than naming npm's/go's renderers): a future row
	// with an EnvExports renderer is picked up with no change here.
	// EnvExportRows hands them over already in export-file order, which is
	// not Table's own classification-precedence order.
	var exports []ecosystem.EnvExport
	var warnings []string
	for _, row := range ecosystem.EnvExportRows() {
		rowExports, rowWarnings := row.EnvExports(port, prefix, os.Getenv, gate.manifest.Routes)
		exports = append(exports, rowExports...)
		warnings = append(warnings, rowWarnings...)
	}

	if err := os.WriteFile(bindingsEnvOutput, []byte(renderEnvExports(exports)), 0o644); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: write bindings env output:", err)
		return 1
	}

	// homeConfigPaths records each row's written path, keyed by row name, so
	// bindingSummaryProse below can name a HomeConfig row's binding path
	// row-generically instead of one lookup per ecosystem. Only rows with a
	// non-nil HomeConfig land here, which is exactly the set
	// bindingSummaryProse falls back to this map for -- a row with its own
	// BindingEnvVar never looks itself up here -- so the lookup is never a
	// miss for a row bindingSummaryProse actually renders.
	homeConfigPaths := make(map[string]string)
	for _, row := range ecosystem.HomeConfigRows() {
		path, ok := resolveHomeConfigPath(stdout, row)
		if !ok {
			return 1
		}
		if err := os.WriteFile(path, []byte(row.HomeConfig.Render(port, prefix, gate.manifest.Routes)), 0o644); err != nil {
			fmt.Fprintf(stdout, "driver-exec bind-registry: write %s home config: %v\n", row.Name, err)
			return 1
		}
		homeConfigPaths[row.Name] = path
	}

	// The Go warning lines and both success lines only print here, after
	// every fallible write above (bindings-env-output, then each row's home
	// resolve/mkdir/write) has succeeded -- printing any of them earlier (as
	// this used to, issue #2931) would claim an override or a successful
	// binding even when a later write fails and the whole function returns
	// 1, which the caller (agent/entrypoint.sh's phase_registry_proxy_bindings)
	// treats as "nothing applied, skip sourcing entirely". Order matches the
	// old bash's own inline echoes: "Forwarder up" before "go bound". The
	// "go bound" line reads the already-computed GOPROXY export rather than
	// re-deriving http://127.0.0.1:<port>/<prefix> itself: under a
	// host-rooted route (issue #3260) that guess is either the wrong URL (a
	// "go"-tagged subtree renders a longer path) or an outright lie (zero or
	// ambiguous tagged paths leave GOPROXY unexported), so this line prints
	// only when the row actually rendered one.
	for _, w := range warnings {
		fmt.Fprintln(stdout, w)
	}
	forwarderLine := "==> registry proxy Forwarder up on 127.0.0.1:" + strconv.Itoa(port)
	if summary := bindingSummaryProse(exports, homeConfigPaths); summary != "" {
		forwarderLine += " — " + summary
	}
	fmt.Fprintln(stdout, forwarderLine)
	if goProxy, ok := ecosystem.ExportValue(exports, "GOPROXY"); ok {
		fmt.Fprintln(stdout, "==> go bound to it via GOPROXY="+goProxy)
	}

	return 0
}

// applyEachRow calls fn for every row in rows, continuing past a per-row
// error so one row's failure (e.g. an unmerged config file) never blocks
// its siblings, and reports whether any row failed.
func applyEachRow(rows []ecosystem.Row, fn func(ecosystem.Row) error) bool {
	failed := false
	for _, row := range rows {
		if err := fn(row); err != nil {
			failed = true
		}
	}
	return failed
}

// hostRewriteCollision names one upstream host that two or more manifest
// routes claim (issue #3142's blocking review finding): ApplyInTreeBinding
// matches rewrite candidates by bare host text in the config file, so it has
// no way to tell which of the colliding routes' prefixes a matched line
// belongs to. Prefixes is every colliding route's Prefix, in manifest table
// order.
type hostRewriteCollision struct {
	Host     string
	Prefixes []string
}

// buildIntreeHostRewrites projects every manifest route with a route
// upstream host into a bindregistry.HostRewrite (issue #3142), replacing the
// old routes[0]-only intreeUpstreamHost: the manifest's route list exists
// for the proxy's own per-prefix routing, and now the in-tree text rewrite
// loops over the same list, so a repo naming more than one route's upstream
// host gets every one of them rewritten, each to its own prefix-scoped
// LocalURL, in one ApplyInTreeBinding pass. A route missing either an
// upstream host or a prefix is skipped rather than producing a rewrite that
// can never match real content or that collides with every other prefixless
// route's LocalURL.
//
// A legal manifest shape -- e.g. one Artifactory host fronting separate npm
// and cargo path prefixes -- can still have two routes name the same
// UpstreamHost. Host-only text matching can't disambiguate which route a
// matched line belongs to, so every rewrite for a duplicated host is
// dropped, not just kept-first: keeping either one would silently rewrite
// the other route's config lines to the wrong LocalURL. The dropped hosts
// come back as the second return value so the caller can warn about them.
func buildIntreeHostRewrites(routes []registrymanifest.Route, port int) ([]bindregistry.HostRewrite, []hostRewriteCollision) {
	var hostOrder []string
	byHost := make(map[string][]registrymanifest.Route)
	for _, route := range routes {
		if route.UpstreamHost == "" || route.Prefix == "" {
			continue
		}
		if _, seen := byHost[route.UpstreamHost]; !seen {
			hostOrder = append(hostOrder, route.UpstreamHost)
		}
		byHost[route.UpstreamHost] = append(byHost[route.UpstreamHost], route)
	}

	var rewrites []bindregistry.HostRewrite
	var collisions []hostRewriteCollision
	for _, host := range hostOrder {
		group := byHost[host]
		if len(group) > 1 {
			prefixes := make([]string, len(group))
			for i, route := range group {
				prefixes[i] = route.Prefix
			}
			collisions = append(collisions, hostRewriteCollision{Host: host, Prefixes: prefixes})
			continue
		}
		route := group[0]
		rewrites = append(rewrites, bindregistry.HostRewrite{
			UpstreamHost: route.UpstreamHost,
			LocalURL:     ecosystem.RouteLocalURL(route, port),
		})
	}
	return rewrites, collisions
}

// rewriteHostNames renders every rewrite's UpstreamHost, comma-joined, for
// the ApplyNoopContent warning below -- issue #3142 generalizes that warning
// from naming a single upstream host to naming every host this call's
// rewrites list carried, since the noop check itself now tests content
// against all of them. Deduped, first-occurrence order preserved: since
// buildIntreeHostRewrites now drops every rewrite for a host two routes
// share, a duplicate host can't reach here in practice, but a repeated host
// would otherwise read as confusingly as "host.example, host.example".
func rewriteHostNames(rewrites []bindregistry.HostRewrite) string {
	seen := make(map[string]bool, len(rewrites))
	var hosts []string
	for _, rw := range rewrites {
		if seen[rw.UpstreamHost] {
			continue
		}
		seen[rw.UpstreamHost] = true
		hosts = append(hosts, rw.UpstreamHost)
	}
	return strings.Join(hosts, ", ")
}

// dropCollidedRoutes filters out every route whose UpstreamHost is one of
// collisions' hosts before handing routes to a row's placeholder deriver:
// buildIntreeHostRewrites already dropped that route's rewrite, so nothing
// in the rewritten config was ever pointed at that route's LocalURL, and its
// own manifest-declared CargoRegistries would otherwise still produce
// placeholders for a rewrite that never happened.
func dropCollidedRoutes(routes []registrymanifest.Route, collisions []hostRewriteCollision) []registrymanifest.Route {
	collidedHosts := make(map[string]bool, len(collisions))
	for _, c := range collisions {
		collidedHosts[c.Host] = true
	}

	var filtered []registrymanifest.Route
	for _, route := range routes {
		if collidedHosts[route.UpstreamHost] {
			continue
		}
		filtered = append(filtered, route)
	}
	return filtered
}

// runBindRegistryIntree is in-tree mode (issue #2932): it ports
// entrypoint.sh's deleted phase_cargo_intree_binding_apply/
// cargo_intree_binding_revert into Go, looping over every
// bindregistry.InTreeBindings() row -- npm, yarn, and pnpm today, cargo
// retired from this loop by issue #3201 in favor of
// runBindRegistryRepoAwareHomeConfigs -- so a future table row needs no
// change here. gate is resolveRegistryProxyGate's shared result (issue
// #3141); nil for action=="revert", which is a pure git operation that never
// needs the manifest or a live Forwarder.
func runBindRegistryIntree(stdout io.Writer, action, workDir string, gate *registryProxyGate) int {
	if action == "revert" {
		failed := applyEachRow(bindregistry.InTreeBindings(), func(row ecosystem.Row) error {
			reverted, err := bindregistry.RevertInTreeBinding(workDir, row)
			if err != nil {
				fmt.Fprintln(stdout, "driver-exec bind-registry: revert in-tree "+row.InTreeConfigPath+":", err)
				return err
			}
			if reverted {
				fmt.Fprintln(stdout, "==> in-tree "+row.Name+" config "+row.InTreeConfigPath+" restored and un-hidden from git")
			}
			return nil
		})
		if failed {
			return 1
		}
		return 0
	}

	// action == "apply" past this point (validated by the caller).
	switch gate.outcome {
	case registryProxyAbsent:
		// REGISTRY_PROXY_MANIFEST unset means the launcher never enabled the
		// registry proxy for this dispatch at all (internal/dispatch/box.go
		// only sets it when RegistryProxyRoutes is non-empty) -- the
		// overwhelmingly common no-proxy dispatch, and issue #3082's one
		// deliberate silence.
		return 0
	case registryProxyUnusable:
		fmt.Fprintln(stdout, "==> WARNING: "+gate.reason+" — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		return 0
	}

	port := gate.port
	rewrites, collisions := buildIntreeHostRewrites(gate.manifest.Routes, port)
	for _, c := range collisions {
		fmt.Fprintln(stdout, "==> WARNING: registry proxy manifest routes "+strings.Join(c.Prefixes, ", ")+" share upstream host "+c.Host+" — host-based in-tree rewriting cannot tell them apart, their in-tree registry rewrite is skipped, those ecosystems fall back to the public registry")
	}
	if len(rewrites) == 0 {
		// A collision warning already explained why every candidate was
		// dropped -- printing the generic "carries no route upstream host"
		// warning too would mislead, since the manifest does carry an
		// upstream host, it's just unusable for host-based rewriting.
		if len(collisions) == 0 {
			fmt.Fprintln(stdout, "==> WARNING: registry proxy manifest carries no route upstream host — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		}
		return 0
	}

	failed := applyEachRow(bindregistry.InTreeBindings(), func(row ecosystem.Row) error {
		outcome, err := bindregistry.ApplyInTreeBinding(workDir, row, rewrites)
		if err != nil {
			fmt.Fprintln(stdout, "driver-exec bind-registry: apply in-tree "+row.InTreeConfigPath+":", err)
			return err
		}
		switch outcome {
		case bindregistry.ApplyMissing:
			fmt.Fprintln(stdout, "==> "+row.Name+" config "+row.InTreeConfigPath+" not found — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		case bindregistry.ApplyNotRegular:
			fmt.Fprintln(stdout, "==> WARNING: "+row.Name+" config "+row.InTreeConfigPath+" exists but is not a regular file — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		case bindregistry.ApplyUntracked:
			fmt.Fprintln(stdout, "==> WARNING: "+row.Name+" config "+row.InTreeConfigPath+" exists but is not tracked by git — skipping the in-tree registry rewrite for it")
		case bindregistry.ApplySkipWorktreeSet:
			// See ApplySkipWorktreeSet's own doc (bindregistry package) for
			// why this differs from ApplyNoopContent's routine no-op.
			fmt.Fprintln(stdout, "==> WARNING: "+row.Name+" config "+row.InTreeConfigPath+" already has the skip-worktree bit set — its content was not re-checked, so if a prior run crashed between tagging the bit and rewriting the content, it may still point at the real upstream while hidden from git status")
		case bindregistry.ApplyNoopContent:
			fmt.Fprintln(stdout, "==> WARNING: "+row.Name+" config "+row.InTreeConfigPath+" no longer references upstream host "+rewriteHostNames(rewrites)+" — the in-tree registry rewrite is skipped, verify the registry proxy manifest's route upstream host is set correctly")
		case bindregistry.ApplyApplied:
			fmt.Fprintln(stdout, "==> in-tree "+row.Name+" config "+row.InTreeConfigPath+" rewritten to point at the local registry proxy Forwarder (127.0.0.1:"+strconv.Itoa(port)+") and hidden from git via skip-worktree")
		}
		return nil
	})

	if failed {
		return 1
	}

	return 0
}

// exportNames renders exports' Name fields, comma-joined, for
// runBindRegistryRepoAwareHomeConfigs' own success line -- the row-generic
// renderer contract (ecosystem.RepoAwareHomeConfigRenderer) hands this verb
// nothing but the rendered content and its EnvExports, so the export var
// names are the only thing the success line can name without this verb
// reaching back into an ecosystem-specific value of its own.
func exportNames(exports []ecosystem.EnvExport) string {
	names := make([]string, len(exports))
	for i, e := range exports {
		names[i] = e.Name
	}
	return strings.Join(names, ", ")
}

// joinProse renders names as an English list with an Oxford comma: "a" for
// one name, "a and b" for two, "a, b, and c" for three or more, so a row
// walked out of ecosystem.Table reads like operator prose rather than a raw
// comma-joined dump. An empty list yields "", which would leave a caller's
// warning reading "... — will fall back to the public registry"; no caller
// today can reach that, since every list here derives from ecosystem.Table,
// which is never empty.
func joinProse(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

// rowNames collects rows' Name fields for joinProse, so the two call sites
// that name a row subset share one collector.
func rowNames(rows []ecosystem.Row) []string {
	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = row.Name
	}
	return names
}

// ecosystemFallbackNames renders every ecosystem.Table row's Name in
// Table order, for both of bindings mode's fallback warnings: every known
// ecosystem falls back to the public registry when the gate is closed, not
// just whichever subset used to be hand-listed at each call site.
func ecosystemFallbackNames() string {
	return joinProse(rowNames(ecosystem.Table))
}

// bindingSummaryProse renders the success summary's "<name> bound to it via
// <where>" clauses, walking ecosystem.Table in order. ecosystem.Row's
// BindingEnvVar doc owns the rule for which of a row's two bindings names it
// here; a row declaring neither contributes nothing.
//
// A BindingEnvVar row is named only when exports actually carries its var,
// because a row's exports are route-conditional: a host-rooted route with no
// path tagged for that ecosystem renders no export at all (go, issue #3260;
// the npm family, issue #3259), and naming the var anyway would advertise a
// binding the child process will not have. A HomeConfig row needs no such
// test -- its file is written unconditionally on this path.
//
// go therefore appears here as "go bound to it via GOPROXY" as well as on
// the separate "==> go bound to it via GOPROXY=<url>" line printed right
// after this one: that line carries the resolved URL, which this summary
// does not, and its place after the summary is pinned by issue #2931. The
// two now stand or fall together, both keyed off the same rendered export.
//
// The result is empty when no row bound anything, which the caller renders
// as the bare "Forwarder up" line rather than one trailing an empty "— ".
func bindingSummaryProse(exports []ecosystem.EnvExport, homeConfigPaths map[string]string) string {
	var fragments []string
	for _, row := range ecosystem.Table {
		switch {
		case row.BindingEnvVar != "":
			if _, ok := ecosystem.ExportValue(exports, row.BindingEnvVar); !ok {
				continue
			}
			fragments = append(fragments, row.Name+" bound to it via "+row.BindingEnvVar)
		case row.HomeConfig != nil:
			fragments = append(fragments, row.Name+" bound to it via "+homeConfigPaths[row.Name])
		}
	}
	return joinProse(fragments)
}

// repoAwareHomeConfigRows is the row subset runBindRegistryRepoAwareHomeConfigs
// governs. It filters HomeConfigRows() rather than Table because the renderer
// re-renders the row's own HomeConfig file; ecosystem.Row's doc pins the
// pairing (a non-nil RepoAwareHomeConfig requires a non-nil HomeConfig) that
// makes the two equivalent.
func repoAwareHomeConfigRows() []ecosystem.Row {
	var rows []ecosystem.Row
	for _, row := range ecosystem.HomeConfigRows() {
		if row.RepoAwareHomeConfig != nil {
			rows = append(rows, row)
		}
	}
	return rows
}

// runBindRegistryRepoAwareHomeConfigs is the post-clone half of in-tree
// apply mode (issue #3201): unlike runBindRegistryIntree's tracked-file
// rewrite, a row carrying a non-nil RepoAwareHomeConfig (cargo today) binds
// by re-rendering its whole home-level config once the Target repo is on
// disk, keying its rewrite off the repo's own un-rewritten in-tree config
// file -- unreadable at bindings mode's pre-clone render time, which is why
// this can't just run there instead. gate is resolveRegistryProxyGate's
// shared result; the caller only invokes this on action=="apply", so gate is
// always non-nil here.
//
// Route collisions are dropped exactly as runBindRegistryIntree's own
// tracked-file rewrite drops them (buildIntreeHostRewrites +
// dropCollidedRoutes) -- host-only matching can't disambiguate a repo
// registry's upstream host between two routes sharing it any better than it
// can disambiguate a tracked-file rewrite -- but the collision warnings
// themselves are not re-printed here: runBindRegistryIntree, called first for
// the same manifest, already printed them once.
func runBindRegistryRepoAwareHomeConfigs(stdout io.Writer, workDir string, gate *registryProxyGate, envOutput string) int {
	switch gate.outcome {
	case registryProxyAbsent:
		return 0
	case registryProxyUnusable:
		fmt.Fprintln(stdout, "==> WARNING: "+gate.reason+" — the repo-aware registry binding is skipped, ecosystems fall back to the public registry")
		return 0
	}
	port := gate.port

	// Only repo-aware rows are at stake in this phase, so the warning names
	// them from the table rather than a hardcoded list: a second such row
	// landing must not leave the message overstating the fallback.
	repoAwareRows := repoAwareHomeConfigRows()
	if len(repoAwareRows) == 0 {
		return 0
	}

	if len(gate.manifest.Routes) == 0 || gate.manifest.Routes[0].Prefix == "" {
		names := joinProse(rowNames(repoAwareRows))
		fmt.Fprintln(stdout, "==> WARNING: registry proxy manifest carries no route prefix — "+names+" will fall back to the public registry")
		return 0
	}
	prefix := gate.manifest.Routes[0].Prefix

	_, collisions := buildIntreeHostRewrites(gate.manifest.Routes, port)
	routes := dropCollidedRoutes(gate.manifest.Routes, collisions)

	var exports []ecosystem.EnvExport
	failed := false
	for _, row := range repoAwareRows {
		raw, err := os.ReadFile(filepath.Join(workDir, row.InTreeConfigPath))
		var repoConfig string
		switch {
		case err == nil:
			repoConfig = string(raw)
		case os.IsNotExist(err):
			// A repo with no tracked in-tree config declares no named
			// registry -- the overwhelmingly common case, not an error (see
			// this function's own doc).
		default:
			fmt.Fprintf(stdout, "driver-exec bind-registry: read %s repo config: %v\n", row.Name, err)
			failed = true
			continue
		}

		content, rowExports, warnings := row.RepoAwareHomeConfig(port, prefix, routes, repoConfig)
		for _, w := range warnings {
			fmt.Fprintln(stdout, w)
		}

		path, ok := resolveHomeConfigPath(stdout, row)
		if !ok {
			failed = true
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			fmt.Fprintf(stdout, "driver-exec bind-registry: write %s home config: %v\n", row.Name, err)
			failed = true
			continue
		}

		if len(rowExports) > 0 {
			fmt.Fprintln(stdout, "==> "+row.Name+" home config "+path+" re-rendered from the repo's own "+row.InTreeConfigPath+" to bind its named registries to the local registry proxy Forwarder (127.0.0.1:"+strconv.Itoa(port)+"), exporting "+exportNames(rowExports))
		}
		exports = append(exports, rowExports...)
	}

	if failed {
		return 1
	}

	if envOutput != "" {
		if err := os.WriteFile(envOutput, []byte(renderEnvExports(exports)), 0o644); err != nil {
			fmt.Fprintln(stdout, "driver-exec bind-registry: write intree bindings env output:", err)
			return 1
		}
	}

	return 0
}
