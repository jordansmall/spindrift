package registryroutes

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
)

// TestParse_SingleValidRouteBearerNetrc verifies that a minimal, valid routes
// file (ADR 0045) parses into one Route carrying the fields the TOML named,
// with its Credential mapped onto credresolver's netrc source.
func TestParse_SingleValidRouteBearerNetrc(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
auth-scheme = "bearer"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(routes))
	}
	r := routes[0]
	if r.MatchHost != "artifactory.example.com" {
		t.Errorf("MatchHost = %q, want %q", r.MatchHost, "artifactory.example.com")
	}
	if r.UpstreamBaseURL != "https://artifactory.example.com/artifactory" {
		t.Errorf("UpstreamBaseURL = %q, want %q", r.UpstreamBaseURL, "https://artifactory.example.com/artifactory")
	}
	if r.AuthScheme != "bearer" {
		t.Errorf("AuthScheme = %q, want %q", r.AuthScheme, "bearer")
	}
	if r.EnforceAllowlist {
		t.Error("EnforceAllowlist = true, want false (absent in TOML)")
	}
	if r.Credential.FromFile != "~/.netrc" {
		t.Errorf("Credential.FromFile = %q, want %q", r.Credential.FromFile, "~/.netrc")
	}
	if r.Credential.FileFormat != "netrc" {
		t.Errorf("Credential.FileFormat = %q, want %q", r.Credential.FileFormat, "netrc")
	}
	if r.Credential.UpstreamURL != r.UpstreamBaseURL {
		t.Errorf("Credential.UpstreamURL = %q, want %q", r.Credential.UpstreamURL, r.UpstreamBaseURL)
	}
}

// TestParse_AuthSchemeAbsentDefaultsToBearer verifies that a route with no
// auth-scheme key at all defaults to "bearer" (ADR 0045), not an empty
// string that later code would have to special-case.
func TestParse_AuthSchemeAbsentDefaultsToBearer(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].AuthScheme != "bearer" {
		t.Errorf("AuthScheme = %q, want %q", routes[0].AuthScheme, "bearer")
	}
}

// TestParse_EnforceAllowlistTrueIsParsed verifies that an explicit
// enforce-allowlist = true is decoded onto Route.EnforceAllowlist -- pinning
// the rawRoute struct's `toml:"enforce-allowlist"` tag against a regression
// that would make DisallowUnknownFields reject the key outright.
func TestParse_EnforceAllowlistTrueIsParsed(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
enforce-allowlist = true
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !routes[0].EnforceAllowlist {
		t.Error("EnforceAllowlist = false, want true")
	}
}

// TestParse_AuthSchemeUnknownValueIsError verifies that an auth-scheme
// naming neither "bearer", "basic", nor "header:<Name>" is rejected.
func TestParse_AuthSchemeUnknownValueIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
auth-scheme = "digest"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for unknown auth-scheme, got nil")
	}
	if !strings.Contains(err.Error(), "digest") {
		t.Errorf("expected error to name the unrecognized scheme, got: %v", err)
	}
}

// TestParse_AuthSchemeValidValuesAccepted verifies that "basic" and a
// "header:<Name>" naming a non-empty header name both parse cleanly.
func TestParse_AuthSchemeValidValuesAccepted(t *testing.T) {
	for _, scheme := range []string{"basic", "header:X-Api-Key"} {
		t.Run(scheme, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
auth-scheme = "` + scheme + `"
credential = { netrc = "~/.netrc" }
`
			routes, err := Parse([]byte(doc))
			if err != nil {
				t.Fatalf("unexpected error for auth-scheme %q: %v", scheme, err)
			}
			if routes[0].AuthScheme != scheme {
				t.Errorf("AuthScheme = %q, want %q", routes[0].AuthScheme, scheme)
			}
		})
	}
}

// TestParse_AuthSchemeHeaderWithEmptyNameIsError verifies that
// "header:" naming an empty header name is rejected rather than silently
// accepted as some header-less scheme.
func TestParse_AuthSchemeHeaderWithEmptyNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
auth-scheme = "header:"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for header: with an empty name, got nil")
	}
}

// TestParse_AuthSchemeHeaderWithInvalidNameIsError verifies that a
// "header:<Name>" whose Name is not a valid RFC 7230 header field name is
// rejected at Parse -- accepting it would pass validation only to 502 every
// proxied request once Go's http layer rejects the header name at request
// time.
func TestParse_AuthSchemeHeaderWithInvalidNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
auth-scheme = "header:X-Evil\r\nX-Injected: yes"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a header: auth-scheme with an invalid header name, got nil")
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_UpstreamBaseURLNonAbsoluteIsError verifies that an
// upstream-base-url missing a scheme or host is rejected -- unlike the
// scalar REGISTRY_PROXY_UPSTREAM_URL knob it replaces, this field permits a
// base path, but it must still be a genuine absolute URL.
func TestParse_UpstreamBaseURLNonAbsoluteIsError(t *testing.T) {
	for _, upstream := range []string{
		"artifactory.example.com/artifactory",
		"/artifactory",
		"",
	} {
		t.Run(upstream, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "` + upstream + `"
credential = { netrc = "~/.netrc" }
`
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for non-absolute upstream-base-url %q, got nil", upstream)
			}
		})
	}
}

