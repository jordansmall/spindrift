package main

import (
	"strings"
	"testing"
)

// TestResolveRegistryRoutesFromFile_MissingFile_WrapsReadError proves the
// read-error wrapper at registryroutesresolve.go:38 fires and names both the
// knob (REGISTRY_PROXY_ROUTES_FILE) and the unreadable path: production only
// reaches this wrapper via buildRegistryProxyRoutes, which the checks.go
// registry-proxy-routes row's identical read/Parse/Peek (checks.go:307-321)
// always shadows by failing first -- this test calls
// resolveRegistryRoutesFromFile directly to exercise the wrapper on its own.
func TestResolveRegistryRoutesFromFile_MissingFile_WrapsReadError(t *testing.T) {
	missing := t.TempDir() + "/does-not-exist.toml"

	routes, err := resolveRegistryRoutesFromFile(missing)
	if err == nil {
		t.Fatal("resolveRegistryRoutesFromFile() = nil error, want an error: the file doesn't exist")
	}
	if routes != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() routes = %+v, want nil", routes)
	}
	if !strings.Contains(err.Error(), "reading REGISTRY_PROXY_ROUTES_FILE") {
		t.Errorf("resolveRegistryRoutesFromFile() error = %q, want it to contain %q", err.Error(), "reading REGISTRY_PROXY_ROUTES_FILE")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("resolveRegistryRoutesFromFile() error = %q, want it to name the unreadable path %q", err.Error(), missing)
	}
}

// TestResolveRegistryRoutesFromFile_InvalidTOML_PropagatesParseError proves
// an unparseable routes file surfaces registryroutes.Parse's own error
// unwrapped -- resolveRegistryRoutesFromFile passes a Parse failure straight
// through (registryroutesresolve.go:42), so this pins that the "err" branch
// really does return, rather than swallowing or re-wrapping, Parse's error.
func TestResolveRegistryRoutesFromFile_InvalidTOML_PropagatesParseError(t *testing.T) {
	invalid := writeRoutesFile(t, `not valid toml [[[`)

	routes, err := resolveRegistryRoutesFromFile(invalid)
	if err == nil {
		t.Fatal("resolveRegistryRoutesFromFile() = nil error, want an error: the file is not valid TOML")
	}
	if routes != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() routes = %+v, want nil", routes)
	}
	if !strings.Contains(err.Error(), "registryroutes:") {
		t.Errorf("resolveRegistryRoutesFromFile() error = %q, want registryroutes.Parse's own \"registryroutes: parsing routes file\" wrapper", err.Error())
	}
}

// TestResolveRegistryRoutesFromFile_ResolveFailure_NamesRoute proves a route
// whose credential fails to resolve (here, an unset env var) is reported
// through resolveRegistryRoutesFromFile's own "resolving credential for
// route %q" wrap (registryroutesresolve.go:48), naming the offending
// route's match-host -- distinct from, and never reached through, the
// checks.go Peek gate's "route %q: %w" wrap that shadows this path in
// production (see
// TestBootstrap_RegistryProxyRoutesFile_ResolveFailure_ChecksGateNamesRoute
// in bootstrap_test.go for that gate).
func TestResolveRegistryRoutesFromFile_ResolveFailure_NamesRoute(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-base-url = "https://registry.example.com"
credential = { env = "SPINDRIFT_TEST_ROUTES_RESOLVE_CRED_DOES_NOT_EXIST" }
`)

	routes, err := resolveRegistryRoutesFromFile(path)
	if err == nil {
		t.Fatal("resolveRegistryRoutesFromFile() = nil error, want an error: the route's env credential is unresolvable")
	}
	if routes != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() routes = %+v, want nil", routes)
	}
	if !strings.Contains(err.Error(), `resolving credential for route "registry.example.com"`) {
		t.Errorf(`resolveRegistryRoutesFromFile() error = %q, want it to contain %q`, err.Error(), `resolving credential for route "registry.example.com"`)
	}
}

// TestResolveRegistryRoutesFromFile_ValidFile_ResolvesCredential is the
// happy path: a valid routes file with its route's env credential set
// returns the route with its credential resolved to that env var's value,
// carrying MatchHost/Upstream/AuthScheme straight from the parsed route.
func TestResolveRegistryRoutesFromFile_ValidFile_ResolvesCredential(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_ROUTES_HAPPY_CRED", "s3kr1t")
	path := writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-base-url = "https://registry.example.com"
credential = { env = "SPINDRIFT_TEST_ROUTES_HAPPY_CRED" }
`)

	routes, err := resolveRegistryRoutesFromFile(path)
	if err != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("resolveRegistryRoutesFromFile() = %d routes, want 1", len(routes))
	}
	got := routes[0]
	if got.MatchHost != "registry.example.com" {
		t.Errorf("routes[0].MatchHost = %q, want %q", got.MatchHost, "registry.example.com")
	}
	if got.Upstream != "https://registry.example.com" {
		t.Errorf("routes[0].Upstream = %q, want %q", got.Upstream, "https://registry.example.com")
	}
	if got.AuthScheme != "bearer" {
		t.Errorf("routes[0].AuthScheme = %q, want %q", got.AuthScheme, "bearer")
	}
	if got.Credential != "s3kr1t" {
		t.Errorf("routes[0].Credential = %q, want %q", got.Credential, "s3kr1t")
	}
}
