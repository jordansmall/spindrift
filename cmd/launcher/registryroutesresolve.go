package main

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrypathset"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryroutes"
)

// buildRegistryProxyRoutes resolves dispatch.Config's registry-proxy route
// table (issue #3139) from the routes file (ADR 0045). AssignPrefixes runs
// here, not in the parser, so every production table carries a Prefix (issue
// #3142) while the parser stays testable alone. rows is the Strategies to
// resolve over: ecosystem.Registered() in production.
func buildRegistryProxyRoutes(c config, rows []ecosystem.Strategy) ([]registryproxy.Route, error) {
	if c.RegistryProxyRoutesFile == "" {
		return nil, nil
	}
	routes, err := resolveRegistryRoutesFromFile(c.RegistryProxyRoutesFile, rows)
	if err != nil {
		return nil, err
	}
	routes, err = resolveHostRootedUpstreams(c, routes, rows)
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
func resolveHostRootedUpstreams(c config, routes []registryproxy.Route, rows []ecosystem.Strategy) ([]registryproxy.Route, error) {
	if len(routes) == 0 {
		return routes, nil
	}
	hosts := make([]string, len(routes))
	for i, r := range routes {
		hosts[i] = r.MatchHost
	}

	sets, err := deriveHostRootedPathSets(c, hosts, rows)
	if err != nil {
		return nil, err
	}
	byHost := make(map[string]registrypathset.HostPathSet, len(sets))
	for _, s := range sets {
		byHost[s.Host] = s
	}

	resolved := make([]registryproxy.Route, len(routes))
	for i, r := range routes {
		route, err := registrypathset.ApplyToRoute(r, byHost, rows)
		if err != nil {
			return nil, err
		}
		resolved[i] = route
	}
	return resolved, nil
}

// deriveHostRootedPathSets keys on c.CodeForge's HostMediatedRemote row rather
// than comparing c.CodeForge against "local", which a future host-mediated
// backend would miss. Such a forge derives from the Accumulation repo's
// baseBranch ref (issue #3310), the one snapshot no in-Box agent can widen, and
// skips checkoutIsTargetRepo, whose contract is remote-based identity.
func deriveHostRootedPathSets(c config, hostRootedHosts []string, rows []ecosystem.Strategy) ([]registrypathset.HostPathSet, error) {
	row, _ := backendkit.ByName(c.CodeForge)
	if row.HostMediatedRemote {
		accumDir := c.hostRepoDir()
		sets, err := registrypathset.DeriveFromGitRef(accumDir, c.BaseBranch, rows)
		if err != nil {
			return nil, fmt.Errorf("registry proxy: host-rooted route(s) %s need the Accumulation repo's %q branch to derive their enforced path-set, and deriving it from %q failed: %w", strings.Join(hostRootedHosts, ", "), c.BaseBranch, accumDir, err)
		}
		return sets, nil
	}

	repoDir, err := registryRouteDriftRepoDirFn()
	if err != nil || repoDir == "" || !checkoutIsTargetRepo(repoDir, c) {
		return nil, fmt.Errorf("registry proxy: host-rooted route(s) %s need a Target-repo checkout to derive their enforced path-set, and none is available here; run inside a checkout of the Target repo", strings.Join(hostRootedHosts, ", "))
	}

	sets, err := registrypathset.Derive(repoDir, rows)
	if err != nil {
		return nil, fmt.Errorf("registry proxy: deriving enforced path-set from %q: %w", repoDir, err)
	}
	return sets, nil
}

// resolveRegistryRoutesFromFile parses routesFile (ADR 0045) and resolves each
// route's credential exactly once: resolving an env-var source unsets it, so a
// second resolve would find nothing. A failure names the route's match-host.
//
// Resolution runs in two file-order passes (issue #3151): pass 1 resolves, and
// so unsets, every route whose credential consumes a process env var
// (credresolver.ResolveUnsetsEnv); pass 2 resolves the rest, exec sources
// included. Every env var the file names is thus gone before any exec helper
// runs, whatever the route order. As a consequence, when an env-sourced and a
// non-env route would both fail, the env failure always surfaces first.
func resolveRegistryRoutesFromFile(routesFile string, rows []ecosystem.Strategy) ([]registryproxy.Route, error) {
	data, err := os.ReadFile(routesFile)
	if err != nil {
		return nil, fmt.Errorf("reading REGISTRY_PROXY_ROUTES_FILE %q: %w", routesFile, err)
	}
	parsed, err := registryroutes.Parse(data, rows)
	if err != nil {
		return nil, err
	}

	routes := make([]registryproxy.Route, len(parsed))
	for i, r := range parsed {
		routes[i] = r.ProxyRoute()
	}

	// resolvePass resolves every route whose credresolver.ResolveUnsetsEnv
	// matches unsetsEnv, skipping the rest.
	resolvePass := func(unsetsEnv bool) error {
		for i, r := range parsed {
			if credresolver.ResolveUnsetsEnv(r.Credential) != unsetsEnv {
				continue
			}
			cred, err := credresolver.New(r.Credential).Resolve()
			if err != nil {
				return fmt.Errorf("resolving credential for route %q: %w", r.MatchHost, err)
			}
			routes[i].Credential = cred
		}
		return nil
	}
	if err := resolvePass(true); err != nil {
		return nil, err
	}
	if err := resolvePass(false); err != nil {
		return nil, err
	}
	return routes, nil
}