// TestParse_UpstreamBaseURLMalformedWrapsParseError verifies that a
// url.Parse failure is distinguishable from a merely relative or non-http(s)
// upstream-base-url: the underlying parse error's detail is wrapped into the
// returned error, not swallowed into the generic "must be absolute" message.
// It asserts the *unwrapped* inner error (url.Parse's own *url.Error embeds
// the raw URL, which may carry userinfo -- see
// TestParse_UpstreamBaseURLMalformedDoesNotEchoUserinfo), so this only
// checks for the parse detail's presence, not a *url.Error type.
func TestParse_UpstreamBaseURLMalformedWrapsParseError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://[::1"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a malformed upstream-base-url, got nil")
	}
	if !strings.Contains(err.Error(), "is malformed") {
		t.Errorf("expected error %q to contain the malformed-URL detail", err.Error())
	}
	if strings.Contains(err.Error(), "must be an absolute http(s) URL") {
		t.Errorf("expected error %q not to be swallowed into the generic \"must be absolute\" message", err.Error())
	}
}

// TestParse_UpstreamBaseURLMalformedDoesNotEchoUserinfo verifies that a
// malformed upstream-base-url's error never echoes back userinfo embedded in
// the raw value -- url.Parse's own *url.Error message echoes the full raw
// URL, so the wrap must unwrap to url.Parse's inner error rather than
// including raw itself (matching the userinfo branch below, which
// deliberately omits raw for the same reason).
func TestParse_UpstreamBaseURLMalformedDoesNotEchoUserinfo(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://user:pw@[::1"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a malformed upstream-base-url, got nil")
	}
	if strings.Contains(err.Error(), "pw") {
		t.Errorf("error %q must not echo the userinfo embedded in a malformed upstream-base-url", err.Error())
	}
}

// TestParse_UpstreamBaseURLWithUserinfoIsError verifies that an
// upstream-base-url carrying userinfo is rejected -- httputil.ProxyRequest's
// SetURL never copies URL.User, so a userinfo-bearing upstream would
// silently drop the password on the outbound leg while still being echoed
// back in error messages if it were merely allowed and stored verbatim.
func TestParse_UpstreamBaseURLWithUserinfoIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://user:pw@artifactory.example.com/artifactory"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an upstream-base-url with userinfo, got nil")
	}
	if strings.Contains(err.Error(), "pw") {
		t.Errorf("expected error not to echo the userinfo itself, got: %v", err)
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_UpstreamBaseURLWithPathIsAccepted verifies that a base path in
// upstream-base-url is accepted (ADR 0045 retires the scalar knob's
// bare-origin rule, since a route's own base path structurally removes the
// path-doubling ambiguity that rule used to guard against).
func TestParse_UpstreamBaseURLWithPathIsAccepted(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory/api/cargo"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://artifactory.example.com/artifactory/api/cargo"; routes[0].UpstreamBaseURL != want {
		t.Errorf("UpstreamBaseURL = %q, want %q", routes[0].UpstreamBaseURL, want)
	}
}

// TestParse_UpstreamBaseURLTrailingSlashNormalized verifies that a trailing
// slash is stripped, so a trailing-slash and a bare form of the same
// upstream-base-url store identically.
func TestParse_UpstreamBaseURLTrailingSlashNormalized(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory/"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://artifactory.example.com/artifactory"; routes[0].UpstreamBaseURL != want {
		t.Errorf("UpstreamBaseURL = %q, want %q (trailing slash stripped)", routes[0].UpstreamBaseURL, want)
	}
}

// TestParse_CargoRegistriesValidNamesAreParsed verifies that a route's
// optional cargo-registries array (ADR 0045) decodes onto
// Route.CargoRegistries unchanged, in file order.
func TestParse_CargoRegistriesValidNamesAreParsed(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
upstream-base-url = "https://crates.example.com/api/v1/crates"
cargo-registries = ["example-remote", "another_one", "third-3"]
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"example-remote", "another_one", "third-3"}
	if !reflect.DeepEqual(routes[0].CargoRegistries, want) {
		t.Errorf("CargoRegistries = %v, want %v", routes[0].CargoRegistries, want)
	}
}

