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
