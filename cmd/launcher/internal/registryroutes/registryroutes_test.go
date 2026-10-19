package registryroutes_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/registryroutes"
	"spindrift.dev/launcher/internal/registryvocab"
)

// parse is Parse over the synthetic row alone: the generic route grammar is
// pinned here without naming a real ecosystem, whose own declarations are
// pinned by the ecosystem corpora.
func parse(data []byte) ([]registryroutes.Route, error) {
	return registryroutes.ParseRows(data, []ecosystem.Row{synthetic.Row})
}

// Pins the minimal valid routes file of ADR 0045 and its mapping onto
// credresolver's netrc source.
func TestParse_SingleValidRouteBearerNetrc(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
auth-scheme = "bearer"
credential = { netrc = "~/.netrc" }
`
	routes, err := parse([]byte(doc))
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
	if r.AuthScheme != "bearer" {
		t.Errorf("AuthScheme = %q, want %q", r.AuthScheme, "bearer")
	}
	if r.Credential.FromFile != "~/.netrc" {
		t.Errorf("Credential.FromFile = %q, want %q", r.Credential.FromFile, "~/.netrc")
	}
	if r.Credential.FileFormat != "netrc" {
		t.Errorf("Credential.FileFormat = %q, want %q", r.Credential.FileFormat, "netrc")
	}
}

// ADR 0045 defaults a missing auth-scheme to "bearer", not to an empty string
// that later code would have to special-case.
func TestParse_AuthSchemeAbsentDefaultsToBearer(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
`
	routes, err := parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].AuthScheme != "bearer" {
		t.Errorf("AuthScheme = %q, want %q", routes[0].AuthScheme, "bearer")
	}
}

// The only valid schemes are "bearer", "basic", and "header:<Name>".
func TestParse_AuthSchemeUnknownValueIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
auth-scheme = "digest"
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for unknown auth-scheme, got nil")
	}
	if !strings.Contains(err.Error(), "digest") {
		t.Errorf("expected error to name the unrecognized scheme, got: %v", err)
	}
}

func TestParse_AuthSchemeValidValuesAccepted(t *testing.T) {
	for _, scheme := range []string{"basic", "header:X-Api-Key"} {
		t.Run(scheme, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "artifactory.example.com"
auth-scheme = "` + scheme + `"
credential = { netrc = "~/.netrc" }
`
			routes, err := parse([]byte(doc))
			if err != nil {
				t.Fatalf("unexpected error for auth-scheme %q: %v", scheme, err)
			}
			if routes[0].AuthScheme != scheme {
				t.Errorf("AuthScheme = %q, want %q", routes[0].AuthScheme, scheme)
			}
		})
	}
}

func TestParse_AuthSchemeHeaderWithEmptyNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
auth-scheme = "header:"
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for header: with an empty name, got nil")
	}
}

// A header name that is not valid under RFC 7230 has to fail at Parse:
// accepting it would pass validation only to 502 every proxied request once
// Go's http layer rejects the name at request time.
func TestParse_AuthSchemeHeaderWithInvalidNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
auth-scheme = "header:X-Evil\r\nX-Injected: yes"
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a header: auth-scheme with an invalid header name, got nil")
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// Omitting allow entirely stays valid for back-compat (ADR 0047).
func TestParse_AllowAbsentIsNil(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
`
	routes, err := parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].Allow != nil {
		t.Errorf("Allow = %v, want nil", routes[0].Allow)
	}
}

// The optional allow array (ADR 0047, issue #3258) decodes unchanged, in file
// order. The fixture is a plain host-rooted route, the only shape a routes
// file has since ADR 0047 (issue #3261).
func TestParse_AllowValidPatternsAreParsed(t *testing.T) {
	for _, tc := range []struct {
		name string
		toml string
		want []string
	}{
		{"single entry", `allow = ["/dl"]`, []string{"/dl"}},
		{"multiple entries", `allow = ["/dl", "/api/v2"]`, []string{"/dl", "/api/v2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "artifactory.example.com"
` + tc.toml + `
credential = { netrc = "~/.netrc" }
`
			routes, err := parse([]byte(doc))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(routes[0].Allow, tc.want) {
				t.Errorf("Allow = %v, want %v", routes[0].Allow, tc.want)
			}
		})
	}
}

// An allow pattern must already be in the canonical subtree-root form that
// validateAllowPatterns defines.

func TestParse_AllowInvalidPatternIsError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		reason  string
	}{
		{"no leading slash", "dl", ""},
		{"trailing slash", "/dl/", ""},
		{"traversal segment", "/dl/../etc", ""},
		{"doubled slash", "//dl", ""},
		{"root pattern", "/", ""},
		{"query string", "/dl?x", `must not contain "?" or "#"`},
		{"fragment", "/dl#x", `must not contain "?" or "#"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "artifactory.example.com"
allow = ["` + tc.pattern + `"]
credential = { netrc = "~/.netrc" }
`
			_, err := parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for allow pattern %q, got nil", tc.pattern)
			}
			if !strings.Contains(err.Error(), "artifactory.example.com") {
				t.Errorf("expected error to name the route, got: %v", err)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", tc.pattern)) {
				t.Errorf("expected error to name the offending pattern %q, got: %v", tc.pattern, err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("expected error to give the reason %q, got: %v", tc.reason, err)
			}
		})
	}
}

// syntheticPathDoc is a host-rooted route whose [routes.ecosystems.synthetic]
// block declares pathLine verbatim, so each case chooses its own TOML string
// flavour: a literal (single-quoted) string lets a backslash reach parse
// unescaped rather than being consumed as a TOML basic-string escape.
func syntheticPathDoc(pathLine string) string {
	return `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.synthetic]
` + pathLine + `
`
}

// A [routes.ecosystems.<name>] block's path key (ADR 0048, issue #3405) is
// stored with every trailing slash stripped, mirroring upstream-origin's own
// normalization. TrimSuffix would leave "/foo/" from "/foo//", which renders a
// double-slash URL strict registries 404 on.
func TestParse_BlockPathValidIsNormalized(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"no trailing slash", "/mirror", "/mirror"},
		{"trailing slash", "/mirror/", "/mirror"},
		{"trailing double slash", "/foo//", "/foo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routes, err := parse([]byte(syntheticPathDoc(`path = "` + tc.path + `"`)))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := routes[0].Ecosystems.Path("synthetic"); got != tc.want {
				t.Errorf("Ecosystems.Path(synthetic) = %q, want %q (all trailing slashes stripped)", got, tc.want)
			}
		})
	}
}

// The [routes.ecosystems.<name>] block is optional.
func TestParse_BlockPathAbsentIsEmpty(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
`
	routes, err := parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := routes[0].Ecosystems.Path("synthetic"); got != "" {
		t.Errorf("Ecosystems.Path(synthetic) = %q, want empty (omitted)", got)
	}
}

// Every refusal below is the shared validateDeclaredPath ban, so it holds for
// any row's path key. The rejected shapes are:
//   - a leading slash is required;
//   - whitespace, and "$", "`" and "\" which flow into shell-sourced exports
//     and init-script string literals downstream;
//   - "?" and "#", which would cut a URL short (issue #3321);
//   - ".", ".." and an interior doubled slash, which the path.Clean-based
//     consumers downstream can never produce or match;
//   - the whole host ("/" or "//"): path only adds a subtree on top of an
//     already-resolved host-rooted route, and TrimSuffix strips only one
//     slash, so "//" must collapse to "" and hit the same check.
func TestParse_BlockPathInvalidIsError(t *testing.T) {
	const host = "repo.example.com"
	const wholeHost = "whole host"
	const urlDelimiter = `must not contain "?" or "#"`
	for _, tc := range []struct {
		name     string
		pathLine string
		want     string
	}{
		{"missing leading slash", `path = "mirror"`, host},
		{"leading whitespace", `path = " /mirror"`, host},
		{"trailing whitespace", `path = "/mirror "`, host},
		{"interior whitespace", `path = "/mir ror"`, host},
		{"dot-dot segment", `path = "/mirror/../etc"`, host},
		{"dot segment", `path = "/mirror/./release"`, host},
		{"empty segment", `path = "/mirror//release"`, host},
		{"dollar sign", `path = "/mirror/$HOME"`, host},
		{"backtick", "path = \"/mirror/`whoami`\"", host},
		{"backslash", `path = '/mirror/\release'`, host},
		{"bare root", `path = "/"`, wholeHost},
		{"double slash whole host", `path = "//"`, wholeHost},
		{"query", `path = "/mirror/?x"`, urlDelimiter},
		{"fragment", `path = "/mirror/#x"`, urlDelimiter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse([]byte(syntheticPathDoc(tc.pathLine)))
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.pathLine)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected error to contain %q, got: %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "ecosystems.synthetic.path") {
				t.Errorf("expected error to name the offending field, got: %v", err)
			}
		})
	}
}