// TestParse_CargoRegistriesAbsentIsNil verifies that a route with no
// cargo-registries key at all parses with a nil CargoRegistries, and that
// omitting the field entirely (back-compat with pre-ADR-0045 files) is not
// an error.
func TestParse_CargoRegistriesAbsentIsNil(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].CargoRegistries != nil {
		t.Errorf("CargoRegistries = %v, want nil", routes[0].CargoRegistries)
	}
}

// TestParse_CargoRegistriesEmptyNameIsError verifies that an empty string in
// cargo-registries is rejected -- an empty name would flow into a
// CARGO_REGISTRIES__TOKEN env var name malformed the same way an empty
// registry name would.
func TestParse_CargoRegistriesEmptyNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
upstream-base-url = "https://crates.example.com/api/v1/crates"
cargo-registries = [""]
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an empty cargo-registries name, got nil")
	}
	if !strings.Contains(err.Error(), "crates.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), "cargo-registries") {
		t.Errorf("expected error to name the offending field, got: %v", err)
	}
}

// TestParse_CargoRegistriesInvalidCharsIsError verifies that a
// cargo-registries name outside cargo's bare-key charset ([A-Za-z0-9_-]) is
// rejected -- these names flow into a CARGO_REGISTRIES_<NAME>_TOKEN shell env
// var name, so a name like "evil; rm" could otherwise smuggle shell metadata
// into a sourced env file.
func TestParse_CargoRegistriesInvalidCharsIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
upstream-base-url = "https://crates.example.com/api/v1/crates"
cargo-registries = ["evil; rm"]
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an invalid cargo-registries name, got nil")
	}
	if !strings.Contains(err.Error(), "evil; rm") {
		t.Errorf("expected error to name the offending value, got: %v", err)
	}
}

// TestParse_CargoRegistriesDuplicateNameIsError verifies that the same
// cargo-registries name repeated within one route is rejected.
func TestParse_CargoRegistriesDuplicateNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
upstream-base-url = "https://crates.example.com/api/v1/crates"
cargo-registries = ["example-remote", "example-remote"]
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a duplicate cargo-registries name, got nil")
	}
	if !strings.Contains(err.Error(), "example-remote") {
		t.Errorf("expected error to name the duplicated value, got: %v", err)
	}
}

// TestFromScalars_EmptyUpstreamURLReturnsNoRoutes verifies that an empty
// upstreamURL -- the documented opt-out that disables the registry proxy
// entirely -- synthesizes no bridge route.
func TestFromScalars_EmptyUpstreamURLReturnsNoRoutes(t *testing.T) {
	routes := FromScalars("", "/some/file", "", "raw", "")
	if len(routes) != 0 {
		t.Errorf("got %d routes, want 0 for an empty upstreamURL", len(routes))
	}
}

