package main

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/registryroutes"
)

// loadRegistryRoutes reads and parses file so doctorCheckSets and
// registryProxyRoutesCheck word a read failure the same way. A Parse
// failure passes through unwrapped: registryroutes.Parse's own
// "registryroutes: ..." message already names what is wrong.
func loadRegistryRoutes(file string) ([]registryroutes.Route, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("reading REGISTRY_PROXY_ROUTES_FILE %q: %w", file, err)
	}
	return registryroutes.Parse(data)
}

// routeChecksFor takes an already-parsed route slice so a test can pass a route
// carrying an invalid upstream-origin, which registryroutes.Parse rejects and
// so no real routes file can produce.
func routeChecksFor(routes []registryroutes.Route) []doctor.Check {
	checks := make([]doctor.Check, 0, len(routes)*2)
	for _, route := range routes {
		checks = append(checks, routeCredentialCheck(route), routeUpstreamCheck(route))
	}
	return checks
}

// routeCredentialCheck reports whether route's credential resolves. It is the
// only credential peek in the doctor report: doctorCheckSets passes
// registryProxyRoutesCheck(c, false) whenever a routes file is configured.
func routeCredentialCheck(route registryroutes.Route) doctor.Check {
	return doctor.Check{
		Name:   fmt.Sprintf("registry-route-credential[%s]", route.MatchHost),
		Tier:   doctor.Required,
		Remedy: fmt.Sprintf("fix the credential source for route %q in the routes file (ADR 0045)", route.MatchHost),
		Probe: func() (any, error) {
			// Peek, never Resolve: the env adapter's Resolve unsets its source
			// var on success, and that must happen exactly once, at
			// resolveRegistryRoutesFromFile's later real resolution.
			if _, err := credresolver.New(route.Credential).Peek(); err != nil {
				return nil, fmt.Errorf("route %q: credential: %w", route.MatchHost, err)
			}
			if route.Credential.NamesNoSource() {
				return "unauthenticated pass-through (no credential key)", nil
			}
			return "resolves", nil
		},
	}
}

// routeUpstreamCheck validates a declared upstream-origin with
// registryroutes.ValidateUpstreamOrigin, the validator Parse runs, so this row
// cannot drift from what Parse accepts. An empty origin is not an error: the
// route derives one from the Target repo's committed config (ADR 0047, issue #3261).
func routeUpstreamCheck(route registryroutes.Route) doctor.Check {
	return doctor.Check{
		Name:   fmt.Sprintf("registry-route-origin[%s]", route.MatchHost),
		Tier:   doctor.Required,
		Remedy: fmt.Sprintf("set route %q's upstream-origin to a bare http(s) origin -- scheme://host[:port], no path and no userinfo -- or omit it and let the Target repo's committed config supply one (ADR 0047)", route.MatchHost),
		Probe: func() (any, error) {
			if route.UpstreamOrigin == "" {
				return "derived from the Target repo's committed config", nil
			}
			if err := registryroutes.ValidateUpstreamOrigin(route.UpstreamOrigin); err != nil {
				return nil, fmt.Errorf("route %q: %w", route.MatchHost, err)
			}
			return route.UpstreamOrigin, nil
		},
	}
}
