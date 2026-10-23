package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/registrypathset"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryvocab"
	"spindrift.dev/launcher/internal/testutil"
)

// These tests drive route resolution over the synthetic Strategy and a twin of it,
// so a host can declare two ecosystems at once; the real Strategies' declaration
// shapes are pinned by the ecosystem corpora under
// internal/ecosystem/testdata/corpus.
var (
	twinRow     = synthetic.Twin("twin")
	otherRow    = synthetic.Twin("other")
	resolveRows = []ecosystem.Strategy{synthetic.Strategy, twinRow}
	threeRows   = []ecosystem.Strategy{synthetic.Strategy, twinRow, otherRow}
)

// Production reaches the read-error wrapper only through
// buildRegistryProxyRoutes, which the checks.go registry-proxy-routes row's
// identical read/Parse/Peek always shadows by failing first, so this test calls
// resolveRegistryRoutesFromFile directly.
func TestResolveRegistryRoutesFromFile_MissingFile_WrapsReadError(t *testing.T) {
	missing := t.TempDir() + "/does-not-exist.toml"

	routes, err := resolveRegistryRoutesFromFile(missing, resolveRows)
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

	routes, err := resolveRegistryRoutesFromFile(invalid, resolveRows)
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

	routes, err := resolveRegistryRoutesFromFile(path, resolveRows)
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

	routes, err := resolveRegistryRoutesFromFile(path, resolveRows)
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

// A [routes.ecosystems.<name>] block (ADR 0048, issue #3405) is read straight
// onto the Route's Ecosystems block, the only place a per-ecosystem declaration
// travels from here on (issues #3259, #3404): the generic path key and a key
// only the Strategy's own RouteDeclaration accepts.
func TestResolveRegistryRoutesFromFile_RouteDeclarationBlockReadIntoEcosystems(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"

[routes.ecosystems.synthetic]
path = "/mirror"
mirror-path = "/other"
`)

	routes, err := resolveRegistryRoutesFromFile(path, resolveRows)
	if err != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("resolveRegistryRoutesFromFile() = %d routes, want 1", len(routes))
	}
	if want := "/mirror"; routes[0].Ecosystems.Path(synthetic.Strategy.Name) != want {
		t.Errorf("routes[0].Ecosystems.Path(synthetic) = %q, want %q", routes[0].Ecosystems.Path(synthetic.Strategy.Name), want)
	}
	if got, want := routes[0].Ecosystems[synthetic.Strategy.Name]["mirror-path"], "/other"; got != want {
		t.Errorf("routes[0].Ecosystems[synthetic][mirror-path] = %v, want %q", got, want)
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

[routes.ecosystems.synthetic]
mirror-path = "/other"
`)

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	routes, err := buildRegistryProxyRoutes(c, resolveRows)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	if routes[0].Prefix == "" {
		t.Error("routes[0].Prefix is empty, want AssignPrefixes to have set it")
	}
	if got, want := routes[0].Ecosystems[synthetic.Strategy.Name]["mirror-path"], "/other"; got != want {
		t.Errorf("routes[0].Ecosystems[synthetic][mirror-path] = %v, want %q", got, want)
	}
}

// Issue #3145: with no routes file, dispatch behaves exactly as a proxy-less
// dispatch does. The five scalar REGISTRY_PROXY_* knobs that used to synthesize
// a bridge route are retired (ADR 0044/0045), so an empty
// registryProxyRoutesFile is the only input buildRegistryProxyRoutes reads.
func TestBuildRegistryProxyRoutes_NoRoutesFile_ReturnsNil(t *testing.T) {
	c := config{}

	routes, err := buildRegistryProxyRoutes(c, resolveRows)
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

	routes, err := resolveRegistryRoutesFromFile(path, resolveRows)
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
	routes, err := buildRegistryProxyRoutes(c, resolveRows)
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
	_, err := buildRegistryProxyRoutes(c, resolveRows)
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
	testutil.WriteFixture(t, repoDir, synthetic.Strategy.InTreeConfigPath, "main = https://other.example.com/index\n")

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
	_, err := buildRegistryProxyRoutes(c, resolveRows)
	if err == nil {
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: the repo declares no registry on host.example.com")
	}
	if !strings.Contains(err.Error(), "host.example.com") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to name the host-rooted route %q", err.Error(), "host.example.com")
	}
}

func TestBuildRegistryProxyRoutes_HostRooted_DerivesUpstreamAndEnforcedPaths(t *testing.T) {
	repoDir := t.TempDir()
	testutil.WriteFixture(t, repoDir, synthetic.Strategy.InTreeConfigPath, "main = https://host.example.com/index\n")

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
	routes, err := buildRegistryProxyRoutes(c, resolveRows)
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
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/index"}) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, []string{"/index"})
	}
}

