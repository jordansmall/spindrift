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

// lookPathFunc is resolveRegistryProxyGate's injectable seam for the socat-on-PATH
// check (issue #3141). Tests must stub it: the Nix go-test sandbox has no socat, so
// a real exec.LookPath("socat") fails there though it succeeds on a developer machine.
type lookPathFunc func(file string) (string, error)

// runBindRegistryWithDeps parses the flags and runs whichever of three modes they
// select: classification, bindings, and in-tree (issue #2932). Any mode's flags may
// be given alone or with another's. probe, spawn, timeout and pollInterval are
// injected so tests can exercise the readiness paths without a real socat or
// listener. Bindings and in-tree apply share one gate, so neither respawns (#3141).
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
	// apply's repo-aware home-config render must be the last writer of a
	// repo-aware row's HomeConfig file: bindings mode in the same invocation
	// would re-render every HomeConfig row from the base template afterward and
	// clobber apply's replacement stanzas. revert renders nothing, so revert
	// plus bindings stays legal.
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

	// Resolved only when a mode that needs a live Forwarder will run, so a
	// classification-only or revert-only call never touches
	// REGISTRY_PROXY_MANIFEST or the probe/spawn deps at all (issue #3141).
	var gate *registryProxyGate
	if *intreeAction == "apply" || *bindingsEnvOutput != "" {
		g := resolveRegistryProxyGate(probe, spawn, lookPath, timeout, pollInterval)
		gate = &g

		// A bats teardown needs the Setsid-detached Forwarder child's PID to
		// kill it directly, and this is the one call site both intree-apply
		// mode and bindings mode share. Silent on the already-ready
		// short-circuit (g.pid == 0): nothing was spawned.
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

// runBindRegistryClassification is classification mode (issue #2930): it classifies
// workDir's lockfiles and writes the sourceable NUDGE_ECOSYSTEM env file.
func runBindRegistryClassification(stdout io.Writer, workDir, ecosystemEnvOutput string) int {
	classification := bindregistry.Classify(workDir)

	// %q emits Go quoting, not shell quoting. Safe here only because
	// classification is always one of bindregistry's own constants, never
	// attacker- or repo-controlled input.
	env := fmt.Sprintf("NUDGE_ECOSYSTEM=%q\n", classification)
	if err := os.WriteFile(ecosystemEnvOutput, []byte(env), 0o644); err != nil {
		fmt.Fprintln(stdout, "driver-exec bind-registry: write ecosystem env output:", err)
		return 1
	}

	return 0
}

// runBindRegistryLockfileScan is lockfile-scan mode (issue #3199): at settle it warns
// about any git-tracked lockfile still naming the run's Forwarder URL, a stale pin
// that would otherwise ship silently in the PR. It parses REGISTRY_PROXY_MANIFEST
// directly and never calls resolveRegistryProxyGate, which probes and can spawn the
// Forwarder; a settle-time scan must never do that. Every failure path only warns.
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

// isMountedSocket confirms a manifest's unix endpoint is actually reachable in
// this Box before resolveRegistryProxyGate probes or spawns against it.
func isMountedSocket(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
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

// spawnHTTPForwarder is an indirection over bindregistry.SpawnHTTPForwarder (issue
// #3111) so tests never invoke the real one: it re-execs os.Executable() detached,
// which under go test is this package's test binary, relaunching the whole suite.
var spawnHTTPForwarder = bindregistry.SpawnHTTPForwarder

// registryProxyGateOutcome classifies how resolveRegistryProxyGate settled (#3141).
type registryProxyGateOutcome int

const (
	// registryProxyAbsent is an unset REGISTRY_PROXY_MANIFEST: the registry
	// proxy is off for this dispatch and every mode gated on the manifest
	// stays silent, the deliberate silence of issue #3082.
	registryProxyAbsent registryProxyGateOutcome = iota
	// registryProxyUnusable is a manifest present but not deliverable as a
	// live Forwarder: malformed manifest, an unmounted unix socket, missing
	// socat, a missing REGISTRY_PROXY_TCP_SECRET, or EnsureForwarderReady
	// failing. Every gated mode warns, naming the endpoint or the parse
	// error, then skips its rewrite entirely, never a partial one (#3112).
	registryProxyUnusable
	// registryProxyReady is a confirmed-listening Forwarder at gate.port.
	registryProxyReady
)

// registryProxyGate is resolveRegistryProxyGate's result: computed at most once per
// invocation and shared by bindings mode and intree-apply mode, so neither redoes
// the other's work or double-spawns the Forwarder.
type registryProxyGate struct {
	outcome  registryProxyGateOutcome
	manifest registrymanifest.Manifest
	port     int
	// pid is set only on a registryProxyReady outcome reached through an
	// actual spawn; zero on every other outcome, including the
	// already-ready short-circuit, which spawns nothing.
	pid int
	// reason explains a registryProxyUnusable outcome, always naming the
	// endpoint or the parse error. Callers append their own mode-specific
	// "...skipped" tail.
	reason string
}

// resolveRegistryProxyGate parses REGISTRY_PROXY_MANIFEST (ADR 0045) exactly once
// and ensures the Forwarder it describes is listening, spawning only if probe finds
// none, so an invocation running both gated modes still probes and spawns once.
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
		// The socat PATH check gates only the spawn path: an already-ready
		// Forwarder needs socat not at all, so checking unconditionally would
		// wrongly warn and skip everything for a Forwarder that is already up.
		if !probe(port) {
			if _, err := lookPath("socat"); err != nil {
				return registryProxyGate{outcome: registryProxyUnusable, manifest: manifest, reason: "registry proxy endpoint " + endpointName + " is mounted but socat is not on PATH"}
			}
		}
	case manifest.Endpoint.IsTCP():
		// A TCP endpoint means the launcher decided the unix socket cannot
		// cross into this Box (issue #3111). spawnHTTPForwarder re-execs this
		// same binary rather than shelling out, so there is no PATH check to
		// gate here. The launcher only mints a TCP endpoint together with
		// REGISTRY_PROXY_TCP_SECRET, so a missing secret is a misconfiguration.
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
		// The zero Endpoint: Parse succeeds on valid JSON whose "endpoint"
		// field was absent, so Endpoint never went through ParseEndpoint and
		// json.Unmarshal had no error to raise over a missing field.
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

// resolveHomeConfigPath resolves row's HomeConfig to an on-disk path and creates its
// parent directory, shared by bindings mode and runBindRegistryRepoAwareHomeConfigs
// so the two writes cannot drift (issue #3201). It returns ok == false, having
// printed why, when both the row's home var and $HOME are unset: concatenation would
// otherwise resolve under the cwd, or to "/.gradle", which MkdirAll creates as root.
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

// runBindRegistryBindings is bindings mode: it computes and writes the Go, npm,
// cargo and gradle bindings from gate.port. Everything downstream of a
// registryProxyReady gate is transport-blind, since ecosystem tooling never needs to
// know which transport the manifest named (issue #3141).
func runBindRegistryBindings(stdout io.Writer, gate *registryProxyGate, bindingsEnvOutput string) int {
	switch gate.outcome {
	case registryProxyAbsent:
		return 0
	case registryProxyUnusable:
		fmt.Fprintln(stdout, "==> WARNING: "+gate.reason+" — "+ecosystemFallbackNames()+" will fall back to the public registry")
		return 0
	}
	port := gate.port

	// A GOPROXY value, an npm registry URL, a cargo config.toml and a gradle
	// init script can each name only ONE upstream, so all bind to the first
	// route's prefix (issue #3142), preserving the pre-prefix routes[0]
	// fallback. A missing route or empty prefix is defensive only, but warn
	// and skip rather than bind everything to a URL that 404s every request.
	if len(gate.manifest.Routes) == 0 || gate.manifest.Routes[0].Prefix == "" {
		// Leaves any already-spawned Forwarder running idle and never writes
		// bindingsEnvOutput. Both are harmless: entrypoint.sh sources an
		// empty mktemp file when the caller never populated it.
		fmt.Fprintln(stdout, "==> WARNING: registry proxy manifest carries no route prefix — "+ecosystemFallbackNames()+" will fall back to the public registry")
		return 0
	}
	prefix := gate.manifest.Routes[0].Prefix

	// Row-generic, so a future row with an EnvExports renderer needs no change
	// here. EnvExportRows hands them over in export-file order, which is not
	// Table's own classification-precedence order.
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

	// homeConfigPaths lets bindingSummaryProse name a HomeConfig row's binding
	// path row-generically. Only rows with a non-nil HomeConfig land here, which
	// is exactly the set that summary looks up, so its lookup never misses.
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

	// These lines print only after every fallible write above has succeeded:
	// printing earlier (issue #2931) would claim a binding even when a later
	// write fails and the caller treats the run as "nothing applied". The "go
	// bound" line reads the computed GOPROXY export rather than re-deriving the
	// URL, which a host-rooted route (issue #3260) makes wrong or absent.
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

// applyEachRow calls fn for every row, continuing past a per-row error so one row's
// failure never blocks its siblings, and reports whether any row failed.
func applyEachRow(rows []ecosystem.Row, fn func(ecosystem.Row) error) bool {
	failed := false
	for _, row := range rows {
		if err := fn(row); err != nil {
			failed = true
		}
	}
	return failed
}

// hostRewriteCollision names one upstream host that two or more manifest routes
// claim (issue #3142): ApplyInTreeBinding matches candidates by bare host text, so
// it cannot tell which colliding route's prefix a matched line belongs to.
type hostRewriteCollision struct {
	Host     string
	Prefixes []string
}

// buildIntreeHostRewrites projects every manifest route carrying both an upstream
// host and a prefix into a bindregistry.HostRewrite (issue #3142); a route missing
// either is skipped. When two routes share an upstream host, as one Artifactory host
// fronting separate npm and cargo prefixes does, every rewrite for that host is
// dropped rather than kept-first, since keeping either rewrites the other wrongly.
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
			LocalURL:     ecosystem.RouteLocalURL(route.Prefix, port),
		})
	}
	return rewrites, collisions
}

