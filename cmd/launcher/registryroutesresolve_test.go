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

// Production reaches the read-error wrapper only through
// buildRegistryProxyRoutes, which the checks.go registry-proxy-routes row's
// identical read/Parse/Peek always shadows by failing first, so this test calls
// resolveRegistryRoutesFromFile directly.
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

// resolveRegistryRoutesFromFile passes a Parse failure straight through, so this
// pins that the error branch returns Parse's own error rather than swallowing or
// re-wrapping it.
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

// The wrap under test is resolveRegistryRoutesFromFile's own "resolving
// credential for route %q". Production reaches the checks.go Peek gate's wrap
// instead; TestBootstrap_RegistryProxyRoutesFile_ResolveFailure_ChecksGateNamesRoute
// in bootstrap_test.go covers that gate.
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

// The [routes.ecosystems.cargo] registries key replaces the retired top-level
// cargo-registries key (ADR 0048, issue #3405). The Route's Ecosystems block is
// the only place a cargo registries list travels from here on (issue #3404).
func TestResolveRegistryRoutesFromFile_CargoRegistriesBlockReadIntoEcosystems(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_ROUTES_CARGO_REGISTRIES_CRED", "s3kr1t")
	path := writeRoutesFile(t, `
[[routes]]
match-host = "crates.example.com"
credential = { env = "SPINDRIFT_TEST_ROUTES_CARGO_REGISTRIES_CRED" }

[routes.ecosystems.cargo]
registries = ["example-remote", "another_one"]
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

// Every production route table must carry a Prefix (issue #3142).
// resolveRegistryRoutesFromFile leaves Prefix unset; only
// buildRegistryProxyRoutes runs registryproxy.AssignPrefixes.
func TestBuildRegistryProxyRoutes_FilePath_AssignsPrefixes(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_ROUTES_PREFIX_CRED", "s3kr1t")
	path := writeRoutesFile(t, `
[[routes]]
match-host = "crates.example.com"
upstream-origin = "https://crates.example.com"
credential = { env = "SPINDRIFT_TEST_ROUTES_PREFIX_CRED" }

[routes.ecosystems.cargo]
registries = ["example-remote"]
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

// Issue #3145: with no routes file, dispatch behaves exactly as a proxy-less
// dispatch does. The five scalar REGISTRY_PROXY_* knobs that used to synthesize
// a bridge route are retired (ADR 0044/0045), so an empty
// registryProxyRoutesFile is the only input buildRegistryProxyRoutes reads.
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

// resolveRegistryRoutesFromFile does no derivation of its own; that is
// buildRegistryProxyRoutes's job. A caller that needs only the parse and
// credential step keeps working with no Target-repo checkout.
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

// When registryRouteDriftRepoDirFn resolves no checkout at all (repoDir == ""),
// the launch must fail naming the match-host rather than serve the route
// unenforced.
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

// c.codeForge is left unset, so checkoutIsTargetRepo hits its default case and
// refuses to identify the resolved checkout as the Target repo. The route must
// fail closed, the same "treat as absent" gate the doctor drift row uses.
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

// The Target repo checkout resolves fine but declares no registry on that host,
// so the derived path-set has no HostPathSet for it and the route must fail
// closed naming its own match-host.
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

// Issue #3258 AC3: one "allow" line in the route TOML patches the derivation gap
// of a repo whose .npmrc only derives "/npm", so EnforcedPaths ends up covering
// both the derived and the allow-declared path.
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

// Issue #3258's acceptance demo: with the route table wired straight into
// registryproxy.New, a request under the allow-only "/dl" path forwards exactly
// like one under the derived "/npm" path, while a path under neither still 403s.
// Host-rooted enforcement stays unconditional and allow never loosens it.
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

// Issue #3259: applyHostPathSet tags each derived path with its declaring
// ecosystem in EnforcedSubtrees, not just the flat, untagged EnforcedPaths the
// Forwarder's admission check already used. The fixture declares both an npm and
// a yarn registry on one host, so each declaration must get its own tag.
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

// registryproxy.New rejects a host-rooted Upstream carrying any path, so the
// Upstream assignment trims a trailing "/" from HostPathSet.Origin.
// registrypathset.Derive never emits one, so this pins the defensive trim
// directly rather than relying on that holding.
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

// Issue #3258's additive merge: route.Allow lands in EnforcedPaths after every
// derived subtree, in declaration order.
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

// A duplicated path would appear twice in the 403 body's path-set listing and
// confuse an operator. The second Allow entry names a genuinely new path, so the
// dedupe must drop only the exact duplicate, never a real gap-patching entry.
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

// The pre-#3400 shape kept only cargo's subtrees, in the since-deleted
// Route.CargoIndexBases field. On a host declaring both a cargo and an npm
// subtree, one ecosystem's filter must no longer drop the other's tag.
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

// Two cargo registries share one host. Derivation order must survive the
// projection into EnforcedSubtrees, not just the filtered cargo-only subset the
// pre-#3400 shape kept it on.
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

// declaredPaths walks ecosystem.Table's rows generically rather than a renamed
// hand list of the fields gradle and go used to get. npm has no dedicated
// declared-path field of its own, yet a [routes.ecosystems.npm] "path" key must
// still land its own npm-tagged subtree and, deduped, its own EnforcedPaths entry.
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

// The paths dedupe must never suppress the subtree tag when a gradle path
// duplicates an Allow entry, or GradleInitScript finds no gradle-tagged entry
// and silently renders the inert no-redirect script even though the path itself
// is enforced.
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

// npm and gradle are both configured at "/npm": the gradle declaration must
// still land its own tagged EnforcedSubtrees entry alongside the derived npm
// one, while EnforcedPaths dedupes to a single occurrence.
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

// Issue #3259: a gradle path alone never establishes a host-rooted route's
// upstream origin, but it rides along once another ecosystem (here npm) has
// established one, landing in both EnforcedPaths and EnforcedSubtrees.
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

[routes.ecosystems.gradle]
path = "/gradle-maven"
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

// A gradle declaration alone resolves no upstream origin, so the "declares no
// registry on that host" error must still fire, extended to name the
// declaration's own limitation by its [routes.ecosystems.gradle] spelling.
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

[routes.ecosystems.gradle]
path = "/gradle-maven"
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

// The [routes.ecosystems.gradle] path key replaces the retired gradle-path field
// (ADR 0048, issue #3405; the field itself was issue #3259) and is read straight
// onto the Route's Ecosystems block, the same treatment cargo's registries block
// gets.
func TestResolveRegistryRoutesFromFile_GradlePathBlockReadIntoEcosystems(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"

[routes.ecosystems.gradle]
path = "/gradle-maven"
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

// Issue #3260, mirroring the gradle case: the paths dedupe must never suppress
// the subtree tag when a go path duplicates an Allow entry, or a go binding
// renderer finds no go-tagged entry and silently exports no GOPROXY even though
// the path itself is enforced.
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

// Issue #3260, mirroring the gradle case: npm and go are both configured at
// "/npm", so the go declaration must still land its own tagged EnforcedSubtrees
// entry while EnforcedPaths dedupes to a single occurrence.
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

// Issue #3260's absence half: a route declaring no go path must leave
// EnforcedSubtrees free of any go-tagged entry, even with another declared path
// present. A stray go tag would make the go binding renderer export a GOPROXY
// the operator never asked for, aimed at another ecosystem's subtree.
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

// gradle and go serve unrelated ecosystems, so applying one must never exclude
// the other. The go block lands first because declaredPaths walks
// ecosystem.Table in its own load-bearing order (cargo, npm, yarn, pnpm, go,
// gradle).
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

// Issue #3260, mirroring the gradle case: a go path alone never establishes a
// host-rooted route's upstream origin, but it rides along once another ecosystem
// (here npm) has established one.
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

[routes.ecosystems.go]
path = "/go-modules"
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

// Issue #3403: one host-rooted route may declare go, gradle, and cargo blocks
// together, go still applied before gradle (ecosystem.Table order). Cargo's block
// carries no path, so it adds nothing to EnforcedPaths or EnforcedSubtrees and
// needs no origin; only its registries list must survive resolution.
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

// Issue #3260, mirroring the gradle case: a go declaration alone resolves no
// upstream origin, so the "declares no registry on that host" error must still
// fire, extended to name the limitation by its [routes.ecosystems.go] spelling.
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

[routes.ecosystems.go]
path = "/go-modules"
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

// The [routes.ecosystems.go] path key replaces the retired go-path field (ADR
// 0048, issue #3405; the field itself was issue #3260) and is read straight onto
// the Route's Ecosystems block, the same treatment gradle's path block gets.
func TestResolveRegistryRoutesFromFile_GoPathBlockReadIntoEcosystems(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"

[routes.ecosystems.go]
path = "/go-modules"
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

// Issue #3405 and ADR 0048 at the launch gate: resolveRegistryRoutesFromFile
// propagates Parse's retirement error unwrapped, so a routes file still spelling
// the retired top-level way (go-path here) never reaches
// buildRegistryProxyRoutes. The error must name the route and the retired key.
func TestResolveRegistryRoutesFromFile_RetiredEcosystemKeyRefusedAtLaunchGate(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
go-path = "/go-modules"
`)

	routes, err := resolveRegistryRoutesFromFile(path)
	if err == nil {
		t.Fatal("resolveRegistryRoutesFromFile() = nil error, want an error: go-path is retired")
	}
	if routes != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() routes = %+v, want nil", routes)
	}
	if !strings.Contains(err.Error(), `"host.example.com"`) {
		t.Errorf("resolveRegistryRoutesFromFile() error = %q, want it to name the route %q", err.Error(), "host.example.com")
	}
	if !strings.Contains(err.Error(), "go-path") {
		t.Errorf("resolveRegistryRoutesFromFile() error = %q, want it to name the retired key %q", err.Error(), "go-path")
	}
}

// Issue #3140 added the exec, npmrc, and gradle-properties sources. This
// exercises the real Resolve path, not doctor's Peek, across all three plus the
// pre-existing env source, checking each credential pairs with the right route
// by match-host.
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

// mustLocalAccumulationRepo seeds a bare Accumulation repo from a throwaway
// "main" checkout through local.SeedAccumulationRepo, the same seed path
// bootstrap()'s seedAccumulationRepoIfHostMediated runs before dispatch. An
// empty npmrc skips the write, leaving the checkout declaring no registry.
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
// CODE_FORGE=local with accumRepo wired in, the config shape
// resolveHostRootedUpstreams' host-mediated branch reads from.
func minimalValidLocalConfigForRoutes(accumRepo string) config {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = accumRepo
	c.baseBranch = "main"
	return c
}

// Issue #3310 AC1 and AC2's positive half: under CODE_FORGE=local the route
// derives from the Accumulation repo's baseBranch snapshot, not a cwd checkout.
// t.Chdir moves the process into an unrelated directory and
// registryRouteDriftRepoDirFn is stubbed to t.Fatal, so a fallback to the
// cwd-checkout branch fails loudly.
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

// Issue #3310 AC2's negative half: the .npmrc is committed to the checkout after
// SeedAccumulationRepo already ran and is never re-seeded, so the derived
// snapshot still declares nothing and the route must fail closed. The local
// derivation reads only what landed in the Accumulation repo.
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

// Issue #3310 AC3: a codeForgeAccumulationRepoDir that doesn't exist, or isn't a
// git repo, fails the launch closed naming the route's match-host rather than
// falling back to an unenforced route.
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

// Issue #3310 AC3's other half: a reachable Accumulation repo whose baseBranch
// snapshot declares no registry still fails the route closed, the same way the
// pre-#3310 cwd-checkout path does for an unmatched host.
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

// ADR 0047's optional upstream-origin, non-default scheme and port half: a
// repo's committed config names the paths but its URL cannot always name the
// origin the launcher must dial (here a non-default port), so the declared
// origin wins while the derived subtrees stay as they were.
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

// ADR 0047's other upstream-origin case: on a host no committed config names,
// the declared origin alone establishes the route and the enforced set is empty,
// which registryproxy reads as "refuse everything", the correct default-deny
// outcome for a route that declares no path to admit.
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

// A declared-origin route on an underived host enforces exactly what it declares
// itself: allow entries, then each declared path, with no derived subtree mixed
// in.
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

[routes.ecosystems.gradle]
path = "/gradle-maven"
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

// Without a declared origin, a route on a host nothing derives for still fails
// the launch, naming the route and both real remedies, never the retired
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