// checkoutIsTargetRepo follows GH_HOST (issue #3912), so a GitHub Enterprise
// origin must derive routes the same way a github.com one does, not fail
// closed as if the checkout weren't the Target repo.
func TestBuildRegistryProxyRoutes_HostRooted_DerivesUpstream_UnderGHHost(t *testing.T) {
	t.Setenv("GH_HOST", "ghe.example.com")

	repoDir := t.TempDir()
	testutil.WriteFixture(t, repoDir, synthetic.Strategy.InTreeConfigPath, "main = https://host.example.com/index\n")

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return repoDir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@ghe.example.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	if _, err := buildRegistryProxyRoutes(c, resolveRows); err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil: a GH_HOST-matching origin must derive routes", err)
	}
}

// Issue #3258 AC3: one "allow" line in the route TOML patches the derivation gap
// of a repo whose config only derives "/index", so EnforcedPaths ends up covering
// both the derived and the allow-declared path.
func TestBuildRegistryProxyRoutes_HostRooted_AllowExtendsDerivedPaths(t *testing.T) {
	repoDir := t.TempDir()
	testutil.WriteFixture(t, repoDir, synthetic.Strategy.InTreeConfigPath, "main = https://host.example.com/index\n")

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
	routes, err := buildRegistryProxyRoutes(c, resolveRows)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	got := routes[0]
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/index", "/dl"}) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, []string{"/index", "/dl"})
	}
}

// Issue #3258's acceptance demo: with the route table wired straight into
// registryproxy.New, a request under the allow-only "/dl" path forwards exactly
// like one under the derived "/index" path, while a path under neither still 403s.
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
	testutil.WriteFixture(t, repoDir, synthetic.Strategy.InTreeConfigPath, "main = http://"+upstreamHost+"/index\n")

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
	routes, err := buildRegistryProxyRoutes(c, resolveRows)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}

	p, err := registryproxy.New(routes, nil)
	if err != nil {
		t.Fatalf("registryproxy.New() error = %v, want nil", err)
	}
	prefix := routes[0].Prefix

	for _, tc := range []string{"/" + prefix + "/index/pkg", "/" + prefix + "/dl/pkg"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc, nil)
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want %d", tc, rr.Code, http.StatusOK)
		}
	}

	wantPaths := []string{"/index/pkg", "/dl/pkg"}
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

// Issue #3259: registrypathset.ApplyToRoute tags each derived path with its
// declaring ecosystem in EnforcedSubtrees, not just the flat, untagged
// EnforcedPaths the Forwarder's admission check already used. The fixture
// declares a synthetic and a twin registry on one host, so each declaration
// must get its own tag.
func TestBuildRegistryProxyRoutes_HostRooted_DerivesEnforcedSubtrees(t *testing.T) {
	repoDir := t.TempDir()
	testutil.WriteFixture(t, repoDir, synthetic.Strategy.InTreeConfigPath, "main = https://host.example.com/index\n")
	testutil.WriteFixture(t, repoDir, twinRow.InTreeConfigPath, "main = https://host.example.com/two\n")

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
	routes, err := buildRegistryProxyRoutes(c, resolveRows)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	got := routes[0].EnforcedSubtrees
	want := []registryvocab.Subtree{
		{Ecosystem: synthetic.Strategy.Name, Path: "/index"},
		{Ecosystem: twinRow.Name, Path: "/two"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("routes[0].EnforcedSubtrees = %+v, want %+v", got, want)
	}
}

// registryproxy.New rejects a host-rooted Upstream carrying any path, so the
// Upstream assignment trims a trailing "/" from HostPathSet.Origin.
// registrypathset.Derive never emits one, so this pins the defensive trim
// directly rather than relying on that holding.
func TestApplyToRoute_TrimsTrailingSlashFromOrigin(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com"}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com/",
			Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index"}},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, resolveRows)
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	if got.Upstream != "https://host.example.com" {
		t.Errorf("registrypathset.ApplyToRoute() Upstream = %q, want %q (trailing slash trimmed)", got.Upstream, "https://host.example.com")
	}
}

// Scoped rides the derived subtree into the route's EnforcedSubtrees (the
// manifest's enforcedPaths) while EnforcedPaths stays flat, so enforcement is
// unchanged.
func TestApplyToRoute_CarriesScopedIntoEnforcedSubtrees(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com"}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:   "host.example.com",
			Origin: "https://host.example.com",
			Subtrees: []registryvocab.Subtree{
				{Ecosystem: synthetic.Strategy.Name, Path: "/scoped", Scoped: true},
				{Ecosystem: synthetic.Strategy.Name, Path: "/plain"},
			},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, resolveRows)
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	want := []registryvocab.Subtree{
		{Ecosystem: synthetic.Strategy.Name, Path: "/scoped", Scoped: true},
		{Ecosystem: synthetic.Strategy.Name, Path: "/plain"},
	}
	if !reflect.DeepEqual(got.EnforcedSubtrees, want) {
		t.Errorf("EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, want)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/scoped", "/plain"}) {
		t.Errorf("EnforcedPaths = %v, want [/scoped /plain]", got.EnforcedPaths)
	}
}