// npmrcFileResolver keys its lookup on the route's match host, not
// UpstreamURL, since npmrc has no analogous upstream-URL concept
// (credresolver.go).
func TestParse_NpmrcSourceMapsToCredresolverConfig(t *testing.T) {
	const doc = `
[[routes]]
match-host = "registry.example.test"
credential = { npmrc = "/run/secrets/registry-npmrc" }
`
	routes, err := parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cred := routes[0].Credential
	if cred.FromFile != "/run/secrets/registry-npmrc" {
		t.Errorf("Credential.FromFile = %q, want %q", cred.FromFile, "/run/secrets/registry-npmrc")
	}
	if cred.FileFormat != "npmrc" {
		t.Errorf("Credential.FileFormat = %q, want %q", cred.FileFormat, "npmrc")
	}
	if cred.MatchHost != "registry.example.test" {
		t.Errorf("Credential.MatchHost = %q, want %q", cred.MatchHost, "registry.example.test")
	}
}

// ADR 0045 shapes the gradle-properties source as a path plus a required "key"
// companion, which lands in Credential.PropertyKey.
func TestParse_GradlePropertiesWithKeySourceMapsToCredresolverConfig(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { gradle-properties = "/run/secrets/registry.properties", key = "tokenKey" }
`
	routes, err := parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cred := routes[0].Credential
	if cred.FromFile != "/run/secrets/registry.properties" {
		t.Errorf("Credential.FromFile = %q, want %q", cred.FromFile, "/run/secrets/registry.properties")
	}
	if cred.FileFormat != "gradle-properties" {
		t.Errorf("Credential.FileFormat = %q, want %q", cred.FileFormat, "gradle-properties")
	}
	if cred.PropertyKey != "tokenKey" {
		t.Errorf("Credential.PropertyKey = %q, want %q", cred.PropertyKey, "tokenKey")
	}
}

// Mirrors cargo-credentials' registry-name requirement (ADR 0045).
func TestParse_GradlePropertiesWithoutKeyIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { gradle-properties = "/run/secrets/registry.properties" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for gradle-properties without key, got nil")
	}
	if !strings.Contains(err.Error(), "gradle-properties") || !strings.Contains(err.Error(), "key") {
		t.Errorf("expected error to name both TOML keys, got: %v", err)
	}
}

// "key" is documented as gradle-properties' companion only, mirroring
// registry-name's cargo-credentials-only rule.
func TestParse_KeyWithoutGradlePropertiesIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { env = "SOME_ENV", key = "tokenKey" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for key without gradle-properties, got nil")
	}
	if !strings.Contains(err.Error(), "key") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// The match host rides along as Credential.MatchHost because execResolver
// names the route in a failed command's error, not to select behavior.
func TestParse_ExecSourceMapsToCredresolverConfig(t *testing.T) {
	const doc = `
[[routes]]
match-host = "vault.example.com"
credential = { exec = ["op", "read", "op://vault/item"] }
`
	routes, err := parse([]byte(doc))
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

// An empty argv would reach exec.Command with no program name at all.
func TestParse_ExecEmptyArrayIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "vault.example.com"
credential = { exec = [] }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an empty exec argv, got nil")
	}
	if !strings.Contains(err.Error(), "exec") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// A non-string element must be rejected, not silently coerced.
func TestParse_ExecArrayWithNonStringElementIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "vault.example.com"
credential = { exec = ["op", 5] }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an exec argv with a non-string element, got nil")
	}
	if !strings.Contains(err.Error(), "exec") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// parseExecArgv's v.([]any) type assertion is what rejects a TOML string here.
func TestParse_ExecValueNotArrayIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "vault.example.com"
credential = { exec = "op read" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an exec value that is not an array, got nil")
	}
	const want = `credential key "exec" must be an array of strings`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("expected error to contain %q, got: %v", want, err)
	}
}

// An empty argv[0] would reach exec.Command as an empty program name and fail
// with a bare OS error that never names the offending route.
func TestParse_ExecArgv0EmptyIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "vault.example.com"
credential = { exec = ["", "x"] }
`
	_, err := parse([]byte(doc))
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

// Decoding into map[string]any means go-toml no longer rejects this shape at
// decode time, so Parse has to.
func TestParse_NonExecSourceWithNonStringValueIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { env = ["not", "a", "string"] }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a non-exec credential key with a non-string value, got nil")
	}
	if !strings.Contains(err.Error(), "env") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// parseCredential sets MatchHost for every source, not only exec and npmrc:
// harmless for the sources that ignore it, and one unconditional assignment is
// simpler to reason about than one gated per source.
func TestParse_ExistingSourcesAlsoSetMatchHost(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
`
	routes, err := parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].Credential.MatchHost != "artifactory.example.com" {
		t.Errorf("Credential.MatchHost = %q, want %q", routes[0].Credential.MatchHost, "artifactory.example.com")
	}
}

// Silently dropping a typo'd top-level key would mean the operator's intended
// config never took effect.
func TestParse_UnknownTopLevelKeyIsError(t *testing.T) {
	const doc = `
enforce-allowlist-globally = true

[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for unknown top-level key, got nil")
	}
}

