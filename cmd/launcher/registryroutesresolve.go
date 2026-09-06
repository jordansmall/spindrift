package main

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/registrypathset"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryroutes"
	"spindrift.dev/launcher/internal/registryvocab"
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
// route (all of them host-rooted since ADR 0047, issue #3261; Upstream still
// "" from resolveRegistryRoutesFromFile) by deriving the enforced path-set
// from deriveHostRootedPathSets, which owns the choice of where that
// path-set comes from (issue #3310) -- host-side, before any Box starts.
// Derivation runs whenever there is a route at all, even one declaring its
// own upstream-origin: a declared origin replaces the derived origin, never
// the derived subtrees, so what such a route enforces must not silently
// depend on whether a checkout happened to be reachable. Every failure path
// fails the launch closed, naming the affected route's match-host -- there
// is no unenforced fallback.
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
		return nil, fmt.Errorf("registry proxy: host-rooted route(s) %s need a Target-repo checkout to derive their enforced path-set, and none is available here; run inside a checkout of the Target repo", strings.Join(hostRootedHosts, ", "))
	}

	sets, err := registrypathset.Derive(repoDir)
	if err != nil {
		return nil, fmt.Errorf("registry proxy: deriving enforced path-set from %q: %w", repoDir, err)
	}
	return sets, nil
}

// applyHostPathSet projects the HostPathSet matching route's match-host (by
// the registryvocab.HostKey normalization Derive already applied to sets' keys)
// onto route: Upstream becomes the route's declared upstream-origin, or
// failing that the path-set's origin with any trailing "/" trimmed, since
// registryproxy.New rejects a host-rooted Upstream carrying a path;
// EnforcedPaths the derived subtrees in derivation order, then
// route.Allow, then each operator-declared path (declaredPaths); and
// EnforcedSubtrees the derived subtrees, each tagged with its Ecosystem
// (allow entries name none, so they never appear), plus one tagged entry
// per declared path.
//
// EnforcedPaths dedupes, since a path repeated across derivation, allow,
// and a declaration would read confusingly in the 403 body's listing.
// EnforcedSubtrees deliberately does not: a declared path always gets its
// own tagged entry even when it duplicates a derived or allow path, because
// a binding renderer looks for its ecosystem's tag, not for the path's
// presence in EnforcedPaths, and suppressing the append on collision would
// silently drop the operator's explicit binding.
//
// A route naming a host absent from sets, and declaring no upstream-origin
// of its own, is an error naming that match-host, never a route left
// unenforced. A declared path names one ecosystem's subtree, not an origin,
// so it cannot establish an origin even when it is all the route declares --
// the !ok branch below names whichever declarations are present rather than
// inventing an origin from them. An upstream-origin can: such a route
// resolves with hp's zero value, enforcing exactly what it declares itself
// (allow plus declared paths, possibly nothing at all, which registryproxy
// reads as default-deny).
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
	// Field-by-field, not a copy of sub itself: this drops
	// hp.Subtrees[i].RegistryName, which the proxy's manifest-facing
	// EnforcedSubtrees has never carried.
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

// declaredPath is one operator-declared path field's value, paired with the
// ecosystem tag its EnforcedSubtrees entry carries and the routes-file key
// an operator knows it by.
type declaredPath struct {
	ecosystem string
	key       string
	path      string
}

// declaredPaths returns the operator-declared path fields route actually
// sets, in the order applyHostPathSet emits them. It is the single
// enumeration of those fields -- both the subtree/path append loop and
// declaredPathAloneLabel drive off it -- so a new declared-path field is
// one row here rather than an edit at every site that knows the set.
func declaredPaths(route registryproxy.Route) []declaredPath {
	all := []declaredPath{
		{ecosystem: "gradle", key: "gradle-path", path: route.GradlePath},
		{ecosystem: "go", key: "go-path", path: route.GoPath},
	}
	var set []declaredPath
	for _, d := range all {
		if d.path != "" {
			set = append(set, d)
		}
	}
	return set
}

// declaredPathAloneLabel names route's set declared-path fields by their
// routes-file keys, for applyHostPathSet's !ok branch: none of them alone
// can establish a host-rooted route's upstream origin, and that limitation
// reads identically whichever field(s) triggered it, so one shared message
// names whichever declaration(s) are present rather than duplicating
// near-identical prose per field or per combination.
func declaredPathAloneLabel(route registryproxy.Route) string {
	var labels []string
	for _, d := range declaredPaths(route) {
		labels = append(labels, d.key)
	}
	return strings.Join(labels, " and ")
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
			MatchHost:       r.MatchHost,
			AuthScheme:      r.AuthScheme,
			Credential:      cred,
			CargoRegistries: r.CargoRegistries,
			GradlePath:      r.GradlePath,
			GoPath:          r.GoPath,
			UpstreamOrigin:  r.UpstreamOrigin,
			Allow:           r.Allow,
			// Upstream and EnforcedPaths are filled in by
			// resolveHostRootedUpstreams, not here, since that needs the
			// whole route slice plus a Target-repo checkout, neither of
			// which resolveRegistryRoutesFromFile has reason to depend on.
		})
	}
	return routes, nil
}
