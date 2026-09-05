package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registrypathset"
	"spindrift.dev/launcher/internal/registryproxy"
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

// TestBuildRegistryProxyRoutes_NoRoutesFile_ReturnsNil pins the issue #3145
// acceptance criterion: with no routes file, dispatch behaves exactly as a
// proxy-less dispatch does. The five scalar REGISTRY_PROXY_* knobs that used
// to synthesize a bridge route are retired (ADR 0044/0045) and no longer
// exist on config, so registryProxyRoutesFile empty is the only input
// buildRegistryProxyRoutes still looks at.
func TestBuildRegistryProxyRoutes_NoRoutesFile_ReturnsNil(t *testing.T) {
	c := config{}

	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if routes != nil {
		t.Fatalf("buildRegistryProxyRoutes() = %+v, want nil", routes)
	}
}

// TestResolveRegistryRoutesFromFile_EnforceAllowlist proves enforce-allowlist
// (issue #3177) is carried all the way from the routes file through
// resolveRegistryRoutesFromFile's conversion into registryproxy.Route and
// into live proxy behaviour, not just parsed and dropped on the floor: one
// route declares enforce-allowlist = true, the other omits the key, and only
// the declaring route's out-of-allowlist path is refused with 403.
func TestResolveRegistryRoutesFromFile_EnforceAllowlist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	path := writeRoutesFile(t, `
[[routes]]
match-host = "enforced.example.com"
upstream-base-url = "`+upstream.URL+`"
enforce-allowlist = true

[[routes]]
match-host = "advisory.example.com"
upstream-base-url = "`+upstream.URL+`"
`)

	routes, err := resolveRegistryRoutesFromFile(path)
	if err != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() error = %v, want nil", err)
	}
	routes = registryproxy.AssignPrefixes(routes)

	p, err := registryproxy.New(routes)
	if err != nil {
		t.Fatalf("registryproxy.New() error = %v, want nil", err)
	}

	var enforcedPrefix, advisoryPrefix string
	for _, r := range routes {
		switch r.MatchHost {
		case "enforced.example.com":
			enforcedPrefix = r.Prefix
		case "advisory.example.com":
			advisoryPrefix = r.Prefix
		}
	}

	// The cargo download endpoint is deliberately outside isAllowedPath's
	// derived allowlist (see ecosystem.go's cargoSparseIndexPatterns
	// comment), making it a reliable out-of-allowlist path for both routes.
	outOfAllowlistPath := "/api/v1/crates/foo/1.0.0/download"

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+enforcedPrefix+outOfAllowlistPath, nil)
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("enforcing route status = %d, want %d (enforce-allowlist = true must have survived routes-file conversion)", rr.Code, http.StatusForbidden)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/"+advisoryPrefix+outOfAllowlistPath, nil)
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("advisory route status = %d, want %d (route omitting enforce-allowlist must stay log-only)", rr.Code, http.StatusOK)
	}
}

// TestResolveRegistryRoutesFromFile_HostRooted_LeavesUpstreamEmpty proves a
// route omitting upstream-base-url (the host-rooted opt-in, slice 1) is
// projected with HostRooted true and Upstream left empty --
// resolveRegistryRoutesFromFile does no derivation of its own; that is
// buildRegistryProxyRoutes's job (slice 3), so a caller that only needs the
// parse/credential step keeps working with no Target-repo checkout.
func TestResolveRegistryRoutesFromFile_HostRooted_LeavesUpstreamEmpty(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)

	routes, err := resolveRegistryRoutesFromFile(path)
	if err != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("resolveRegistryRoutesFromFile() = %d routes, want 1", len(routes))
	}
	if !routes[0].HostRooted {
		t.Error("routes[0].HostRooted = false, want true for a route omitting upstream-base-url")
	}
	if routes[0].Upstream != "" {
		t.Errorf("routes[0].Upstream = %q, want empty (unresolved until buildRegistryProxyRoutes)", routes[0].Upstream)
	}
}