func TestApplyToRoute_NoMatchingHost_NamesRoute(t *testing.T) {
	route := registryproxy.Route{MatchHost: "unknown.example.com"}

	_, err := registrypathset.ApplyToRoute(route, map[string]registrypathset.HostPathSet{}, resolveRows)
	if err == nil {
		t.Fatal("registrypathset.ApplyToRoute() = nil error, want an error: no HostPathSet for this host")
	}
	if !strings.Contains(err.Error(), "unknown.example.com") {
		t.Errorf("registrypathset.ApplyToRoute() error = %q, want it to name %q", err.Error(), "unknown.example.com")
	}
	if !strings.Contains(err.Error(), "upstream-origin") {
		t.Errorf("registrypathset.ApplyToRoute() error = %q, want it to offer the upstream-origin remedy", err.Error())
	}
	if strings.Contains(err.Error(), "upstream-base-url") {
		t.Errorf("registrypathset.ApplyToRoute() error = %q, want it to never name the retired upstream-base-url knob", err.Error())
	}
}

// Issue #3258's additive merge: route.Allow lands in EnforcedPaths after every
// derived subtree, in declaration order.
func TestApplyToRoute_AllowAppendsAfterDerivedPaths(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Allow: []string{"/extra"}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index"}},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, resolveRows)
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/index", "/extra"}) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedPaths = %v, want %v", got.EnforcedPaths, []string{"/index", "/extra"})
	}
}

// A duplicated path would appear twice in the 403 body's path-set listing and
// confuse an operator. The second Allow entry names a genuinely new path, so the
// dedupe must drop only the exact duplicate, never a real gap-patching entry.
func TestApplyToRoute_AllowDuplicatingDerivedPathIsNotRepeated(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Allow: []string{"/index", "/extra"}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index"}},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, resolveRows)
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/index", "/extra"}) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedPaths = %v, want %v (duplicate /index collapsed, /extra still appended)", got.EnforcedPaths, []string{"/index", "/extra"})
	}
}

// The pre-#3400 shape kept only one ecosystem's subtrees, in the since-deleted
// Route.CargoIndexBases field. On a host declaring two ecosystems'
// subtrees, one ecosystem's filter must no longer drop the other's tag.
func TestApplyToRoute_EnforcedSubtreesKeepsEveryEcosystemTagFromMixedEcosystems(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com"}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:   "host.example.com",
			Origin: "https://host.example.com",
			Subtrees: []registryvocab.Subtree{
				{Ecosystem: synthetic.Strategy.Name, Path: "/index-a"},
				{Ecosystem: twinRow.Name, Path: "/index"},
			},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, resolveRows)
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/index-a", "/index"}) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedPaths = %v, want %v", got.EnforcedPaths, []string{"/index-a", "/index"})
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index-a"}, {Ecosystem: twinRow.Name, Path: "/index"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedSubtrees = %v, want %v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// Two registries of one ecosystem share one host. Derivation order must survive the
// projection into EnforcedSubtrees, not just the single-ecosystem subset the
// pre-#3400 shape kept it on.
func TestApplyToRoute_EnforcedSubtreesTwoSameEcosystemSubtreesInDerivationOrder(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com"}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:   "host.example.com",
			Origin: "https://host.example.com",
			Subtrees: []registryvocab.Subtree{
				{Ecosystem: synthetic.Strategy.Name, Path: "/index-a"},
				{Ecosystem: synthetic.Strategy.Name, Path: "/index-b"},
			},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, resolveRows)
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index-a"}, {Ecosystem: synthetic.Strategy.Name, Path: "/index-b"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedSubtrees = %v, want %v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// A [routes.ecosystems.<name>] "path" key lands its own tagged subtree and,
// deduped, its own EnforcedPaths entry.
func TestApplyToRoute_EcosystemsBlockDeclaredPathLandsTaggedSubtree(t *testing.T) {
	route := registryproxy.Route{
		MatchHost:  "host.example.com",
		Ecosystems: registryvocab.RouteEcosystems{twinRow.Name: registryvocab.RouteDeclaration{"path": "/declared"}},
	}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index"}},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, resolveRows)
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	wantPaths := []string{"/index", "/declared"}
	if !reflect.DeepEqual(got.EnforcedPaths, wantPaths) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedPaths = %v, want %v", got.EnforcedPaths, wantPaths)
	}
	wantSubtrees := []registryvocab.Subtree{
		{Ecosystem: synthetic.Strategy.Name, Path: "/index"},
		{Ecosystem: twinRow.Name, Path: "/declared"},
	}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// The paths dedupe must never suppress the subtree tag when a declared path
