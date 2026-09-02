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
	"time"

	"spindrift.dev/launcher/internal/bindregistry"
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
//     reverts the in-tree config-file rewrite (e.g. cargo's
//     .cargo/config.toml) that points a tracked ecosystem config file at the
//     local Forwarder instead of the real upstream registry, gated on the
//     same Forwarder readiness bindings mode uses.
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
	intreeBindingsEnvOutput := fs.String("intree-bindings-env-output", "", "path to write the sourceable cargo-registry-placeholder env file to (optional, pairs with -intree-work-dir/-intree-action=apply)")
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
	if *workDir == "" && *ecosystemEnvOutput == "" && *bindingsEnvOutput == "" && *intreeWorkDir == "" && *intreeAction == "" {
		fmt.Fprintln(stdout, "driver-exec bind-registry: at least one of -work-dir/-ecosystem-env-output, -bindings-env-output, or -intree-work-dir/-intree-action is required")
		return 1
	}

	if *workDir != "" {
		if rc := runBindRegistryClassification(stdout, *workDir, *ecosystemEnvOutput); rc != 0 {
			return rc
		}
	}

	// Resolved once, only when a mode that actually needs a live Forwarder
	// will run this invocation -- a classification-only or intree-revert-only
	// call never touches REGISTRY_PROXY_MANIFEST or the probe/spawn deps at
	// all, matching their pre-#3141 behavior exactly.
	var gate *registryProxyGate
	if *intreeAction == "apply" || *bindingsEnvOutput != "" {
		g := resolveRegistryProxyGate(probe, spawn, lookPath, timeout, pollInterval)
		gate = &g
	}

	if *intreeAction != "" {
		if rc := runBindRegistryIntree(stdout, *intreeAction, *intreeWorkDir, gate, *intreeBindingsEnvOutput); rc != 0 {
			return rc
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

// isMountedSocket reports whether path exists and is a unix socket -- the
// `[ -S "$path" ]`-equivalent guard resolveRegistryProxyGate uses to confirm
// a manifest's unix endpoint is actually reachable in this Box before ever
// probing/spawning against it.
func isMountedSocket(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

// renderEnvExports renders exports into `export NAME=VALUE\n` lines, one per
// entry, shared by bindings mode's own env output and intree-apply's cargo
// registry placeholder env output below.
//
// %q emits Go quoting, not shell quoting -- safe here only because both
// callers' values are always port-derived (http://127.0.0.1:<port>/...),
// bindregistry's own fixed constant strings ("none", "off", "local",
// CargoPlaceholderToken, ...), or the current callers' input, never
// attacker- or repo-controlled input, so %q's Go-quoting output happens to
// still be valid shell input for this file's later `source` by
// agent/entrypoint.sh -- a property of the current callers, not a general
// guarantee.
func renderEnvExports(exports []bindregistry.EnvExport) string {
	var rendered string
	for _, e := range exports {
		rendered += fmt.Sprintf("export %s=%q\n", e.Name, e.Value)
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
		effectiveSpawn = func(_ string, listenPort int) error {
			return spawnHTTPForwarder(host, upstreamPort, secret, listenPort)
		}
	default:
		// The zero Endpoint: registrymanifest.Parse succeeds (valid JSON)
		// but the "endpoint" field was itself absent from the payload, so
		// Endpoint never went through ParseEndpoint/UnmarshalJSON at all --
		// json.Unmarshal has no error to raise over a merely-missing field.
		return registryProxyGate{outcome: registryProxyUnusable, manifest: manifest, reason: "REGISTRY_PROXY_MANIFEST has no usable endpoint"}
	}

	ready, err := bindregistry.EnsureForwarderReady(forwarderSocketArg, port, probe, effectiveSpawn, timeout, pollInterval)
	if err != nil {
		return registryProxyGate{outcome: registryProxyUnusable, manifest: manifest, reason: "registry proxy Forwarder for endpoint " + endpointName + " failed to start: " + err.Error()}
	}
	if !ready {
		return registryProxyGate{outcome: registryProxyUnusable, manifest: manifest, reason: "registry proxy Forwarder for endpoint " + endpointName + " did not start listening on 127.0.0.1:" + strconv.Itoa(port) + " within " + timeout.String()}
	}

	return registryProxyGate{outcome: registryProxyReady, manifest: manifest, port: port}
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
		fmt.Fprintln(stdout, "==> WARNING: "+gate.reason+" — cargo, npm, pnpm, yarn, and gradle will fall back to the public registry")
		return 0
	}
	port := gate.port

	goBindings := bindregistry.ComputeGoBindings(port, bindregistry.GoBindingInput{
		GOTOOLCHAIN: os.Getenv("GOTOOLCHAIN"),
		GONOPROXY:   os.Getenv("GONOPROXY"),
		GOPRIVATE:   os.Getenv("GOPRIVATE"),
		GOSUMDB:     os.Getenv("GOSUMDB"),
		GONOSUMDB:   os.Getenv("GONOSUMDB"),
	})

	exports := append(append([]bindregistry.EnvExport{}, goBindings.Exports...), bindregistry.NpmFamilyBindings(port)...)

	if err := os.WriteFile(bindingsEnvOutput, []byte(renderEnvExports(exports)), 0o644); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: write bindings env output:", err)
		return 1
	}

	cargoHome := os.Getenv("CARGO_HOME")
	if cargoHome == "" {
		home := os.Getenv("HOME")
		if home == "" {
			// Mirrors bash's `set -u`: an unset $HOME there would have died
			// on expansion rather than let filepath.Join("", ".cargo")
			// silently resolve to a relative ".cargo" under the process's
			// cwd -- the wrong location, with no error.
			fmt.Fprintln(stdout, "driver-exec bind-registry: CARGO_HOME and HOME are both unset, cannot resolve a cargo home")
			return 1
		}
		cargoHome = filepath.Join(home, ".cargo")
	}
	if err := os.MkdirAll(cargoHome, 0o755); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: create cargo home:", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(cargoHome, "config.toml"), []byte(bindregistry.CargoConfigTOML(port)), 0o644); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: write cargo config:", err)
		return 1
	}

	gradleUserHome := os.Getenv("GRADLE_USER_HOME")
	if gradleUserHome == "" {
		home := os.Getenv("HOME")
		if home == "" {
			// Mirrors bash's `set -u`: an unset $HOME there would have died
			// on expansion of `${GRADLE_USER_HOME:-$HOME/.gradle}` rather
			// than let string concatenation silently resolve to the literal
			// "/.gradle" -- an absolute root-level path that MkdirAll/
			// WriteFile below would happily create when running as root,
			// claiming gradle is bound at a path nothing will ever read
			// from. Matches cargo's own both-unset guard above.
			fmt.Fprintln(stdout, "driver-exec bind-registry: GRADLE_USER_HOME and HOME are both unset, cannot resolve a gradle home")
			return 1
		}
		gradleUserHome = filepath.Join(home, ".gradle")
	}
	gradleInitDir := filepath.Join(gradleUserHome, "init.d")
	if err := os.MkdirAll(gradleInitDir, 0o755); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: create gradle init.d:", err)
		return 1
	}
	gradleInitScript := filepath.Join(gradleInitDir, "spindrift-registry-proxy.init.gradle")
	if err := os.WriteFile(gradleInitScript, []byte(bindregistry.GradleInitScript(port)), 0o644); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: write gradle init script:", err)
		return 1
	}

	// The Go warning lines and both success lines only print here, after
	// every fallible write above (bindings-env-output, cargo home
	// resolve/mkdir, cargo config.toml, gradle init.d resolve/mkdir/init
	// script) has succeeded -- printing any of them earlier (as this used
	// to, issue #2931) would claim an override or a successful binding even
	// when a later write fails and the whole function returns 1, which the
	// caller (agent/entrypoint.sh's phase_registry_proxy_bindings) treats as
	// "nothing applied, skip sourcing entirely". Order matches the old
	// bash's own inline echoes: "Forwarder up" before "go bound".
	for _, w := range goBindings.Warnings {
		fmt.Fprintln(stdout, w)
	}
	fmt.Fprintln(stdout, "==> registry proxy Forwarder up on 127.0.0.1:"+strconv.Itoa(port)+" — cargo bound to it via "+cargoHome+"/config.toml, npm bound to it via npm_config_registry, pnpm bound to it via pnpm_config_registry, yarn berry bound to it via YARN_NPM_REGISTRY_SERVER, and gradle bound to it via "+gradleInitScript)
	fmt.Fprintln(stdout, "==> go bound to it via GOPROXY=http://127.0.0.1:"+strconv.Itoa(port))

	return 0
}

