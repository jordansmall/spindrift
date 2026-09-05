package main

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/registryroutes"
)

// loadRegistryRoutes reads and parses file, the read+Parse sequence shared
// by registryRouteChecks, registryRouteDriftCheck (registryroutesdrift_
// doctor_checks.go), and registryProxyRoutesCheck (checks.go) -- one helper
// so the three never drift apart on how a read failure is worded. The read
// error is wrapped with file's own name, matching registryProxyRoutesCheck's
// wording before this helper existed; a Parse failure passes through
// unwrapped since registryroutes.Parse's own "registryroutes: ..." messages
// already name what's wrong without a second prefix here.
func loadRegistryRoutes(file string) ([]registryroutes.Route, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("reading REGISTRY_PROXY_ROUTES_FILE %q: %w", file, err)
	}
	return registryroutes.Parse(data)
}

// registryRouteChecks returns two doctor.Check rows per route declared in
// c.registryProxyRoutesFile (ADR 0045, issue #3144 slice 1): a credential
// row and an upstream-base-url row, each named after that route's own match
// host so a failure points straight at the offending route without an
// operator having to cross-reference a bundled multi-route error.
//
// Gated on c.registryProxyRoutesFile: nil when it's unset, since per-route
// rows only make sense alongside a routes file -- with none set, there's
// nothing to configure (the scalar REGISTRY_PROXY_* knobs are retired,
// issue #3145).
//
// A read or parse failure here also yields nil rather than a failing row:
// the registry-proxy-routes row (checks.go) already reads and parses this
// same file and reports that failure, so a second row over the identical
// cause would be duplicate noise, not new information. Credential
// resolution gets the same one-cause-one-row treatment, just from the other
// direction: doctorReportChecks (bwrap_doctor_checks.go) substitutes
// registryProxyRoutesCheck(c, false) for the aggregate row whenever these
// per-route rows are in play, so it's the routeCredentialCheck Peek below
// that owns credential peeking in the doctor report, not a second Peek loop
// in the aggregate row.
func registryRouteChecks(c config) []doctor.Check {
	if c.registryProxyRoutesFile == "" {
		return nil
	}
	routes, err := loadRegistryRoutes(c.registryProxyRoutesFile)
	if err != nil {
		return nil
	}
	return routeChecksFor(routes)
}

// routeChecksFor builds the credential and upstream-base-url rows for an
// already-parsed route slice. Split out from registryRouteChecks so a test
// can hand it a route built without going through registryroutes.Parse --
// Parse itself already rejects a malformed upstream-base-url at parse time
// (via registryroutes.ValidateUpstreamBaseURL, the same validator
// routeUpstreamCheck below calls), so a route with an invalid one is
// otherwise unreachable from a real routes file; the upstream row still
// exists as its own per-route signal, and this seam lets that failing
// branch be exercised directly.
func routeChecksFor(routes []registryroutes.Route) []doctor.Check {
	checks := make([]doctor.Check, 0, len(routes)*2)
	for _, route := range routes {
		checks = append(checks, routeCredentialCheck(route), routeUpstreamCheck(route))
	}
	return checks
}

// routeCredentialCheck reports whether route's credential resolves, without
// consuming it.
func routeCredentialCheck(route registryroutes.Route) doctor.Check {
	return doctor.Check{
		Name:   fmt.Sprintf("registry-route-credential[%s]", route.MatchHost),
		Tier:   doctor.Required,
		Remedy: fmt.Sprintf("fix the credential source for route %q in the routes file (ADR 0045)", route.MatchHost),
		Probe: func() (any, error) {
			// Peek, never Resolve: the env adapter's Resolve unsets its source
			// var on success, and that must happen exactly once, at
			// resolveRegistryRoutesFromFile's later real resolution
			// (registryroutesresolve.go), not here.
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

// routeUpstreamCheck reports whether route's upstream-base-url is a valid
// absolute http(s) URL with no userinfo, via registryroutes.
// ValidateUpstreamBaseURL -- the package's own validator, the same one
// normalizeUpstreamBaseURL runs at parse time, so this row's signal can
// never drift from what Parse itself accepts.
//
// An empty route.UpstreamBaseURL is the host-rooted opt-in (issue #3256
// slice 1), not a broken URL -- ValidateUpstreamBaseURL rejects "" outright,
// so this row short-circuits to "host-rooted" rather than calling it and
// reporting a route that Parse itself already accepted as a failure.
func routeUpstreamCheck(route registryroutes.Route) doctor.Check {
	return doctor.Check{
		Name:   fmt.Sprintf("registry-route-upstream[%s]", route.MatchHost),
		Tier:   doctor.Required,
		Remedy: fmt.Sprintf("set route %q's upstream-base-url to an absolute http(s) URL with no userinfo (ADR 0045)", route.MatchHost),
		Probe: func() (any, error) {
			if route.UpstreamBaseURL == "" {
				return "host-rooted (no upstream-base-url)", nil
			}
			if err := registryroutes.ValidateUpstreamBaseURL(route.UpstreamBaseURL); err != nil {
				return nil, fmt.Errorf("route %q: %w", route.MatchHost, err)
			}
			return "valid", nil
		},
	}
}
