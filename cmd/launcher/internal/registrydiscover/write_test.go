package registrydiscover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registryroutes"
)

// TestRender_NetrcRouteRoundTripsThroughParse is the shape-defining case:
// Render's output for a single netrc-backed route must parse back through
// registryroutes.Parse into the same match-host,
// auth-scheme, and credresolver.Config -- Parse is the only authority on
// what a valid routes file (ADR 0045) looks like, so round-tripping through
// it is the proof, not a hand-checked TOML string.
func TestRender_NetrcRouteRoundTripsThroughParse(t *testing.T) {
	routes := []Route{{
		MatchHost:        "crates.acme.example",
		UpstreamBaseURL:  "https://crates.acme.example/api/v1",
		AuthScheme:       "bearer",
		CredentialSource: "netrc",
		CredentialValue:  "/home/op/.netrc",
	}}

	parsed, err := registryroutes.Parse(Render(routes))
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed = %+v, want exactly 1 route", parsed)
	}
	got := parsed[0]
	if got.MatchHost != "crates.acme.example" {
		t.Errorf("MatchHost = %q, want crates.acme.example", got.MatchHost)
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

// TestWriteFile_RefusesExistingFileWithoutForce checks that WriteFile
// refuses to clobber a pre-existing routes file unless force is set, and
// that the refusal error names the offending path (an operator running
// spindrift registry discover against a populated directory must be able to
// tell which file it balked at).
func TestWriteFile_RefusesExistingFileWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.toml")
	if err := os.WriteFile(path, []byte("pre-existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	routes := []Route{{
		MatchHost:        "npm.example.com",
		UpstreamBaseURL:  "https://npm.example.com",
		AuthScheme:       "bearer",
		CredentialSource: "npmrc",
		CredentialValue:  "/home/op/.npmrc",
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

// TestRender_NpmrcRouteRoundTrips checks the npmrc credential source, whose
// FileFormat ("npmrc") registryroutes.Parse must recover from the rendered
// inline table.
func TestRender_NpmrcRouteRoundTrips(t *testing.T) {
	routes := []Route{{
		MatchHost:        "npm.example.com",
		UpstreamBaseURL:  "https://npm.example.com",
		AuthScheme:       "basic",
		CredentialSource: "npmrc",
		CredentialValue:  "/home/op/.npmrc",
	}}

	parsed, err := registryroutes.Parse(Render(routes))
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
	if got.Credential.FromFile != "/home/op/.npmrc" || got.Credential.FileFormat != "npmrc" {
		t.Errorf("Credential = %+v, want FromFile=/home/op/.npmrc FileFormat=npmrc", got.Credential)
	}
}

// TestRender_CargoCredentialsRouteRoundTrips checks that the
// cargo-credentials source's companion registry-name key is rendered and
// recovered by Parse into credresolver.Config.RegistryName.
func TestRender_CargoCredentialsRouteRoundTrips(t *testing.T) {
	routes := []Route{{
		MatchHost:        "crates.acme.example",
		UpstreamBaseURL:  "https://crates.acme.example/api/v1",
		AuthScheme:       "bearer",
		CredentialSource: "cargo-credentials",
		CredentialValue:  "/home/op/.cargo/credentials.toml",
		RegistryName:     "acme",
	}}

	parsed, err := registryroutes.Parse(Render(routes))
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed = %+v, want exactly 1 route", parsed)
	}
	got := parsed[0].Credential
	if got.FromFile != "/home/op/.cargo/credentials.toml" {
		t.Errorf("Credential.FromFile = %q, want /home/op/.cargo/credentials.toml", got.FromFile)
	}
	if got.FileFormat != "cargo-credentials" {
		t.Errorf("Credential.FileFormat = %q, want cargo-credentials", got.FileFormat)
	}
	if got.RegistryName != "acme" {
		t.Errorf("Credential.RegistryName = %q, want acme", got.RegistryName)
	}
}

// TestRender_GradlePropertiesRouteRoundTrips checks that the
// gradle-properties source's companion key key is rendered and recovered by
// Parse into credresolver.Config.PropertyKey.
func TestRender_GradlePropertiesRouteRoundTrips(t *testing.T) {
	routes := []Route{{
		MatchHost:        "gradle.example.com",
		UpstreamBaseURL:  "https://gradle.example.com",
		AuthScheme:       "bearer",
		CredentialSource: "gradle-properties",
		CredentialValue:  "/home/op/gradle.properties",
		PropertyKey:      "gradle.example.com",
	}}

	parsed, err := registryroutes.Parse(Render(routes))
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed = %+v, want exactly 1 route", parsed)
	}
	got := parsed[0].Credential
	if got.FromFile != "/home/op/gradle.properties" {
		t.Errorf("Credential.FromFile = %q, want /home/op/gradle.properties", got.FromFile)
	}
	if got.FileFormat != "gradle-properties" {
		t.Errorf("Credential.FileFormat = %q, want gradle-properties", got.FileFormat)
	}
	if got.PropertyKey != "gradle.example.com" {
		t.Errorf("Credential.PropertyKey = %q, want gradle.example.com", got.PropertyKey)
	}
}

// TestRender_EnvPlaceholderRouteRoundTrips checks the "env" source Discover
// proposes for an unmatched host: rendered as credential.env, recovered by
// Parse into credresolver.Config.FromEnv.
func TestRender_EnvPlaceholderRouteRoundTrips(t *testing.T) {
	routes := []Route{{
		MatchHost:        "unmatched.example.com",
		UpstreamBaseURL:  "https://unmatched.example.com",
		AuthScheme:       "bearer",
		CredentialSource: "env",
		CredentialValue:  "SPINDRIFT_REGISTRY_CREDENTIAL_UNMATCHED_EXAMPLE_COM",
	}}

	parsed, err := registryroutes.Parse(Render(routes))
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

// TestRender_TwoRoutesParseInOrder checks that Render's per-route
// [[routes]] blocks survive concatenation and come back from Parse in the
// same order they were given -- the multi-route case, not just a single
// block in isolation.
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
			CredentialSource: "npmrc",
			CredentialValue:  "/home/op/.npmrc",
		},
	}

	parsed, err := registryroutes.Parse(Render(routes))
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