// TestFromScalars_BuildsOneRouteFromScalarKnobs verifies that FromScalars
// synthesizes exactly one bridge route whose match host is the upstream
// URL's own host and whose Credential is the identical credresolver.Config
// the pre-ADR-0045 scalar knobs produced (registrycredential.go's
// resolveRegistryProxyCredential), so a Consumer still on the scalar knobs
// gets byte-for-byte the same credential resolution through the new path.
func TestFromScalars_BuildsOneRouteFromScalarKnobs(t *testing.T) {
	routes := FromScalars(
		"https://registry.example.com:8443",
		"", "SOME_ENV", "raw", "",
	)
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(routes))
	}
	r := routes[0]
	if r.MatchHost != "registry.example.com:8443" {
		t.Errorf("MatchHost = %q, want %q", r.MatchHost, "registry.example.com:8443")
	}
	if r.UpstreamBaseURL != "https://registry.example.com:8443" {
		t.Errorf("UpstreamBaseURL = %q, want %q", r.UpstreamBaseURL, "https://registry.example.com:8443")
	}
	if r.AuthScheme != "bearer" {
		t.Errorf("AuthScheme = %q, want %q", r.AuthScheme, "bearer")
	}
	want := credresolver.Config{
		FromEnv:     "SOME_ENV",
		FileFormat:  "raw",
		UpstreamURL: "https://registry.example.com:8443",
	}
	// credresolver.Config gained a []string field (ExecArgv) for the exec
	// credential source, so it is no longer comparable with != -- reflect
	// remains slice-aware where == cannot compile.
	if !reflect.DeepEqual(r.Credential, want) {
		t.Errorf("Credential = %+v, want %+v", r.Credential, want)
	}
}

// TestParse_NpmrcSourceMapsToCredresolverConfig verifies that the npmrc
// source maps onto credresolver's npmrc FileFormat, carrying the route's
// match host through as Credential.MatchHost -- npmrcFileResolver keys its
// lookup on the route's match host, not UpstreamURL, since npmrc has no
// analogous upstream-URL concept (credresolver.go).
func TestParse_NpmrcSourceMapsToCredresolverConfig(t *testing.T) {
	const doc = `
[[routes]]
match-host = "registry.npmjs.org"
upstream-base-url = "https://registry.npmjs.org"
credential = { npmrc = "~/.npmrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cred := routes[0].Credential
	if cred.FromFile != "~/.npmrc" {
		t.Errorf("Credential.FromFile = %q, want %q", cred.FromFile, "~/.npmrc")
	}
	if cred.FileFormat != "npmrc" {
		t.Errorf("Credential.FileFormat = %q, want %q", cred.FileFormat, "npmrc")
	}
	if cred.MatchHost != "registry.npmjs.org" {
		t.Errorf("Credential.MatchHost = %q, want %q", cred.MatchHost, "registry.npmjs.org")
	}
}

// TestParse_GradlePropertiesWithKeySourceMapsToCredresolverConfig verifies
// that the gradle-properties source maps onto credresolver's
// gradle-properties FileFormat, with its required "key" companion carried
// through as Credential.PropertyKey (ADR 0045: the shape is path + key).
func TestParse_GradlePropertiesWithKeySourceMapsToCredresolverConfig(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
upstream-base-url = "https://repo.example.com/maven"
credential = { gradle-properties = "/home/build/.gradle/gradle.properties", key = "mavenToken" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cred := routes[0].Credential
	if cred.FromFile != "/home/build/.gradle/gradle.properties" {
		t.Errorf("Credential.FromFile = %q, want %q", cred.FromFile, "/home/build/.gradle/gradle.properties")
	}
	if cred.FileFormat != "gradle-properties" {
		t.Errorf("Credential.FileFormat = %q, want %q", cred.FileFormat, "gradle-properties")
	}
	if cred.PropertyKey != "mavenToken" {
		t.Errorf("Credential.PropertyKey = %q, want %q", cred.PropertyKey, "mavenToken")
	}
}

// TestParse_GradlePropertiesWithoutKeyIsError verifies that
// gradle-properties without its required "key" companion is rejected --
// mirroring cargo-credentials' registry-name requirement (ADR 0045).
func TestParse_GradlePropertiesWithoutKeyIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
upstream-base-url = "https://repo.example.com/maven"
credential = { gradle-properties = "/home/build/.gradle/gradle.properties" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for gradle-properties without key, got nil")
	}
	if !strings.Contains(err.Error(), "gradle-properties") || !strings.Contains(err.Error(), "key") {
		t.Errorf("expected error to name both TOML keys, got: %v", err)
	}
}

// TestParse_KeyWithoutGradlePropertiesIsError verifies that "key" is
// rejected when the credential's source is not gradle-properties -- "key" is
// documented as gradle-properties' companion key only, mirroring
// registry-name's cargo-credentials-only rule.
func TestParse_KeyWithoutGradlePropertiesIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
upstream-base-url = "https://repo.example.com/maven"
credential = { env = "SOME_ENV", key = "mavenToken" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for key without gradle-properties, got nil")
	}
	if !strings.Contains(err.Error(), "key") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// TestParse_ExecSourceMapsToCredresolverConfig verifies that the exec
// source's argv array decodes onto Credential.ExecArgv, and that the route's
// match host rides along as Credential.MatchHost -- execResolver names the
// route in a failed command's error, not to select behavior.
func TestParse_ExecSourceMapsToCredresolverConfig(t *testing.T) {
	const doc = `
[[routes]]
match-host = "vault.example.com"
upstream-base-url = "https://vault.example.com/api"
credential = { exec = ["op", "read", "op://vault/item"] }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cred := routes[0].Credential
	want := []string{"op", "read", "op://vault/item"}
	if !reflect.DeepEqual(cred.ExecArgv, want) {
		t.Errorf("Credential.ExecArgv = %v, want %v", cred.ExecArgv, want)
	}
	if cred.MatchHost != "vault.example.com" {
		t.Errorf("Credential.MatchHost = %q, want %q", cred.MatchHost, "vault.example.com")
	}
}

