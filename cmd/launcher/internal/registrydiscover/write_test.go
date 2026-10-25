package registrydiscover

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/registryroutes"
)

// TestRender_CompanionKeyMatchesKindTable pins Render's companion rendering to
// the credresolver kind table itself rather than a parallel copy. For every
// kind, the rendered route carries the value that kind's CompanionField
// selects and survives registryroutes.Parse, and it carries no companion at
// all when CompanionKey is "".
func TestRender_CompanionKeyMatchesKindTable(t *testing.T) {
	for _, kind := range credresolver.Kinds() {
		if kind.ArgvValue {
			// exec's TOML value is an argv array, and this fixture's
			// CredentialValue is a plain string that registryroutes.Parse
			// rejects for exec.
			continue
		}
		t.Run(kind.SourceKey, func(t *testing.T) {
			route := Route{
				MatchHost:        "host.example.com",
				UpstreamBaseURL:  "https://host.example.com",
				AuthScheme:       "bearer",
				CredentialSource: kind.SourceKey,
				CredentialValue:  "/fake/path",
				RegistryName:     "mycorp",
				PropertyKey:      "host.example.com",
			}
			rendered := Render([]Route{route})

			if kind.CompanionKey == "" {
				for _, key := range []string{"registry-name", "key"} {
					if strings.Contains(string(rendered), key+" = ") {
						t.Errorf("rendered = %s, must not contain companion key %q for a kind with none", rendered, key)
					}
				}
				return
			}
			if !strings.Contains(string(rendered), kind.CompanionKey+" = ") {
				t.Errorf("rendered = %s, want to contain companion key %q", rendered, kind.CompanionKey)
			}

			parsed, err := registryroutes.Parse(rendered, nil)
			if err != nil {
				t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
			}
			if len(parsed) != 1 {
				t.Fatalf("parsed = %+v, want exactly 1 route", parsed)
			}

			projected := credresolver.Config{RegistryName: route.RegistryName, PropertyKey: route.PropertyKey}
			want := *kind.CompanionField(&projected)
			got := *kind.CompanionField(&parsed[0].Credential)
			if got != want {
				t.Errorf("companion value = %q, want %q", got, want)
			}
		})
	}
}

// TestRender_NetrcRouteRoundTripsThroughParse is the shape-defining case.
// registryroutes.Parse is the only authority on what a valid routes file
// (ADR 0045) looks like, so round-tripping through it proves the output where
// a hand-checked TOML string would not.
func TestRender_NetrcRouteRoundTripsThroughParse(t *testing.T) {
	routes := []Route{{
		MatchHost:        "registry.acme.example",
		UpstreamBaseURL:  "https://registry.acme.example/api/v1",
		AuthScheme:       "bearer",
		CredentialSource: "netrc",
		CredentialValue:  "/home/op/.netrc",
	}}

	parsed, err := registryroutes.Parse(Render(routes), nil)
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed = %+v, want exactly 1 route", parsed)
	}
	got := parsed[0]
	if got.MatchHost != "registry.acme.example" {
		t.Errorf("MatchHost = %q, want registry.acme.example", got.MatchHost)
	}
	if got.AuthScheme != "bearer" {
		t.Errorf("AuthScheme = %q, want bearer", got.AuthScheme)
	}
	if got.Credential.FromFile != "/home/op/.netrc" {
		t.Errorf("Credential.FromFile = %q, want /home/op/.netrc", got.Credential.FromFile)
	}
	if got.Credential.FileFormat != "netrc" {
		t.Errorf("Credential.FileFormat = %q, want netrc", got.Credential.FileFormat)
	}
}

