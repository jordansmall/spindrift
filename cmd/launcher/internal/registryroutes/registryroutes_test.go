package registryroutes

import (
	"errors"
	"net/url"
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
// url.Parse failure is distinguishable from a merely relative or
// non-http(s) upstream-base-url: the underlying parse error is wrapped into
// the returned error, not swallowed into the generic "must be absolute"
// message.
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
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Errorf("expected error to wrap a *url.Error, got: %v", err)
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
// never equal a real inbound Host header, so silently accepting it would
// leave the route unreachable.
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
// credential inline table naming none of env/file/netrc/cargo-credentials is
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

// TestParse_CredentialUnknownKeyIsErrorNamingRouteAndKey verifies that a
// credential key outside env/file/netrc/cargo-credentials/registry-name is
// rejected, naming both the route and the offending key.
func TestParse_CredentialUnknownKeyIsErrorNamingRouteAndKey(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { npmrc = "~/.npmrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an unknown credential key, got nil")
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), "npmrc") {
		t.Errorf("expected error to name the unknown key %q, got: %v", "npmrc", err)
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