// TestParse_ExecEmptyArrayIsError verifies that an exec credential naming an
// empty argv array is rejected -- an empty argv would reach exec.Command
// with no program name at all.
func TestParse_ExecEmptyArrayIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "vault.example.com"
upstream-base-url = "https://vault.example.com/api"
credential = { exec = [] }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an empty exec argv, got nil")
	}
	if !strings.Contains(err.Error(), "exec") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// TestParse_ExecArrayWithNonStringElementIsError verifies that an exec argv
// array containing a non-string element (here, a bare integer) is rejected
// rather than panicking or silently coercing it.
func TestParse_ExecArrayWithNonStringElementIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "vault.example.com"
upstream-base-url = "https://vault.example.com/api"
credential = { exec = ["op", 5] }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an exec argv with a non-string element, got nil")
	}
	if !strings.Contains(err.Error(), "exec") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// TestParse_ExecValueNotArrayIsError verifies that an "exec" credential given
// a TOML string (rather than an array) is rejected by parseExecArgv's
// v.([]any) type assertion, naming the offending key and what shape it must
// be.
func TestParse_ExecValueNotArrayIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "vault.example.com"
upstream-base-url = "https://vault.example.com/api"
credential = { exec = "op read" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an exec value that is not an array, got nil")
	}
	const want = `credential key "exec" must be an array of strings`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("expected error to contain %q, got: %v", want, err)
	}
}

// TestParse_ExecArgv0EmptyIsError verifies that an exec argv whose first
// element is the empty string is rejected -- an empty argv[0] would reach
// exec.Command as an empty program name and fail with a bare OS error that
// never names the offending route.
func TestParse_ExecArgv0EmptyIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "vault.example.com"
upstream-base-url = "https://vault.example.com/api"
credential = { exec = ["", "x"] }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an exec argv with an empty argv[0], got nil")
	}
	if !strings.Contains(err.Error(), "vault.example.com") {
		t.Errorf("expected error to name the route by its match-host, got: %v", err)
	}
	if !strings.Contains(err.Error(), "empty argv[0]") {
		t.Errorf("expected error to mention the empty argv[0], got: %v", err)
	}
}

// TestParse_NonExecSourceWithNonStringValueIsError verifies that a
// non-"exec" credential key given a non-string value (here, a TOML array
// where env expects a string) is rejected -- catching the case go-toml's
// decode into map[string]any can no longer reject for us at decode-time.
func TestParse_NonExecSourceWithNonStringValueIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { env = ["not", "a", "string"] }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a non-exec credential key with a non-string value, got nil")
	}
	if !strings.Contains(err.Error(), "env") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// TestParse_ExistingSourcesAlsoSetMatchHost verifies that parseCredential