// duplicates an Allow entry, or a binding renderer finds no entry tagged for the
// declaring ecosystem and silently renders an inert binding even though the path itself
// is enforced.
func TestApplyToRoute_DeclaredPathCollidingWithAllowStillTagsSubtree(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Allow: []string{"/mirror"}, Ecosystems: registryvocab.RouteEcosystems{twinRow.Name: registryvocab.RouteDeclaration{"path": "/mirror"}}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index"}},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, resolveRows)
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/index", "/mirror"}) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedPaths = %v, want %v (duplicate /mirror collapsed to one entry)", got.EnforcedPaths, []string{"/index", "/mirror"})
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index"}, {Ecosystem: twinRow.Name, Path: "/mirror"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedSubtrees = %v, want %v (declared path must still tag EnforcedSubtrees despite colliding with Allow)", got.EnforcedSubtrees, wantSubtrees)
	}
}

// Two ecosystems are both configured at "/index": the declaring one must
// still land its own tagged EnforcedSubtrees entry alongside the derived
// one, while EnforcedPaths dedupes to a single occurrence.
func TestApplyToRoute_DeclaredPathCollidingWithDerivedSubtreeStillTagsSubtree(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Ecosystems: registryvocab.RouteEcosystems{twinRow.Name: registryvocab.RouteDeclaration{"path": "/index"}}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index"}},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, resolveRows)
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/index"}) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedPaths = %v, want %v (duplicate /index collapsed to one entry)", got.EnforcedPaths, []string{"/index"})
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index"}, {Ecosystem: twinRow.Name, Path: "/index"}}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedSubtrees = %v, want %v (declared path must still tag EnforcedSubtrees despite colliding with an already-derived path)", got.EnforcedSubtrees, wantSubtrees)
	}
}

// Issue #3259: a declared path alone never establishes a host-rooted route's
// upstream origin, but it rides along once another ecosystem has
// established one, landing in both EnforcedPaths and EnforcedSubtrees.
func TestBuildRegistryProxyRoutes_HostRooted_DeclaredPathRidesAlongWithDerivedSubtree(t *testing.T) {
	repoDir := t.TempDir()
	testutil.WriteFixture(t, repoDir, synthetic.Strategy.InTreeConfigPath, "main = https://host.example.com/index\n")

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return repoDir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"

[routes.ecosystems.twin]
path = "/mirror"
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	routes, err := buildRegistryProxyRoutes(c, resolveRows)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	got := routes[0]
	wantPaths := []string{"/index", "/mirror"}
	if !reflect.DeepEqual(got.EnforcedPaths, wantPaths) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, wantPaths)
	}
	wantSubtrees := []registryvocab.Subtree{
		{Ecosystem: synthetic.Strategy.Name, Path: "/index"},
		{Ecosystem: twinRow.Name, Path: "/mirror"},
	}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("routes[0].EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// A declared path alone resolves no upstream origin, so the "declares no
// registry on that host" error must still fire, extended to name the
// declaration's own limitation by its [routes.ecosystems.twin] spelling.
func TestBuildRegistryProxyRoutes_HostRooted_DeclaredPathAlone_NoOriginFailsClosed(t *testing.T) {
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

[routes.ecosystems.twin]
path = "/mirror"
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	_, err := buildRegistryProxyRoutes(c, resolveRows)
	if err == nil {
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: no other ecosystem's config is discoverable on this host")
	}
	if !strings.Contains(err.Error(), "host.example.com") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to name the host-rooted route %q", err.Error(), "host.example.com")
	}
	if !strings.Contains(err.Error(), "ecosystems.twin.path") {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to mention ecosystems.twin.path's limitation", err.Error())
	}
	wantAdvice := "declare a discoverable " + synthetic.Strategy.Name + "/" + twinRow.Name + " registry"
	if !strings.Contains(err.Error(), wantAdvice) {
		t.Errorf("buildRegistryProxyRoutes() error = %q, want it to contain %q", err.Error(), wantAdvice)
	}
}

// A route declaring no path for a Strategy must leave EnforcedSubtrees free of any
// entry tagged for that Strategy, even with another Strategy's declared path present. A
// stray tag would make that Strategy's binding renderer bind a registry the operator
// never asked for, aimed at another ecosystem's subtree.
func TestApplyToRoute_UndeclaredRowTagsNoSubtree(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Ecosystems: registryvocab.RouteEcosystems{twinRow.Name: registryvocab.RouteDeclaration{"path": "/declared"}}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index"}},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, threeRows)
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	for _, sub := range got.EnforcedSubtrees {
		if sub.Ecosystem == otherRow.Name {
			t.Errorf("registrypathset.ApplyToRoute() EnforcedSubtrees = %+v, want no %q-tagged entry (the route declares no path for it)", got.EnforcedSubtrees, otherRow.Name)
		}
	}
}

