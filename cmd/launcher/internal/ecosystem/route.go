package ecosystem

import (
	"spindrift.dev/launcher/internal/registrymanifest"
)

// firstRoute returns routes[0], or the zero Route when routes is empty. Bindings
// always resolve against the first manifest route's prefix.
func firstRoute(routes []registrymanifest.Route) registrymanifest.Route {
	if len(routes) > 0 {
		return routes[0]
	}
	return registrymanifest.Route{}
}

// declaredPath returns the path the first route declares for ecosystem, or "" when
// it declares none. A manifest may carry blocks for ecosystems this Box cannot
// render; those read as no declaration rather than as an error. A route declares at
// most one path per ecosystem, so callers need no ambiguity handling.
func declaredPath(routes []registrymanifest.Route, ecosystem string) string {
	return firstRoute(routes).Ecosystems.Path(ecosystem)
}
