package registrypathset

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryvocab"
)

// ApplyToRoute projects the HostPathSet matching route's match-host onto
// route. Upstream loses any trailing "/", since registryproxy.New rejects a
// host-rooted Upstream carrying a path. A route naming a host absent from sets
// and declaring no upstream-origin of its own is an error, never a route left
// unenforced; one declaring an origin enforces only what it declares itself.
func ApplyToRoute(route registryproxy.Route, sets map[string]HostPathSet, rows []ecosystem.Strategy) (registryproxy.Route, error) {
	hp, ok := sets[registryvocab.HostKey(route.MatchHost)]
	if !ok && route.UpstreamOrigin == "" {
		if label := declaredPathAloneLabel(route, rows); label != "" {
			return registryproxy.Route{}, fmt.Errorf("registry proxy: route %q is host-rooted but the Target repo declares no registry on that host; %s alone cannot establish a host-rooted route's upstream origin -- declare a discoverable %s registry on this host in the Target repo's committed config, or set the route's upstream-origin", route.MatchHost, label, discoverableNames(rows))
		}
		return registryproxy.Route{}, fmt.Errorf("registry proxy: route %q is host-rooted but the Target repo declares no registry on that host; declare a discoverable registry on this host in the Target repo's committed config, or set the route's upstream-origin", route.MatchHost)
	}
	route.Upstream = strings.TrimSuffix(hp.Origin, "/")
	if route.UpstreamOrigin != "" {
		route.Upstream = route.UpstreamOrigin
	}
	paths := make([]string, len(hp.Subtrees))
	// RegistryName is host-only (json:"-"); EnforcedSubtrees drops it.
	subtrees := make([]registryvocab.Subtree, len(hp.Subtrees))
	for i, sub := range hp.Subtrees {
		paths[i] = sub.Path
		sub.RegistryName = ""
		subtrees[i] = sub
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
	for _, d := range declaredPaths(route, rows) {
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

// declaredPath is one registered Strategy's declared path (issue #3403), read
// out of route.Ecosystems with the tag its EnforcedSubtrees entry carries.
type declaredPath struct {
	ecosystem string
	path      string
}

// declaredPaths walks Strategies in their own order (issue #3403), not
// route.Ecosystems' map, because Go map iteration is random and both callers
// need this enumeration deterministic.
func declaredPaths(route registryproxy.Route, rows []ecosystem.Strategy) []declaredPath {
	var set []declaredPath
	for _, row := range rows {
		if path := route.Ecosystems.Path(row.Name); path != "" {
			set = append(set, declaredPath{ecosystem: row.Name, path: path})
		}
	}
	return set
}

// declaredPathAloneLabel names route's declared paths by their
// [routes.ecosystems.<name>] spelling, for ApplyToRoute's error message.
func declaredPathAloneLabel(route registryproxy.Route, rows []ecosystem.Strategy) string {
	var labels []string
	for _, d := range declaredPaths(route, rows) {
		labels = append(labels, registryvocab.RouteDeclarationKeyLabel(d.ecosystem, registryvocab.RouteDeclarationPathKey))
	}
	return strings.Join(labels, " and ")
}

// discoverableNames joins, in Strategies' order, the names of the Strategies whose in-tree
// config registrydiscover.Extract can read: the same filter, so the error
// advice never names an ecosystem whose declaration would go undiscovered.
func discoverableNames(rows []ecosystem.Strategy) string {
	var names []string
	for _, row := range rows {
		if row.InTreeConfigPath == "" || row.ConfigParser == nil {
			continue
		}
		names = append(names, row.Name)
	}
	return strings.Join(names, "/")
}