// Two Strategies' declared paths serve unrelated ecosystems, so applying one must
// never exclude the other. Their blocks land in Strategy order, not declaration
// order, because declaredPaths walks the rows slice in its own load-bearing
// order.
func TestApplyToRoute_TwoDeclaredPathsCoexistBothTaggedInRowOrder(t *testing.T) {
	route := registryproxy.Route{MatchHost: "host.example.com", Ecosystems: registryvocab.RouteEcosystems{otherRow.Name: registryvocab.RouteDeclaration{"path": "/other-mirror"}, twinRow.Name: registryvocab.RouteDeclaration{"path": "/declared"}}}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:     "host.example.com",
			Origin:   "https://host.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index"}},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, threeRows)
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	wantPaths := []string{"/index", "/declared", "/other-mirror"}
	if !reflect.DeepEqual(got.EnforcedPaths, wantPaths) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedPaths = %v, want %v", got.EnforcedPaths, wantPaths)
	}
	wantSubtrees := []registryvocab.Subtree{
		{Ecosystem: synthetic.Strategy.Name, Path: "/index"},
		{Ecosystem: twinRow.Name, Path: "/declared"},
		{Ecosystem: otherRow.Name, Path: "/other-mirror"},
	}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("registrypathset.ApplyToRoute() EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, wantSubtrees)
	}
}

// Issue #3403: one host-rooted route may declare several Strategies' blocks together,
// applied in Strategy order. A block carrying no path, only a Strategy-specific key, adds
// nothing to EnforcedPaths or EnforcedSubtrees and needs no origin of its own;
// the key must still survive resolution. The real Strategies' non-path keys are pinned
// by the ecosystem corpora (cargo's registries-declared).
func TestBuildRegistryProxyRoutes_HostRooted_DeclarationBlocksCoexist(t *testing.T) {
	repoDir := t.TempDir()
	testutil.WriteFixture(t, repoDir, synthetic.Strategy.InTreeConfigPath, "main = https://host.example.com/index\n")

	origDir := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return repoDir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = origDir })
	origRemote := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = origRemote })

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"

[routes.ecosystems.twin]
path = "/declared"

[routes.ecosystems.other]
path = "/other-mirror"

[routes.ecosystems.synthetic]
mirror-path = "/keyed"
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	routes, err := buildRegistryProxyRoutes(c, threeRows)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	got := routes[0]

	wantPaths := []string{"/index", "/declared", "/other-mirror"}
	if !reflect.DeepEqual(got.EnforcedPaths, wantPaths) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, wantPaths)
	}
	wantSubtrees := []registryvocab.Subtree{
		{Ecosystem: synthetic.Strategy.Name, Path: "/index"},
		{Ecosystem: twinRow.Name, Path: "/declared"},
		{Ecosystem: otherRow.Name, Path: "/other-mirror"},
	}
	if !reflect.DeepEqual(got.EnforcedSubtrees, wantSubtrees) {
		t.Errorf("routes[0].EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, wantSubtrees)
	}

	if want := "/declared"; got.Ecosystems.Path(twinRow.Name) != want {
		t.Errorf("routes[0].Ecosystems.Path(twin) = %q, want %q", got.Ecosystems.Path(twinRow.Name), want)
	}
	if want := "/other-mirror"; got.Ecosystems.Path(otherRow.Name) != want {
		t.Errorf("routes[0].Ecosystems.Path(other) = %q, want %q", got.Ecosystems.Path(otherRow.Name), want)
	}
	if gotKey, want := got.Ecosystems[synthetic.Strategy.Name]["mirror-path"], "/keyed"; gotKey != want {
		t.Errorf("routes[0].Ecosystems[synthetic][mirror-path] = %v, want %q", gotKey, want)
	}
}