// rewriteHostNames renders every rewrite's UpstreamHost, comma-joined, for the
// ApplyNoopContent warning below (issue #3142). Deduped because a repeated host
// would read as "host.example, host.example", though none can reach here today.
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

// dropCollidedRoutes removes every route whose UpstreamHost collided before a row's
// placeholder deriver sees it: buildIntreeHostRewrites already dropped that route's
// rewrite, so nothing in the config points at its LocalURL.
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

// runBindRegistryIntree is in-tree mode (issue #2932): it loops over every
// bindregistry.InTreeBindings() row (npm, yarn and pnpm today, cargo retired from
// this loop by issue #3201), so a future table row needs no change here. gate is
// nil for action=="revert", a pure git operation that needs neither the manifest
// nor a live Forwarder.
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
		// An unset REGISTRY_PROXY_MANIFEST means the launcher never enabled
		// the registry proxy for this dispatch: the common no-proxy case, and
		// issue #3082's one deliberate silence.
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
		// dropped; the generic "carries no route upstream host" warning would
		// mislead, since the manifest does carry one, just an unusable one.
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

// exportNames names the export vars for runBindRegistryRepoAwareHomeConfigs'
// success line, the only thing the row-generic renderer contract hands this verb
// that it can name without an ecosystem-specific value of its own.
func exportNames(exports []ecosystem.EnvExport) string {
	names := make([]string, len(exports))
	for i, e := range exports {
		names[i] = e.Name
	}
	return strings.Join(names, ", ")
}

