package bindregistry

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// LookPathFunc is ResolveGate's injectable seam for the socat-on-PATH
// check (issue #3141). Tests must stub it: the Nix go-test sandbox has no socat, so
// a real exec.LookPath("socat") fails there though it succeeds on a developer machine.
type LookPathFunc func(file string) (string, error)

// TCPSpawnFunc starts the TCP Forwarder (issue #3111) on 127.0.0.1:listenPort relaying
// to upstreamHost:upstreamPort and returns its pid. It is injected so tests never
// invoke the real one, which re-execs a binary detached.
type TCPSpawnFunc func(upstreamHost string, upstreamPort int, secret string, listenPort int) (int, error)

// GateDeps are ResolveGate's injected dependencies.
type GateDeps struct {
	Probe        ProbeFunc
	Spawn        SpawnFunc
	SpawnTCP     TCPSpawnFunc
	LookPath     LookPathFunc
	Timeout      time.Duration
	PollInterval time.Duration
}

// PublishFunc receives the env exports a bind mode computed, at the point the
// driver-exec verb writes its env file: BindHomes calls it before any home config is
// written, ApplyInTree after every write succeeded. It returns false, having printed
// why, to fail the mode. A nil PublishFunc is skipped.
type PublishFunc func([]ecosystem.EnvExport) bool

// gateOutcome classifies how ResolveGate settled (#3141).
type gateOutcome int

const (
	// gateAbsent is an unset REGISTRY_PROXY_MANIFEST: the registry
	// proxy is off for this dispatch and every mode gated on the manifest
	// stays silent, the deliberate silence of issue #3082.
	gateAbsent gateOutcome = iota
	// gateUnusable is a manifest present but not deliverable as a
	// live Forwarder: malformed manifest, an unmounted unix socket, missing
	// socat, a missing REGISTRY_PROXY_TCP_SECRET, or EnsureForwarderReady
	// failing. Every gated mode warns, naming the endpoint or the parse
	// error, then skips its rewrite entirely, never a partial one (#3112).
	gateUnusable
	// gateReady is a confirmed-listening Forwarder at gate.port.
	gateReady
)

// Gate is ResolveGate's result: computed at most once per invocation and shared by
// BindHomes and ApplyInTree, so neither redoes the other's work or double-spawns the
// Forwarder. The zero Gate is the absent one.
type Gate struct {
	outcome  gateOutcome
	manifest registrymanifest.Manifest
	port     int
	// pid is set only on a gateReady outcome reached through an
	// actual spawn; zero on every other outcome, including the
	// already-ready short-circuit, which spawns nothing.
	pid int
	// reason explains a gateUnusable outcome, always naming the
	// endpoint or the parse error. Callers append their own mode-specific
	// "...skipped" tail.
	reason string
}