// sets Credential.MatchHost for every source, not only exec and npmrc --
// harmless for the sources that ignore it, but a single unconditional
// assignment is simpler to reason about than one gated per source.
func TestParse_ExistingSourcesAlsoSetMatchHost(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].Credential.MatchHost != "artifactory.example.com" {
		t.Errorf("Credential.MatchHost = %q, want %q", routes[0].Credential.MatchHost, "artifactory.example.com")
	}
}

// TestParse_UnknownTopLevelKeyIsError verifies that strict decoding rejects
// a top-level key outside "routes" -- silently dropping a typo'd key would
// mean the operator's intended config never took effect.
func TestParse_UnknownTopLevelKeyIsError(t *testing.T) {
	const doc = `
enforce-allowlist-globally = true

[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for unknown top-level key, got nil")
	}
}

// TestParse_UnknownRouteLevelKeyIsError verifies that strict decoding
// rejects an unknown key inside a [[routes]] entry, e.g. a typo of
// match-host.
func TestParse_UnknownRouteLevelKeyIsError(t *testing.T) {
	const doc = `
[[routes]]
match-hosts = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for unknown route-level key, got nil")
	}
}

// TestParse_ZeroRoutesIsError verifies that a routes file declaring no
// [[routes]] entries at all is rejected -- an empty routes file is
// indistinguishable from a typo (e.g. a stray top-level table name) and
// silently disabling the registry proxy this way would be surprising.
func TestParse_ZeroRoutesIsError(t *testing.T) {
	_, err := Parse([]byte(""))
	if err == nil {
		t.Fatal("expected error for a routes file with no routes, got nil")
	}
}