// TestWriteFile_RefusesExistingFileWithoutForce also checks that the refusal
// error names the offending path, so an operator running spindrift registry
// discover against a populated directory can tell which file it balked at.
func TestWriteFile_RefusesExistingFileWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.toml")
	if err := os.WriteFile(path, []byte("pre-existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	routes := []Route{{
		MatchHost:        "registry.example.com",
		UpstreamBaseURL:  "https://registry.example.com",
		AuthScheme:       "bearer",
		CredentialSource: "netrc",
		CredentialValue:  "/home/op/.netrc",
	}}

	err := WriteFile(path, routes, false)
	if err == nil {
		t.Fatal("WriteFile: want error for existing file without force, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("WriteFile error = %q, want it to name path %q", err.Error(), path)
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "pre-existing" {
		t.Errorf("file contents = %q, want untouched %q", got, "pre-existing")
	}
}

// TestRender_StoreKindRouteRoundTrips round-trips a route through every
// credential-store kind, so a kind the table gains is covered without a new
// test. Each must survive Parse with its file path, file format and, where the
// kind takes one, its companion value.
func TestRender_StoreKindRouteRoundTrips(t *testing.T) {
	for _, kind := range storeKinds() {
		t.Run(kind.SourceKey, func(t *testing.T) {
			cfg := credresolver.Config{}
			if kind.CompanionKey != "" {
				*kind.CompanionField(&cfg) = "acme"
			}
			routes := []Route{{
				MatchHost:        "registry.acme.example",
				UpstreamBaseURL:  "https://registry.acme.example",
				AuthScheme:       "basic",
				CredentialSource: kind.SourceKey,
				CredentialValue:  "/home/op/store",
				RegistryName:     cfg.RegistryName,
				PropertyKey:      cfg.PropertyKey,
			}}

			parsed, err := registryroutes.Parse(Render(routes), nil)
			if err != nil {
				t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
			}
			if len(parsed) != 1 {
				t.Fatalf("parsed = %+v, want exactly 1 route", parsed)
			}
			got := parsed[0]
			if got.AuthScheme != "basic" {
				t.Errorf("AuthScheme = %q, want basic", got.AuthScheme)
			}
			if got.Credential.FromFile != "/home/op/store" || got.Credential.FileFormat != kind.FileFormat {
				t.Errorf("Credential = %+v, want FromFile=/home/op/store FileFormat=%s", got.Credential, kind.FileFormat)
			}
			if kind.CompanionKey != "" {
				if v := *kind.CompanionField(&got.Credential); v != "acme" {
					t.Errorf("companion %s = %q, want acme", kind.CompanionKey, v)
				}
			}
		})
	}
}

// TestRender_EnvPlaceholderRouteRoundTrips covers the "env" source Discover
// proposes for a host it could not match to a credential store.
func TestRender_EnvPlaceholderRouteRoundTrips(t *testing.T) {
	routes := []Route{{
		MatchHost:        "unmatched.example.com",
		UpstreamBaseURL:  "https://unmatched.example.com",
		AuthScheme:       "bearer",
		CredentialSource: "env",
		CredentialValue:  "SPINDRIFT_REGISTRY_CREDENTIAL_UNMATCHED_EXAMPLE_COM",
	}}

	parsed, err := registryroutes.Parse(Render(routes), nil)
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed = %+v, want exactly 1 route", parsed)
	}
	got := parsed[0].Credential
	if got.FromEnv != "SPINDRIFT_REGISTRY_CREDENTIAL_UNMATCHED_EXAMPLE_COM" {
		t.Errorf("Credential.FromEnv = %q, want SPINDRIFT_REGISTRY_CREDENTIAL_UNMATCHED_EXAMPLE_COM", got.FromEnv)
	}

	if !strings.Contains(string(Render(routes)), "placeholder env credential") {
		t.Error("Render header should note that a placeholder env credential needs filling in")
	}
}

func TestRender_TwoRoutesParseInOrder(t *testing.T) {
	routes := []Route{
		{
			MatchHost:        "first.example.com",
			UpstreamBaseURL:  "https://first.example.com",
			AuthScheme:       "bearer",
			CredentialSource: "netrc",
			CredentialValue:  "/home/op/.netrc",
		},
		{
			MatchHost:        "second.example.com",
			UpstreamBaseURL:  "https://second.example.com",
			AuthScheme:       "basic",
			CredentialSource: "netrc",
			CredentialValue:  "/home/op/.netrc",
		},
	}

	parsed, err := registryroutes.Parse(Render(routes), nil)
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("parsed = %+v, want exactly 2 routes", parsed)
	}
	if parsed[0].MatchHost != "first.example.com" {
		t.Errorf("parsed[0].MatchHost = %q, want first.example.com", parsed[0].MatchHost)
	}
	if parsed[1].MatchHost != "second.example.com" {
		t.Errorf("parsed[1].MatchHost = %q, want second.example.com", parsed[1].MatchHost)
	}
}