// joinProse renders names as an English list with an Oxford comma. An empty list
// yields "", which no caller reaches today: every list here comes from
// ecosystem.Table, which is never empty.
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

func rowNames(rows []ecosystem.Row) []string {
	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = row.Name
	}
	return names
}

// ecosystemFallbackNames renders every ecosystem.Table row's Name in Table order:
// when the gate is closed, every known ecosystem falls back to the public registry.
func ecosystemFallbackNames() string {
	return joinProse(rowNames(ecosystem.Table))
}

// bindingSummaryProse renders the success summary's "<name> bound to it via <where>"
// clauses in ecosystem.Table order. A BindingEnvVar row is named only when exports
// actually carries its var, because a host-rooted route can render no export for
// that ecosystem (issues #3259, #3260) and naming it would advertise a binding the
// child process will not have. An empty result leaves the caller's bare line.
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

// repoAwareHomeConfigRows filters HomeConfigRows() rather than Table because the
// renderer re-renders the row's own HomeConfig file; a non-nil RepoAwareHomeConfig
// requires a non-nil HomeConfig, which makes the two equivalent.
func repoAwareHomeConfigRows() []ecosystem.Row {
	var rows []ecosystem.Row
	for _, row := range ecosystem.HomeConfigRows() {
		if row.RepoAwareHomeConfig != nil {
			rows = append(rows, row)
		}
	}
	return rows
}

// runBindRegistryRepoAwareHomeConfigs is the post-clone half of in-tree apply mode
// (issue #3201): a row with a non-nil RepoAwareHomeConfig (cargo today) binds by
// re-rendering its whole home config against the repo's own un-rewritten in-tree
// config, which bindings mode cannot read pre-clone. Collided routes are dropped as
// runBindRegistryIntree drops them, but it ran first and printed those warnings.
func runBindRegistryRepoAwareHomeConfigs(stdout io.Writer, workDir string, gate *registryProxyGate, envOutput string) int {
	switch gate.outcome {
	case registryProxyAbsent:
		return 0
	case registryProxyUnusable:
		fmt.Fprintln(stdout, "==> WARNING: "+gate.reason+" — the repo-aware registry binding is skipped, ecosystems fall back to the public registry")
		return 0
	}
	port := gate.port

	// The warning names the repo-aware rows from the table rather than a
	// hardcoded list, so a second such row landing cannot leave it
	// overstating the fallback.
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
			// registry, the common case, not an error.
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