func TestParse_UnknownRouteLevelKeyIsError(t *testing.T) {
	const doc = `
[[routes]]
match-hosts = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for unknown route-level key, got nil")
	}
}

// An empty routes file is indistinguishable from a typo, such as a stray
// top-level table name, and silently disabling the registry proxy that way
// would surprise the operator.
func TestParse_ZeroRoutesIsError(t *testing.T) {
	_, err := parse([]byte(""))
	if err == nil {
		t.Fatal("expected error for a routes file with no routes, got nil")
	}
}

// The error names the route by its 1-based position because match-host, the
// field routes are otherwise identified by, is what is missing.
func TestParse_EmptyMatchHostIsErrorNamingRouteByIndex(t *testing.T) {
	const doc = `
[[routes]]
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a route with no match-host, got nil")
	}
	if !strings.Contains(err.Error(), "route 1") {
		t.Errorf("expected error to name the route by 1-based index (\"route 1\"), got: %v", err)
	}
}

// A padded match-host parses clean but can never be a real registry hostname,
// so accepting it would corrupt the route's derived path prefix.
func TestParse_MatchHostWithWhitespaceIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = " h.example "
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a match-host with leading/trailing whitespace, got nil")
	}
	if !strings.Contains(err.Error(), "h.example") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// A Box-inbound request's Host header could otherwise match either route, an
// ambiguity the file's author should resolve rather than the parser guessing
// "first wins".
func TestParse_DuplicateMatchHostIsErrorNamingHost(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }

[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for duplicate match-host, got nil")
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the duplicated host, got: %v", err)
	}
}

// The proxy's route selection (registryvocab.HostKey) lowercases and strips
// the port before comparing, so "H.Example", "h.example:443" and "h.example"
// all collapse onto one key at request time. Letting the raw-string check here
// accept the file would silently shadow the second and third routes with the
// first.
func TestParse_DuplicateMatchHostAfterNormalizationIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "H.Example"
credential = { netrc = "~/.netrc" }

[[routes]]
match-host = "h.example:443"
credential = { netrc = "~/.netrc" }

[[routes]]
match-host = "h.example"
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for match-hosts that collapse to the same host after normalization, got nil")
	}
	if !strings.Contains(err.Error(), "h.example") {
		t.Errorf("expected error to name the duplicated host, got: %v", err)
	}
}

// An inbound "Host: [::1]:443" normalizes (net.SplitHostPort) to "::1", so
// "[::1]" must normalize the same way or it would never match its own route.
func TestParse_DuplicateMatchHostBracketedIPv6WithAndWithoutPortIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "[::1]"
credential = { netrc = "~/.netrc" }

[[routes]]
match-host = "[::1]:443"
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for match-hosts that collapse to the same bracketed IPv6 host, got nil")
	}
}

// An operator who wrote credential = {} meant to configure something, unlike
// TestParse_CredentialKeyAbsentIsUnauthenticated below, where the key is
// missing altogether.
func TestParse_CredentialWithNoSourceIsErrorNamingRoute(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = {}
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a credential with no source, got nil")
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// Omitting the credential key is an unauthenticated pass-through route (ADR
// 0045), distinct from the present-but-empty credential = {} case above, which
// still errors.
func TestParse_CredentialKeyAbsentIsUnauthenticated(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
`
	routes, err := parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(routes))
	}
	if got := routes[0].Credential; !reflect.DeepEqual(got, credresolver.Config{}) {
		t.Errorf("Credential = %+v, want zero value", got)
	}
}

func TestParse_CredentialWithMultipleSourcesIsErrorNamingThem(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { env = "SOME_ENV", netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a credential naming two sources, got nil")
	}
	if !strings.Contains(err.Error(), "env") || !strings.Contains(err.Error(), "netrc") {
		t.Errorf("expected error to name both offending keys (env, netrc), got: %v", err)
	}
}

// Present-but-empty must not count as "exactly one source": credresolver.Resolve
// on an empty source returns no credential and registryproxy.go then sends the
// request with no auth header at all, turning an operator's typo'd or
// unsubstituted TOML value into a silent unauthenticated pass-through.
func TestParse_CredentialSourceWithEmptyValueIsErrorNamingKey(t *testing.T) {
	for _, doc := range []string{
		`
[[routes]]
match-host = "artifactory.example.com"
credential = { env = "" }
`,
		`
[[routes]]
match-host = "artifactory.example.com"
credential = { file = "" }
`,
		`
[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "" }
`,
		`
[[routes]]
match-host = "artifactory.example.com"
credential = { cargo-credentials = "" }
`,
	} {
		t.Run("", func(t *testing.T) {
			_, err := parse([]byte(doc))
			if err == nil {
				t.Fatal("expected error for a credential source with an empty value, got nil")
			}
			if !strings.Contains(err.Error(), "artifactory.example.com") {
				t.Errorf("expected error to name the route, got: %v", err)
			}
		})
	}
}

// Two messages can fire for a wrong registry-name. When it is present but "",
// the generic empty-value check every credential key goes through fires first,
// not the companion-required message that fires only when registry-name is
// absent entirely (TestParse_CargoCredentialsWithoutRegistryNameIsError).
func TestParse_CargoCredentialsRegistryNameEmptyIsGenericEmptyValueError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "registry.example.test"
credential = { cargo-credentials = "/run/secrets/registry-credentials", registry-name = "" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an empty registry-name, got nil")
	}
	const want = `credential key "registry-name" is empty`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("expected error to contain %q, got: %v", want, err)
	}
}

// Pins the companion-required message's exact text, which fires only when
// registry-name is absent from the table rather than merely empty.
func TestParse_CargoCredentialsWithoutRegistryNameIsCompanionRequiredError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "registry.example.test"
credential = { cargo-credentials = "/run/secrets/registry-credentials" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for cargo-credentials without registry-name, got nil")
	}
	const want = `credential key "cargo-credentials" requires companion key "registry-name"`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("expected error to contain %q, got: %v", want, err)
	}
}

// The accepted set is credentialSourceKeys plus the registry-name and key
// companions.
func TestParse_CredentialUnknownKeyIsErrorNamingRouteAndKey(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { tokenfile = "/run/secrets/tokenfile" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an unknown credential key, got nil")
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), "tokenfile") {
		t.Errorf("expected error to name the unknown key %q, got: %v", "tokenfile", err)
	}
}

// registry-name is documented as cargo-credentials' companion key only, and
// silently dropping it for other sources would contradict that without telling
// the operator.
func TestParse_RegistryNameWithoutCargoCredentialsIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { env = "SOME_ENV", registry-name = "example-remote" }
`
	_, err := parse([]byte(doc))
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

// Parse phrases this in TOML-key vocabulary rather than credresolver's own
// scalar-env-knob error, since a routes-file operator never sees the scalar
// knobs.
func TestParse_CargoCredentialsWithoutRegistryNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "registry.example.test"
credential = { cargo-credentials = "/run/secrets/registry-credentials" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for cargo-credentials without registry-name, got nil")
	}
	if !strings.Contains(err.Error(), "registry.example.test") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), "registry-name") || !strings.Contains(err.Error(), "cargo-credentials") {
		t.Errorf("expected error to name both TOML keys, got: %v", err)
	}
}

