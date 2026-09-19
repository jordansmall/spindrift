package main

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrypathset"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryroutes"
	"spindrift.dev/launcher/internal/registryvocab"
)

// buildRegistryProxyRoutes resolves dispatch.Config's registry-proxy route
// table (issue #3139) from the routes file (ADR 0045), the only input since
// issue #3145 retired the scalar REGISTRY_PROXY_* knobs. AssignPrefixes runs
// here, not in the file parser, so every production table carries a Prefix
// (issue #3142) while the parser stays testable on its own.
func buildRegistryProxyRoutes(c config) ([]registryproxy.Route, error) {
	if c.registryProxyRoutesFile == "" {
		return nil, nil
	}
	routes, err := resolveRegistryRoutesFromFile(c.registryProxyRoutesFile)
	if err != nil {
		return nil, err
	}
	routes, err = resolveHostRootedUpstreams(c, routes)
	if err != nil {
		return nil, err
	}
	return registryproxy.AssignPrefixes(routes), nil
}

// resolveHostRootedUpstreams fills Upstream and EnforcedPaths for every route
// (all host-rooted since ADR 0047, issue #3261). Derivation runs even for a
// route declaring its own upstream-origin: that origin replaces the derived
// origin, never the derived subtrees, so what a route enforces never depends
// on whether a checkout was reachable. Every failure fails the launch closed.
func resolveHostRootedUpstreams(c config, routes []registryproxy.Route) ([]registryproxy.Route, error) {
	if len(routes) == 0 {
		return routes, nil
	}
	hosts := make([]string, len(routes))
	for i, r := range routes {
		hosts[i] = r.MatchHost
	}

	sets, err := deriveHostRootedPathSets(c, hosts)
	if err != nil {
		return nil, err
	}
	byHost := make(map[string]registrypathset.HostPathSet, len(sets))
	for _, s := range sets {
		byHost[s.Host] = s
	}

	resolved := make([]registryproxy.Route, len(routes))
	for i, r := range routes {
		route, err := applyHostPathSet(r, byHost)
		if err != nil {
			return nil, err
		}
		resolved[i] = route
	}
	return resolved, nil
}

// deriveHostRootedPathSets keys on c.codeForge's HostMediatedRemote row rather
// than comparing c.codeForge against "local", which a future host-mediated
// backend would miss. Such a forge derives from the Accumulation repo's
// baseBranch ref (issue #3310), the one snapshot no in-Box agent can widen, and
// skips checkoutIsTargetRepo, whose contract is remote-based identity.
func deriveHostRootedPathSets(c config, hostRootedHosts []string) ([]registrypathset.HostPathSet, error) {
	row, _ := backendByName(c.codeForge)
	if row.HostMediatedRemote {
		sets, err := registrypathset.DeriveFromGitRef(c.codeForgeAccumulationRepoDir, c.baseBranch)
		if err != nil {
			return nil, fmt.Errorf("registry proxy: host-rooted route(s) %s need the Accumulation repo's %q branch to derive their enforced path-set, and deriving it from %q failed: %w", strings.Join(hostRootedHosts, ", "), c.baseBranch, c.codeForgeAccumulationRepoDir, err)
		}
		return sets, nil
	}

	repoDir, err := registryRouteDriftRepoDirFn()
	if err != nil || repoDir == "" || !checkoutIsTargetRepo(repoDir, c) {
		return nil, fmt.Errorf("registry proxy: host-rooted route(s) %s need a Target-repo checkout to derive their enforced path-set, and none is available here; run inside a checkout of the Target repo", strings.Join(hostRootedHosts, ", "))
	}

	sets, err := registrypathset.Derive(repoDir)
	if err != nil {
		return nil, fmt.Errorf("registry proxy: deriving enforced path-set from %q: %w", repoDir, err)
	}
	return sets, nil
}