// applyEachRow calls fn for every row in rows, continuing past a per-row
// error so one row's failure (e.g. an unmerged config file) never blocks
// its siblings, and reports whether any row failed.
func applyEachRow(rows []bindregistry.InTreeBinding, fn func(bindregistry.InTreeBinding) error) bool {
	failed := false
	for _, row := range rows {
		if err := fn(row); err != nil {
			failed = true
		}
	}
	return failed
}

// intreeUpstreamHost returns the single upstream host intree-apply rewrites
// tracked config files away from. ApplyInTreeBinding's own signature takes
// one upstreamHost string, not a per-route list -- a single-route assumption
// this slice keeps rather than growing (issue #3141): the manifest's route
// list exists for the proxy's own per-prefix routing, but the in-tree text
// rewrite has always pointed every ecosystem's config at exactly one
// upstream, so routes[0]'s host is the one read here. A manifest with more
// than one route is a real config the proxy itself already routes
// correctly; any route past the first is simply invisible to intree-apply's
// rewrite until a later slice teaches ApplyInTreeBinding to loop over routes
// too.
func intreeUpstreamHost(routes []registrymanifest.Route) string {
	if len(routes) == 0 {
		return ""
	}
	return routes[0].UpstreamHost
}

// runBindRegistryIntree is in-tree mode (issue #2932): it ports
// entrypoint.sh's deleted phase_cargo_intree_binding_apply/
// cargo_intree_binding_revert into Go, looping over every
// bindregistry.InTreeBindings() row rather than hardcoding cargo, so a
// future table row needs no change here. gate is resolveRegistryProxyGate's
// shared result (issue #3141); nil for action=="revert", which is a pure git
// operation that never needs the manifest or a live Forwarder.
func runBindRegistryIntree(stdout io.Writer, action, workDir string, gate *registryProxyGate, intreeBindingsEnvOutput string) int {
	if action == "revert" {
		failed := applyEachRow(bindregistry.InTreeBindings(), func(row bindregistry.InTreeBinding) error {
			reverted, err := bindregistry.RevertInTreeBinding(workDir, row)
			if err != nil {
				fmt.Fprintln(stdout, "driver-exec bind-registry: revert in-tree "+row.ConfigPath+":", err)
				return err
			}
			if reverted {
				fmt.Fprintln(stdout, "==> in-tree "+row.Ecosystem+" config "+row.ConfigPath+" restored and un-hidden from git")
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

	upstreamHost := intreeUpstreamHost(gate.manifest.Routes)
	if upstreamHost == "" {
		fmt.Fprintln(stdout, "==> WARNING: registry proxy manifest carries no route upstream host — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		return 0
	}

	port := gate.port
	localURL := "http://127.0.0.1:" + strconv.Itoa(port)
	var cargoExports []bindregistry.EnvExport
	failed := applyEachRow(bindregistry.InTreeBindings(), func(row bindregistry.InTreeBinding) error {
		outcome, err := bindregistry.ApplyInTreeBinding(workDir, row, upstreamHost, localURL)
		if err != nil {
			fmt.Fprintln(stdout, "driver-exec bind-registry: apply in-tree "+row.ConfigPath+":", err)
			return err
		}
		switch outcome {
		case bindregistry.ApplyMissing:
			fmt.Fprintln(stdout, "==> "+row.Ecosystem+" config "+row.ConfigPath+" not found — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		case bindregistry.ApplyNotRegular:
			fmt.Fprintln(stdout, "==> WARNING: "+row.Ecosystem+" config "+row.ConfigPath+" exists but is not a regular file — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		case bindregistry.ApplyUntracked:
			fmt.Fprintln(stdout, "==> WARNING: "+row.Ecosystem+" config "+row.ConfigPath+" exists but is not tracked by git — skipping the in-tree registry rewrite for it")
		case bindregistry.ApplySkipWorktreeSet:
			// See ApplySkipWorktreeSet's own doc (bindregistry package) for
			// why this differs from ApplyNoopContent's routine no-op.
			fmt.Fprintln(stdout, "==> WARNING: "+row.Ecosystem+" config "+row.ConfigPath+" already has the skip-worktree bit set — its content was not re-checked, so if a prior run crashed between tagging the bit and rewriting the content, it may still point at the real upstream while hidden from git status")
		case bindregistry.ApplyNoopContent:
			fmt.Fprintln(stdout, "==> WARNING: "+row.Ecosystem+" config "+row.ConfigPath+" no longer references upstream host "+upstreamHost+" — the in-tree registry rewrite is skipped, verify the registry proxy manifest's route upstream host is set correctly")
		case bindregistry.ApplyApplied:
			fmt.Fprintln(stdout, "==> in-tree "+row.Ecosystem+" config "+row.ConfigPath+" rewritten to point at the local registry proxy Forwarder (127.0.0.1:"+strconv.Itoa(port)+") and hidden from git via skip-worktree")

			if row.Ecosystem == "cargo" {
				content, err := os.ReadFile(filepath.Join(workDir, row.ConfigPath))
				if err != nil {
					fmt.Fprintln(stdout, "driver-exec bind-registry: read rewritten cargo config "+row.ConfigPath+":", err)
					return err
				}
				names := bindregistry.ParseCargoRegistryNames(string(content), localURL)
				cargoExports = append(cargoExports, bindregistry.CargoRegistryPlaceholders(names)...)
			}
		}
		return nil
	})

	if failed {
		return 1
	}

	if intreeBindingsEnvOutput != "" {
		if err := os.WriteFile(intreeBindingsEnvOutput, []byte(renderEnvExports(cargoExports)), 0o644); err != nil {
			fmt.Fprintln(stdout, "driver-exec bind-registry: write intree bindings env output:", err)
			return 1
		}
	}

	return 0
}
