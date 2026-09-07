package ecosystem

import (
	"spindrift.dev/launcher/internal/registrymanifest"
)

// firstRoute returns routes[0], or the zero Route when routes is empty --
// the shared "look up the one manifest route these bindings point at" guard
// NpmFamilyBindings, ComputeGoBindings, and GradleInitScript all apply to
// their own routes parameter (see any of their doc comments for why it's
// always the first manifest route's prefix).
func firstRoute(routes []registrymanifest.Route) registrymanifest.Route {
	if len(routes) > 0 {
		return routes[0]
	}
	return registrymanifest.Route{}
}

// declaredPath returns the path routes' first route declares for ecosystem
// in its own ecosystems block, or "" when it declares none -- the shared
// "which subtree does this ecosystem bind to" lookup ComputeGoBindings and
// GradleInitScript apply to their own routes parameter. A manifest can
// carry blocks for ecosystems this Box has no renderer for; such a block is
// simply not this ecosystem's, so it reads here as no declaration at all
// rather than as an error.
//
// A declared path is a single operator-supplied string, not a discovery
// scan that could produce duplicates (unlike npm's 0/1/>1 EnforcedPaths
// case in NpmFamilyBindings) -- a route declares at most one path per
// ecosystem, so no caller of this lookup needs ambiguity handling.
func declaredPath(routes []registrymanifest.Route, ecosystem string) string {
	return firstRoute(routes).Ecosystems.Path(ecosystem)
}
