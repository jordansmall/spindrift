package main

import (
	"fmt"
	"net"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/registrypathset"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryroutes"
)

// buildRegistryProxyRoutes is the single synthesis point for
// dispatch.Config's resolved registry-proxy route table (issue #3139): a
// routes file (ADR 0045), when set, is parsed and every route's credential
// resolved. With no routes file, the registry proxy stays off -- there is no
// other input this function looks at (issue #3145 retired the five scalar
// REGISTRY_PROXY_* knobs' bridge-route synthesis; validateRetiredRegistryProxyKnobs
// already refuses a run where any of them is still set, before this runs).
//
// AssignPrefixes runs here, once, over resolveRegistryRoutesFromFile's
// result, so every production route table carries a Prefix (issue #3142)
// while resolveRegistryRoutesFromFile stays testable on its own without also
// having to reason about prefix assignment.
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

// resolveHostRootedUpstreams fills in Upstream and EnforcedPaths for every
// host-rooted route (HostRooted true, Upstream still "" from
// resolveRegistryRoutesFromFile) by deriving the enforced path-set from the
// Target repo's own committed config -- host-side, before any Box starts,
// and this function's only caller of registrypathset.Derive. A routes file
// with no host-rooted route returns routes untouched without resolving a
// repo checkout at all, so a legacy-only launch never grows a dependency on
// one being present.
//
// The repo-checkout gate is the same one registryRouteDriftCheckForRoutes
// uses for the doctor drift row (registryRouteDriftRepoDirFn +
// checkoutIsTargetRepo): a candidate checkout not positively identified as
// the Target repo is treated as absent, since deriving from the wrong repo
// would produce the wrong (or an empty) path-set rather than erroring.
// Every failure path here fails the launch closed, naming the affected
// route's match-host -- there is no unenforced fallback.
func resolveHostRootedUpstreams(c config, routes []registryproxy.Route) ([]registryproxy.Route, error) {
	var hostRootedHosts []string
	for _, r := range routes {
		if r.HostRooted {
			hostRootedHosts = append(hostRootedHosts, r.MatchHost)
		}
	}
	if len(hostRootedHosts) == 0 {
		return routes, nil
	}

	repoDir, err := registryRouteDriftRepoDirFn()
	if err != nil || repoDir == "" || !checkoutIsTargetRepo(repoDir, c) {
		return nil, fmt.Errorf("registry proxy: host-rooted route(s) %s need a Target-repo checkout to derive their enforced path-set, and none is available here; run inside a checkout of the Target repo, or add upstream-base-url to make them legacy routes", strings.Join(hostRootedHosts, ", "))
	}

	sets, err := registrypathset.Derive(repoDir)
	if err != nil {
		return nil, fmt.Errorf("registry proxy: deriving enforced path-set from %q: %w", repoDir, err)
	}
	byHost := make(map[string]registrypathset.HostPathSet, len(sets))
	for _, s := range sets {
		byHost[s.Host] = s
	}

	resolved := make([]registryproxy.Route, len(routes))
	for i, r := range routes {
		if !r.HostRooted {
			resolved[i] = r
			continue
		}
		route, err := applyHostPathSet(r, byHost)
		if err != nil {
			return nil, err
		}
		resolved[i] = route
	}
	return resolved, nil
}

// applyHostPathSet projects the HostPathSet matching route's match-host (by
// the shared hostOnly normalization Derive already applied to sets' keys)
// onto route: Upstream becomes the path-set's origin with any trailing "/"
// trimmed, since registryproxy.New rejects a host-rooted Upstream carrying
// any path, and EnforcedPaths becomes every subtree path declared on that
// host, in derivation order, followed by route.Allow (issue #3258) -- an
// allow entry patches a gap in the derived set and, once merged, forwards
// indistinguishably from a derived one. An allow entry that exactly
// duplicates an already-derived path, or an earlier allow entry, is skipped
// rather than appended a second time, since a repeated path would otherwise
// ride into EnforcedPaths and read confusingly in the 403 body's listing. A route
// naming a host absent from sets -- the Target repo checkout declares no
// registry there -- is an error naming the route's match-host, never a
// route left unenforced; allow does not rescue that case.
func applyHostPathSet(route registryproxy.Route, sets map[string]registrypathset.HostPathSet) (registryproxy.Route, error) {
	hp, ok := sets[hostOnly(route.MatchHost)]
	if !ok {
		return registryproxy.Route{}, fmt.Errorf("registry proxy: route %q is host-rooted but the Target repo declares no registry on that host; add upstream-base-url to make it a legacy route", route.MatchHost)
	}
	route.Upstream = strings.TrimSuffix(hp.Origin, "/")
	paths := make([]string, len(hp.Subtrees))
	for i, sub := range hp.Subtrees {
		paths[i] = sub.Path
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
	route.EnforcedPaths = paths
	return route, nil
}

// hostOnly lowercases hostport and strips any ":port" suffix -- a local copy
// of registryproxy's hostOnly (itself mirroring registryroutes' and
// registrypathset's), kept in sync by convention rather than a shared
// dependency: it exists here only so a host-rooted route's MatchHost
// compares equal to the hostOnly-normalized keys registrypathset.Derive
// returns.
func hostOnly(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
	}
	return strings.ToLower(host)
}

// resolveRegistryRoutesFromFile reads and parses routesFile (ADR 0045), then
// resolves every route's credential exactly once via credresolver.New(...)
// .Resolve() -- destructive for an env-var source (os.Unsetenv on success),
// so a route's credential is never resolved (and never unset) more than once
// per run. A resolve failure names the offending route's match-host, so a
// multi-route file's failure doesn't leave an operator guessing which route
// broke.
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
			MatchHost:        r.MatchHost,
			Upstream:         r.UpstreamBaseURL,
			AuthScheme:       r.AuthScheme,
			Credential:       cred,
			CargoRegistries:  r.CargoRegistries,
			EnforceAllowlist: r.EnforceAllowlist,
			Allow:            r.Allow,
			// UpstreamBaseURL == "" is the host-rooted opt-in (slice 1);
			// Upstream and EnforcedPaths are filled in by
			// resolveHostRootedUpstreams, not here, since that needs the
			// whole route slice plus a Target-repo checkout, neither of
			// which resolveRegistryRoutesFromFile has reason to depend on.
			HostRooted: r.UpstreamBaseURL == "",
		})
	}
	return routes, nil
}