// isMountedSocket confirms a manifest's unix endpoint is actually reachable in
// this Box before ResolveGate probes or spawns against it.
func isMountedSocket(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

// ResolveGate parses REGISTRY_PROXY_MANIFEST (ADR 0045) exactly once
// and ensures the Forwarder it describes is listening, spawning only if probe finds
// none, so an invocation running both gated modes still probes and spawns once.
func ResolveGate(w io.Writer, deps GateDeps) Gate {
	g := resolveGate(deps)

	// Logs the detached Forwarder child's PID so an operator can find it in the
	// run log; the one call site both intree-apply and bindings modes share.
	// Silent on the already-ready short-circuit (g.pid == 0): nothing spawned.
	if g.outcome == gateReady && g.pid != 0 {
		fmt.Fprintln(w, "==> registry proxy Forwarder pid "+strconv.Itoa(g.pid))
	}
	return g
}

func resolveGate(deps GateDeps) Gate {
	probe, lookPath, timeout := deps.Probe, deps.LookPath, deps.Timeout
	manifest, err := registrymanifest.Parse(os.Getenv(registrymanifest.EnvVar))
	if err != nil {
		if errors.Is(err, registrymanifest.ErrAbsent) {
			return Gate{outcome: gateAbsent}
		}
		return Gate{outcome: gateUnusable, reason: "REGISTRY_PROXY_MANIFEST is malformed: " + err.Error()}
	}

	port := ForwarderPort
	endpointName := manifest.Endpoint.String()
	forwarderSocketArg := ""
	effectiveSpawn := deps.Spawn

	switch {
	case manifest.Endpoint.IsUnix():
		socketPath := manifest.Endpoint.SocketPath()
		if !isMountedSocket(socketPath) {
			return Gate{outcome: gateUnusable, manifest: manifest, reason: "registry proxy endpoint " + endpointName + " is not mounted"}
		}
		forwarderSocketArg = socketPath
		// The socat PATH check gates only the spawn path: an already-ready
		// Forwarder needs socat not at all, so checking unconditionally would
		// wrongly warn and skip everything for a Forwarder that is already up.
		if !probe(port) {
			if _, err := lookPath("socat"); err != nil {
				return Gate{outcome: gateUnusable, manifest: manifest, reason: "registry proxy endpoint " + endpointName + " is mounted but socat is not on PATH"}
			}
		}
	case manifest.Endpoint.IsTCP():
		// A TCP endpoint means the launcher decided the unix socket cannot
		// cross into this Box (issue #3111). SpawnTCP re-execs a binary rather
		// than shelling out, so there is no PATH check to gate here: a missing
		// binary surfaces as an EnsureForwarderReady failure (warn-and-skip). The
		// launcher only mints a TCP endpoint together with
		// REGISTRY_PROXY_TCP_SECRET, so a missing secret is a misconfiguration.
		secret := os.Getenv("REGISTRY_PROXY_TCP_SECRET")
		if secret == "" {
			return Gate{outcome: gateUnusable, manifest: manifest, reason: "registry proxy endpoint " + endpointName + " requires REGISTRY_PROXY_TCP_SECRET, which is not set"}
		}
		host := manifest.Endpoint.Host()
		upstreamPort, err := strconv.Atoi(manifest.Endpoint.Port())
		if err != nil {
			return Gate{outcome: gateUnusable, manifest: manifest, reason: "registry proxy endpoint " + endpointName + " has a non-numeric port"}
		}
		effectiveSpawn = func(_ string, listenPort int) (int, error) {
			return deps.SpawnTCP(host, upstreamPort, secret, listenPort)
		}
	default:
		// The zero Endpoint: Parse succeeds on valid JSON whose "endpoint"
		// field was absent, so Endpoint never went through ParseEndpoint and
		// json.Unmarshal had no error to raise over a missing field.
		return Gate{outcome: gateUnusable, manifest: manifest, reason: "REGISTRY_PROXY_MANIFEST has no usable endpoint"}
	}

	ready, pid, err := EnsureForwarderReady(forwarderSocketArg, port, probe, effectiveSpawn, timeout, deps.PollInterval)
	if err != nil {
		return Gate{outcome: gateUnusable, manifest: manifest, reason: "registry proxy Forwarder for endpoint " + endpointName + " failed to start: " + err.Error()}
	}
	if !ready {
		return Gate{outcome: gateUnusable, manifest: manifest, reason: "registry proxy Forwarder for endpoint " + endpointName + " did not start listening on 127.0.0.1:" + strconv.Itoa(port) + " within " + timeout.String()}
	}

	return Gate{outcome: gateReady, manifest: manifest, port: port, pid: pid}
}

// resolveHomeConfigPath resolves row's HomeConfig to an on-disk path and creates its
// parent directory, shared by BindHomes and repoAwareHomeConfigs
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

// BindHomes is bindings mode: it computes the Go, npm, cargo and gradle env exports
// from gate.port, hands them to publish, then writes each HomeConfig row's home config.
// publish runs before the home writes because the verb's env file was always written
// first, so a failing home write still leaves it behind; it is not called when the
// gate is closed or the manifest carries no route prefix. It returns false on a failed
// publish or write. Everything downstream of a gateReady gate is transport-blind,
// since ecosystem tooling never needs to know which transport the manifest named
// (issue #3141).
func BindHomes(stdout io.Writer, gate Gate, publish PublishFunc) bool {
	switch gate.outcome {
	case gateAbsent:
		return true
	case gateUnusable:
		fmt.Fprintln(stdout, "==> WARNING: "+gate.reason+" — "+ecosystemFallbackNames()+" will fall back to the public registry")
		return true
	}
	port := gate.port

	// A GOPROXY value, an npm registry URL, a cargo config.toml and a gradle
	// init script can each name only ONE upstream, so all bind to the first
	// route's prefix (issue #3142), preserving the pre-prefix routes[0]
	// fallback. A missing route or empty prefix is defensive only, but warn
	// and skip rather than bind everything to a URL that 404s every request.
	if len(gate.manifest.Routes) == 0 || gate.manifest.Routes[0].Prefix == "" {
		// Leaves any already-spawned Forwarder running idle and never calls
		// publish. Both are harmless: the verb's env file simply stays empty.
		fmt.Fprintln(stdout, "==> WARNING: registry proxy manifest carries no route prefix — "+ecosystemFallbackNames()+" will fall back to the public registry")
		return true
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

	if publish != nil && !publish(exports) {
		return false
	}

	// homeConfigPaths lets bindingSummaryProse name a HomeConfig row's binding
	// path row-generically. Only rows with a non-nil HomeConfig land here, which
	// is exactly the set that summary looks up, so its lookup never misses.
	homeConfigPaths := make(map[string]string)
	for _, row := range ecosystem.HomeConfigRows() {
		path, ok := resolveHomeConfigPath(stdout, row)
		if !ok {
			return false
		}
		if err := os.WriteFile(path, []byte(row.HomeConfig.Render(port, prefix, gate.manifest.Routes)), 0o644); err != nil {
			fmt.Fprintf(stdout, "driver-exec bind-registry: write %s home config: %v\n", row.Name, err)
			return false
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

	return true
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
// host and a prefix into a HostRewrite (issue #3142); a route missing
// either is skipped. When two routes share an upstream host, as one Artifactory host
// fronting separate npm and cargo prefixes does, every rewrite for that host is
// dropped rather than kept-first, since keeping either rewrites the other wrongly.
func buildIntreeHostRewrites(routes []registrymanifest.Route, port int) ([]HostRewrite, []hostRewriteCollision) {
	// Keys fold case to match ApplyInTreeBinding's own duplicate guard (issue
	// #3706); the collision report uses each group's first-seen spelling.
	var keyOrder []string
	byHost := make(map[string][]registrymanifest.Route)
	for _, route := range routes {
		if route.UpstreamHost == "" || route.Prefix == "" {
			continue
		}
		key := FoldHost(route.UpstreamHost)
		if _, seen := byHost[key]; !seen {
			keyOrder = append(keyOrder, key)
		}
		byHost[key] = append(byHost[key], route)
	}

	var rewrites []HostRewrite
	var collisions []hostRewriteCollision
	for _, key := range keyOrder {
		group := byHost[key]
		if len(group) > 1 {
			prefixes := make([]string, len(group))
			for i, route := range group {
				prefixes[i] = route.Prefix
			}
			collisions = append(collisions, hostRewriteCollision{Host: group[0].UpstreamHost, Prefixes: prefixes})
			continue
		}
		route := group[0]
		rewrites = append(rewrites, HostRewrite{
			UpstreamHost: route.UpstreamHost,
			LocalURL:     ecosystem.RouteLocalURL(route.Prefix, port),
		})
	}
	return rewrites, collisions
}

// rewriteHostNames renders every rewrite's UpstreamHost, comma-joined, for the
// ApplyNoopContent warning below (issue #3142). Deduped because a repeated host
// would read as "host.example, host.example", though none can reach here today.
func rewriteHostNames(rewrites []HostRewrite) string {
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
		collidedHosts[FoldHost(c.Host)] = true
	}

	var filtered []registrymanifest.Route
	for _, route := range routes {
		if collidedHosts[FoldHost(route.UpstreamHost)] {
			continue
		}
		filtered = append(filtered, route)
	}
	return filtered
}

// ApplyInTree is in-tree apply mode (issue #2932) followed by the repo-aware home
// configs (issue #3201): it rewrites every tracked in-tree registry config the
// manifest names, then re-renders each repo-aware home config, handing their env
// exports to publish once every write succeeded (publish is not called when the
// gate is closed or nothing repo-aware applies). The repo-aware half is skipped
// when the in-tree apply failed. It returns false on any failure. Revert is
// RevertInTreeBindings, which needs neither the manifest nor a Forwarder.
func ApplyInTree(stdout io.Writer, workDir string, gate Gate, publish PublishFunc) bool {
	if !applyInTree(stdout, workDir, gate) {
		return false
	}
	return applyRepoAwareHomeConfigs(stdout, workDir, gate, publish)
}

// applyInTree loops over every InTreeBindings() row (npm, yarn and pnpm today, cargo
// retired from this loop by issue #3201), so a future table row needs no change here.
func applyInTree(stdout io.Writer, workDir string, gate Gate) bool {
	switch gate.outcome {
	case gateAbsent:
		// An unset REGISTRY_PROXY_MANIFEST means the launcher never enabled
		// the registry proxy for this dispatch: the common no-proxy case, and
		// issue #3082's one deliberate silence.
		return true
	case gateUnusable:
		fmt.Fprintln(stdout, "==> WARNING: "+gate.reason+" — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		return true
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
		return true
	}

	failed := applyEachRow(InTreeBindings(), func(row ecosystem.Row) error {
		outcome, err := ApplyInTreeBinding(workDir, row, rewrites)
		if err != nil {
			fmt.Fprintln(stdout, "driver-exec bind-registry: apply in-tree "+row.InTreeConfigPath+":", err)
			return err
		}
		switch outcome {
		case ApplyMissing:
			fmt.Fprintln(stdout, "==> "+row.Name+" config "+row.InTreeConfigPath+" not found — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		case ApplyNotRegular:
			fmt.Fprintln(stdout, "==> WARNING: "+row.Name+" config "+row.InTreeConfigPath+" exists but is not a regular file — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry")
		case ApplyUntracked:
			fmt.Fprintln(stdout, "==> WARNING: "+row.Name+" config "+row.InTreeConfigPath+" exists but is not tracked by git — skipping the in-tree registry rewrite for it")
		case ApplySkipWorktreeSet:
			fmt.Fprintln(stdout, "==> WARNING: "+row.Name+" config "+row.InTreeConfigPath+" already has the skip-worktree bit set — its content was not re-checked, so if a prior run crashed between tagging the bit and rewriting the content, it may still point at the real upstream while hidden from git status")
		case ApplyNoopContent:
			fmt.Fprintln(stdout, "==> WARNING: "+row.Name+" config "+row.InTreeConfigPath+" no longer references upstream host "+rewriteHostNames(rewrites)+" — the in-tree registry rewrite is skipped, verify the registry proxy manifest's route upstream host is set correctly")
		case ApplyApplied:
			fmt.Fprintln(stdout, "==> in-tree "+row.Name+" config "+row.InTreeConfigPath+" rewritten to point at the local registry proxy Forwarder (127.0.0.1:"+strconv.Itoa(port)+") and hidden from git via skip-worktree")
		}
		return nil
	})

	return !failed
}

// exportNames names the export vars for applyRepoAwareHomeConfigs' success line, the
// only thing the row-generic renderer contract hands this verb that it can name
// without an ecosystem-specific value of its own.
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

// applyRepoAwareHomeConfigs is the post-clone half of in-tree apply mode
// (issue #3201): a row with a non-nil RepoAwareHomeConfig (cargo today) binds by
// re-rendering its whole home config against the repo's own un-rewritten in-tree
// config, which bindings mode cannot read pre-clone. Collided routes are dropped as
// applyInTree drops them, but it ran first and printed those warnings.
func applyRepoAwareHomeConfigs(stdout io.Writer, workDir string, gate Gate, publish PublishFunc) bool {
	switch gate.outcome {
	case gateAbsent:
		return true
	case gateUnusable:
		fmt.Fprintln(stdout, "==> WARNING: "+gate.reason+" — the repo-aware registry binding is skipped, ecosystems fall back to the public registry")
		return true
	}
	port := gate.port

	// The warning names the repo-aware rows from the table rather than a
	// hardcoded list, so a second such row landing cannot leave it
	// overstating the fallback.
	repoAwareRows := repoAwareHomeConfigRows()
	if len(repoAwareRows) == 0 {
		return true
	}

	if len(gate.manifest.Routes) == 0 || gate.manifest.Routes[0].Prefix == "" {
		names := joinProse(rowNames(repoAwareRows))
		fmt.Fprintln(stdout, "==> WARNING: registry proxy manifest carries no route prefix — "+names+" will fall back to the public registry")
		return true
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
		return false
	}

	return publish == nil || publish(exports)
}