// The registry-name companion rides along as Credential.RegistryName rather
// than being rejected as a second source.
func TestParse_CargoCredentialsSourceMapsRegistryNameCompanion(t *testing.T) {
	const doc = `
[[routes]]
match-host = "registry.example.test"
credential = { cargo-credentials = "/run/secrets/registry-credentials", registry-name = "example-remote" }
`
	routes, err := parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cred := routes[0].Credential
	if cred.FromFile != "/run/secrets/registry-credentials" {
		t.Errorf("Credential.FromFile = %q, want %q", cred.FromFile, "/run/secrets/registry-credentials")
	}
	if cred.FileFormat != "cargo-credentials" {
		t.Errorf("Credential.FileFormat = %q, want %q", cred.FileFormat, "cargo-credentials")
	}
	if cred.RegistryName != "example-remote" {
		t.Errorf("Credential.RegistryName = %q, want %q", cred.RegistryName, "example-remote")
	}
}

// env's value becomes FromEnv with no FileFormat; file's becomes FromFile with
// FileFormat "raw".
func TestParse_EnvAndFileSourcesMapToCredresolverConfig(t *testing.T) {
	for name, doc := range map[string]string{
		"env": `
[[routes]]
match-host = "artifactory.example.com"
credential = { env = "REGISTRY_TOKEN" }
`,
		"file": `
[[routes]]
match-host = "artifactory.example.com"
credential = { file = "/run/secrets/registry-token" }
`,
	} {
		t.Run(name, func(t *testing.T) {
			routes, err := parse([]byte(doc))
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

// The retired-key remedy (ADR 0047, issue #3261) names the key, the route and
// the migration, and prints a replacement [[routes]] stanza that itself
// parses, meaning one carrying neither retired key.
func TestParse_RetiredUpstreamBaseURLIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired upstream-base-url, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"upstream-base-url",
		"artifactory.example.com",
		"ADR 0047",
		"#3261",
		"allow",
		"[[routes]]",
		`match-host = "artifactory.example.com"`,
		`credential = { netrc = "~/.netrc" }`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}
	stanza := msg[strings.Index(msg, "[[routes]]"):]
	if strings.Contains(stanza, "upstream-base-url") || strings.Contains(stanza, "enforce-allowlist") {
		t.Errorf("replacement stanza must not carry a retired key, got:\n%s", stanza)
	}
}

// Detection is by presence, not truthiness: enforce-allowlist = false is as
// retired as a true one (ADR 0047, issue #3261), since enforcement is now
// unconditional and allow is the only recourse.
func TestParse_RetiredEnforceAllowlistFalseIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
enforce-allowlist = false
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired enforce-allowlist = false, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"enforce-allowlist", "artifactory.example.com", "ADR 0047", "#3261", "allow"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}
}

// Both retired keys are reported in one error rather than stopping at the
// first.
func TestParse_RetiredKeysBothDeclaredNamesBoth(t *testing.T) {
	const doc = `
[[routes]]
upstream-base-url = "https://artifactory.example.com/artifactory"
match-host = "artifactory.example.com"
enforce-allowlist = true
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for both retired keys, got nil")
	}
	if !strings.Contains(err.Error(), "upstream-base-url") || !strings.Contains(err.Error(), "enforce-allowlist") {
		t.Errorf("expected error to name both retired keys, got: %v", err)
	}
}

// The replacement stanza is built from the offending route's own remaining
// keys, not a generic template: auth-scheme, allow and the credential (with its
// companion key) survive. The retired per-ecosystem keys that become
// [routes.ecosystems.<name>] blocks (ADR 0048) are pinned per row by the
// ecosystem corpora.
func TestParse_RetiredUpstreamBaseURLStanzaEchoesDeclaredKeys(t *testing.T) {
	const doc = `
[[routes]]
upstream-base-url = "https://artifactory.example.com/artifactory"
match-host = "artifactory.example.com"
auth-scheme = "basic"
credential = { cargo-credentials = "/run/secrets/registry-credentials", registry-name = "artifactory" }
allow = ["/dl"]
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired upstream-base-url, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		`auth-scheme = "basic"`,
		`credential = { cargo-credentials = "/run/secrets/registry-credentials", registry-name = "artifactory" }`,
		`allow = ["/dl"]`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected stanza to contain %q, got: %v", want, err)
		}
	}
}

// The stanza carries upstream-origin only when the retired URL said something
// a committed config cannot: a non-default scheme or an explicit port. A plain
// https URL on the default port adds nothing match-host does not already say,
// and when an origin is printed it is the origin alone, never the retired
// URL's path.
func TestParse_RetiredUpstreamBaseURLStanzaOriginOnlyWhenNonDefault(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream string
		want     string
	}{
		{"plain https, default port", "https://artifactory.example.com/artifactory", ""},
		{"explicit port", "https://artifactory.example.com:8443/artifactory", `upstream-origin = "https://artifactory.example.com:8443"`},
		{"http scheme", "http://artifactory.example.com/artifactory", `upstream-origin = "http://artifactory.example.com"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
[[routes]]
upstream-base-url = "` + tc.upstream + `"
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
`
			_, err := parse([]byte(doc))
			if err == nil {
				t.Fatal("expected error for a retired upstream-base-url, got nil")
			}
			msg := err.Error()
			if tc.want == "" {
				if strings.Contains(msg, "upstream-origin") {
					t.Errorf("expected no upstream-origin line, got: %v", err)
				}
				return
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("expected stanza to contain %q, got: %v", tc.want, err)
			}
			if strings.Contains(msg, "/artifactory\"") {
				t.Errorf("upstream-origin must carry no path, got: %v", err)
			}
		})
	}
}

// The retirement error reaches stderr and CI logs, so it must never echo a
// credential embedded in the retired URL.
func TestParse_RetiredUpstreamBaseURLDoesNotEchoUserinfo(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://user:s3cr3t@artifactory.example.com:8443/artifactory"
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired upstream-base-url, got nil")
	}
	if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "user:") {
		t.Errorf("error must not echo the userinfo embedded in the retired URL, got: %v", err)
	}
}