// Issue #3405 and ADR 0048 at the launch gate: resolveRegistryRoutesFromFile
// propagates Parse's retirement error unwrapped, so a routes file still spelling
// a retired key never reaches buildRegistryProxyRoutes. The error must name the
// route and the retired key. The retired per-ecosystem keys are fixed names of
// real Strategies, so this uses enforce-allowlist; the ecosystem corpora's
// retired-route-key cases pin the per-ecosystem ones.
func TestResolveRegistryRoutesFromFile_RetiredRouteKeyRefusedAtLaunchGate(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
enforce-allowlist = true
`)

	routes, err := resolveRegistryRoutesFromFile(path, resolveRows)
	if err == nil {
		t.Fatal("resolveRegistryRoutesFromFile() = nil error, want an error: enforce-allowlist is retired")
	}
	if routes != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() routes = %+v, want nil", routes)
	}
	if !strings.Contains(err.Error(), `"host.example.com"`) {
		t.Errorf("resolveRegistryRoutesFromFile() error = %q, want it to name the route %q", err.Error(), "host.example.com")
	}
	if !strings.Contains(err.Error(), "enforce-allowlist") {
		t.Errorf("resolveRegistryRoutesFromFile() error = %q, want it to name the retired key %q", err.Error(), "enforce-allowlist")
	}
}

// Issue #3140 added the exec, npmrc, and gradle-properties sources. This
// exercises the real Resolve path, not doctor's Peek, across all three plus the
// pre-existing env source, checking each credential pairs with the right route
// by match-host.
func TestResolveRegistryRoutesFromFile_MixedSources_ResolvesEachRouteCredential(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_ROUTES_MIXED_ENV_CRED", "tok-env")

	dir := t.TempDir()
	npmrcPath := filepath.Join(dir, "npmrc-creds")
	if err := os.WriteFile(npmrcPath, []byte("//npmrc.example.com/:_authToken=tok-npmrc\n"), 0o600); err != nil {
		t.Fatalf("writing npmrc fixture: %v", err)
	}
	propsPath := filepath.Join(dir, "props-creds")
	if err := os.WriteFile(propsPath, []byte("registryToken=tok-gradle\n"), 0o600); err != nil {
		t.Fatalf("writing gradle-properties fixture: %v", err)
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

	routes, err := resolveRegistryRoutesFromFile(path, resolveRows)
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

// assertRouteCredentials checks routes against want (keyed by MatchHost),
// failing on a route count mismatch, an unexpected MatchHost, a Credential
// that doesn't match, or a want key that no returned route matched.
func assertRouteCredentials(t *testing.T, routes []registryproxy.Route, want map[string]string) {
	t.Helper()
	if len(routes) != len(want) {
		t.Fatalf("got %d routes, want %d", len(routes), len(want))
	}
	seen := make(map[string]bool, len(routes))
	for _, r := range routes {
		wantCred, ok := want[r.MatchHost]
		if !ok {
			t.Errorf("got unexpected route %q", r.MatchHost)
			continue
		}
		seen[r.MatchHost] = true
		if r.Credential != wantCred {
			t.Errorf("route %q Credential = %q, want %q", r.MatchHost, r.Credential, wantCred)
		}
	}
	for host := range want {
		if !seen[host] {
			t.Errorf("want route %q, got none", host)
		}
	}
}

// execEchoOrAbsentArgv is the TOML `exec` array for a credential helper that
// echoes envVar's value if set (including set-but-empty) in the child, or
// ABSENT if unset -- shared by the two tests below that route a named env
// var through a real child process to probe execCredentialEnv's allowlist.
// It tests `${VAR+set}` rather than `[ -z "$VAR" ]` because the latter
// can't distinguish unset from set-but-empty, the exact distinction
// execCredentialEnv's os.LookupEnv is built on.
func execEchoOrAbsentArgv(envVar string) string {
	return fmt.Sprintf(`["/bin/sh", "-c", "if [ -z \"${%s+set}\" ]; then echo ABSENT; else echo \"$%s\"; fi"]`, envVar, envVar)
}

// Issue #3151 regression test: an exec route listed BEFORE an env route in
// the routes file must never see the env route's credential var, whatever
// the file order. This uses a non-allowlisted name, so it exercises the
// real child-process boundary (execCredentialEnv's fixed allowlist) rather
// than resolveRegistryRoutesFromFile's own pass ordering; see
// TestResolveRegistryRoutesFromFile_OrderOfSourcesDoesNotAffectResult below
// for a check that pins the pass ordering itself.
func TestResolveRegistryRoutesFromFile_ExecBeforeEnv_ExecNeverSeesEnvRouteCredential(t *testing.T) {
	t.Setenv("SPINDRIFT_TEST_ROUTES_ORDER_ENV_CRED", "should-not-leak")

	path := writeRoutesFile(t, fmt.Sprintf(`
[[routes]]
match-host = "exec.example.com"
credential = { exec = %s }

[[routes]]
match-host = "env.example.com"
credential = { env = "SPINDRIFT_TEST_ROUTES_ORDER_ENV_CRED" }
`, execEchoOrAbsentArgv("SPINDRIFT_TEST_ROUTES_ORDER_ENV_CRED")))

	routes, err := resolveRegistryRoutesFromFile(path, resolveRows)
	if err != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() error = %v, want nil", err)
	}
	assertRouteCredentials(t, routes, map[string]string{
		"exec.example.com": "ABSENT",
		"env.example.com":  "should-not-leak",
	})
}

// This pins the two-pass ordering at resolveRegistryRoutesFromFile's own
// seam, independent of execCredentialEnv's allowlist: the env source here
// (SSH_AUTH_SOCK) is itself one of the names the allowlist forwards to an
// exec child, so the exec route can only see it if the env route's Resolve
// (and its Unsetenv) ran first -- a non-allowlisted name would pass on both
// orders for the wrong reason (the allowlist blocks it either way) and never
// actually exercise this function's own pass ordering. A single-pass,
// file-order resolver would run the exec route before unsetting the env var
// on the exec-then-env order and leak it; the two-pass resolver unsets every
// env source in pass 1, before any exec source runs in pass 2, so the exec
// route never observes it in either file order.
func TestResolveRegistryRoutesFromFile_OrderOfSourcesDoesNotAffectResult(t *testing.T) {
	argv := execEchoOrAbsentArgv("SSH_AUTH_SOCK")
	envThenExec := fmt.Sprintf(`
[[routes]]
match-host = "env.example.com"
credential = { env = "SSH_AUTH_SOCK" }

[[routes]]
match-host = "exec.example.com"
credential = { exec = %s }
`, argv)
	execThenEnv := fmt.Sprintf(`
[[routes]]
match-host = "exec.example.com"
credential = { exec = %s }

[[routes]]
match-host = "env.example.com"
credential = { env = "SSH_AUTH_SOCK" }
`, argv)
	for name, doc := range map[string]string{"env-then-exec": envThenExec, "exec-then-env": execThenEnv} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SSH_AUTH_SOCK", "permute-secret")
			path := writeRoutesFile(t, doc)

			routes, err := resolveRegistryRoutesFromFile(path, resolveRows)
			if err != nil {
				t.Fatalf("resolveRegistryRoutesFromFile() error = %v, want nil", err)
			}
			assertRouteCredentials(t, routes, map[string]string{
				"exec.example.com": "ABSENT",
				"env.example.com":  "permute-secret",
			})
			if v, ok := os.LookupEnv("SSH_AUTH_SOCK"); ok {
				t.Errorf("os.LookupEnv(SSH_AUTH_SOCK) after resolveRegistryRoutesFromFile() = %q, want unset", v)
			}
		})
	}
}

// Pins the error-ordering consequence recorded in
// resolveRegistryRoutesFromFile's doc comment: when both an exec route and
// an env route would fail to resolve, the env route's failure surfaces
// first, because pass 1 (env sources) runs to completion before pass 2
// (everything else) starts -- independent of which route sits first in the
// file.
func TestResolveRegistryRoutesFromFile_BothFail_EnvErrorSurfacesFirst(t *testing.T) {
	path := writeRoutesFile(t, `
[[routes]]
match-host = "exec.example.com"
credential = { exec = ["/bin/sh", "-c", "exit 1"] }

[[routes]]
match-host = "env.example.com"
credential = { env = "SPINDRIFT_TEST_ROUTES_BOTH_FAIL_ENV_DOES_NOT_EXIST" }
`)

	routes, err := resolveRegistryRoutesFromFile(path, resolveRows)
	if err == nil {
		t.Fatal("resolveRegistryRoutesFromFile() = nil error, want an error: both routes fail to resolve")
	}
	if routes != nil {
		t.Fatalf("resolveRegistryRoutesFromFile() routes = %+v, want nil", routes)
	}
	if !strings.Contains(err.Error(), `resolving credential for route "env.example.com"`) {
		t.Errorf(`resolveRegistryRoutesFromFile() error = %q, want it to name "env.example.com" (pass 1), not "exec.example.com"`, err.Error())
	}
}

// mustLocalAccumulationRepo seeds a bare Accumulation repo from a throwaway
// "main" checkout through local.SeedAccumulationRepo, the same seed path
// bootstrap()'s seedAccumulationRepoIfHostMediated runs before dispatch. An
// empty registryConfig skips writing relPath, leaving the checkout declaring no
// registry.
func mustLocalAccumulationRepo(t *testing.T, relPath, registryConfig string) string {
	t.Helper()
	checkout := mustSeedableCheckout(t)
	if registryConfig != "" {
		testutil.WriteFixture(t, checkout, relPath, registryConfig)
		mustRunGit(t, checkout, "add", relPath)
		mustRunGit(t, checkout, "commit", "-m", "add registry config")
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
	c.setRowKnob("CODE_FORGE_ACCUMULATION_REPO_DIR", accumRepo)
	c.baseBranch = "main"
	return c
}

// Issue #3310 AC1 and AC2's positive half: under CODE_FORGE=local the route
// derives from the Accumulation repo's baseBranch snapshot, not a cwd checkout.
// t.Chdir moves the process into an unrelated directory and
// registryRouteDriftRepoDirFn is stubbed to t.Fatal, so a fallback to the
// cwd-checkout branch fails loudly.
func TestBuildRegistryProxyRoutes_HostRooted_Local_DerivesFromAccumulationRepo(t *testing.T) {
	accumRepo := mustLocalAccumulationRepo(t, synthetic.Strategy.InTreeConfigPath, "main = https://host.example.com/index\n")

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

	routes, err := buildRegistryProxyRoutes(c, resolveRows)
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
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/index"}) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, []string{"/index"})
	}
}

// Issue #3310 AC2's negative half: the registry config is committed to the checkout after
// SeedAccumulationRepo already ran and is never re-seeded, so the derived
// snapshot still declares nothing and the route must fail closed. The local
// derivation reads only what landed in the Accumulation repo.
func TestBuildRegistryProxyRoutes_HostRooted_Local_CheckoutOnlyConfigIgnored(t *testing.T) {
	checkout := mustSeedableCheckout(t)
	accumRepo := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumRepo, checkout, "main"); err != nil {
		t.Fatalf("SeedAccumulationRepo() error = %v, want nil", err)
	}

	testutil.WriteFixture(t, checkout, synthetic.Strategy.InTreeConfigPath, "main = https://host.example.com/index\n")
	mustRunGit(t, checkout, "add", synthetic.Strategy.InTreeConfigPath)
	mustRunGit(t, checkout, "commit", "-m", "add registry config, never pushed to accum")

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)
	c := minimalValidLocalConfigForRoutes(accumRepo)
	c.registryProxyRoutesFile = path

	_, err := buildRegistryProxyRoutes(c, resolveRows)
	if err == nil {
		t.Fatal("buildRegistryProxyRoutes() = nil error, want an error: the Accumulation repo's main branch never received the config commit")
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

	_, err := buildRegistryProxyRoutes(c, resolveRows)
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
	accumRepo := mustLocalAccumulationRepo(t, "", "")

	path := writeRoutesFile(t, `
[[routes]]
match-host = "host.example.com"
`)
	c := minimalValidLocalConfigForRoutes(accumRepo)
	c.registryProxyRoutesFile = path

	_, err := buildRegistryProxyRoutes(c, resolveRows)
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
	testutil.WriteFixture(t, repoDir, synthetic.Strategy.InTreeConfigPath, "main = https://host.example.com/index\n")

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
	routes, err := buildRegistryProxyRoutes(c, resolveRows)
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
	if !reflect.DeepEqual(got.EnforcedPaths, []string{"/index"}) {
		t.Errorf("routes[0].EnforcedPaths = %v, want the derived %v", got.EnforcedPaths, []string{"/index"})
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/index"}}
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
	testutil.WriteFixture(t, repoDir, synthetic.Strategy.InTreeConfigPath, "main = https://other.example.com/index\n")

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
	routes, err := buildRegistryProxyRoutes(c, resolveRows)
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

[routes.ecosystems.twin]
path = "/declared"
`)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = path
	routes, err := buildRegistryProxyRoutes(c, resolveRows)
	if err != nil {
		t.Fatalf("buildRegistryProxyRoutes() error = %v, want nil", err)
	}
	if len(routes) != 1 {
		t.Fatalf("buildRegistryProxyRoutes() = %d routes, want 1", len(routes))
	}
	got := routes[0]
	if want := []string{"/dl", "/declared"}; !reflect.DeepEqual(got.EnforcedPaths, want) {
		t.Errorf("routes[0].EnforcedPaths = %v, want %v", got.EnforcedPaths, want)
	}
	wantSubtrees := []registryvocab.Subtree{{Ecosystem: twinRow.Name, Path: "/declared"}}
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
	_, err := buildRegistryProxyRoutes(c, resolveRows)
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

