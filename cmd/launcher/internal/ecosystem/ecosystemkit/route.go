package ecosystemkit

import "spindrift.dev/launcher/internal/registrymanifest"

// DeclaredPath returns the path the first route declares for ecosystem, or ""
// when routes is empty or the first route declares none. A manifest may carry
// blocks for ecosystems this Box cannot render; those read as no declaration
// rather than as an error. A route declares at most one path per ecosystem, so
// callers need no ambiguity handling.
func DeclaredPath(routes []registrymanifest.Route, ecosystem string) string {
	if len(routes) == 0 {
		return ""
	}
	return routes[0].Ecosystems.Path(ecosystem)
}