// TestRender_QuotedCredentialValueEscapesAndRoundTrips uses a double quote in
// the CredentialValue, an unlikely but possible store path, because an
// unescaped one corrupts the whole TOML document.
func TestRender_QuotedCredentialValueEscapesAndRoundTrips(t *testing.T) {
	const tricky = `/home/op/weird"path/.netrc`
	routes := []Route{{
		MatchHost:        "quoted.example.com",
		UpstreamBaseURL:  "https://quoted.example.com",
		AuthScheme:       "bearer",
		CredentialSource: "netrc",
		CredentialValue:  tricky,
	}}

	rendered := Render(routes)
	if strings.Contains(string(rendered), `netrc = "/home/op/weird"path`) {
		t.Fatalf("rendered TOML contains an unescaped quote, corrupting the document:\n%s", rendered)
	}

	parsed, err := registryroutes.Parse(rendered, nil)
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed = %+v, want exactly 1 route", parsed)
	}
	if parsed[0].Credential.FromFile != tricky {
		t.Errorf("Credential.FromFile = %q, want %q", parsed[0].Credential.FromFile, tricky)
	}
}

func TestWriteFile_ForceOverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.toml")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	routes := []Route{{
		MatchHost:        "registry.example.com",
		UpstreamBaseURL:  "https://registry.example.com",
		AuthScheme:       "bearer",
		CredentialSource: "netrc",
		CredentialValue:  "/home/op/.netrc",
	}}

	if err := WriteFile(path, routes, true); err != nil {
		t.Fatalf("WriteFile with force: unexpected error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "stale") {
		t.Errorf("file still contains stale content: %s", got)
	}
	if _, err := registryroutes.Parse(got, nil); err != nil {
		t.Errorf("Parse of overwritten file: unexpected error: %v", err)
	}
}

func TestWriteFile_NewFilePermsAndParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.toml")

	routes := []Route{{
		MatchHost:        "registry.example.com",
		UpstreamBaseURL:  "https://registry.example.com",
		AuthScheme:       "bearer",
		CredentialSource: "netrc",
		CredentialValue:  "/home/op/.netrc",
	}}

	if err := WriteFile(path, routes, false); err != nil {
		t.Fatalf("WriteFile: unexpected error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registryroutes.Parse(got, nil); err != nil {
		t.Errorf("Parse of written file: unexpected error: %v", err)
	}
}

// TestRender_RouteCarriesNoCredentialValueField documents the guarantee behind
// "does Render leak a secret": Route has no field able to hold a resolved
// credential value, only a store path or an env var name. The check below is a
// trip-wire for a future field that starts threading resolved values through,
// not proof on its own; this comment is the guarantee.
func TestRender_RouteCarriesNoCredentialValueField(t *testing.T) {
	const neverProvidedSecret = "sk_live_should_never_appear_in_rendered_output"

	routes := []Route{{
		MatchHost:        "unmatched.example.com",
		UpstreamBaseURL:  "https://unmatched.example.com",
		AuthScheme:       "bearer",
		CredentialSource: "env",
		CredentialValue:  "SPINDRIFT_REGISTRY_CREDENTIAL_UNMATCHED_EXAMPLE_COM",
	}}
	for i, kind := range storeKinds() {
		routes = append(routes, Route{
			MatchHost:        fmt.Sprintf("host%d.example.com", i),
			UpstreamBaseURL:  fmt.Sprintf("https://host%d.example.com", i),
			AuthScheme:       "bearer",
			CredentialSource: kind.SourceKey,
			CredentialValue:  "/home/op/" + kind.SourceKey,
		})
	}

	rendered := string(Render(routes))
	if strings.Contains(rendered, neverProvidedSecret) {
		t.Fatalf("rendered output unexpectedly contains a secret-shaped string; Render output:\n%s", rendered)
	}
	for _, kind := range storeKinds() {
		if !strings.Contains(rendered, "/home/op/"+kind.SourceKey) {
			t.Errorf("rendered output should contain the %s store reference path, not a resolved secret", kind.SourceKey)
		}
	}
}

