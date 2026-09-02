package main

import (
	"os"
	"path/filepath"
	"reflect"
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

// TestResolveRegistryRoutesFromFile_CargoRegistriesProjected verifies that a
// route's cargo-registries field (ADR 0045) is projected onto the returned
// registryproxy.Route's CargoRegistries, straight from the parsed route.
func TestResolveRegistryRoutesFromFile_CargoRegistriesProjected(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_ROUTES_CARGO_REGISTRIES_CRED", "s3kr1t")
	path := writeRoutesFile(t, `
[[routes]]
match-host = "crates.example.com"
upstream-base-url = "https://crates.example.com/api/v1/crates"
cargo-registries = ["example-remote", "another_one"]
credential = { env = "SPINDRIFT_TEST_ROUTES_CARGO_REGISTRIES_CRED" }
`)

	routes, err := resolveRegistryRoutesFromFile(path)
	if err != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("resolveRegistryRoutesFromFile() = %d routes, want 1", len(routes))
	}
	want := []string{"example-remote", "another_one"}
	if !reflect.DeepEqual(routes[0].CargoRegistries, want) {
		t.Errorf("routes[0].CargoRegistries = %v, want %v", routes[0].CargoRegistries, want)
	}
}

// TestBuildRegistryProxyRoutes_FilePath_AssignsPrefixes verifies that
// buildRegistryProxyRoutes runs registryproxy.AssignPrefixes over the
// routes-file path's synthesized routes, so every production route table
// carries a Prefix (issue #3142) -- resolveRegistryRoutesFromFile itself
// leaves Prefix unset, only buildRegistryProxyRoutes assigns it.
func TestBuildRegistryProxyRoutes_FilePath_AssignsPrefixes(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_ROUTES_PREFIX_CRED", "s3kr1t")
	path := writeRoutesFile(t, `
[[routes]]
match-host = "crates.example.com"
upstream-base-url = "https://crates.example.com/api/v1/crates"
cargo-registries = ["example-remote"]
credential = { env = "SPINDRIFT_TEST_ROUTES_PREFIX_CRED" }
`)

	c := config{schemaConfig: schemaConfig{registryProxyRoutesFile: path}}
	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	if routes[0].Prefix == "" {
		t.Error("routes[0].Prefix is empty, want AssignPrefixes to have set it")
	}
	want := []string{"example-remote"}
	if !reflect.DeepEqual(routes[0].CargoRegistries, want) {
		t.Errorf("routes[0].CargoRegistries = %v, want %v", routes[0].CargoRegistries, want)
	}
}

// TestBuildRegistryProxyRoutes_ScalarPath_AssignsPrefixAndNoCargoRegistries
// verifies that the scalar bridge route also gets a Prefix from
// buildRegistryProxyRoutes, and that the scalar cargo registry name knob is
// NOT projected into CargoRegistries -- the legacy placeholder derivation
// for the scalar path stays parse-based, box-side (coordinator decision,
// scout brief).
func TestBuildRegistryProxyRoutes_ScalarPath_AssignsPrefixAndNoCargoRegistries(t *testing.T) {
	c := config{schemaConfig: schemaConfig{
		registryProxyUpstreamURL:                 "https://registry.example.com",
		registryProxyCredentialEnv:               "SPINDRIFT_TEST_ROUTES_SCALAR_PREFIX_CRED",
		registryProxyCredentialFileFormat:        "raw",
		registryProxyCredentialCargoRegistryName: "example-remote",
	}}
	t.Setenv("SPINDRIFT_TEST_ROUTES_SCALAR_PREFIX_CRED", "s3kr1t")

	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	if routes[0].Prefix == "" {
		t.Error("routes[0].Prefix is empty, want AssignPrefixes to have set it")
	}
	if routes[0].CargoRegistries != nil {
		t.Errorf("routes[0].CargoRegistries = %v, want nil: the scalar bridge must not project registryProxyCredentialCargoRegistryName", routes[0].CargoRegistries)
	}
}

// TestResolveRegistryRoutesFromFile_MixedSources_ResolvesEachRouteCredential
// exercises the real Resolve path (not doctor's Peek) across a routes file
// mixing the three sources added for issue #3140 -- exec, npmrc, and
// gradle-properties -- alongside the pre-existing env source, asserting each
// route's resolved Credential value lands correctly and is paired with the
// right route by match-host.
func TestResolveRegistryRoutesFromFile_MixedSources_ResolvesEachRouteCredential(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_ROUTES_MIXED_ENV_CRED", "tok-env")

	dir := t.TempDir()
	npmrcPath := filepath.Join(dir, ".npmrc")
	if err := os.WriteFile(npmrcPath, []byte("//npmrc.example.com/:_authToken=tok-npmrc\n"), 0o600); err != nil {
		t.Fatalf("writing npmrc fixture: %v", err)
	}
	propsPath := filepath.Join(dir, "gradle.properties")
	if err := os.WriteFile(propsPath, []byte("registryToken=tok-gradle\n"), 0o600); err != nil {
		t.Fatalf("writing gradle.properties fixture: %v", err)
	}

	path := writeRoutesFile(t, `
[[routes]]
match-host = "env.example.com"
upstream-base-url = "https://env.example.com"
credential = { env = "SPINDRIFT_TEST_ROUTES_MIXED_ENV_CRED" }

[[routes]]
match-host = "exec.example.com"
upstream-base-url = "https://exec.example.com"
credential = { exec = ["/bin/sh", "-c", "echo tok-exec"] }

[[routes]]
match-host = "npmrc.example.com"
upstream-base-url = "https://npmrc.example.com"
credential = { npmrc = "`+npmrcPath+`" }

[[routes]]
match-host = "gradle.example.com"
upstream-base-url = "https://gradle.example.com"
credential = { gradle-properties = "`+propsPath+`", key = "registryToken" }
`)

	routes, err := resolveRegistryRoutesFromFile(path)
	if err != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() error = %v, want nil", err)
	}
	want := map[string]string{
		"env.example.com":    "tok-env",
		"exec.example.com":   "tok-exec",
		"npmrc.example.com":  "tok-npmrc",
		"gradle.example.com": "tok-gradle",
	}
	if len(routes) != len(want) {
		t.Fatalf("resolveRegistryRoutesFromFile() = %d routes, want %d", len(routes), len(want))
	}
	for _, r := range routes {
		wantCred, ok := want[r.MatchHost]
		if !ok {
			t.Errorf("unexpected route with MatchHost %q", r.MatchHost)
			continue
		}
		if r.Credential != wantCred {
			t.Errorf("route %q Credential = %q, want %q", r.MatchHost, r.Credential, wantCred)
		}
	}
}
