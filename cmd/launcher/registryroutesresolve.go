package main

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/credresolver"
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
	return registryproxy.AssignPrefixes(routes), nil
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
		})
	}
	return routes, nil
}