// TestRender_PlainHTTPSHostOmitsUpstreamOrigin pins the exact bytes for the
// common case: an upstream on plain https with no port says nothing match-host
// does not already say, so the stanza carries no upstream-origin and the
// derivation supplies the origin (ADR 0047, issue #3261).
func TestRender_PlainHTTPSHostOmitsUpstreamOrigin(t *testing.T) {
	routes := []Route{{
		MatchHost:        "registry.acme.example",
		UpstreamBaseURL:  "https://registry.acme.example/api/v1",
		AuthScheme:       "bearer",
		CredentialSource: "netrc",
		CredentialValue:  "/home/op/.netrc",
	}}

	const want = `# Registry routes file (ADR 0045). Generated by spindrift registry discover.

[[routes]]
match-host = "registry.acme.example"
auth-scheme = "bearer"
credential = { netrc = "/home/op/.netrc" }
`
	if got := string(Render(routes)); got != want {
		t.Errorf("Render() =\n%s\nwant:\n%s", got, want)
	}

	if _, err := registryroutes.Parse(Render(routes), nil); err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
}

// TestRender_ExplicitPortEmitsOriginWithoutPath covers the port case:
// match-host is port-stripped, so an upstream on a non-default port needs
// upstream-origin to reach the registry. The base URL's path is dropped
// because a host-rooted route derives the paths it serves rather than joining
// a base path.
func TestRender_ExplicitPortEmitsOriginWithoutPath(t *testing.T) {
	routes := []Route{{
		MatchHost:        "mirror.example.com",
		UpstreamBaseURL:  "https://mirror.example.com:8443/mirror/api/repo-remote",
		AuthScheme:       "bearer",
		CredentialSource: "netrc",
		CredentialValue:  "/home/op/.netrc",
	}}

	rendered := string(Render(routes))
	if !strings.Contains(rendered, `upstream-origin = "https://mirror.example.com:8443"`+"\n") {
		t.Errorf("Render() =\n%s\nwant an upstream-origin line carrying the explicit port", rendered)
	}
	if strings.Contains(rendered, "/mirror/api/repo-remote") {
		t.Errorf("Render() =\n%s\nwant the base URL's path dropped from the origin", rendered)
	}

	parsed, err := registryroutes.Parse([]byte(rendered), nil)
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed = %+v, want exactly 1 route", parsed)
	}
	if parsed[0].UpstreamOrigin != "https://mirror.example.com:8443" {
		t.Errorf("UpstreamOrigin = %q, want https://mirror.example.com:8443", parsed[0].UpstreamOrigin)
	}
}

// TestRender_HTTPSchemeEmitsOrigin covers the scheme case: a route with no
// upstream-origin is derived as https, so a plaintext http upstream has to
// survive into the file.
func TestRender_HTTPSchemeEmitsOrigin(t *testing.T) {
	routes := []Route{{
		MatchHost:        "registry.internal",
		UpstreamBaseURL:  "http://registry.internal/simple",
		AuthScheme:       "basic",
		CredentialSource: "netrc",
		CredentialValue:  "/home/op/.netrc",
	}}

	rendered := string(Render(routes))
	if !strings.Contains(rendered, `upstream-origin = "http://registry.internal"`+"\n") {
		t.Errorf("Render() =\n%s\nwant an upstream-origin line carrying the http scheme", rendered)
	}

	parsed, err := registryroutes.Parse([]byte(rendered), nil)
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed = %+v, want exactly 1 route", parsed)
	}
	if parsed[0].UpstreamOrigin != "http://registry.internal" {
		t.Errorf("UpstreamOrigin = %q, want http://registry.internal", parsed[0].UpstreamOrigin)
	}
}

// TestRender_MixedOriginRoutesRoundTripThroughParse holds the invariant this
// writer exists for, across whatever mix of origin-bearing and origin-less
// routes discovery proposes: Render must never write what
// registryroutes.Parse would reject.
func TestRender_MixedOriginRoutesRoundTripThroughParse(t *testing.T) {
	routes := []Route{
		{
			MatchHost:        "plain.example.com",
			UpstreamBaseURL:  "https://plain.example.com/index",
			AuthScheme:       "bearer",
			CredentialSource: "netrc",
			CredentialValue:  "/home/op/.netrc",
		},
		{
			MatchHost:        "ported.example.com",
			UpstreamBaseURL:  "https://ported.example.com:9443/repo",
			AuthScheme:       "basic",
			CredentialSource: "env",
			CredentialValue:  "SPINDRIFT_REGISTRY_CREDENTIAL_PORTED_EXAMPLE_COM",
		},
		{
			MatchHost:        "plaintext.example.com",
			UpstreamBaseURL:  "http://plaintext.example.com/api/v1",
			AuthScheme:       "bearer",
			CredentialSource: "netrc",
			CredentialValue:  "/home/op/.netrc",
		},
	}

	parsed, err := registryroutes.Parse(Render(routes), nil)
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v; rendered:\n%s", err, Render(routes))
	}
	if len(parsed) != 3 {
		t.Fatalf("parsed = %+v, want exactly 3 routes", parsed)
	}
	want := []string{"", "https://ported.example.com:9443", "http://plaintext.example.com"}
	for i, w := range want {
		if parsed[i].UpstreamOrigin != w {
			t.Errorf("parsed[%d].UpstreamOrigin = %q, want %q", i, parsed[i].UpstreamOrigin, w)
		}
	}
}