// An operator-declared [routes.ecosystems.<name>] path is never scoped, even
// when it duplicates a derived scoped path: EnforcedPaths dedupes it, but
// EnforcedSubtrees keeps a separate unscoped entry, so the npm-family binding
// still counts the declared path.
func TestApplyToRoute_DeclaredPathLandsUnscopedEvenBesideDerivedScopedDuplicate(t *testing.T) {
	route := registryproxy.Route{
		MatchHost: "host.example.com",
		Ecosystems: registryvocab.RouteEcosystems{
			twinRow.Name:  registryvocab.RouteDeclaration{"path": "/dup"},
			otherRow.Name: registryvocab.RouteDeclaration{"path": "/only"},
		},
	}
	sets := map[string]registrypathset.HostPathSet{
		"host.example.com": {
			Host:   "host.example.com",
			Origin: "https://host.example.com",
			Subtrees: []registryvocab.Subtree{
				{Ecosystem: twinRow.Name, Path: "/dup", Scoped: true},
			},
		},
	}

	got, err := registrypathset.ApplyToRoute(route, sets, []ecosystem.Strategy{synthetic.Strategy, twinRow, otherRow})
	if err != nil {
		t.Fatalf("registrypathset.ApplyToRoute() error = %v, want nil", err)
	}
	want := []registryvocab.Subtree{
		{Ecosystem: twinRow.Name, Path: "/dup", Scoped: true},
		{Ecosystem: twinRow.Name, Path: "/dup"},
		{Ecosystem: otherRow.Name, Path: "/only"},
	}
	if !reflect.DeepEqual(got.EnforcedSubtrees, want) {
		t.Errorf("EnforcedSubtrees = %+v, want %+v", got.EnforcedSubtrees, want)
	}
	if wantPaths := []string{"/dup", "/only"}; !reflect.DeepEqual(got.EnforcedPaths, wantPaths) {
		t.Errorf("EnforcedPaths = %v, want %v", got.EnforcedPaths, wantPaths)
	}
}