// TestParse_EmptyMatchHostIsErrorNamingRouteByIndex verifies that a route
// with an empty (or absent) match-host is rejected, and that the error
// names the route by its 1-based position since match-host itself -- the
// field routes are otherwise identified by -- is what's missing.
func TestParse_EmptyMatchHostIsErrorNamingRouteByIndex(t *testing.T) {
	const doc = `
[[routes]]
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a route with no match-host, got nil")
	}
	if !strings.Contains(err.Error(), "route 1") {
		t.Errorf("expected error to name the route by 1-based index (\"route 1\"), got: %v", err)
	}
}

// TestParse_MatchHostWithWhitespaceIsError verifies that a match-host with
// leading or trailing whitespace is rejected -- it parses clean but can
// never be a real registry hostname, so silently accepting it would corrupt
// the route's derived path prefix.
func TestParse_MatchHostWithWhitespaceIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = " h.example "
upstream-base-url = "https://h.example/artifactory"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a match-host with leading/trailing whitespace, got nil")
	}
	if !strings.Contains(err.Error(), "h.example") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_DuplicateMatchHostIsErrorNamingHost verifies that two routes
// naming the same match-host are rejected -- a Box-inbound request's Host
// header could otherwise match either one, an ambiguity the file's author
// should resolve rather than the parser guessing "first wins".
func TestParse_DuplicateMatchHostIsErrorNamingHost(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { netrc = "~/.netrc" }

[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/other"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for duplicate match-host, got nil")
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the duplicated host, got: %v", err)
	}
}

// TestParse_DuplicateMatchHostAfterNormalizationIsError verifies that three
// routes whose match-host strings differ only in case or a trailing ":port"
// are rejected as duplicates -- the proxy's own route selection (hostOnly in
// registryproxy.go) lowercases and strips the port before comparing, so
// "H.Example", "h.example:443", and "h.example" all collapse onto the same
// key at request time; letting the raw-string check here accept the file
// would silently shadow the file's second and third routes with the first.
func TestParse_DuplicateMatchHostAfterNormalizationIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "H.Example"
upstream-base-url = "https://h.example/one"
credential = { netrc = "~/.netrc" }

[[routes]]
match-host = "h.example:443"
upstream-base-url = "https://h.example/two"
credential = { netrc = "~/.netrc" }

[[routes]]
match-host = "h.example"
upstream-base-url = "https://h.example/three"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for match-hosts that collapse to the same host after normalization, got nil")
	}
	if !strings.Contains(err.Error(), "h.example") {
		t.Errorf("expected error to name the duplicated host, got: %v", err)
	}
}

// TestParse_DuplicateMatchHostBracketedIPv6WithAndWithoutPortIsError verifies
// that a bracketed IPv6 match-host with no port and the same host with an
// explicit port collapse onto the same route -- an inbound "Host:
// [::1]:443" normalizes (net.SplitHostPort) to "::1", so "[::1]" must
// normalize the same way or it would never match its own route.
func TestParse_DuplicateMatchHostBracketedIPv6WithAndWithoutPortIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "[::1]"
upstream-base-url = "https://example.com/one"
credential = { netrc = "~/.netrc" }

[[routes]]
match-host = "[::1]:443"
upstream-base-url = "https://example.com/two"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for match-hosts that collapse to the same bracketed IPv6 host, got nil")
	}
}

// TestParse_CredentialWithNoSourceIsErrorNamingRoute verifies that a
// credential inline table naming none of credentialSourceKeys is
// rejected -- a route with no credential source would silently run
// unauthenticated, the same fail-closed posture credresolver's own
// validateRegistryProxyCredential enforces for the scalar knobs.
func TestParse_CredentialWithNoSourceIsErrorNamingRoute(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = {}
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a credential with no source, got nil")
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_CredentialWithMultipleSourcesIsErrorNamingThem verifies that a
// credential naming two sources at once is rejected and that the error
// names both offending keys, not just the fact that there's a problem.
func TestParse_CredentialWithMultipleSourcesIsErrorNamingThem(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { env = "SOME_ENV", netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a credential naming two sources, got nil")
	}
	if !strings.Contains(err.Error(), "env") || !strings.Contains(err.Error(), "netrc") {
		t.Errorf("expected error to name both offending keys (env, netrc), got: %v", err)
	}
}

// TestParse_CredentialSourceWithEmptyValueIsErrorNamingKey verifies that a
// credential naming a source key with an empty string value is rejected --
// present-but-empty must not count as "exactly one source" the way an
// absent key correctly doesn't, since credresolver.Resolve on an empty
// source silently returns no credential (registryproxy.go then sends the
// request with no auth header at all), turning an operator's typo'd or
// unsubstituted TOML value into a silent unauthenticated pass-through.
func TestParse_CredentialSourceWithEmptyValueIsErrorNamingKey(t *testing.T) {
	for _, doc := range []string{
		`
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { env = "" }
`,
		`
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { file = "" }
`,
		`
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { netrc = "" }
`,
		`
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { cargo-credentials = "" }
`,
	} {
		t.Run("", func(t *testing.T) {
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatal("expected error for a credential source with an empty value, got nil")
			}
			if !strings.Contains(err.Error(), "artifactory.example.com") {
				t.Errorf("expected error to name the route, got: %v", err)
			}
		})
	}
}

// TestParse_CargoCredentialsRegistryNameEmptyIsGenericEmptyValueError pins
// down which of the two "registry-name is wrong" messages fires when
// registry-name is present but set to "": the generic empty-value check (the
// same one every other credential key goes through) fires first and names
// "registry-name", not the companion-required message that fires only when
// registry-name is absent entirely (see
// TestParse_CargoCredentialsWithoutRegistryNameIsError below).
func TestParse_CargoCredentialsRegistryNameEmptyIsGenericEmptyValueError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
upstream-base-url = "https://crates.example.com/api/v1/crates"
credential = { cargo-credentials = "~/.cargo/credentials.toml", registry-name = "" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an empty registry-name, got nil")
	}
	const want = `credential key "registry-name" is empty`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("expected error to contain %q, got: %v", want, err)
	}
}

// TestParse_CargoCredentialsWithoutRegistryNameIsCompanionRequiredError pins
// down the companion-required message's exact text, distinguishing it from
// the generic empty-value message pinned above -- this fires only when
// registry-name is absent from the table, not merely empty.
func TestParse_CargoCredentialsWithoutRegistryNameIsCompanionRequiredError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
upstream-base-url = "https://crates.example.com/api/v1/crates"
credential = { cargo-credentials = "~/.cargo/credentials.toml" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for cargo-credentials without registry-name, got nil")
	}
	const want = `credential key "cargo-credentials" requires companion key "registry-name"`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("expected error to contain %q, got: %v", want, err)
	}
}

// TestParse_CredentialUnknownKeyIsErrorNamingRouteAndKey verifies that a
// credential key outside credentialSourceKeys plus the registry-name/key
// companions is rejected, naming both the route and the offending key.
func TestParse_CredentialUnknownKeyIsErrorNamingRouteAndKey(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { pypirc = "~/.pypirc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an unknown credential key, got nil")
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), "pypirc") {
		t.Errorf("expected error to name the unknown key %q, got: %v", "pypirc", err)
	}
}

// TestParse_RegistryNameWithoutCargoCredentialsIsError verifies that
// registry-name is rejected when the credential's source is not
// cargo-credentials -- registry-name is documented as cargo-credentials'
// companion key only, and silently dropping it for other sources would
// contradict that without telling the operator.
func TestParse_RegistryNameWithoutCargoCredentialsIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { env = "SOME_ENV", registry-name = "example-remote" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for registry-name without cargo-credentials, got nil")
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), "registry-name") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// TestParse_CargoCredentialsWithoutRegistryNameIsError verifies that
// cargo-credentials without its registry-name companion is rejected at
// Parse, phrased in TOML-key vocabulary rather than credresolver's own
// scalar-env-knob error, since a routes-file operator never sees the scalar
// knobs.
func TestParse_CargoCredentialsWithoutRegistryNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
upstream-base-url = "https://crates.example.com/api/v1/crates"
credential = { cargo-credentials = "~/.cargo/credentials.toml" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for cargo-credentials without registry-name, got nil")
	}
	if !strings.Contains(err.Error(), "crates.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), "registry-name") || !strings.Contains(err.Error(), "cargo-credentials") {
		t.Errorf("expected error to name both TOML keys, got: %v", err)
	}
}

// TestParse_CargoCredentialsSourceMapsRegistryNameCompanion verifies that
// the cargo-credentials source maps onto credresolver's cargo-credentials
// FileFormat, and that its registry-name companion key rides along as
// Credential.RegistryName rather than being rejected as a second source.
func TestParse_CargoCredentialsSourceMapsRegistryNameCompanion(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
upstream-base-url = "https://crates.example.com/api/v1/crates"
credential = { cargo-credentials = "~/.cargo/credentials.toml", registry-name = "example-remote" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cred := routes[0].Credential
	if cred.FromFile != "~/.cargo/credentials.toml" {
		t.Errorf("Credential.FromFile = %q, want %q", cred.FromFile, "~/.cargo/credentials.toml")
	}
	if cred.FileFormat != "cargo-credentials" {
		t.Errorf("Credential.FileFormat = %q, want %q", cred.FileFormat, "cargo-credentials")
	}
	if cred.RegistryName != "example-remote" {
		t.Errorf("Credential.RegistryName = %q, want %q", cred.RegistryName, "example-remote")
	}
}

// TestParse_EnvAndFileSourcesMapToCredresolverConfig verifies the remaining
// two credential sources' mapping onto credresolver.Config: env's value
// becomes FromEnv with no FileFormat, and file's value becomes FromFile with
// FileFormat "raw".
func TestParse_EnvAndFileSourcesMapToCredresolverConfig(t *testing.T) {
	for name, doc := range map[string]string{
		"env": `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { env = "REGISTRY_TOKEN" }
`,
		"file": `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { file = "/run/secrets/registry-token" }
`,
	} {
		t.Run(name, func(t *testing.T) {
			routes, err := Parse([]byte(doc))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			cred := routes[0].Credential
			switch name {
			case "env":
				if cred.FromEnv != "REGISTRY_TOKEN" {
					t.Errorf("Credential.FromEnv = %q, want %q", cred.FromEnv, "REGISTRY_TOKEN")
				}
			case "file":
				if cred.FromFile != "/run/secrets/registry-token" {
					t.Errorf("Credential.FromFile = %q, want %q", cred.FromFile, "/run/secrets/registry-token")
				}
				if cred.FileFormat != "raw" {
					t.Errorf("Credential.FileFormat = %q, want %q", cred.FileFormat, "raw")
				}
			}
		})
	}
}