// TestRender_BlockGrammarRoundTripsRenderParseRender pins the discovery
// acceptance criterion (issue #3405), including that the output carries only
// the keys of the block grammar, never one of the per-ecosystem keys
// registryroutes now refuses at the launch gate. Discovery has never emitted
// one, but Render's output would still parse if a future change did, so
// nothing else would catch that.
func TestRender_BlockGrammarRoundTripsRenderParseRender(t *testing.T) {
	routes := []Route{{
		MatchHost:        "registry.acme.example",
		UpstreamBaseURL:  "http://registry.acme.example:8080/api/v1",
		AuthScheme:       "bearer",
		CredentialSource: "netrc",
		CredentialValue:  "/home/op/.netrc",
	}}
	for i, kind := range storeKinds() {
		route := Route{
			MatchHost:        fmt.Sprintf("host%d.acme.example", i),
			AuthScheme:       "header:X-Auth",
			CredentialSource: kind.SourceKey,
			CredentialValue:  "/home/op/" + kind.SourceKey,
		}
		if kind.CompanionKey != "" {
			cfg := credresolver.Config{}
			*kind.CompanionField(&cfg) = "acme"
			route.RegistryName, route.PropertyKey = cfg.RegistryName, cfg.PropertyKey
		}
		routes = append(routes, route)
	}

	first := Render(routes)
	allowedKeys := []string{"match-host", "upstream-origin", "auth-scheme", "credential"}
	for _, line := range strings.Split(string(first), "\n") {
		if line == "" || line == "[[routes]]" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, _ := strings.Cut(line, " = ")
		if !slices.Contains(allowedKeys, key) {
			t.Errorf("Render output carries key %q outside the block grammar %v:\n%s", key, allowedKeys, first)
		}
	}

	parsed, err := registryroutes.Parse(first, nil)
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v; rendered:\n%s", err, first)
	}
	if len(parsed) != len(routes) {
		t.Fatalf("parsed %d routes, want %d", len(parsed), len(routes))
	}

	reconstructed := make([]Route, len(parsed))
	for i, pr := range parsed {
		reconstructed[i] = routeFromParsedRoute(pr)
	}
	second := Render(reconstructed)
	if !bytes.Equal(first, second) {
		t.Errorf("re-render is not byte-identical to the first render:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// routeFromParsedRoute is the inverse of Render, only as far as
// TestRender_BlockGrammarRoundTripsRenderParseRender needs of it.
func routeFromParsedRoute(pr registryroutes.Route) Route {
	r := Route{
		MatchHost:       pr.MatchHost,
		UpstreamBaseURL: pr.UpstreamOrigin,
		AuthScheme:      pr.AuthScheme,
	}
	if pr.Credential.FromEnv != "" {
		r.CredentialSource = "env"
		r.CredentialValue = pr.Credential.FromEnv
		return r
	}
	r.CredentialSource = pr.Credential.FileFormat
	r.CredentialValue = pr.Credential.FromFile
	r.RegistryName = pr.Credential.RegistryName
	r.PropertyKey = pr.Credential.PropertyKey
	return r
}

// storeKinds returns the credential kinds that read an operator store file.
func storeKinds() []credresolver.Kind {
	var kinds []credresolver.Kind
	for _, kind := range credresolver.Kinds() {
		if kind.StoreConfig != nil {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func TestRender_HostProfileCommentPrecedesAuthSchemeAndParses(t *testing.T) {
	route := Route{
		MatchHost:         "repo.corp.example",
		UpstreamBaseURL:   "https://repo.corp.example/artifactory/api/npm/",
		AuthScheme:        "bearer",
		Profile:           ProfileAdvice{Name: "artifactory"},
		SchemeFromProfile: true,
		CredentialSource:  "env",
		CredentialValue:   "SPINDRIFT_REGISTRY_CREDENTIAL_REPO_CORP_EXAMPLE",
	}
	rendered := Render([]Route{route})

	want := "# auth-scheme from host profile \"artifactory\": the probe saw no usable challenge\nauth-scheme = \"bearer\"\n"
	if !strings.Contains(string(rendered), want) {
		t.Errorf("Render output missing %q:\n%s", want, rendered)
	}
	parsed, err := registryroutes.Parse(rendered, nil)
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 1 || parsed[0].AuthScheme != "bearer" {
		t.Errorf("parsed = %+v, want one bearer route", parsed)
	}

	route.Profile, route.SchemeFromProfile = ProfileAdvice{}, false
	if got := string(Render([]Route{route})); strings.Contains(got, "host profile") {
		t.Errorf("Render without a Profile mentions a host profile:\n%s", got)
	}
}

func TestRender_HostProfileAdviceCommentsAndAllowParse(t *testing.T) {
	route := Route{
		MatchHost:         "repo.corp.example",
		UpstreamBaseURL:   "https://repo.corp.example/artifactory/api/cargo/repo/index",
		AuthScheme:        "bearer",
		SchemeFromProfile: true,
		Profile: ProfileAdvice{
			Name:             "artifactory",
			Note:             "JFrog\nArtifactory",
			BasicUsername:    "the user",
			CredentialHelper: "an access token from `jf`",
		},
		Allow:            []string{"/artifactory/api/cargo/repo/v1/crates", "/other"},
		CredentialSource: "env",
		CredentialValue:  "SPINDRIFT_REGISTRY_CREDENTIAL_REPO_CORP_EXAMPLE",
	}
	rendered := string(Render([]Route{route}))

	for _, want := range []string{
		"[[routes]]\n# host profile \"artifactory\": JFrog Artifactory\n# basic username: the user\n# credential helper: an access token from `jf`\nmatch-host",
		"# auth-scheme from host profile \"artifactory\": the probe saw no usable challenge\nauth-scheme = \"bearer\"\n# allow from host profile \"artifactory\": sibling paths outside the declared registry path\nallow = [\"/artifactory/api/cargo/repo/v1/crates\", \"/other\"]\ncredential",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("Render output missing %q:\n%s", want, rendered)
		}
	}
	parsed, err := registryroutes.Parse([]byte(rendered), nil)
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 1 || !slices.Equal(parsed[0].Allow, route.Allow) {
		t.Errorf("parsed = %+v, want one route allowing %v", parsed, route.Allow)
	}
}

func TestRender_ReceivedSchemeKeepsAdviceWithoutSchemeComment(t *testing.T) {
	route := Route{
		MatchHost:        "repo.corp.example",
		UpstreamBaseURL:  "https://repo.corp.example/npm/",
		AuthScheme:       "basic",
		Profile:          ProfileAdvice{Name: "artifactory", Note: "JFrog Artifactory", BasicUsername: "the user"},
		CredentialSource: "env",
		CredentialValue:  "X",
	}
	got := string(Render([]Route{route}))
	if !strings.Contains(got, "# host profile \"artifactory\"") || !strings.Contains(got, "# basic username: the user\n") {
		t.Errorf("Render output missing profile advice:\n%s", got)
	}
	if strings.Contains(got, "auth-scheme from host profile") || strings.Contains(got, "allow") {
		t.Errorf("Render output carries a scheme or allow comment it has no basis for:\n%s", got)
	}
}

func TestRender_PublicProfileWritesOnlyTheNoteComment(t *testing.T) {
	const note = "known public host, needs no credential, nothing to configure"
	route := Route{
		MatchHost:        "registry.npmjs.org",
		UpstreamBaseURL:  "https://registry.npmjs.org/",
		AuthScheme:       "bearer",
		Profile:          ProfileAdvice{Name: "registry.npmjs.org", Note: note},
		CredentialSource: "env",
		CredentialValue:  "X",
	}
	plain := route
	plain.Profile = ProfileAdvice{}

	got := string(Render([]Route{route}))
	want := "# host profile \"registry.npmjs.org\": " + note + "\n"
	if !strings.Contains(got, want) {
		t.Errorf("Render output missing %q:\n%s", want, got)
	}
	if stripped := strings.Replace(got, want, "", 1); stripped != string(Render([]Route{plain})) {
		t.Errorf("a public profile changed more than its comment:\n%s", got)
	}
}
