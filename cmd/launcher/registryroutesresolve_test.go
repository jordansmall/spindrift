package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/registrypathset"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryvocab"
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
// carrying MatchHost/UpstreamOrigin/AuthScheme straight from the parsed
// route.
func TestResolveRegistryRoutesFromFile_ValidFile_ResolvesCredential(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_ROUTES_HAPPY_CRED", "s3kr1t")
	path := writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-origin = "https://registry.example.com"
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
	if got.UpstreamOrigin != "https://registry.example.com" {
		t.Errorf("routes[0].UpstreamOrigin = %q, want %q", got.UpstreamOrigin, "https://registry.example.com")
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
// registryproxy.Route's Ecosystems block, straight from the parsed route.
func TestResolveRegistryRoutesFromFile_CargoRegistriesProjected(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_ROUTES_CARGO_REGISTRIES_CRED", "s3kr1t")
	path := writeRoutesFile(t, `
[[routes]]
match-host = "crates.example.com"
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
	if !reflect.DeepEqual(routes[0].Ecosystems.Strings("cargo", "registries"), want) {
		t.Errorf("routes[0].Ecosystems.Strings(cargo, registries) = %v, want %v", routes[0].Ecosystems.Strings("cargo", "registries"), want)
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
upstream-origin = "https://crates.example.com"
cargo-registries = ["example-remote"]
credential = { env = "SPINDRIFT_TEST_ROUTES_PREFIX_CRED" }
`)

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
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
	if !reflect.DeepEqual(routes[0].Ecosystems.Strings("cargo", "registries"), want) {
		t.Errorf("routes[0].Ecosystems.Strings(cargo, registries) = %v, want %v", routes[0].Ecosystems.Strings("cargo", "registries"), want)
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

// TestResolveRegistryRoutesFromFile_HostRooted_LeavesUpstreamEmpty proves a
// route is projected with Upstream left empty --
// resolveRegistryRoutesFromFile does no derivation of its own; that is
// buildRegistryProxyRoutes's job, so a caller that only needs the
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
	if routes[0].Upstream != "" {
		t.Errorf("routes[0].Upstream = %q, want empty (unresolved until buildRegistryProxyRoutes)", routes[0].Upstream)
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
// neither still 403s -- proving host-rooted enforcement is unconditional and
// allow never loosens it.
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
credential = { env = "SPINDRIFT_TEST_ALLOW_LOOP_CRED" }
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}

	p, err := registryproxy.New(routes, nil)
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
// registry on the same host-rooted host must produce one tagged subtree per
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
	want := []registryvocab.Subtree{
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
	route := registryproxy.Route{MatchHost: "host.example.com"}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com/",
			Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
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
	route := registryproxy.Route{MatchHost: "unknown.example.com"}

	_, err := applyHostPathSet(route, map[string]registrypathset.HostPathSet{})
	if err == nil {
		t.Fatal("applyHostPathSet() = nil error, want an error: no HostPathSet for this host")
	}
	if !strings.Contains(err.Error(), "unknown.example.com") {
		t.Errorf("applyHostPathSet() error = %q, want it to name %q", err.Error(), "unknown.example.com")
	}
	if !strings.Contains(err.Error(), "upstream-origin") {
		t.Errorf("applyHostPathSet() error = %q, want it to offer the upstream-origin remedy", err.Error())
	}
	if strings.Contains(err.Error(), "upstream-base-url") {
		t.Errorf("applyHostPathSet() error = %q, want it to never name the retired upstream-base-url knob", err.Error())
	}
}

// TestApplyHostPathSet_AllowAppendsAfterDerivedPaths proves route.Allow lands
// in EnforcedPaths after every derived subtree, in declaration order --
// issue #3258's additive merge.
func TestApplyHostPathSet_AllowAppendsAfterDerivedPaths(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Allow: []string{"/extra"}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
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
	route := registryproxy.Route{MatchHost: "host.example.com", Allow: []string{"/npm", "/extra"}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
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

// TestApplyHostPathSet_EnforcedSubtreesKeepsEveryEcosystemTagFromMixedEcosystems
// proves that on a host declaring both a cargo and an npm subtree,
// EnforcedPaths keeps both (every ecosystem) and EnforcedSubtrees tags each
// with its own ecosystem, rather than one ecosystem's filter dropping the
// other's tag (the pre-#3400 shape kept only cargo's subtrees, in the
// since-deleted Route.CargoIndexBases field).
func TestApplyHostPathSet_EnforcedSubtreesKeepsEveryEcosystemTagFromMixedEcosystems(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com"}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:   "host.example.com",
			Origin: "https://host.example.com",
			Subtrees: []registryvocab.Subtree{
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
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}, {Ecosystem: "npm", Path: "/npm"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("applyHostPathSet() EnforcedSubtrees = %v, want %v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestApplyHostPathSet_EnforcedSubtreesTwoCargoSubtreesInDerivationOrder
// proves that two cargo registries sharing one host (the two-registries-one-
// host shape) both land in EnforcedSubtrees, in the same order Subtrees
// carries them -- derivation order survives the projection, not just the
// filtered cargo-only subset the pre-#3400 shape kept it on.
func TestApplyHostPathSet_EnforcedSubtreesTwoCargoSubtreesInDerivationOrder(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com"}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:   "host.example.com",
			Origin: "https://host.example.com",
			Subtrees: []registryvocab.Subtree{
				{Ecosystem: "cargo", Path: "/index-a"},
				{Ecosystem: "cargo", Path: "/index-b"},
			},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index-a"}, {Ecosystem: "cargo", Path: "/index-b"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("applyHostPathSet() EnforcedSubtrees = %v, want %v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestApplyHostPathSet_EcosystemsBlockDeclaredPathForRowWithNoDedicatedField
// proves declaredPaths walks ecosystem.Table's rows generically, reading
// each row's Ecosystems block, rather than a renamed hand list of the
// fields gradle and go used to get: npm has no dedicated declared-path
// field of its own (its path is normally derived, never operator-declared),
// yet a [routes.ecosystems.npm] block's "path" key still lands its own
// npm-tagged EnforcedSubtrees entry and, deduped, its own EnforcedPaths
// entry, exactly as gradle's or go's declared block would.
func TestApplyHostPathSet_EcosystemsBlockDeclaredPathForRowWithNoDedicatedField(t *testing.T) {
	route := registryproxy.Route{
		MatchHost:  "host.example.com",
		Ecosystems: registryvocab.RouteEcosystems{"npm": registryvocab.RouteDeclaration{"path": "/npm-declared"}},
	}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index"}},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	wantPaths := []string{"/index", "/npm-declared"}
	if !reflect.DeepEqual(got.EnforcedPaths, wantPaths) {
		t.Errorf("applyHostPathSet() EnforcedPaths = %v, want %v", got.EnforcedPaths, wantPaths)
	}
	wantSubtrees := []registryvocab.Subtree{
		{Ecosystem: "cargo", Path: "/index"},
		{Ecosystem: "npm", Path: "/npm-declared"},
	}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("applyHostPathSet() EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestApplyHostPathSet_GradlePathCollidingWithAllowStillTagsSubtree proves a
// gradle-path that exactly duplicates an Allow entry (both naming the same
// path) still lands a "gradle"-tagged EnforcedSubtrees entry -- the paths
// dedupe must never suppress the subtree tag, or GradleInitScript finds no
// gradle-tagged entry and silently renders the inert no-redirect script even
// though the path itself is enforced.
func TestApplyHostPathSet_GradlePathCollidingWithAllowStillTagsSubtree(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Allow: []string{"/maven2"}, Ecosystems: registryvocab.RouteEcosystems{"gradle": registryvocab.RouteDeclaration{"path": "/maven2"}}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/npm", "/maven2"}) {
		t.Errorf("applyHostPathSet() EnforcedPaths = %v, want %v (duplicate /maven2 collapsed to one entry)", got.EnforcedPaths, []string{"/npm", "/maven2"})
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}, {Ecosystem: "gradle", Path: "/maven2"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("applyHostPathSet() EnforcedSubtrees = %v, want %v (gradle-path must still tag EnforcedSubtrees despite colliding with Allow)", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestApplyHostPathSet_GradlePathCollidingWithDerivedSubtreeStillTagsSubtree
// proves a gradle-path that exactly duplicates an already-derived subtree
// path (e.g. npm and gradle both configured at "/npm") still lands its own
// "gradle"-tagged EnforcedSubtrees entry alongside the derived "npm" one --
// EnforcedPaths still dedupes to a single occurrence of the shared path.
func TestApplyHostPathSet_GradlePathCollidingWithDerivedSubtreeStillTagsSubtree(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Ecosystems: registryvocab.RouteEcosystems{"gradle": registryvocab.RouteDeclaration{"path": "/npm"}}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/npm"}) {
		t.Errorf("applyHostPathSet() EnforcedPaths = %v, want %v (duplicate /npm collapsed to one entry)", got.EnforcedPaths, []string{"/npm"})
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}, {Ecosystem: "gradle", Path: "/npm"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("applyHostPathSet() EnforcedSubtrees = %v, want %v (gradle-path must still tag EnforcedSubtrees despite colliding with an already-derived path)", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_GradlePathRidesAlongWithNpm proves
// that a route's gradle-path (issue #3259) is appended to both
// EnforcedPaths and EnforcedSubtrees, tagged "gradle", alongside whatever
// other ecosystem's config the Target repo checkout makes discoverable on
// the same host -- gradle-path alone never establishes the host-rooted
// route's upstream origin, but it can ride along once some other ecosystem
// (here, npm) already has.
func TestBuildRegistryProxyRoutes_HostRooted_GradlePathRidesAlongWithNpm(t *testing.T) {
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
gradle-path = "/gradle-maven"
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
	wantPaths := []string{"/npm", "/gradle-maven"}
	if !reflect.DeepEqual(got.EnforcedPaths, wantPaths) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, wantPaths)
	}
	wantSubtrees := []registryvocab.Subtree{
		{Ecosystem: "npm", Path: "/npm"},
		{Ecosystem: "gradle", Path: "/gradle-maven"},
	}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("routes[0].EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_GradlePathAlone_NoOriginFailsClosed
// proves that a gradle-path declaration alone, with no other ecosystem's
// config discoverable on the same host, still cannot resolve a host-rooted
// route's upstream origin -- the existing "declares no registry on that
// host" error must still fire, extended to mention the declaration's own
// limitation by its [routes.ecosystems.gradle] spelling.
func TestBuildRegistryProxyRoutes_HostRooted_GradlePathAlone_NoOriginFailsClosed(t *testing.T) {
	repoDir := t.TempDir()

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return repoDir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
gradle-path = "/gradle-maven"
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	_, err := buildRegistryProxyRoutes(c)
	if err == nil {
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: no other ecosystem's config is discoverable on this host")
	}
	if !strings.Contains(err.Error(), "host.example.com") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to name the host-rooted route %q", err.Error(), "host.example.com")
	}
	if !strings.Contains(err.Error(), "ecosystems.gradle.path") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to mention ecosystems.gradle.path's limitation", err.Error())
	}
}

// TestResolveRegistryRoutesFromFile_GradlePathProjected verifies that a
// route's gradle-path field (issue #3259) is projected onto the returned
// registryproxy.Route's Ecosystems block, straight from the parsed route --
// the same treatment cargo-registries gets.
func TestResolveRegistryRoutesFromFile_GradlePathProjected(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
gradle-path = "/gradle-maven"
`)

	routes, err := resolveRegistryRoutesFromFile(path)
	if err != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("resolveRegistryRoutesFromFile() = %d routes, want 1", len(routes))
	}
	if want := "/gradle-maven"; routes[0].Ecosystems.Path("gradle") != want {
		t.Errorf("routes[0].Ecosystems.Path(gradle) = %q, want %q", routes[0].Ecosystems.Path("gradle"), want)
	}
}

// TestApplyHostPathSet_GoPathCollidingWithAllowStillTagsSubtree mirrors
// TestApplyHostPathSet_GradlePathCollidingWithAllowStillTagsSubtree
// (issue #3260): a go-path that exactly duplicates an Allow entry still
// lands its own "go"-tagged EnforcedSubtrees entry -- the paths dedupe must
// never suppress the subtree tag, or a go binding renderer finds no
// go-tagged entry and silently exports no GOPROXY even though the path
// itself is enforced.
func TestApplyHostPathSet_GoPathCollidingWithAllowStillTagsSubtree(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Allow: []string{"/go-modules"}, Ecosystems: registryvocab.RouteEcosystems{"go": registryvocab.RouteDeclaration{"path": "/go-modules"}}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/npm", "/go-modules"}) {
		t.Errorf("applyHostPathSet() EnforcedPaths = %v, want %v (duplicate /go-modules collapsed to one entry)", got.EnforcedPaths, []string{"/npm", "/go-modules"})
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}, {Ecosystem: "go", Path: "/go-modules"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("applyHostPathSet() EnforcedSubtrees = %v, want %v (go-path must still tag EnforcedSubtrees despite colliding with Allow)", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestApplyHostPathSet_GoPathCollidingWithDerivedSubtreeStillTagsSubtree
// mirrors TestApplyHostPathSet_GradlePathCollidingWithDerivedSubtreeStillTagsSubtree
// (issue #3260): a go-path that exactly duplicates an already-derived
// subtree path (e.g. npm and go both configured at "/npm") still lands its
// own "go"-tagged EnforcedSubtrees entry alongside the derived "npm" one --
// EnforcedPaths still dedupes to a single occurrence of the shared path.
func TestApplyHostPathSet_GoPathCollidingWithDerivedSubtreeStillTagsSubtree(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Ecosystems: registryvocab.RouteEcosystems{"go": registryvocab.RouteDeclaration{"path": "/npm"}}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/npm"}) {
		t.Errorf("applyHostPathSet() EnforcedPaths = %v, want %v (duplicate /npm collapsed to one entry)", got.EnforcedPaths, []string{"/npm"})
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}, {Ecosystem: "go", Path: "/npm"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("applyHostPathSet() EnforcedSubtrees = %v, want %v (go-path must still tag EnforcedSubtrees despite colliding with an already-derived path)", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestApplyHostPathSet_WithoutGoPathTagsNoGoSubtree pins the absence half of
// the go-path contract (issue #3260): a host-rooted route that declares no
// go-path must leave EnforcedSubtrees free of any "go"-tagged entry, even
// when another declared path (here gradle-path) is present. A stray go tag
// would make the go binding renderer export a GOPROXY the operator never
// asked for, aimed at some other ecosystem's subtree.
func TestApplyHostPathSet_WithoutGoPathTagsNoGoSubtree(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Ecosystems: registryvocab.RouteEcosystems{"gradle": registryvocab.RouteDeclaration{"path": "/maven2"}}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	for _, sub := range got.EnforcedSubtrees {
		if sub.Ecosystem == "go" {
			t.Errorf("applyHostPathSet() EnforcedSubtrees = %+v, want no %q-tagged entry (the route declares no go-path)", got.EnforcedSubtrees, "go")
		}
	}
}

// TestApplyHostPathSet_GradlePathAndGoPathCoexistBothTagged pins that a
// single route may declare both operator-declared path fields at once --
// gradle and go serve unrelated ecosystems, so nothing about applying one
// should exclude the other. Both must land in EnforcedPaths and both must
// produce their own tagged EnforcedSubtrees entry, go first -- declaredPaths
// walks ecosystem.Table in its own load-bearing order (cargo, npm, yarn,
// pnpm, go, gradle), so go's block is always applied before gradle's.
func TestApplyHostPathSet_GradlePathAndGoPathCoexistBothTagged(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Ecosystems: registryvocab.RouteEcosystems{"gradle": registryvocab.RouteDeclaration{"path": "/maven2"}, "go": registryvocab.RouteDeclaration{"path": "/go-modules"}}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
		},
	}

	got, err := applyHostPathSet(route, sets)
	if err != nil {
		t.Fatalf("applyHostPathSet() error = %v, want nil", err)
	}
	wantPaths := []string{"/npm", "/go-modules", "/maven2"}
	if !reflect.DeepEqual(got.EnforcedPaths, wantPaths) {
		t.Errorf("applyHostPathSet() EnforcedPaths = %v, want %v", got.EnforcedPaths, wantPaths)
	}
	wantSubtrees := []registryvocab.Subtree{
		{Ecosystem: "npm", Path: "/npm"},
		{Ecosystem: "go", Path: "/go-modules"},
		{Ecosystem: "gradle", Path: "/maven2"},
	}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("applyHostPathSet() EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_GoPathRidesAlongWithNpm mirrors
// TestBuildRegistryProxyRoutes_HostRooted_GradlePathRidesAlongWithNpm
// (issue #3260): a route's go-path is appended to both EnforcedPaths and
// EnforcedSubtrees, tagged "go", alongside whatever other ecosystem's
// config the Target repo checkout makes discoverable on the same host --
// go-path alone never establishes the host-rooted route's upstream origin,
// but it can ride along once some other ecosystem (here, npm) already has.
func TestBuildRegistryProxyRoutes_HostRooted_GoPathRidesAlongWithNpm(t *testing.T) {
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
go-path = "/go-modules"
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
	wantPaths := []string{"/npm", "/go-modules"}
	if !reflect.DeepEqual(got.EnforcedPaths, wantPaths) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, wantPaths)
	}
	wantSubtrees := []registryvocab.Subtree{
		{Ecosystem: "npm", Path: "/npm"},
		{Ecosystem: "go", Path: "/go-modules"},
	}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("routes[0].EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_GoGradleCargoBlocksCoexist pins
// issue #3403's acceptance criterion: a single host-rooted route may declare
// go, gradle, and cargo blocks together via [routes.ecosystems.<name>]. Go
// and gradle's declared paths ride along the npm-derived origin the same way
// their retired go-path/gradle-path fields did in
// TestBuildRegistryProxyRoutes_HostRooted_GoPathRidesAlongWithNpm and its
// gradle sibling above, go still applied before gradle (ecosystem.Table
// order); cargo's block carries no path at all, so it neither adds to
// EnforcedPaths/EnforcedSubtrees nor needs an origin of its own -- only its
// registries list, and the block itself, must survive resolution onto the
// returned registryproxy.Route.
func TestBuildRegistryProxyRoutes_HostRooted_GoGradleCargoBlocksCoexist(t *testing.T) {
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

[routes.ecosystems.go]
path = "/go-modules"

[routes.ecosystems.gradle]
path = "/maven2"

[routes.ecosystems.cargo]
registries = ["internal", "crates-remote"]
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

	wantPaths := []string{"/npm", "/go-modules", "/maven2"}
	if !reflect.DeepEqual(got.EnforcedPaths, wantPaths) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, wantPaths)
	}
	wantSubtrees := []registryvocab.Subtree{
		{Ecosystem: "npm", Path: "/npm"},
		{Ecosystem: "go", Path: "/go-modules"},
		{Ecosystem: "gradle", Path: "/maven2"},
	}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("routes[0].EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, wantSubtrees)
	}

	if want := "/go-modules"; got.Ecosystems.Path("go") != want {
		t.Errorf("routes[0].Ecosystems.Path(go) = %q, want %q", got.Ecosystems.Path("go"), want)
	}
	if want := "/maven2"; got.Ecosystems.Path("gradle") != want {
		t.Errorf("routes[0].Ecosystems.Path(gradle) = %q, want %q", got.Ecosystems.Path("gradle"), want)
	}
	wantRegistries := []string{"internal", "crates-remote"}
	if gotRegistries := ecosystem.CargoRouteRegistries(got.Ecosystems); !reflect.DeepEqual(gotRegistries, wantRegistries) {
		t.Errorf("ecosystem.CargoRouteRegistries(routes[0].Ecosystems) = %v, want %v", gotRegistries, wantRegistries)
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_GoPathAlone_NoOriginFailsClosed
// mirrors TestBuildRegistryProxyRoutes_HostRooted_GradlePathAlone_NoOriginFailsClosed
// (issue #3260): a go-path declaration alone, with no other ecosystem's
// config discoverable on the same host, still cannot resolve a host-rooted
// route's upstream origin -- the existing "declares no registry on that
// host" error must still fire, extended to mention the declaration's own
// limitation by its [routes.ecosystems.go] spelling.
func TestBuildRegistryProxyRoutes_HostRooted_GoPathAlone_NoOriginFailsClosed(t *testing.T) {
	repoDir := t.TempDir()

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return repoDir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
go-path = "/go-modules"
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	_, err := buildRegistryProxyRoutes(c)
	if err == nil {
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: no other ecosystem's config is discoverable on this host")
	}
	if !strings.Contains(err.Error(), "host.example.com") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to name the host-rooted route %q", err.Error(), "host.example.com")
	}
	if !strings.Contains(err.Error(), "ecosystems.go.path") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to mention ecosystems.go.path's limitation", err.Error())
	}
}

// TestResolveRegistryRoutesFromFile_GoPathProjected verifies that a route's
// go-path field (issue #3260) is projected onto the returned
// registryproxy.Route's Ecosystems block, straight from the parsed route --
// the same treatment gradle-path gets.
func TestResolveRegistryRoutesFromFile_GoPathProjected(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
go-path = "/go-modules"
`)

	routes, err := resolveRegistryRoutesFromFile(path)
	if err != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("resolveRegistryRoutesFromFile() = %d routes, want 1", len(routes))
	}
	if want := "/go-modules"; routes[0].Ecosystems.Path("go") != want {
		t.Errorf("routes[0].Ecosystems.Path(go) = %q, want %q", routes[0].Ecosystems.Path("go"), want)
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
credential = { env = "SPINDRIFT_TEST_ROUTES_MIXED_ENV_CRED" }

[[routes]]
match-host = "exec.example.com"
credential = { exec = ["/bin/sh", "-c", "echo tok-exec"] }

[[routes]]
match-host = "npmrc.example.com"
credential = { npmrc = "`+npmrcPath+`" }

[[routes]]
match-host = "gradle.example.com"
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

// mustLocalAccumulationRepo builds a throwaway working checkout on "main",
// writes .npmrc there (npmrc == "" skips the write, leaving the checkout
// declaring no registry at all), commits it, then seeds a fresh bare
// Accumulation repo from that checkout via local.SeedAccumulationRepo --
// the same seed path bootstrap()'s seedAccumulationRepoIfHostMediated runs
// before dispatch -- and returns the Accumulation repo's path.
func mustLocalAccumulationRepo(t *testing.T, npmrc string) string {
	t.Helper()
	checkout := mustSeedableCheckout(t)
	if npmrc != "" {
		if err := os.WriteFile(filepath.Join(checkout, ".npmrc"), []byte(npmrc), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRunGit(t, checkout, "add", ".npmrc")
		mustRunGit(t, checkout, "commit", "-m", "add .npmrc")
	}
	accumRepo := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumRepo, checkout, "main"); err != nil {
		t.Fatalf("SeedAccumulationRepo() error = %v, want nil", err)
	}
	return accumRepo
}

// minimalValidLocalConfigForRoutes returns minimalValidConfig() switched to
// CODE_FORGE=local with accumRepo wired in as the Accumulation repo -- the
// config shape resolveHostRootedUpstreams' host-mediated branch reads from.
func minimalValidLocalConfigForRoutes(accumRepo string) config {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = accumRepo
	c.baseBranch = "main"
	return c
}

// TestBuildRegistryProxyRoutes_HostRooted_Local_DerivesFromAccumulationRepo
// covers issue #3310 AC1 and AC2's positive half: under CODE_FORGE=local, a
// host-rooted route derives Upstream and EnforcedPaths from the
// Accumulation repo's baseBranch snapshot, not from a cwd checkout --
// t.Chdir moves the process into an unrelated directory and
// registryRouteDriftRepoDirFn is stubbed to t.Fatal (the
// TestBuildRegistryProxyRoutes_LegacyOnly_NeverConsultsRepoDir pattern), so
// the test fails loudly if the local path ever falls back to the
// cwd-checkout branch.
func TestBuildRegistryProxyRoutes_HostRooted_Local_DerivesFromAccumulationRepo(t *testing.T) {
	accumRepo := mustLocalAccumulationRepo(t, "registry=https://host.example.com/npm\n")

	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) {
		t.Fatal("registryRouteDriftRepoDirFn called under CODE_FORGE=local; the local path must derive from the Accumulation repo, never a cwd checkout")
		return "", nil
	}
	t.Cleanup(func() { registryRouteDriftRepoDirFn = orig })
	t.Chdir(t.TempDir())

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)
	c := minimalValidLocalConfigForRoutes(accumRepo)
	c.registryProxyRoutesFile = path

	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	got := routes[0]
	if got.Upstream != "https://host.example.com" {
		t.Errorf("routes[0].Upstream = %q, want %q", got.Upstream, "https://host.example.com")
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/npm"}) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, []string{"/npm"})
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_Local_CheckoutOnlyConfigIgnored
// covers issue #3310 AC2's negative half: a .npmrc committed to the working
// checkout's "main" branch after the Accumulation repo was already seeded
// from that checkout never reaches the Accumulation repo (SeedAccumulationRepo
// is not re-run), so the derived snapshot still declares nothing for
// host.example.com and the route must fail closed naming it -- proving the
// local derivation reads only what actually landed in the Accumulation
// repo, not whatever the working checkout currently holds.
func TestBuildRegistryProxyRoutes_HostRooted_Local_CheckoutOnlyConfigIgnored(t *testing.T) {
	checkout := mustSeedableCheckout(t)
	accumRepo := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumRepo, checkout, "main"); err != nil {
		t.Fatalf("SeedAccumulationRepo() error = %v, want nil", err)
	}

	if err := os.WriteFile(filepath.Join(checkout, ".npmrc"), []byte("registry=https://host.example.com/npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, checkout, "add", ".npmrc")
	mustRunGit(t, checkout, "commit", "-m", "add .npmrc, never pushed to accum")

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)
	c := minimalValidLocalConfigForRoutes(accumRepo)
	c.registryProxyRoutesFile = path

	_, err := buildRegistryProxyRoutes(c)
	if err == nil {
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: the Accumulation repo's main branch never received the .npmrc commit")
	}
	if !strings.Contains(err.Error(), "host.example.com") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to name the host-rooted route %q", err.Error(), "host.example.com")
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_Local_MissingAccumulationRepo_FailsClosed
// covers issue #3310 AC3: a codeForgeAccumulationRepoDir that doesn't exist
// (or isn't a git repo) fails the launch closed, naming the route's
// match-host, rather than falling back to an unenforced route.
func TestBuildRegistryProxyRoutes_HostRooted_Local_MissingAccumulationRepo_FailsClosed(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)
	c := minimalValidLocalConfigForRoutes(filepath.Join(t.TempDir(), "does-not-exist.git"))
	c.registryProxyRoutesFile = path

	_, err := buildRegistryProxyRoutes(c)
	if err == nil {
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: the Accumulation repo does not exist")
	}
	if !strings.Contains(err.Error(), "host.example.com") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to name the host-rooted route %q", err.Error(), "host.example.com")
	}
}

// TestBuildRegistryProxyRoutes_HostRooted_Local_NoMatchingHost_FailsClosed
// covers issue #3310 AC3's other half: a real, reachable Accumulation repo
// whose baseBranch snapshot declares no registry at all still fails the
// route closed, naming its match-host, the same way the pre-#3310
// cwd-checkout path already does for an unmatched host.
func TestBuildRegistryProxyRoutes_HostRooted_Local_NoMatchingHost_FailsClosed(t *testing.T) {
	accumRepo := mustLocalAccumulationRepo(t, "")

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)
	c := minimalValidLocalConfigForRoutes(accumRepo)
	c.registryProxyRoutesFile = path

	_, err := buildRegistryProxyRoutes(c)
	if err == nil {
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: the Accumulation repo declares no registry on host.example.com")
	}
	if !strings.Contains(err.Error(), "host.example.com") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to name the host-rooted route %q", err.Error(), "host.example.com")
	}
}

// TestBuildRegistryProxyRoutes_UpstreamOrigin_OverridesDerivedOrigin covers
// the non-default scheme/port half of ADR 0047's optional upstream-origin: a
// repo's committed config names the paths but its URL cannot always name the
// origin the launcher must actually dial (here, a non-default port), so the
// declared origin wins over the derived one while the derived subtrees stay
// exactly as they were.
func TestBuildRegistryProxyRoutes_UpstreamOrigin_OverridesDerivedOrigin(t *testing.T) {
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
upstream-origin = "https://host.example.com:8443"
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
	if want := "https://host.example.com:8443"; got.Upstream != want {
		t.Errorf("routes[0].Upstream = %q, want the declared origin %q", got.Upstream, want)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/npm"}) {
		t.Errorf("routes[0].EnforcedPaths = %v, want the derived %v", got.EnforcedPaths, []string{"/npm"})
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("routes[0].EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestBuildRegistryProxyRoutes_UpstreamOrigin_UndeclaredHost_ResolvesEmpty
// covers ADR 0047's other upstream-origin case: a host serving only
// ecosystems no committed config names, so nothing derives for it. The
// declared origin alone establishes the route, and the enforced set is
// empty -- which registryproxy reads as "refuse everything", the correct
// default-deny outcome for a route that declares no path to admit. A
// non-default scheme and port ride through to Upstream verbatim.
func TestBuildRegistryProxyRoutes_UpstreamOrigin_UndeclaredHost_ResolvesEmpty(t *testing.T) {
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
upstream-origin = "http://host.example.com:8081"
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil: upstream-origin alone establishes the route", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	got := routes[0]
	if want := "http://host.example.com:8081"; got.Upstream != want {
		t.Errorf("routes[0].Upstream = %q, want the declared origin %q", got.Upstream, want)
	}
	if len(got.EnforcedPaths) != 0 {
		t.Errorf("routes[0].EnforcedPaths = %v, want empty: the route declares no path to admit", got.EnforcedPaths)
	}
}

// TestBuildRegistryProxyRoutes_UpstreamOrigin_UndeclaredHost_AllowIsTheSet
// pins what a declared-origin route on an underived host actually enforces:
// exactly what it declares itself -- allow entries, then each declared path
// -- with no derived subtree mixed in.
func TestBuildRegistryProxyRoutes_UpstreamOrigin_UndeclaredHost_AllowIsTheSet(t *testing.T) {
	repoDir := t.TempDir()

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return repoDir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
upstream-origin = "https://host.example.com"
allow = ["/dl"]
gradle-path = "/gradle-maven"
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
	if want := []string{"/dl", "/gradle-maven"}; !reflect.DeepEqual(got.EnforcedPaths, want) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, want)
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: "gradle", Path: "/gradle-maven"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("routes[0].EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// TestBuildRegistryProxyRoutes_NoUpstreamOrigin_UndeclaredHost_FailsClosed
// proves the gap upstream-origin fills is still a closed door without it: a
// route on a host nothing derives for, and with no declared origin, fails
// the launch naming the route and both real remedies -- never the retired
// upstream-base-url knob.
func TestBuildRegistryProxyRoutes_NoUpstreamOrigin_UndeclaredHost_FailsClosed(t *testing.T) {
	repoDir := t.TempDir()

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
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: nothing establishes this route's upstream origin")
	}
	for _, want := range []string{"host.example.com", "committed config", "upstream-origin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("buildRegistryProxyRoutes() error = %q, want it to contain %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "upstream-base-url") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to never name the retired upstream-base-url knob", err.Error())
	}
}
