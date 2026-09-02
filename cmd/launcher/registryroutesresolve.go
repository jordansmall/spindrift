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
// routes file (ADR 0045), when set, is parsed and every route's
// credential resolved; otherwise the five legacy scalar REGISTRY_PROXY_*
// knobs (ADR 0044) synthesize the same single bridge route they always have,
// via registryroutes.FromScalars. validateRegistryProxyRoutesAmbiguity
// (wired into validate(c) via checks.go's registry-proxy-routes row) has
// already refused the two being set together by the time this runs, so
// there's no third case to handle. Returns nil, nil when neither is
// configured -- the registry proxy's documented off state.
//
// AssignPrefixes runs here, once, over whichever branch's result -- not
// inside either branch -- so every production route table carries a Prefix
// (issue #3142) while resolveRegistryRoutesFromFile and
// resolveRegistryRoutesFromScalars stay testable on their own without also
// having to reason about prefix assignment.
func buildRegistryProxyRoutes(c config) ([]registryproxy.Route, error) {
	var (
		routes []registryproxy.Route
		err    error
	)
	if c.registryProxyRoutesFile != "" {
		routes, err = resolveRegistryRoutesFromFile(c.registryProxyRoutesFile)
	} else {
		routes, err = resolveRegistryRoutesFromScalars(c)
	}
	if err != nil {
		return nil, err
	}
	return registryproxy.AssignPrefixes(routes), nil
}

// resolveRegistryRoutesFromFile reads and parses routesFile (ADR 0045), then
// resolves every route's credential exactly once via credresolver.New(...)
// .Resolve() -- destructive for an env-var source (os.Unsetenv on success),
// same as the scalar knobs' own single resolution point. A resolve failure
// names the offending route's match-host, so a multi-route file's failure
// doesn't leave an operator guessing which route broke.
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
			Upstream:        r.UpstreamBaseURL,
			AuthScheme:      r.AuthScheme,
			Credential:      cred,
			CargoRegistries: r.CargoRegistries,
		})
	}
	return routes, nil
}

// resolveRegistryRoutesFromScalars reproduces the pre-ADR-0045 scalar-knob
// path byte-identically: resolveRegistryProxyCredential is called exactly as
// it always was (same error text, same destructive env-unset), and
// registryroutes.FromScalars supplies the same MatchHost/Upstream/AuthScheme
// derivation the old hand-written bridge route used. Returns nil, nil when
// REGISTRY_PROXY_UPSTREAM_URL is unset -- the documented opt-out.
func resolveRegistryRoutesFromScalars(c config) ([]registryproxy.Route, error) {
	if c.registryProxyUpstreamURL == "" {
		return nil, nil
	}
	cred, err := resolveRegistryProxyCredential(c.registryProxyCredentialFile, c.registryProxyCredentialEnv, c.registryProxyCredentialFileFormat, c.registryProxyUpstreamURL, c.registryProxyCredentialCargoRegistryName)
	if err != nil {
		return nil, err
	}
	bridge := registryroutes.FromScalars(c.registryProxyUpstreamURL, c.registryProxyCredentialFile, c.registryProxyCredentialEnv, c.registryProxyCredentialFileFormat, c.registryProxyCredentialCargoRegistryName)
	return []registryproxy.Route{{
		MatchHost:  bridge[0].MatchHost,
		Upstream:   bridge[0].UpstreamBaseURL,
		AuthScheme: bridge[0].AuthScheme,
		Credential: cred,
	}}, nil
}