// The optional upstream-origin key (ADR 0047, issue #3261) is stored with any
// trailing "/" stripped, and a route omitting it stores "".
func TestParse_UpstreamOriginAccepted(t *testing.T) {
	for _, tc := range []struct {
		name string
		toml string
		want string
	}{
		{"absent", "", ""},
		{"explicit port", `upstream-origin = "https://artifactory.example.com:8443"`, "https://artifactory.example.com:8443"},
		{"http scheme", `upstream-origin = "http://artifactory.example.com"`, "http://artifactory.example.com"},
		{"trailing slash", `upstream-origin = "https://artifactory.example.com/"`, "https://artifactory.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "artifactory.example.com"
` + tc.toml + `
credential = { netrc = "~/.netrc" }
`
			routes, err := parse([]byte(doc))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if routes[0].UpstreamOrigin != tc.want {
				t.Errorf("UpstreamOrigin = %q, want %q", routes[0].UpstreamOrigin, tc.want)
			}
		})
	}
}

// upstream-origin is an origin, not a URL: a path, query, fragment, userinfo,
// a relative or non-http(s) URL, and an empty host are all rejected.
func TestParse_UpstreamOriginInvalidIsError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin string
	}{
		{"path", "https://artifactory.example.com/artifactory"},
		{"root path", "https://artifactory.example.com//"},
		{"query", "https://artifactory.example.com?a=b"},
		{"fragment", "https://artifactory.example.com#frag"},
		{"userinfo", "https://user:pw@artifactory.example.com"},
		{"relative", "artifactory.example.com"},
		{"non-http scheme", "ftp://artifactory.example.com"},
		{"empty host", "https://"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "artifactory.example.com"
upstream-origin = "` + tc.origin + `"
credential = { netrc = "~/.netrc" }
`
			_, err := parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for upstream-origin %q, got nil", tc.origin)
			}
			if !strings.Contains(err.Error(), "artifactory.example.com") {
				t.Errorf("expected error to name the route, got: %v", err)
			}
			if !strings.Contains(err.Error(), "upstream-origin") {
				t.Errorf("expected error to name the key, got: %v", err)
			}
		})
	}
}

func TestParse_UpstreamOriginWithUserinfoDoesNotEcho(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-origin = "https://user:s3cr3t@artifactory.example.com"
credential = { netrc = "~/.netrc" }
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for upstream-origin with userinfo, got nil")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("error must not echo userinfo, got: %v", err)
	}
}

// Every route is host-rooted since ADR 0047 (issue #3261), so allow and a
// [routes.ecosystems.<name>] block coexist freely with a minimal route of
// match-host plus a credential; the legacy-route rejections those keys used to
// hit are gone.
func TestParse_MinimalHostRootedRouteParses(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
allow = ["/dl"]

[routes.ecosystems.synthetic]
path = "/mirror"
mirror-path = "/other"
`
	routes, err := parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(routes))
	}
	r := routes[0]
	if !reflect.DeepEqual(r.Allow, []string{"/dl"}) {
		t.Errorf("Allow = %v, want [/dl]", r.Allow)
	}
	if got := r.Ecosystems.Path("synthetic"); got != "/mirror" {
		t.Errorf("Ecosystems.Path(synthetic) = %q, want /mirror", got)
	}
	if want := "https://artifactory.example.com"; r.Credential.UpstreamURL != want {
		t.Errorf("Credential.UpstreamURL = %q, want %q", r.Credential.UpstreamURL, want)
	}
}

// A declared upstream-origin, not the "https://" + match-host stand-in, is
// what the credential carries as UpstreamURL: the netrc source keys its
// machine-name match on that value.
func TestParse_UpstreamOriginFeedsCredentialUpstreamURL(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-origin = "http://artifactory.example.com:8081"
credential = { netrc = "~/.netrc" }
`
	routes, err := parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "http://artifactory.example.com:8081"; routes[0].Credential.UpstreamURL != want {
		t.Errorf("Credential.UpstreamURL = %q, want %q", routes[0].Credential.UpstreamURL, want)
	}
}

// Three call sites share this rule: the migration stanza Parse prints, the
// retired-scalar-knob stanza the launch gate prints, and what "spindrift
// registry discover" writes. A remedy telling an operator what to write can
// never disagree with the generator that writes it for them.
func TestUpstreamOriginFor(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"plain https on the default port", "https://artifactory.example.com", ""},
		{"plain https with a path", "https://artifactory.example.com/artifactory/api/registry/remote", ""},
		{"https on an explicit 443", "https://artifactory.example.com:443", "https://artifactory.example.com:443"},
		{"http scheme", "http://registry.internal", "http://registry.internal"},
		{"http on an explicit 80", "http://registry.internal:80", "http://registry.internal:80"},
		{"explicit non-default port", "https://artifactory.example.com:8443", "https://artifactory.example.com:8443"},
		{"port and path", "https://artifactory.example.com:8443/artifactory", "https://artifactory.example.com:8443"},
		{"http scheme and path", "http://registry.internal/repo", "http://registry.internal"},
		{"userinfo on a non-default port", "https://user:s3cr3t@artifactory.example.com:8443/repo", "https://artifactory.example.com:8443"},
		{"userinfo on plain https", "https://user:s3cr3t@artifactory.example.com", ""},
		{"empty", "", ""},
		{"unparseable", "https://exa mple.com:8443", ""},
		{"hostless scheme-less userinfo", "user:s3cr3t@artifactory.example.com", ""},
		{"relative path only", "/artifactory", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := registryroutes.UpstreamOriginFor(tc.raw)
			if got != tc.want {
				t.Fatalf("UpstreamOriginFor(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if strings.Contains(got, "s3cr3t") || strings.Contains(got, "@") {
				t.Errorf("UpstreamOriginFor(%q) = %q, must never echo userinfo", tc.raw, got)
			}
			if got != "" {
				if err := registryroutes.ValidateUpstreamOrigin(got); err != nil {
					t.Errorf("UpstreamOriginFor(%q) = %q, which the key's own validator rejects: %v", tc.raw, got, err)
				}
			}
		})
	}
}

// The synthetic row's RouteDeclaration hook accepts only mirror-path, so any
// other key in its block is refused, naming the route and the key.
func TestParse_EcosystemsKeyTheHookRejectsIsError(t *testing.T) {
	_, err := parse([]byte(syntheticPathDoc(`widget = ["gizmo"]`)))
	if err == nil {
		t.Fatal("expected error for a key the row's hook rejects, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ecosystems.synthetic.widget") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// An ecosystem is unknown when it has no row among those ParseRows was given.
func TestParse_EcosystemsUnknownEcosystemNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.unlisted]
path = "/mirror"
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an unknown ecosystem name, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), `[routes.ecosystems."unlisted"]`) {
		t.Errorf("expected error to name the unknown ecosystem, got: %v", err)
	}
}

func TestParse_EcosystemsPathNotStringIsError(t *testing.T) {
	_, err := parse([]byte(syntheticPathDoc(`path = 5`)))
	if err == nil {
		t.Fatal("expected error for a non-string path, got nil")
	}
	if !strings.Contains(err.Error(), "ecosystems.synthetic.path") {
		t.Errorf("expected error to name the block spelling, got: %v", err)
	}
}