// TestBuildRegistryProxyRoutes_LegacyOnly_NeverConsultsRepoDir pins the
// no-repo-dependency invariant: a routes file made entirely of legacy
// upstream-base-url routes must never call registryRouteDriftRepoDirFn --
// derivation only runs when a route actually needs it, so a legacy-only
// launch never grows a dependency on a Target-repo checkout being present.
func TestBuildRegistryProxyRoutes_LegacyOnly_NeverConsultsRepoDir(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_ROUTES_LEGACY_ONLY_CRED", "s3kr1t")
	path := writeRoutesFile(t, `
[[routes]]
match-host = "legacy.example.com"
upstream-base-url = "https://legacy.example.com"
credential = { env = "SPINDRIFT_TEST_ROUTES_LEGACY_ONLY_CRED" }
`)

	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) {
		t.Fatal("registryRouteDriftRepoDirFn called for a routes file with no host-rooted route")
		return "", nil
	}
	t.Cleanup(func() { registryRouteDriftRepoDirFn = orig })

	c := config{schemaConfig: schemaConfig{registryProxyRoutesFile: path}}
	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 || routes[0].Upstream != "https://legacy.example.com" {
		t.Fatalf("buildRegistryProxyRoutes() = %+v, want the legacy route's Upstream untouched", routes)
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_NoRepoCheckout_FailsClosed proves a
// host-rooted route fails the launch, naming its match-host, when
// registryRouteDriftRepoDirFn resolves no checkout at all (repoDir == "") --
// fail closed rather than serving the route unenforced.
func TestBuildRegistryProxyRoutes_HostRooted_NoRepoCheckout_FailsClosed(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)

	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return "", nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = orig })

	c := config{schemaConfig: schemaConfig{registryProxyRoutesFile: path}}
	routes, err := buildRegistryProxyRoutes(c)
	if err == nil {
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: no Target-repo checkout is available")
	}
	if routes != nil {
		t.Fatalf("buildRegistryProxyRoutes() routes = %+v, want nil", routes)
	}
	if !strings.Contains(err.Error(), "host.example.com") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to name the host-rooted route %q", err.Error(), "host.example.com")
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_NotTargetRepo_FailsClosed proves a
// host-rooted route fails closed when registryRouteDriftRepoDirFn resolves a
// checkout that checkoutIsTargetRepo does not positively identify as the
// Target repo (here, c.codeForge is left unset, the default case
// checkoutIsTargetRepo always refuses) -- the same "treat as absent" gate
// the doctor drift row uses.
func TestBuildRegistryProxyRoutes_HostRooted_NotTargetRepo_FailsClosed(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)

	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = orig })

	c := config{schemaConfig: schemaConfig{registryProxyRoutesFile: path}}
	_, err := buildRegistryProxyRoutes(c)
	if err == nil {
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: the resolved checkout is not the Target repo")
	}
	if !strings.Contains(err.Error(), "host.example.com") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to name the host-rooted route %q", err.Error(), "host.example.com")
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_NoMatchingHost_FailsClosed proves a
// host-rooted route fails closed, naming its own match-host, when the
// derived path-set has no HostPathSet for that host -- the Target repo
// checkout resolves fine but declares no registry there.
func TestBuildRegistryProxyRoutes_HostRooted_NoMatchingHost_FailsClosed(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte("registry=https://other.example.com/npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return repoDir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	_, err := buildRegistryProxyRoutes(c)
	if err == nil {
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: the repo declares no registry on host.example.com")
	}
	if !strings.Contains(err.Error(), "host.example.com") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to name the host-rooted route %q", err.Error(), "host.example.com")
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_DerivesUpstreamAndEnforcedPaths is
// the happy path: a host-rooted route matching a host the Target repo
// checkout declares a registry on gets its Upstream and EnforcedPaths filled
// in from the derived HostPathSet.
func TestBuildRegistryProxyRoutes_HostRooted_DerivesUpstreamAndEnforcedPaths(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte("registry=https://host.example.com/npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return repoDir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	got := routes[0]
	if !got.HostRooted {
		t.Error("routes[0].HostRooted = false, want true")
	}
	if got.Upstream != "https://host.example.com" {
		t.Errorf("routes[0].Upstream = %q, want %q", got.Upstream, "https://host.example.com")
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/npm"}) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, []string{"/npm"})
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_AllowExtendsDerivedPaths covers
// issue #3258 AC3: a repo whose .npmrc only derives "/npm" gets its
// derivation gap patched by one "allow" line in the route TOML, ending up
// with EnforcedPaths covering both the derived and the allow-declared path.
func TestBuildRegistryProxyRoutes_HostRooted_AllowExtendsDerivedPaths(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte("registry=https://host.example.com/npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return repoDir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
allow = ["/dl"]
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	got := routes[0]
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/npm", "/dl"}) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, []string{"/npm", "/dl"})
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_AllowPathForwardsLikeDerivedPath is
// the full "recourse loop" acceptance demo for issue #3258: a routes file
// declares allow = ["/dl"] on top of a repo that only derives "/npm", and
// the resulting route table, wired straight into registryproxy.New, forwards
// a request under the allow-only "/dl" path exactly like one under the
// derived "/npm" path (200, route credential attached), while a path under
// neither still 403s -- with enforce-allowlist explicitly set false (AC4),
// proving host-rooted enforcement is unconditional and allow never loosens it.
func TestBuildRegistryProxyRoutes_HostRooted_AllowPathForwardsLikeDerivedPath(t *testing.T) {
	var gotPaths []string
	var gotAuths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		gotAuths = append(gotAuths, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	repoDir := t.TempDir()
	upstreamHost := strings.TrimPrefix(strings.TrimPrefix(upstream.URL, "http://"), "https://")
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte("registry=http://"+upstreamHost+"/npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return repoDir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	t.Setenv("SPINDRIFT_TEST_ALLOW_LOOP_CRED", "s3kr1t")
	path := writeRoutesFile(t, `
[[routes]]
match-host = "`+upstreamHost+`"
allow = ["/dl"]
enforce-allowlist = false
credential = { env = "SPINDRIFT_TEST_ALLOW_LOOP_CRED" }
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}

	p, err := registryproxy.New(routes)
	if err != nil {
		t.Fatalf("registryproxy.New() error = %v, want nil", err)
	}
	prefix := routes[0].Prefix

	for _, tc := range []string{"/" + prefix + "/npm/pkg", "/" + prefix + "/dl/pkg"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc, nil)
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want %d", tc, rr.Code, http.StatusOK)
		}
	}

	wantPaths := []string{"/npm/pkg", "/dl/pkg"}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("upstream saw paths %v, want %v", gotPaths, wantPaths)
	}
	for i, auth := range gotAuths {
		if want := "Bearer s3kr1t"; auth != want {
			t.Errorf("request %d: upstream got Authorization %q, want %q", i, auth, want)
		}
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/other/pkg", nil)
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("path outside derived and allow sets: status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_DerivesEnforcedSubtrees proves that
// applyHostPathSet, reached via buildRegistryProxyRoutes end to end, tags
// each derived path with its declaring ecosystem in EnforcedSubtrees (issue
// #3259) -- not just the flat, untagged EnforcedPaths the Forwarder's own
// admission check already used. A repo declaring both an npm and a yarn
// registry on the same host-rooted host must produce one EnforcedSubtree per
// declaration, each carrying its own Ecosystem.
func TestBuildRegistryProxyRoutes_HostRooted_DerivesEnforcedSubtrees(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte("registry=https://host.example.com/npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".yarnrc.yml"), []byte("npmRegistryServer: \"https://host.example.com/yarn\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return repoDir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	got := routes[0].EnforcedSubtrees
	want := []registryproxy.EnforcedSubtree{
		{Ecosystem: "npm", Path: "/npm"},
		{Ecosystem: "yarn", Path: "/yarn"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("routes[0].EnforcedSubtrees = %+v, want %+v", got, want)
	}
}

// TestApplyHostPathSet_TrimsTrailingSlashFromOrigin proves the Upstream
// assignment strips a trailing "/" from HostPathSet.Origin before handing it
// to registryproxy, whose New rejects a host-rooted Upstream carrying any
// path -- registrypathset.Derive never actually emits a trailing slash, but
// this pins the defensive trim directly rather than relying on that holding.
func TestApplyHostPathSet_TrimsTrailingSlashFromOrigin(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", HostRooted: true}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com/",
			Subtrees: []registrypathset.Subtree{{Ecosystem: "npm", Path: "/npm"}},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	if got.Upstream != "https://host.example.com" {
		t.Errorf("applyHostPathSet() Upstream = %q, want %q (trailing slash trimmed)", got.Upstream, "https://host.example.com")
	}
}

// TestApplyHostPathSet_NoMatchingHost_NamesRoute proves a route whose
// match-host has no entry in sets fails, naming the route's match-host.
func TestApplyHostPathSet_NoMatchingHost_NamesRoute(t *testing.T) {
	route := registryproxy.Route{MatchHost: "unknown.example.com", HostRooted: true}

	_, err := applyHostPathSet(route, map[string]registrypathset.HostPathSet{})
	if err == nil {
		t.Fatal("applyHostPathSet() = nil error, want an error: no HostPathSet for this host")
	}
	if !strings.Contains(err.Error(), "unknown.example.com") {
		t.Errorf("applyHostPathSet() error = %q, want it to name %q", err.Error(), "unknown.example.com")
	}
}

// TestApplyHostPathSet_AllowAppendsAfterDerivedPaths proves route.Allow lands
// in EnforcedPaths after every derived subtree, in declaration order --
// issue #3258's additive merge.
func TestApplyHostPathSet_AllowAppendsAfterDerivedPaths(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", HostRooted: true, Allow: []string{"/extra"}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registrypathset.Subtree{{Ecosystem: "npm", Path: "/npm"}},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/npm", "/extra"}) {
		t.Errorf("applyHostPathSet() EnforcedPaths = %v, want %v", got.EnforcedPaths, []string{"/npm", "/extra"})
	}
}

// TestApplyHostPathSet_AllowDuplicatingDerivedPathIsNotRepeated proves an
// Allow entry equal to an already-derived subtree path lands in
// EnforcedPaths once, not twice -- a duplicate would otherwise ride into the
// 403 body's path-set listing and read confusingly to an operator. A second
// Allow entry that names a genuinely new path still appends normally, so the
// dedupe only swallows the exact-duplicate case and doesn't drop real
// gap-patching entries.
func TestApplyHostPathSet_AllowDuplicatingDerivedPathIsNotRepeated(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", HostRooted: true, Allow: []string{"/npm", "/extra"}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registrypathset.Subtree{{Ecosystem: "npm", Path: "/npm"}},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/npm", "/extra"}) {
		t.Errorf("applyHostPathSet() EnforcedPaths = %v, want %v (duplicate /npm collapsed, /extra still appended)", got.EnforcedPaths, []string{"/npm", "/extra"})
	}
}

// TestApplyHostPathSet_CargoIndexBasesFilteredFromMixedEcosystems proves that
// on a host declaring both a cargo and an npm subtree, EnforcedPaths keeps
// both (every ecosystem) while CargoIndexBases keeps only the cargo one.
func TestApplyHostPathSet_CargoIndexBasesFilteredFromMixedEcosystems(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", HostRooted: true}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:   "host.example.com",
			Origin: "https://host.example.com",
			Subtrees: []registrypathset.Subtree{
				{Ecosystem: "cargo", Path: "/index-a"},
				{Ecosystem: "npm", Path: "/npm"},
			},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/index-a", "/npm"}) {
		t.Errorf("applyHostPathSet() EnforcedPaths = %v, want %v", got.EnforcedPaths, []string{"/index-a", "/npm"})
	}
	if !reflect.DeepEqual(got.CargoIndexBases, []string{"/index-a"}) {
		t.Errorf("applyHostPathSet() CargoIndexBases = %v, want %v", got.CargoIndexBases, []string{"/index-a"})
	}
}

// TestApplyHostPathSet_CargoIndexBasesTwoCargoSubtreesInDerivationOrder
// proves that two cargo registries sharing one host (the two-registries-one-
// host shape) both land in CargoIndexBases, in the same order Subtrees
// carries them.
func TestApplyHostPathSet_CargoIndexBasesTwoCargoSubtreesInDerivationOrder(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", HostRooted: true}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:   "host.example.com",
			Origin: "https://host.example.com",
			Subtrees: []registrypathset.Subtree{
				{Ecosystem: "cargo", Path: "/index-a"},
				{Ecosystem: "cargo", Path: "/index-b"},
			},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.CargoIndexBases, []string{"/index-a", "/index-b"}) {
		t.Errorf("applyHostPathSet() CargoIndexBases = %v, want %v", got.CargoIndexBases, []string{"/index-a", "/index-b"})
	}
}

// TestApplyHostPathSet_CargoIndexBasesNilWhenNoCargoSubtrees proves that a
// host declaring only non-cargo subtrees leaves CargoIndexBases nil, rather
// than an empty non-nil slice.
func TestApplyHostPathSet_CargoIndexBasesNilWhenNoCargoSubtrees(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", HostRooted: true}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registrypathset.Subtree{{Ecosystem: "npm", Path: "/npm"}},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	if got.CargoIndexBases != nil {
		t.Errorf("applyHostPathSet() CargoIndexBases = %v, want nil", got.CargoIndexBases)
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
