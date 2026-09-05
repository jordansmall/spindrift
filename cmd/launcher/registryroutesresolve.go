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
// resolveRegistryRoutesFromFile) by deriving the enforced path-set from
// deriveHostRootedPathSets, which owns the choice of where that path-set
// comes from (issue #3310) -- host-side, before any Box starts. A routes
// file with no host-rooted route returns routes untouched without resolving
// anything at all, so a legacy-only launch never grows a dependency on a
// Target-repo checkout or an Accumulation repo being present. Every failure
// path fails the launch closed, naming the affected route's match-host --
// there is no unenforced fallback.
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

	sets, err := deriveHostRootedPathSets(c, hostRootedHosts)
	if err != nil {
		return nil, err
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

// deriveHostRootedPathSets picks the one source of truth
// resolveHostRootedUpstreams derives host-rooted routes' enforced path-sets
// from, keyed on c.codeForge's HostMediatedRemote row the same way
// seedAccumulationRepoIfHostMediated (bootstrap.go) and
// absCodeForgeAccumulationRepoDir (main.go) already do, rather than a raw
// c.codeForge == "local" string compare that a future host-mediated backend
// would silently miss.
//
// A host-mediated forge (CODE_FORGE=local) derives from
// c.codeForgeAccumulationRepoDir's c.baseBranch ref, not a cwd checkout:
// buildRegistryProxyRoutes runs once per process in bootstrap(), after
// seedAccumulationRepoIfHostMediated has already pushed pwd's own
// baseBranch ref into the Accumulation repo and before any Box is
// dispatched, so baseBranch is the one snapshot every route in this launch
// can key on -- there is no per-seam integration/<parent> ref yet at this
// point in bootstrap, and keying on one would be wrong regardless: that
// branch carries landed agent work, and ADR 0047's containment story
// requires the enforced set to come from a snapshot no in-Box agent can
// widen. checkoutIsTargetRepo is skipped entirely on this path rather than
// taught a "local" case -- its contract is remote-based identity, and a
// host-mediated forge has no remote to compare against.
//
// Every other forge keeps the pre-#3310 gate byte-for-byte: a checkout
// registryRouteDriftRepoDirFn resolves and checkoutIsTargetRepo positively
// identifies as the Target repo, or the launch fails closed.
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
		return nil, fmt.Errorf("registry proxy: host-rooted route(s) %s need a Target-repo checkout to derive their enforced path-set, and none is available here; run inside a checkout of the Target repo, or add upstream-base-url to make them legacy routes", strings.Join(hostRootedHosts, ", "))
	}

	sets, err := registrypathset.Derive(repoDir)
	if err != nil {
		return nil, fmt.Errorf("registry proxy: deriving enforced path-set from %q: %w", repoDir, err)
	}
	return sets, nil
}

// applyHostPathSet projects the HostPathSet matching route's match-host (by
// the shared hostOnly normalization Derive already applied to sets' keys)
// onto route: Upstream becomes the path-set's origin with any trailing "/"
// trimmed, since registryproxy.New rejects a host-rooted Upstream carrying
// host, in derivation order, followed by route.Allow (issue #3258) and then
// route.GradlePath (issue #3259) -- an allow entry patches a gap in the
// derived set, and a gradle-path declares gradle's own undiscoverable path
// (gradle has no in-tree config Derive could ever tag, so its path comes
// from the operator's own routes-file declaration instead of repo
// scanning); both forward indistinguishably from a derived one once merged.
// CargoIndexBases becomes the derived (not allow-extended) subtrees filtered
// to Ecosystem == "cargo", in derivation order. Either allow or gradle-path
// that exactly duplicates an already-derived path, or an earlier allow/
// gradle-path entry, is skipped from EnforcedPaths only rather than appended
// a second time, since a repeated path would otherwise ride into
// EnforcedPaths and read confusingly in the 403 body's listing -- this
// dedupe never applies to EnforcedSubtrees, so a gradle-path colliding with
// an allow entry or an already-derived path still always gets its own
// "gradle"-tagged subtree entry (issue #3259 review fix): GradleInitScript
// looks for that tag, not for the path's presence in EnforcedPaths, so
// suppressing the subtree append on collision would silently drop the
// operator's explicit gradle binding. A route naming a host absent from
// sets -- the Target repo checkout declares no registry there -- is an
// error naming the route's match-host, never a route left unenforced;
// neither allow nor gradle-path rescues that case -- a gradle-path
// declaration alone, with no other ecosystem's config discoverable on this
// host, cannot establish hp.Origin either, so the !ok branch below still
// fires (extended to name that limitation) rather than inventing an origin
// from gradle-path alone. EnforcedSubtrees carries the derived subtrees
// again (never allow entries, which name no ecosystem), each tagged with
// its Ecosystem (issue #3259) -- gradle-path's own entry is tagged
// "gradle" -- so a pre-clone client-side binding renderer can pick out just
// its own ecosystem's path(s) once this reaches the manifest.
func applyHostPathSet(route registryproxy.Route, sets map[string]registrypathset.HostPathSet) (registryproxy.Route, error) {
	hp, ok := sets[hostOnly(route.MatchHost)]
	if !ok {
		if route.GradlePath != "" {
			return registryproxy.Route{}, fmt.Errorf("registry proxy: route %q is host-rooted but the Target repo declares no registry on that host; gradle-path alone cannot establish a host-rooted route's upstream origin -- declare a discoverable npm/yarn/pnpm/cargo registry on this host too, or add upstream-base-url to make it a legacy route", route.MatchHost)
		}
		return registryproxy.Route{}, fmt.Errorf("registry proxy: route %q is host-rooted but the Target repo declares no registry on that host; add upstream-base-url to make it a legacy route", route.MatchHost)
	}
	route.Upstream = strings.TrimSuffix(hp.Origin, "/")
	paths := make([]string, len(hp.Subtrees))
	var cargoBases []string
	subtrees := make([]registryproxy.EnforcedSubtree, len(hp.Subtrees))
	for i, sub := range hp.Subtrees {
		paths[i] = sub.Path
		if sub.Ecosystem == "cargo" {
			cargoBases = append(cargoBases, sub.Path)
		}
		subtrees[i] = registryproxy.EnforcedSubtree{Ecosystem: sub.Ecosystem, Path: sub.Path}
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
	if route.GradlePath != "" {
		subtrees = append(subtrees, registryproxy.EnforcedSubtree{Ecosystem: "gradle", Path: route.GradlePath})
		if !derived[route.GradlePath] {
			derived[route.GradlePath] = true
			paths = append(paths, route.GradlePath)
		}
	}
	route.EnforcedPaths = paths
	route.CargoIndexBases = cargoBases
	route.EnforcedSubtrees = subtrees
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
			GradlePath:       r.GradlePath,
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