// Drives ParseRows, the seam Parse wraps, with a fake row rather
// than a real ecosystem, so the hook contract is tested without depending on
// any shipped ecosystem's rules.
func TestParseRoutes_FakeRowRouteDeclarationHookSeesBlockKey(t *testing.T) {
	var gotKey string
	var gotValue any
	row := ecosystem.Row{
		Name: "fake",
		RouteDeclaration: func(key string, value any) error {
			gotKey, gotValue = key, value
			return nil
		},
	}
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.fake]
widget = "gizmo"
`
	routes, err := registryroutes.ParseRows([]byte(doc), []ecosystem.Row{row})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotKey != "widget" || gotValue != "gizmo" {
		t.Errorf("hook received (%q, %v), want (\"widget\", \"gizmo\")", gotKey, gotValue)
	}
	want := registryvocab.RouteEcosystems{"fake": registryvocab.RouteDeclaration{"widget": "gizmo"}}
	if !reflect.DeepEqual(routes[0].Ecosystems, want) {
		t.Errorf("Ecosystems = %#v, want %#v", routes[0].Ecosystems, want)
	}
}

// A nil RouteDeclaration hook means "this row's block accepts no key beyond
// path".
func TestParseRoutes_FakeRowNilHookRejectsNonPathKey(t *testing.T) {
	row := ecosystem.Row{Name: "fake"}
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.fake]
widget = "gizmo"
`
	_, err := registryroutes.ParseRows([]byte(doc), []ecosystem.Row{row})
	if err == nil {
		t.Fatal("expected error for a key a nil-hook row doesn't accept, got nil")
	}
	if !strings.Contains(err.Error(), "widget") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// A hook error is a bare noun phrase per RouteDeclarationValidator's contract,
// so the caller must wrap it with the block's own "ecosystems.<name>.<key>"
// spelling.
func TestParseRoutes_FakeRowHookErrorIsWrappedWithBlockSpelling(t *testing.T) {
	row := ecosystem.Row{
		Name: "fake",
		RouteDeclaration: func(key string, value any) error {
			return errors.New("boom")
		},
	}
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.fake]
widget = "gizmo"
`
	_, err := registryroutes.ParseRows([]byte(doc), []ecosystem.Row{row})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ecosystems.fake.widget") {
		t.Errorf("expected error to name the block spelling, got: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected error to carry the hook's own message, got: %v", err)
	}
}

// The stanza carries the route's [routes.ecosystems.<name>] blocks too, not
// just its top-level keys: an operator who pastes back a stanza missing them
// silently loses every per-ecosystem declaration the route had. The retirement
// gate fires before any row validates a block, so a block no row accepts is
// echoed all the same.
func TestParse_RetiredKeyStanzaEchoesEcosystemBlocks(t *testing.T) {
	const doc = `
[[routes]]
upstream-base-url = "https://artifactory.example.com/artifactory"
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.synthetic]
path = "/mirror"

[routes.ecosystems.unlisted]
widgets = ["gizmo", "gadget"]
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired upstream-base-url, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"[routes.ecosystems.synthetic]",
		`path = "/mirror"`,
		"[routes.ecosystems.unlisted]",
		`widgets = ["gizmo", "gadget"]`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected stanza to contain %q, got: %v", want, err)
		}
	}
}

// Checks the promise the retirement error makes, "paste this stanza back", by
// feeding the printed stanza to Parse.
func TestParse_RetiredKeyStanzaRoundTripsThroughParse(t *testing.T) {
	const doc = `
[[routes]]
upstream-base-url = "https://artifactory.example.com:8443/artifactory"
match-host = "artifactory.example.com"
auth-scheme = "basic"
credential = { netrc = "~/.netrc" }
allow = ["/dl"]

[routes.ecosystems.synthetic]
path = "/mirror"
mirror-path = "/other"
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired upstream-base-url, got nil")
	}
	msg := err.Error()
	idx := strings.Index(msg, "[[routes]]")
	if idx < 0 {
		t.Fatalf("expected a replacement stanza in the error, got: %v", err)
	}
	stanza := msg[idx:]

	routes, err := parse([]byte(stanza))
	if err != nil {
		t.Fatalf("expected the replacement stanza to parse, got: %v\nstanza:\n%s", err, stanza)
	}
	if len(routes) != 1 {
		t.Fatalf("expected 1 route from the stanza, got %d", len(routes))
	}
	got := routes[0]
	want := registryvocab.RouteEcosystems{"synthetic": registryvocab.RouteDeclaration{"path": "/mirror", "mirror-path": "/other"}}
	if !reflect.DeepEqual(got.Ecosystems, want) {
		t.Errorf("Ecosystems = %#v, want %#v", got.Ecosystems, want)
	}
	if got.AuthScheme != "basic" || !slices.Equal(got.Allow, []string{"/dl"}) {
		t.Errorf("auth-scheme/allow = %q/%v, want basic/[/dl]", got.AuthScheme, got.Allow)
	}
	if got.UpstreamOrigin != "https://artifactory.example.com:8443" {
		t.Errorf("expected the stanza's upstream-origin to survive, got %q", got.UpstreamOrigin)
	}
}

// A block whose ecosystem name or key was written as a quoted TOML key must be
// quoted back, so the stanza an operator is told to paste still parses. The
// pasted stanza is expected to fail validation here (nothing named "bad name"
// is an ecosystem), just not to fail to parse.
func TestParse_RetiredKeyStanzaQuotesKeysThatNeedQuoting(t *testing.T) {
	const doc = `
[[routes]]
upstream-base-url = "https://artifactory.example.com/artifactory"
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems."bad name"]
"weird key" = "gizmo"
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired upstream-base-url, got nil")
	}
	msg := err.Error()
	idx := strings.Index(msg, "[[routes]]")
	if idx < 0 {
		t.Fatalf("expected a replacement stanza in the error, got: %v", err)
	}
	stanza := msg[idx:]
	if want := `[routes.ecosystems."bad name"]`; !strings.Contains(stanza, want) {
		t.Errorf("expected stanza to quote the ecosystem name (%s), got:\n%s", want, stanza)
	}
	if want := `"weird key" = "gizmo"`; !strings.Contains(stanza, want) {
		t.Errorf("expected stanza to quote the block key (%s), got:\n%s", want, stanza)
	}

	_, err = parse([]byte(stanza))
	if err != nil && strings.Contains(err.Error(), "parsing routes file") {
		t.Errorf("expected the replacement stanza to parse, got: %v\nstanza:\n%s", err, stanza)
	}
}

// Pins the full error text a retired top-level key produces, stanza included,
// byte for byte: the credential renders inline and the route's block follows
// every top-level key.
func TestParse_RetiredKeyErrorWording(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
enforce-allowlist = false
credential = { netrc = "~/.netrc" }

[routes.ecosystems.synthetic]
mirror-path = "/mirror"
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired enforce-allowlist, got nil")
	}
	const want = "registryroutes: route \"repo.example.com\": enforce-allowlist is retired (ADR 0047, issue #3261): every route is now host-rooted, and enforcement against the derived path-set is unconditional -- there is no off switch, and allow is the only recourse for a path that set misses; equivalent routes-file stanza:\n\n[[routes]]\nmatch-host = \"repo.example.com\"\ncredential = { netrc = \"~/.netrc\" }\n\n[routes.ecosystems.synthetic]\nmirror-path = \"/mirror\"\n"
	if got := err.Error(); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// An empty block, and an empty list under a block, are both blocks the