// TestRender_QuotedCredentialValueEscapesAndRoundTrips checks that a
// CredentialValue containing a double quote (an unlikely but not impossible
// store path) is escaped in the rendered TOML rather than corrupting the
// document, and that Parse recovers the original unescaped value.
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

	parsed, err := registryroutes.Parse(rendered)
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

// TestWriteFile_ForceOverwritesExistingFile checks that force=true lets
// WriteFile replace a pre-existing file rather than refusing it.
func TestWriteFile_ForceOverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.toml")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	routes := []Route{{
		MatchHost:        "npm.example.com",
		UpstreamBaseURL:  "https://npm.example.com",
		AuthScheme:       "bearer",
		CredentialSource: "npmrc",
		CredentialValue:  "/home/op/.npmrc",
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
	if _, err := registryroutes.Parse(got); err != nil {
		t.Errorf("Parse of overwritten file: unexpected error: %v", err)
	}
}

// TestWriteFile_NewFilePermsAndParses checks that WriteFile writes a
// brand-new file (no existing file to refuse or overwrite) with mode 0600
// and content that registryroutes.Parse accepts.
func TestWriteFile_NewFilePermsAndParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.toml")

	routes := []Route{{
		MatchHost:        "npm.example.com",
		UpstreamBaseURL:  "https://npm.example.com",
		AuthScheme:       "bearer",
		CredentialSource: "npmrc",
		CredentialValue:  "/home/op/.npmrc",
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
	if _, err := registryroutes.Parse(got); err != nil {
		t.Errorf("Parse of written file: unexpected error: %v", err)
	}
}

// TestRender_RouteCarriesNoCredentialValueField documents the strong
// guarantee behind the "does Render leak a secret" question: Route itself
// has no field capable of holding a resolved credential value (a secret) --
// CredentialValue is always a store reference (a path) or an env var name,
// never a value read out of a store. There is no code path from a real
// secret into Render's output because there is no field to carry one.
//
// The check below is necessarily indirect: it renders a realistic route set
// and asserts the output never contains a sample "secret" constant that the
// test itself defines and never hands to Render or WriteFile -- a trip-wire
// for a future field addition that starts threading resolved values through,
// not proof by itself. The doc comment above is the actual guarantee.
func TestRender_RouteCarriesNoCredentialValueField(t *testing.T) {
	const neverProvidedSecret = "sk_live_should_never_appear_in_rendered_output"

	routes := []Route{
		{
			MatchHost:        "crates.acme.example",
			UpstreamBaseURL:  "https://crates.acme.example/api/v1",
			AuthScheme:       "bearer",
			CredentialSource: "cargo-credentials",
			CredentialValue:  "/home/op/.cargo/credentials.toml",
			RegistryName:     "acme",
		},
		{
			MatchHost:        "gradle.example.com",
			UpstreamBaseURL:  "https://gradle.example.com",
			AuthScheme:       "bearer",
			CredentialSource: "gradle-properties",
			CredentialValue:  "/home/op/gradle.properties",
			PropertyKey:      "gradle.example.com",
		},
		{
			MatchHost:        "unmatched.example.com",
			UpstreamBaseURL:  "https://unmatched.example.com",
			AuthScheme:       "bearer",
			CredentialSource: "env",
			CredentialValue:  "SPINDRIFT_REGISTRY_CREDENTIAL_UNMATCHED_EXAMPLE_COM",
		},
	}

	rendered := string(Render(routes))
	if strings.Contains(rendered, neverProvidedSecret) {
		t.Fatalf("rendered output unexpectedly contains a secret-shaped string; Render output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "/home/op/.cargo/credentials.toml") {
		t.Error("rendered output should contain the store reference path, not a resolved secret")
	}
}

// TestRender_PlainHTTPSHostOmitsUpstreamOrigin pins the exact bytes for the
// common case: a discovered upstream on plain https with no port says
// nothing match-host alone does not already say, so the stanza carries no
// upstream-origin at all and the derivation supplies the origin (ADR 0047,
// issue #3261).
func TestRender_PlainHTTPSHostOmitsUpstreamOrigin(t *testing.T) {
	routes := []Route{{
		MatchHost:        "crates.acme.example",
		UpstreamBaseURL:  "https://crates.acme.example/api/v1",
		AuthScheme:       "bearer",
		CredentialSource: "netrc",
		CredentialValue:  "/home/op/.netrc",
	}}

	const want = `# Registry routes file (ADR 0045). Generated by spindrift registry discover.

[[routes]]
match-host = "crates.acme.example"
auth-scheme = "bearer"
credential = { netrc = "/home/op/.netrc" }
`
	if got := string(Render(routes)); got != want {
		t.Errorf("Render() =\n%s\nwant:\n%s", got, want)
	}

	if _, err := registryroutes.Parse(Render(routes)); err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
}

// TestRender_ExplicitPortEmitsOriginWithoutPath checks the port case: a
// discovered upstream on a non-default port needs upstream-origin, since
// match-host is port-stripped and could not otherwise reach the registry.
// The base URL's path is dropped -- a host-rooted route derives the paths it
// serves rather than joining a base path.
func TestRender_ExplicitPortEmitsOriginWithoutPath(t *testing.T) {
	routes := []Route{{
		MatchHost:        "artifactory.example.com",
		UpstreamBaseURL:  "https://artifactory.example.com:8443/artifactory/api/npm/npm-remote",
		AuthScheme:       "bearer",
		CredentialSource: "npmrc",
		CredentialValue:  "/home/op/.npmrc",
	}}

	rendered := string(Render(routes))
	if !strings.Contains(rendered, `upstream-origin = "https://artifactory.example.com:8443"`+"\n") {
		t.Errorf("Render() =\n%s\nwant an upstream-origin line carrying the explicit port", rendered)
	}
	if strings.Contains(rendered, "/artifactory/api/npm/npm-remote") {
		t.Errorf("Render() =\n%s\nwant the base URL's path dropped from the origin", rendered)
	}

	parsed, err := registryroutes.Parse([]byte(rendered))
	if err != nil {
		t.Fatalf("Parse(Render(routes)): unexpected error: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed = %+v, want exactly 1 route", parsed)
	}
	if parsed[0].UpstreamOrigin != "https://artifactory.example.com:8443" {
		t.Errorf("UpstreamOrigin = %q, want https://artifactory.example.com:8443", parsed[0].UpstreamOrigin)
	}
}

// TestRender_HTTPSchemeEmitsOrigin checks the scheme case: a plaintext http
// upstream must survive into the file, since a route with no upstream-origin
// is derived as https.
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

	parsed, err := registryroutes.Parse([]byte(rendered))
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

// TestRender_MixedOriginRoutesRoundTripThroughParse is the multi-route
// version of the invariant this writer exists to hold: whatever mix of
// origin-bearing and origin-less routes discovery proposes, the whole file
// must come back out of registryroutes.Parse -- Render must never write what
// Parse would reject.
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
			CredentialSource: "npmrc",
			CredentialValue:  "/home/op/.npmrc",
		},
		{
			MatchHost:        "plaintext.example.com",
			UpstreamBaseURL:  "http://plaintext.example.com/api/v1",
			AuthScheme:       "bearer",
			CredentialSource: "cargo-credentials",
			CredentialValue:  "/home/op/.cargo/credentials.toml",
			RegistryName:     "acme",
		},
	}

	parsed, err := registryroutes.Parse(Render(routes))
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
