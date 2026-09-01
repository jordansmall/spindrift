package main

import (
	"fmt"
	"net/url"
	"strings"

	"spindrift.dev/launcher/internal/credresolver"
)

// validateRegistryProxyCredential reports a mutual-exclusion configuration
// error when both fromFile and fromEnv are set -- a Credential reference
// (ADR 0044) names exactly one source. Neither set is valid (the proxy then
// runs unauthenticated). Pure: does no I/O and touches no process state;
// this function alone can be called fail-fast without side effects -- the
// row's own Probe (checks.go) separately peeks the credential's actual
// resolvability, which does I/O.
func validateRegistryProxyCredential(fromFile, fromEnv string) error {
	if fromFile != "" && fromEnv != "" {
		return fmt.Errorf("REGISTRY_PROXY_CREDENTIAL_FILE and REGISTRY_PROXY_CREDENTIAL_ENV are mutually exclusive: a registry proxy credential names exactly one source")
	}
	return nil
}

// validateRegistryProxyUpstreamURL reports a configuration error when
// upstreamURL carries a non-empty path -- REGISTRY_PROXY_UPSTREAM_URL must
// name a bare origin with no path, since the proxy's rewrite logic joins the
// incoming request path onto whatever path the upstream URL already
// carries; a non-empty path here would double onto every proxied request
// path, guaranteeing 404s upstream. A query string is not a path and is
// left alone: registryproxy's rewrite hook deliberately merges an upstream
// RawQuery with the inbound one. Pure: does no I/O and touches no process
// state. An empty upstreamURL is not this function's problem to reject:
// unset is the documented opt-out that disables the registry proxy
// entirely, a policy owned elsewhere, not here.
func validateRegistryProxyUpstreamURL(upstreamURL string) error {
	if upstreamURL == "" {
		return nil
	}
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return fmt.Errorf("parsing REGISTRY_PROXY_UPSTREAM_URL %q: %w", upstreamURL, err)
	}
	// A scheme-less "host:port/path" upstream (missing "//") parses its
	// path into Opaque, not Path (net/url treats "host:port" as scheme
	// "host" with opaque "port/path") -- check there too, or this plausible
	// operator typo slips through here and fails downstream at
	// registryproxy.New with an unrelated "must be absolute" error instead
	// of naming the actual problem.
	path := u.Path
	if u.Opaque != "" {
		if i := strings.IndexByte(u.Opaque, '/'); i >= 0 {
			path = u.Opaque[i:]
		}
	}
	if path != "" && path != "/" {
		return fmt.Errorf("REGISTRY_PROXY_UPSTREAM_URL has path %q: it must name a bare origin with no path (e.g. https://registry.example.com)", path)
	}
	return nil
}

// resolveRegistryProxyCredential resolves a Credential reference (ADR 0044)
// to its value exactly once, via credresolver.New (see its doc comment for
// the adapter dispatch order, and each adapter for the trim/newline/empty/
// fail-closed rules and for what registryName is used for). Callers must
// call validateRegistryProxyCredential first to reject both fromFile and
// fromEnv being set; this function does not re-check that itself. If a
// caller skips validation and both are set anyway, it deterministically
// prefers fromEnv rather than erroring, since re-validating here would just
// duplicate that check.
func resolveRegistryProxyCredential(fromFile, fromEnv, fileFormat, upstreamURL, registryName string) (string, error) {
	return credresolver.New(credresolver.Config{
		FromFile:     fromFile,
		FromEnv:      fromEnv,
		FileFormat:   fileFormat,
		UpstreamURL:  upstreamURL,
		RegistryName: registryName,
	}).Resolve()
}

// peekRegistryProxyCredential resolves a Credential reference (ADR 0044) via
// the same credresolver.New adapter as resolveRegistryProxyCredential, but
// calls Peek instead of Resolve -- a non-destructive read for callers, such
// as doctor's registry-proxy-credential check, that need to report on
// resolvability without consuming the credential ahead of the real
// resolution that must still happen later (see credresolver.Resolver's doc
// comment for why the env-var adapter's Resolve-time unset is load-bearing).
func peekRegistryProxyCredential(fromFile, fromEnv, fileFormat, upstreamURL, registryName string) (string, error) {
	return credresolver.New(credresolver.Config{
		FromFile:     fromFile,
		FromEnv:      fromEnv,
		FileFormat:   fileFormat,
		UpstreamURL:  upstreamURL,
		RegistryName: registryName,
	}).Peek()
}
