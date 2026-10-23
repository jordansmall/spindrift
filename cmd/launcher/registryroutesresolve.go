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
// table (issue #3139) from the routes file (ADR 0045), the only input since
// issue #3145 retired the scalar REGISTRY_PROXY_* knobs. AssignPrefixes runs
// here, not in the file parser, so every production table carries a Prefix
// (issue #3142) while the parser stays testable on its own.
// rows is the ecosystem table to resolve over: production passes
// ecosystem.Table, tests a synthetic row.
func buildRegistryProxyRoutes(c config, rows []ecosystem.Strategy) ([]registryproxy.Route, error) {
	if c.registryProxyRoutesFile == "" {
		return nil, nil
	}
	routes, err := resolveRegistryRoutesFromFile(c.registryProxyRoutesFile, rows)
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

// deriveHostRootedPathSets keys on c.codeForge's HostMediatedRemote row rather
// than comparing c.codeForge against "local", which a future host-mediated
// backend would miss. Such a forge derives from the Accumulation repo's
// baseBranch ref (issue #3310), the one snapshot no in-Box agent can widen, and
// skips checkoutIsTargetRepo, whose contract is remote-based identity.
func deriveHostRootedPathSets(c config, hostRootedHosts []string, rows []ecosystem.Strategy) ([]registrypathset.HostPathSet, error) {
	row, _ := backendkit.ByName(c.codeForge)
	if row.HostMediatedRemote {
		accumDir := c.hostRepoDir()
		sets, err := registrypathset.DeriveFromGitRefRows(accumDir, c.baseBranch, rows)
		if err != nil {
			return nil, fmt.Errorf("registry proxy: host-rooted route(s) %s need the Accumulation repo's %q branch to derive their enforced path-set, and deriving it from %q failed: %w", strings.Join(hostRootedHosts, ", "), c.baseBranch, accumDir, err)
		}
		return sets, nil
	}

	repoDir, err := registryRouteDriftRepoDirFn()
	if err != nil || repoDir == "" || !checkoutIsTargetRepo(repoDir, c) {
		return nil, fmt.Errorf("registry proxy: host-rooted route(s) %s need a Target-repo checkout to derive their enforced path-set, and none is available here; run inside a checkout of the Target repo", strings.Join(hostRootedHosts, ", "))
	}

	sets, err := registrypathset.DeriveRows(repoDir, rows)
	if err != nil {
		return nil, fmt.Errorf("registry proxy: deriving enforced path-set from %q: %w", repoDir, err)
	}
	return sets, nil
}

// resolveRegistryRoutesFromFile parses routesFile (ADR 0045) and resolves each
// route's credential exactly once: resolving an env-var source is destructive
// (os.Unsetenv on success), so a second resolve would find nothing. A failure
// names the offending route's match-host.
//
// Resolving parsed's credentials runs in two passes, both in file order
// (issue #3151): pass 1 resolves (and so unsets) every route whose
// credential consumes a process env var (credresolver.ResolveUnsetsEnv),
// before pass 2 resolves everything else, including exec sources. A single
// file-order pass would let an exec route listed before an env route run
// while that env var was still set, so which secrets a credential helper can
// observe would depend on where its route happened to sit in the routes
// file. Pass 1 removes that dependency: every env var this file names is
// gone from the process environment before any exec helper runs, whatever
// order the routes appear in. One consequence: when both an env-sourced and
// a non-env route would fail to resolve, the env route's failure always
// surfaces first, because pass 1 runs to completion before pass 2 starts.
func resolveRegistryRoutesFromFile(routesFile string, rows []ecosystem.Strategy) ([]registryproxy.Route, error) {
	data, err := os.ReadFile(routesFile)
	if err != nil {
		return nil, fmt.Errorf("reading REGISTRY_PROXY_ROUTES_FILE %q: %w", routesFile, err)
	}
	parsed, err := registryroutes.ParseRows(data, rows)
	if err != nil {
		return nil, err
	}

	routes := make([]registryproxy.Route, len(parsed))
	for i, r := range parsed {
		routes[i] = r.ProxyRoute()
	}

	// resolvePass resolves every route whose credresolver.ResolveUnsetsEnv
	// matches unsetsEnv, skipping the rest; the two calls below are what
	// give parsed its two file-order passes (see the doc comment above).
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