// operator wrote, so each is preserved exactly as declared rather than
// collapsing to "declared nothing". A row accepting every key stands in for a
// real one so the list shape is not at the mercy of any row's own rules.
func TestParse_EcosystemsEmptyDeclarationShapes(t *testing.T) {
	row := ecosystem.Row{
		Name:             "fake",
		RouteDeclaration: func(string, any) error { return nil },
	}
	for _, tc := range []struct {
		name  string
		block string
		want  registryvocab.RouteDeclaration
	}{
		{"block with an empty list keeps the key", "widgets = []", registryvocab.RouteDeclaration{"widgets": []any{}}},
		{"empty block keeps the ecosystem", "", registryvocab.RouteDeclaration{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.fake]
` + tc.block + "\n"
			routes, err := registryroutes.ParseRows([]byte(doc), []ecosystem.Row{row})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := registryvocab.RouteEcosystems{"fake": tc.want}
			if !reflect.DeepEqual(routes[0].Ecosystems, want) {
				t.Errorf("Ecosystems = %#v, want %#v", routes[0].Ecosystems, want)
			}
		})
	}
}

// Dropping a declared upstream-origin would print a stanza that parses clean
// but silently falls back to the origin derived from match-host, losing the
// operator's port and scheme.
func TestParse_RetiredKeyStanzaKeepsDeclaredUpstreamOrigin(t *testing.T) {
	const origin = "https://acme.example:8443"
	const doc = `
[[routes]]
match-host = "acme.example"
upstream-origin = "` + origin + `"
credential = { netrc = "~/.netrc" }
enforce-allowlist = false

[routes.ecosystems.synthetic]
path = "/mirror"
`
	_, err := parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired enforce-allowlist, got nil")
	}
	msg := err.Error()
	idx := strings.Index(msg, "[[routes]]")
	if idx < 0 {
		t.Fatalf("expected a replacement stanza in the error, got: %v", err)
	}
	stanza := msg[idx:]

	routes, err := parse([]byte(stanza))
	if err != nil {
		t.Fatalf("expected the replacement stanza to parse, got: %v\nstanza:\n%s", err, stanza)
	}
	if len(routes) != 1 {
		t.Fatalf("expected 1 route from the stanza, got %d", len(routes))
	}
	if got := routes[0].UpstreamOrigin; got != origin {
		t.Errorf("UpstreamOrigin = %q, want %q\nstanza:\n%s", got, origin, stanza)
	}
	if got := routes[0].Ecosystems.Path("synthetic"); got != "/mirror" {
		t.Errorf("Ecosystems.Path(synthetic) = %q, want %q", got, "/mirror")
	}
}

// kindRoundTripFixture supplies one credresolver.Kind's credential inline
// table (minus braces) and expected decoded values, so
// TestParse_AllKindsRoundTripThroughRetiredKeyStanza can walk
// credresolver.Kinds() without hardcoding per-kind assertions.
type kindRoundTripFixture struct {
	credentialTOML string
	wantValue      string
	wantCompanion  string
	wantExecArgv   []string
}

var kindRoundTripFixtures = map[string]kindRoundTripFixture{
	"env":               {credentialTOML: `env = "TOKEN_ENV"`, wantValue: "TOKEN_ENV"},
	"file":              {credentialTOML: `file = "/etc/cred"`, wantValue: "/etc/cred"},
	"netrc":             {credentialTOML: `netrc = "~/.netrc"`, wantValue: "~/.netrc"},
	"cargo-credentials": {credentialTOML: `cargo-credentials = "~/creds/registry.toml", registry-name = "my-registry"`, wantValue: "~/creds/registry.toml", wantCompanion: "my-registry"},
	"exec":              {credentialTOML: `exec = ["op", "read", "op://vault/item"]`, wantExecArgv: []string{"op", "read", "op://vault/item"}},
	"npmrc":             {credentialTOML: `npmrc = "~/creds/registry.rc"`, wantValue: "~/creds/registry.rc"},
	"gradle-properties": {credentialTOML: `gradle-properties = "/home/build/creds.properties", key = "tokenKey"`, wantValue: "/home/build/creds.properties", wantCompanion: "tokenKey"},
}

// Issue #3407's acceptance criterion 2: for every credresolver.Kind, the
// printed replacement names that kind's source key (and companion, where it
// has one) and re-parses to the same source value, file format and companion.
// Walking credresolver.Kinds() rather than hand-listing the seven means an
// eighth kind is covered as soon as it is added, given a fixture here too.
func TestParse_AllKindsRoundTripThroughRetiredKeyStanza(t *testing.T) {
	for _, kind := range credresolver.Kinds() {
		kind := kind
		t.Run(kind.SourceKey, func(t *testing.T) {
			fx, ok := kindRoundTripFixtures[kind.SourceKey]
			if !ok {
				t.Fatalf("no round-trip fixture for kind %q -- add one alongside the new kind", kind.SourceKey)
			}
			doc := fmt.Sprintf(`
[[routes]]
enforce-allowlist = false
match-host = "repo.example.com"
credential = { %s }
`, fx.credentialTOML)
			_, err := parse([]byte(doc))
			if err == nil {
				t.Fatal("expected error for a retired enforce-allowlist, got nil")
			}
			msg := err.Error()
			if !strings.Contains(msg, kind.SourceKey+" = ") {
				t.Errorf("expected stanza to name source key %q, got: %v", kind.SourceKey, err)
			}
			if kind.CompanionKey != "" && !strings.Contains(msg, kind.CompanionKey+" = ") {
				t.Errorf("expected stanza to name companion key %q, got: %v", kind.CompanionKey, err)
			}

			idx := strings.Index(msg, "[[routes]]")
			if idx < 0 {
				t.Fatalf("expected a replacement stanza in the error, got: %v", err)
			}
			stanza := msg[idx:]

			routes, err := parse([]byte(stanza))
			if err != nil {
				t.Fatalf("expected the replacement stanza to parse, got: %v\nstanza:\n%s", err, stanza)
			}
			if len(routes) != 1 {
				t.Fatalf("expected 1 route from the stanza, got %d", len(routes))
			}
			cred := routes[0].Credential

			if kind.ArgvValue {
				if !slices.Equal(cred.ExecArgv, fx.wantExecArgv) {
					t.Errorf("ExecArgv = %v, want %v", cred.ExecArgv, fx.wantExecArgv)
				}
			} else {
				if got := *kind.ValueField(&cred); got != fx.wantValue {
					t.Errorf("value field = %q, want %q", got, fx.wantValue)
				}
				if cred.FileFormat != kind.FileFormat {
					t.Errorf("FileFormat = %q, want %q", cred.FileFormat, kind.FileFormat)
				}
			}
			if kind.CompanionKey != "" {
				if got := *kind.CompanionField(&cred); got != fx.wantCompanion {
					t.Errorf("companion field = %q, want %q", got, fx.wantCompanion)
				}
			}
		})
	}
}

// Issue #3436: a route carrying a retired key must still validate its
// credential first. A malformed credential fails on parseCredential's own
// error before ParseRows ever reaches the retired-key check, so no
// replacement stanza is printed for it -- the stanza-echo path only ever
// sees a credential parseCredential already accepted.
func TestParse_RetiredKeyRouteWithMalformedCredentialFailsOnCredential(t *testing.T) {
	credCases := []struct {
		name          string
		credential    string
		wantErrSubstr string
		// secretValue, if set, is a credential-shaped value that must never
		// appear anywhere in the error (issue #3436).
		secretValue string
	}{
		{"unknown-only", `{ bogus = "y" }`, `credential has unknown key "bogus"`, ""},
		{"empty-table", `{}`, `credential names no source`, ""},
		{"valid-plus-unknown", `{ netrc = "/n", bogus = "y" }`, `credential has unknown key "bogus"`, ""},
		{"orphan-companion", `{ registry-name = "my-registry" }`, `credential key "registry-name" is only valid alongside "cargo-credentials"`, ""},
		{"unknown-secret-value", `{ token = "ghp_supersecret" }`, `credential has unknown key "token"`, "ghp_supersecret"},
		{"netrc-not-a-string", `{ netrc = 123 }`, `credential key "netrc" must be a string`, ""},
		{"exec-not-strings", `{ exec = [1, 2] }`, `credential key "exec" must be an array of strings`, ""},
		{"env-nan", `{ env = nan }`, `credential key "env" must be a string`, ""},
		{"netrc-inf", `{ netrc = inf }`, `credential key "netrc" must be a string`, ""},
		{"netrc-local-date", `{ netrc = 1979-05-27 }`, `credential key "netrc" must be a string`, ""},
		{"netrc-offset-datetime", `{ netrc = 1979-05-27T07:32:00Z }`, `credential key "netrc" must be a string`, ""},
		{"netrc-table", `{ netrc = { a = "b" } }`, `credential key "netrc" must be a string`, ""},
	}
	retiredCases := []struct {
		name string
		decl string
	}{
		{"adr-0047", `enforce-allowlist = false`},
		{"adr-0047-upstream-base-url", `upstream-base-url = "https://repo.example.com/x"`},
	}

	for _, cc := range credCases {
		for _, rc := range retiredCases {
			t.Run(cc.name+"/"+rc.name, func(t *testing.T) {
				doc := fmt.Sprintf(`
[[routes]]
match-host = "repo.example.com"
credential = %s
%s
`, cc.credential, rc.decl)
				_, err := parse([]byte(doc))
				if err == nil {
					t.Fatal("expected error for malformed credential, got nil")
				}
				msg := err.Error()
				if !strings.Contains(msg, cc.wantErrSubstr) {
					t.Errorf("error = %v, want substring %q", err, cc.wantErrSubstr)
				}
				if strings.Contains(msg, "[[routes]]") {
					t.Errorf("error prints a replacement stanza for a malformed credential, want parseCredential's error only, got: %v", err)
				}
				for _, key := range []string{"enforce-allowlist", "upstream-base-url"} {
					if strings.Contains(msg, key) {
						t.Errorf("error names retired key %q, want parseCredential's error to win, got: %v", key, err)
					}
				}
				if cc.secretValue != "" && strings.Contains(msg, cc.secretValue) {
					t.Errorf("error echoes secret value %q, got: %v", cc.secretValue, err)
				}
			})
		}
	}
}

// The retired-key stanza echoes upstream-origin, auth-scheme, and allow too,
// so an invalid one fails on its own error before any stanza prints (#3436).
func TestParse_RetiredKeyRouteWithInvalidFieldFailsOnField(t *testing.T) {
	fieldCases := []struct {
		name          string
		decl          string
		wantErrSubstr string
	}{
		{"upstream-origin-with-path", `upstream-origin = "https://repo.example.com/x"`, `upstream-origin`},
		{"auth-scheme-digest", `auth-scheme = "digest"`, `auth-scheme "digest" is not one of`},
		{"allow-relative", `allow = ["no-slash"]`, `allow pattern "no-slash" must be an absolute path`},
	}
	for _, fc := range fieldCases {
		t.Run(fc.name, func(t *testing.T) {
			doc := fmt.Sprintf(`
[[routes]]
match-host = "repo.example.com"
enforce-allowlist = false
%s
`, fc.decl)
			_, err := parse([]byte(doc))
			if err == nil {
				t.Fatal("expected error for invalid field, got nil")
			}
			msg := err.Error()
			if !strings.Contains(msg, fc.wantErrSubstr) {
				t.Errorf("error = %v, want substring %q", err, fc.wantErrSubstr)
			}
			if strings.Contains(msg, "[[routes]]") || strings.Contains(msg, "enforce-allowlist") {
				t.Errorf("error prints the retired-key stanza for an invalid field, want the field's own error only, got: %v", err)
			}
		})
	}
}

// kindMissingCompanionFixtures supplies, for each kind with a CompanionKey,
// a credential inline table naming the source but omitting its companion.
var kindMissingCompanionFixtures = map[string]string{
	"cargo-credentials": `cargo-credentials = "~/creds/registry.toml"`,
	"gradle-properties": `gradle-properties = "/home/build/creds.properties"`,
}

// Issue #3407's acceptance criterion 2, second half: generalizes
// TestParse_GradlePropertiesWithoutKeyIsError across credresolver.Kinds(), so
// a future companion-bearing kind is covered without a new hand-written test.
func TestParse_MissingCompanionKeyIsErrorForEveryKind(t *testing.T) {
	for _, kind := range credresolver.Kinds() {
		if kind.CompanionKey == "" {
			continue
		}
		kind := kind
		t.Run(kind.SourceKey, func(t *testing.T) {
			credTOML, ok := kindMissingCompanionFixtures[kind.SourceKey]
			if !ok {
				t.Fatalf("no missing-companion fixture for kind %q -- add one alongside the new kind", kind.SourceKey)
			}
			doc := fmt.Sprintf(`
[[routes]]
match-host = "repo.example.com"
credential = { %s }
`, credTOML)
			_, err := parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for %s without %s, got nil", kind.SourceKey, kind.CompanionKey)
			}
			msg := err.Error()
			if !strings.Contains(msg, `route "repo.example.com"`) {
				t.Errorf("expected error to name the route, got: %v", err)
			}
			if !strings.Contains(msg, kind.CompanionKey) {
				t.Errorf("expected error to name companion key %q, got: %v", kind.CompanionKey, err)
			}
		})
	}
}