// applyHostPathSet projects the HostPathSet matching route's match-host onto
// route. Upstream loses any trailing "/", since registryproxy.New rejects a
// host-rooted Upstream carrying a path. A route naming a host absent from sets
// and declaring no upstream-origin of its own is an error, never a route left
// unenforced; one declaring an origin enforces only what it declares itself.
func applyHostPathSet(route registryproxy.Route, sets map[string]registrypathset.HostPathSet) (registryproxy.Route, error) {
	hp, ok := sets[registryvocab.HostKey(route.MatchHost)]
	if !ok && route.UpstreamOrigin == "" {
		if label := declaredPathAloneLabel(route); label != "" {
			return registryproxy.Route{}, fmt.Errorf("registry proxy: route %q is host-rooted but the Target repo declares no registry on that host; %s alone cannot establish a host-rooted route's upstream origin -- declare a discoverable npm/yarn/pnpm/cargo registry on this host in the Target repo's committed config, or set the route's upstream-origin", route.MatchHost, label)
		}
		return registryproxy.Route{}, fmt.Errorf("registry proxy: route %q is host-rooted but the Target repo declares no registry on that host; declare a discoverable registry on this host in the Target repo's committed config, or set the route's upstream-origin", route.MatchHost)
	}
	route.Upstream = strings.TrimSuffix(hp.Origin, "/")
	if route.UpstreamOrigin != "" {
		route.Upstream = route.UpstreamOrigin
	}
	paths := make([]string, len(hp.Subtrees))
	// Field-by-field, not a copy of sub: this drops RegistryName, which the
	// proxy's manifest-facing EnforcedSubtrees has never carried.
	subtrees := make([]registryvocab.Subtree, len(hp.Subtrees))
	for i, sub := range hp.Subtrees {
		paths[i] = sub.Path
		subtrees[i] = registryvocab.Subtree{Ecosystem: sub.Ecosystem, Path: sub.Path}
	}
	derived := make(map[string]bool, len(paths))
	for _, p := range paths {
		derived[p] = true
	}
	for _, allow := range route.Allow {
		if derived[allow] {
			continue
		}
		derived[allow] = true
		paths = append(paths, allow)
	}
	// EnforcedPaths dedupes, EnforcedSubtrees does not: a declared path keeps
	// its own tagged entry even when it duplicates a derived or allow path,
	// because the Forwarder keys a rewrite row's bases off that tag (see
	// routeState.basesByEcosystem in registryproxy), not off EnforcedPaths.
	for _, d := range declaredPaths(route) {
		subtrees = append(subtrees, registryvocab.Subtree{Ecosystem: d.ecosystem, Path: d.path})
		if !derived[d.path] {
			derived[d.path] = true
			paths = append(paths, d.path)
		}
	}
	route.EnforcedPaths = paths
	route.EnforcedSubtrees = subtrees
	return route, nil
}

// declaredPath is one ecosystem.Table row's declared path (issue #3403), read
// out of route.Ecosystems with the tag its EnforcedSubtrees entry carries.
type declaredPath struct {
	ecosystem string
	path      string
}

// declaredPaths walks ecosystem.Table in Table's own order (issue #3403), not
// route.Ecosystems' map, because Go map iteration is random and both callers
// need this enumeration deterministic.
func declaredPaths(route registryproxy.Route) []declaredPath {
	var set []declaredPath
	for _, row := range ecosystem.Table {
		if path := route.Ecosystems.Path(row.Name); path != "" {
			set = append(set, declaredPath{ecosystem: row.Name, path: path})
		}
	}
	return set
}

// declaredPathAloneLabel names route's declared paths by their
// [routes.ecosystems.<name>] spelling, for applyHostPathSet's error message.
func declaredPathAloneLabel(route registryproxy.Route) string {
	var labels []string
	for _, d := range declaredPaths(route) {
		labels = append(labels, registryvocab.RouteDeclarationKeyLabel(d.ecosystem, registryvocab.RouteDeclarationPathKey))
	}
	return strings.Join(labels, " and ")
}

// resolveRegistryRoutesFromFile parses routesFile (ADR 0045) and resolves each
// route's credential exactly once: resolving an env-var source is destructive
// (os.Unsetenv on success), so a second resolve would find nothing. A failure
// names the offending route's match-host.
func resolveRegistryRoutesFromFile(routesFile string) ([]registryproxy.Route, error) {
	data, err := os.ReadFile(routesFile)
	if err != nil {
		return nil, fmt.Errorf("reading REGISTRY_PROXY_ROUTES_FILE %q: %w", routesFile, err)
	}
	parsed, err := registryroutes.Parse(data)
	if err != nil {
		return nil, err
	}
	routes := make([]registryproxy.Route, 0, len(parsed))
	for _, r := range parsed {
		cred, err := credresolver.New(r.Credential).Resolve()
		if err != nil {
			return nil, fmt.Errorf("resolving credential for route %q: %w", r.MatchHost, err)
		}
		routes = append(routes, registryproxy.Route{
			MatchHost:      r.MatchHost,
			AuthScheme:     r.AuthScheme,
			Credential:     cred,
			Ecosystems:     r.Ecosystems,
			UpstreamOrigin: r.UpstreamOrigin,
			Allow:          r.Allow,
			// resolveHostRootedUpstreams fills Upstream and EnforcedPaths;
			// it needs the whole route slice and a Target-repo checkout,
			// which this function does not depend on.
		})
	}
	return routes, nil
}
