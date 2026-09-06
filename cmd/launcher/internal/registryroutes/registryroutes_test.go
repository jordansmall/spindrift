package registryroutes

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
)

// TestParse_SingleValidRouteBearerNetrc verifies that a minimal, valid routes
// file (ADR 0045) parses into one Route carrying the fields the TOML named,
// with its Credential mapped onto credresolver's netrc source.
func TestParse_SingleValidRouteBearerNetrc(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
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

// TestParse_AuthSchemeAbsentDefaultsToBearer verifies that a route with no
// auth-scheme key at all defaults to "bearer" (ADR 0045), not an empty
// string that later code would have to special-case.
func TestParse_AuthSchemeAbsentDefaultsToBearer(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
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

// TestParse_AuthSchemeUnknownValueIsError verifies that an auth-scheme
// naming neither "bearer", "basic", nor "header:<Name>" is rejected.
func TestParse_AuthSchemeUnknownValueIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
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

// TestParse_CargoRegistriesValidNamesAreParsed verifies that a route's
// optional cargo-registries array (ADR 0045) decodes onto
// Route.CargoRegistries unchanged, in file order.
func TestParse_CargoRegistriesValidNamesAreParsed(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
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

// TestParse_AllowAbsentIsNil verifies that a route with no allow key at all
// parses with a nil Route.Allow, and that omitting the field entirely
// (back-compat, ADR 0047) is not an error.
func TestParse_AllowAbsentIsNil(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].Allow != nil {
		t.Errorf("Allow = %v, want nil", routes[0].Allow)
	}
}

// TestParse_AllowValidPatternsAreParsed verifies that a route's optional
// allow array (ADR 0047, issue #3258) decodes onto Route.Allow unchanged, in
// file order, for a single-entry pattern and a multi-entry list alike. The
// fixture is a plain host-rooted route, the only shape a routes file has
// since ADR 0047 (issue #3261).
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
			routes, err := Parse([]byte(doc))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(routes[0].Allow, tc.want) {
				t.Errorf("Allow = %v, want %v", routes[0].Allow, tc.want)
			}
		})
	}
}

// TestParse_AllowInvalidPatternIsError verifies that an allow pattern not
// already in canonical subtree-root form (see validateAllowPatterns) is
// rejected, and that the error names both the offending route and the exact
// bad pattern.

func TestParse_AllowInvalidPatternIsError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
	}{
		{"no leading slash", "dl"},
		{"trailing slash", "/dl/"},
		{"traversal segment", "/dl/../etc"},
		{"doubled slash", "//dl"},
		{"root pattern", "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "artifactory.example.com"
allow = ["` + tc.pattern + `"]
credential = { netrc = "~/.netrc" }
`
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for allow pattern %q, got nil", tc.pattern)
			}
			if !strings.Contains(err.Error(), "artifactory.example.com") {
				t.Errorf("expected error to name the route, got: %v", err)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", tc.pattern)) {
				t.Errorf("expected error to name the offending pattern %q, got: %v", tc.pattern, err)
			}
		})
	}
}

// TestParse_GradlePathValidIsNormalized verifies that a valid gradle-path
// (issue #3259) decodes onto Route.GradlePath with a trailing slash
// stripped, mirroring upstream-origin's own trailing-slash normalization.
func TestParse_GradlePathValidIsNormalized(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
gradle-path = "/maven/"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/maven"; routes[0].GradlePath != want {
		t.Errorf("GradlePath = %q, want %q (trailing slash stripped)", routes[0].GradlePath, want)
	}
}

// TestParse_GradlePathAbsentIsEmpty verifies that a route omitting
// gradle-path altogether parses cleanly with Route.GradlePath left "" --
// the field is optional (ADR 0045-style back-compat).
func TestParse_GradlePathAbsentIsEmpty(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].GradlePath != "" {
		t.Errorf("GradlePath = %q, want empty (omitted)", routes[0].GradlePath)
	}
}

// TestParse_GradlePathMissingLeadingSlashIsError verifies that a
// gradle-path not starting with "/" is rejected.
func TestParse_GradlePathMissingLeadingSlashIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
gradle-path = "maven"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a gradle-path missing a leading slash, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_GradlePathWhitespaceIsError verifies that a gradle-path
// containing whitespace (leading, trailing, or embedded) is rejected.
func TestParse_GradlePathWhitespaceIsError(t *testing.T) {
	for _, path := range []string{" /maven", "/maven ", "/mav en"} {
		t.Run(path, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "repo.example.com"
gradle-path = "` + path + `"
credential = { netrc = "~/.netrc" }
`
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for gradle-path %q with whitespace, got nil", path)
			}
		})
	}
}

// TestParse_GradlePathDotDotSegmentIsError verifies that a gradle-path
// containing a ".." segment is rejected as basic hygiene against a
// malformed declaration.
func TestParse_GradlePathDotDotSegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
gradle-path = "/maven/../etc"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a gradle-path with a \"..\" segment, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_GradlePathDotSegmentIsError verifies that a gradle-path
// containing a "." segment is rejected -- path.Clean-based consumers
// downstream can never produce or match such a value.
func TestParse_GradlePathDotSegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
gradle-path = "/maven/./release"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a gradle-path with a \".\" segment, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_GradlePathEmptySegmentIsError verifies that a gradle-path
// containing an interior doubled slash (an empty segment) is rejected --
// path.Clean-based consumers downstream can never produce or match such a
// value, and the trailing-slash case alone is already covered by
// TestParse_GradlePathTrailingDoubleSlashIsNormalized.
func TestParse_GradlePathEmptySegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
gradle-path = "/maven//release"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a gradle-path with an interior doubled slash, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_GradlePathShellMetacharacterIsError verifies that a gradle-path
// containing "$" or "`" is rejected: gradle-path is operator-declared but
// ultimately flows into gradleRedirectScript's Groovy double-quoted string
// literal (ecosystem.GradleInitScript), where an unescaped "$" triggers
// Groovy's GString interpolation at init-script load time. Both cases splice
// tc.path into a TOML basic (double-quoted) string, so the path itself must
// avoid TOML's own escape syntax -- the "\" case is exercised separately in
// TestParse_GradlePathBackslashIsError via a TOML literal string instead.
func TestParse_GradlePathShellMetacharacterIsError(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"dollar sign", "/maven/$HOME"},
		{"backtick", "/maven/`whoami`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "repo.example.com"
gradle-path = "` + tc.path + `"
credential = { netrc = "~/.netrc" }
`
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for gradle-path %q, got nil", tc.path)
			}
			if !strings.Contains(err.Error(), "repo.example.com") {
				t.Errorf("expected error to name the route, got: %v", err)
			}
		})
	}
}

// TestParse_GradlePathBackslashIsError verifies that a gradle-path
// containing "\" is rejected, the same as "$" and "`" above. It uses a TOML
// literal (single-quoted) string so the backslash reaches Parse unescaped,
// rather than being consumed as a TOML basic-string escape sequence.
func TestParse_GradlePathBackslashIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
gradle-path = '/maven/\release'
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a gradle-path containing \"\\\", got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_GradlePathBareRootIsError verifies that gradle-path = "/" is
// rejected: gradle-path only ever adds a subtree on top of an
// already-resolved host-rooted route, so "the whole host" needs no special
// field and declaring it is an error naming that limitation explicitly.
func TestParse_GradlePathBareRootIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
gradle-path = "/"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for gradle-path = \"/\", got nil")
	}
	if !strings.Contains(err.Error(), "whole host") {
		t.Errorf("expected error to explain the whole-host limitation, got: %v", err)
	}
}

// TestParse_GradlePathDoubleSlashWholeHostIsError verifies that gradle-path
// = "//" is rejected the same way as "/": TrimSuffix only strips one
// trailing slash, so a naive normalization would leave "/" -- a
// specific-looking path that is really the same rejected whole-host value
// -- rather than collapsing to "" and hitting the bare-root check.
func TestParse_GradlePathDoubleSlashWholeHostIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
gradle-path = "//"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for gradle-path = \"//\", got nil")
	}
	if !strings.Contains(err.Error(), "whole host") {
		t.Errorf("expected error to explain the whole-host limitation, got: %v", err)
	}
}

// TestParse_GradlePathTrailingDoubleSlashIsNormalized verifies that
// gradle-path = "/foo//" normalizes all the way down to "/foo" -- not the
// "/foo/" a single TrimSuffix leaves behind, which would render a
// double-slash init-script URL that strict Maven registries 404 on.
func TestParse_GradlePathTrailingDoubleSlashIsNormalized(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
gradle-path = "/foo//"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/foo"; routes[0].GradlePath != want {
		t.Errorf("GradlePath = %q, want %q (all trailing slashes stripped)", routes[0].GradlePath, want)
	}
}

// TestParse_GoPathValidIsNormalized verifies that a valid go-path (issue
// #3260) decodes onto Route.GoPath with a trailing slash stripped,
// mirroring gradle-path's own trailing-slash normalization.
func TestParse_GoPathValidIsNormalized(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
go-path = "/go/"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/go"; routes[0].GoPath != want {
		t.Errorf("GoPath = %q, want %q (trailing slash stripped)", routes[0].GoPath, want)
	}
}

// TestParse_GoPathAbsentIsEmpty verifies that a route omitting go-path
// altogether parses cleanly with Route.GoPath left "" -- the field is
// optional (ADR 0045-style back-compat).
func TestParse_GoPathAbsentIsEmpty(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].GoPath != "" {
		t.Errorf("GoPath = %q, want empty (omitted)", routes[0].GoPath)
	}
}

// TestParse_GoPathMissingLeadingSlashIsError verifies that a go-path not
// starting with "/" is rejected.
func TestParse_GoPathMissingLeadingSlashIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
go-path = "go"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a go-path missing a leading slash, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_GoPathWhitespaceIsError verifies that a go-path containing
// whitespace (leading, trailing, or embedded) is rejected.
func TestParse_GoPathWhitespaceIsError(t *testing.T) {
	for _, path := range []string{" /go", "/go ", "/g o"} {
		t.Run(path, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "repo.example.com"
go-path = "` + path + `"
credential = { netrc = "~/.netrc" }
`
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for go-path %q with whitespace, got nil", path)
			}
		})
	}
}

// TestParse_GoPathDotDotSegmentIsError verifies that a go-path containing a
// ".." segment is rejected as basic hygiene against a malformed declaration.
func TestParse_GoPathDotDotSegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
go-path = "/go/../etc"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a go-path with a \"..\" segment, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_GoPathDotSegmentIsError verifies that a go-path containing a
// "." segment is rejected -- path.Clean-based consumers downstream can
// never produce or match such a value.
func TestParse_GoPathDotSegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
go-path = "/go/./release"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a go-path with a \".\" segment, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_GoPathEmptySegmentIsError verifies that a go-path containing an
// interior doubled slash (an empty segment) is rejected -- path.Clean-based
// consumers downstream can never produce or match such a value, and the
// trailing-slash case alone is already covered by
// TestParse_GoPathTrailingDoubleSlashIsNormalized.
func TestParse_GoPathEmptySegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
go-path = "/go//release"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a go-path with an interior doubled slash, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_GoPathShellMetacharacterIsError verifies that a go-path
// containing "$" or "`" is rejected: go-path is operator-declared but
// ultimately flows into a shell-sourced "export GOPROXY='<value>'" line
// (bindregistry_cmd.go, registrymanifest.go) -- a GOPROXY URL path has no
// legitimate use for those bytes, and the same ban keeps it and gradle-path
// from drifting via validateDeclaredPath.
func TestParse_GoPathShellMetacharacterIsError(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"dollar sign", "/go/$HOME"},
		{"backtick", "/go/`whoami`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "repo.example.com"
go-path = "` + tc.path + `"
credential = { netrc = "~/.netrc" }
`
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for go-path %q, got nil", tc.path)
			}
			if !strings.Contains(err.Error(), "repo.example.com") {
				t.Errorf("expected error to name the route, got: %v", err)
			}
		})
	}
}

// TestParse_GoPathBackslashIsError verifies that a go-path containing "\"
// is rejected, the same as "$" and "`" above. It uses a TOML literal
// (single-quoted) string so the backslash reaches Parse unescaped, rather
// than being consumed as a TOML basic-string escape sequence.
func TestParse_GoPathBackslashIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
go-path = '/go/\release'
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a go-path containing \"\\\", got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// TestParse_GoPathBareRootIsError verifies that go-path = "/" is rejected:
// go-path only ever adds a subtree on top of an already-resolved
// host-rooted route, so "the whole host" needs no special field and
// declaring it is an error naming that limitation explicitly.
func TestParse_GoPathBareRootIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
go-path = "/"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for go-path = \"/\", got nil")
	}
	if !strings.Contains(err.Error(), "whole host") {
		t.Errorf("expected error to explain the whole-host limitation, got: %v", err)
	}
}

// TestParse_GoPathDoubleSlashWholeHostIsError verifies that go-path = "//"
// is rejected the same way as "/": TrimSuffix only strips one trailing
// slash, so a naive normalization would leave "/" -- a specific-looking
// path that is really the same rejected whole-host value -- rather than
// collapsing to "" and hitting the bare-root check.
func TestParse_GoPathDoubleSlashWholeHostIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
go-path = "//"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for go-path = \"//\", got nil")
	}
	if !strings.Contains(err.Error(), "whole host") {
		t.Errorf("expected error to explain the whole-host limitation, got: %v", err)
	}
}

// TestParse_GoPathTrailingDoubleSlashIsNormalized verifies that go-path =
// "/foo//" normalizes all the way down to "/foo" -- not the "/foo/" a
// single TrimSuffix leaves behind, which would render a double-slash GOPROXY
// URL that some proxies 404 on.
func TestParse_GoPathTrailingDoubleSlashIsNormalized(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
go-path = "/foo//"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/foo"; routes[0].GoPath != want {
		t.Errorf("GoPath = %q, want %q (all trailing slashes stripped)", routes[0].GoPath, want)
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
credential = { netrc = "~/.netrc" }

[[routes]]
match-host = "artifactory.example.com"
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
// are rejected as duplicates -- the proxy's own route selection
// (registryvocab.HostKey) lowercases and strips the port before comparing, so
// "H.Example", "h.example:443", and "h.example" all collapse onto the same
// key at request time; letting the raw-string check here accept the file
// would silently shadow the file's second and third routes with the first.
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
credential = { netrc = "~/.netrc" }

[[routes]]
match-host = "[::1]:443"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for match-hosts that collapse to the same bracketed IPv6 host, got nil")
	}
}

// TestParse_CredentialWithNoSourceIsErrorNamingRoute verifies that a
// present-but-empty credential inline table (credential = {}) is rejected --
// an operator who wrote the table meant to configure something, unlike
// TestParse_CredentialKeyAbsentIsUnauthenticated below, where the key is
// missing altogether.
func TestParse_CredentialWithNoSourceIsErrorNamingRoute(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
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

// TestParse_CredentialKeyAbsentIsUnauthenticated verifies that a route
// omitting the credential key altogether parses successfully with a zero
// Credential -- an unauthenticated pass-through route (ADR 0045), distinct
// from the present-but-empty credential = {} case above, which still
// errors.
func TestParse_CredentialKeyAbsentIsUnauthenticated(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
`
	routes, err := Parse([]byte(doc))
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

// TestParse_CredentialWithMultipleSourcesIsErrorNamingThem verifies that a
// credential naming two sources at once is rejected and that the error
// names both offending keys, not just the fact that there's a problem.
func TestParse_CredentialWithMultipleSourcesIsErrorNamingThem(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
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
credential = { env = "REGISTRY_TOKEN" }
`,
		"file": `
[[routes]]
match-host = "artifactory.example.com"
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

// TestParse_RetiredUpstreamBaseURLIsError verifies that a routes file still
// declaring upstream-base-url is rejected with the retired-key remedy (ADR
// 0047, issue #3261): the error names the key, the route, and the migration,
// and prints a replacement [[routes]] stanza that itself parses -- i.e. one
// carrying neither retired key.
func TestParse_RetiredUpstreamBaseURLIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
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

// TestParse_RetiredEnforceAllowlistFalseIsError verifies that detection is by
// presence, not truthiness: an explicit enforce-allowlist = false is as
// retired as a true one (ADR 0047, issue #3261), since enforcement is now
// unconditional and allow is the only recourse.
func TestParse_RetiredEnforceAllowlistFalseIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
enforce-allowlist = false
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
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

// TestParse_RetiredKeysBothDeclaredNamesBoth verifies that a route declaring
// both retired keys is reported once, naming both keys rather than stopping
// at the first.
func TestParse_RetiredKeysBothDeclaredNamesBoth(t *testing.T) {
	const doc = `
[[routes]]
upstream-base-url = "https://artifactory.example.com/artifactory"
match-host = "artifactory.example.com"
enforce-allowlist = true
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for both retired keys, got nil")
	}
	if !strings.Contains(err.Error(), "upstream-base-url") || !strings.Contains(err.Error(), "enforce-allowlist") {
		t.Errorf("expected error to name both retired keys, got: %v", err)
	}
}

// TestParse_RetiredUpstreamBaseURLStanzaEchoesDeclaredKeys verifies that the
// replacement stanza is built from the offending route's own remaining keys,
// not a generic template: auth-scheme, cargo-registries, allow, gradle-path,
// and go-path all survive into it.
func TestParse_RetiredUpstreamBaseURLStanzaEchoesDeclaredKeys(t *testing.T) {
	const doc = `
[[routes]]
upstream-base-url = "https://artifactory.example.com/artifactory"
match-host = "artifactory.example.com"
auth-scheme = "basic"
credential = { cargo-credentials = "~/.cargo/credentials.toml", registry-name = "artifactory" }
cargo-registries = ["artifactory"]
allow = ["/dl"]
gradle-path = "/maven"
go-path = "/go"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired upstream-base-url, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		`auth-scheme = "basic"`,
		`credential = { cargo-credentials = "~/.cargo/credentials.toml", registry-name = "artifactory" }`,
		`cargo-registries = ["artifactory"]`,
		`allow = ["/dl"]`,
		`gradle-path = "/maven"`,
		`go-path = "/go"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected stanza to contain %q, got: %v", want, err)
		}
	}
}

// TestParse_RetiredUpstreamBaseURLStanzaOriginOnlyWhenNonDefault verifies
// that the replacement stanza carries upstream-origin only when the retired
// URL said something a committed config can't: a non-default scheme or an
// explicit port. A plain https URL on the default port adds nothing the
// match-host doesn't already say, so no upstream-origin line is printed --
// and when one is, it is the origin alone, never the retired URL's path.
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
			_, err := Parse([]byte(doc))
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

// TestParse_RetiredUpstreamBaseURLDoesNotEchoUserinfo verifies that the
// retirement error never echoes a credential embedded in the retired URL:
// this error reaches stderr and CI logs.
func TestParse_RetiredUpstreamBaseURLDoesNotEchoUserinfo(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://user:s3cr3t@artifactory.example.com:8443/artifactory"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired upstream-base-url, got nil")
	}
	if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "user:") {
		t.Errorf("error must not echo the userinfo embedded in the retired URL, got: %v", err)
	}
}

// TestParse_UpstreamOriginAccepted verifies that the optional
// upstream-origin key (ADR 0047, issue #3261) decodes onto
// Route.UpstreamOrigin, normalized with any trailing "/" stripped, and that
// a route omitting it stores "".
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
			routes, err := Parse([]byte(doc))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if routes[0].UpstreamOrigin != tc.want {
				t.Errorf("UpstreamOrigin = %q, want %q", routes[0].UpstreamOrigin, tc.want)
			}
		})
	}
}

// TestParse_UpstreamOriginInvalidIsError verifies that upstream-origin is an
// origin, not a URL: a path (or query, or fragment) is rejected, as are
// userinfo, a relative or non-http(s) URL, and an empty host. Every such
// error names both the offending route and the key.
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
			_, err := Parse([]byte(doc))
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

// TestParse_UpstreamOriginWithUserinfoDoesNotEcho verifies that an
// upstream-origin carrying userinfo is rejected without echoing the
// credential back into the error.
func TestParse_UpstreamOriginWithUserinfoDoesNotEcho(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-origin = "https://user:s3cr3t@artifactory.example.com"
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for upstream-origin with userinfo, got nil")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("error must not echo userinfo, got: %v", err)
	}
}

// TestParse_MinimalHostRootedRouteParses verifies that the post-retirement
// minimal route -- match-host plus a credential, no upstream key at all --
// parses, and that allow, gradle-path, and go-path now coexist with it
// freely: every route is host-rooted, so the legacy-route rejections those
// three keys used to hit are gone (ADR 0047, issue #3261).
func TestParse_MinimalHostRootedRouteParses(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
allow = ["/dl"]
gradle-path = "/maven"
go-path = "/go"
`
	routes, err := Parse([]byte(doc))
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
	if r.GradlePath != "/maven" || r.GoPath != "/go" {
		t.Errorf("GradlePath = %q, GoPath = %q, want /maven and /go", r.GradlePath, r.GoPath)
	}
	if want := "https://artifactory.example.com"; r.Credential.UpstreamURL != want {
		t.Errorf("Credential.UpstreamURL = %q, want %q", r.Credential.UpstreamURL, want)
	}
}

// TestParse_UpstreamOriginFeedsCredentialUpstreamURL verifies that a
// declared upstream-origin, not the "https://" + match-host stand-in, is
// what a route's credential carries as its UpstreamURL -- the netrc source
// keys its machine-name match on that value.
func TestParse_UpstreamOriginFeedsCredentialUpstreamURL(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
upstream-origin = "http://artifactory.example.com:8081"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "http://artifactory.example.com:8081"; routes[0].Credential.UpstreamURL != want {
		t.Errorf("Credential.UpstreamURL = %q, want %q", routes[0].Credential.UpstreamURL, want)
	}
}

// TestUpstreamOriginFor covers the single rule three call sites share -- the
// migration stanza Parse prints, the retired-scalar-knob stanza the launch
// gate prints, and what "spindrift registry discover" writes -- so a remedy
// telling an operator what to write can never disagree with the generator
// that writes it for them.
func TestUpstreamOriginFor(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"plain https on the default port", "https://artifactory.example.com", ""},
		{"plain https with a path", "https://artifactory.example.com/artifactory/api/npm/npm-remote", ""},
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
			got := UpstreamOriginFor(tc.raw)
			if got != tc.want {
				t.Fatalf("UpstreamOriginFor(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if strings.Contains(got, "s3cr3t") || strings.Contains(got, "@") {
				t.Errorf("UpstreamOriginFor(%q) = %q, must never echo userinfo", tc.raw, got)
			}
			if got != "" {
				if err := ValidateUpstreamOrigin(got); err != nil {
					t.Errorf("UpstreamOriginFor(%q) = %q, which the key's own validator rejects: %v", tc.raw, got, err)
				}
			}
		})
	}
}

// TestParse_EcosystemsBlockPathIsNormalized verifies that a
// [routes.ecosystems.<name>] block's "path" key is validated by the same
// canonical-path rules gradle-path and go-path always used, and that the
// normalized value both lands in Route.Ecosystems and is read back onto the
// legacy GradlePath field (issue #3403).
func TestParse_EcosystemsBlockPathIsNormalized(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
path = "/maven/"
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/maven"; routes[0].Ecosystems.Path("gradle") != want {
		t.Errorf("Ecosystems.Path(gradle) = %q, want %q", routes[0].Ecosystems.Path("gradle"), want)
	}
	if want := "/maven"; routes[0].GradlePath != want {
		t.Errorf("GradlePath = %q, want %q", routes[0].GradlePath, want)
	}
}

// TestParse_EcosystemsCargoRegistriesUnderCargoParses verifies that
// [routes.ecosystems.cargo]'s "registries" key is validated by cargo's own
// RouteDeclaration hook and read back onto the legacy CargoRegistries field.
func TestParse_EcosystemsCargoRegistriesUnderCargoParses(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.cargo]
registries = ["internal", "crates-remote"]
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"internal", "crates-remote"}
	if !reflect.DeepEqual(routes[0].CargoRegistries, want) {
		t.Errorf("CargoRegistries = %v, want %v", routes[0].CargoRegistries, want)
	}
}

// TestParse_EcosystemsRegistriesUnderNonCargoIsError verifies that
// "registries" -- cargo's own key -- is rejected under a different
// ecosystem's block, naming the route and the offending key: a nil
// RouteDeclaration hook (gradle's) accepts no key beyond "path" at all.
func TestParse_EcosystemsRegistriesUnderNonCargoIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
registries = ["internal"]
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for registries under a non-cargo block, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), "registries") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// TestParse_EcosystemsUnknownEcosystemNameIsError verifies that a
// [routes.ecosystems.<name>] block naming an ecosystem with no
// ecosystem.Table row is rejected, naming the route and the unknown name.
func TestParse_EcosystemsUnknownEcosystemNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.maven]
path = "/maven"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an unknown ecosystem name, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), `[routes.ecosystems."maven"]`) {
		t.Errorf("expected error to name the unknown ecosystem, got: %v", err)
	}
}

// TestParse_EcosystemsPathNotStringIsError verifies that a block's "path"
// key with a non-string value is rejected, naming the route and the block
// spelling "ecosystems.<name>.path".
func TestParse_EcosystemsPathNotStringIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = 5
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a non-string path, got nil")
	}
	if !strings.Contains(err.Error(), "ecosystems.go.path") {
		t.Errorf("expected error to name the block spelling, got: %v", err)
	}
}

// TestParse_EcosystemsLegacyAndBlockBothDeclaredIsError verifies that a
// route naming the same ecosystem both via a retired top-level key and via
// its [routes.ecosystems.<name>] block is rejected, naming the route, the
// retired key, and the block -- there is no rule for merging the two. One
// case per retired key: all three take the same translation path, and a
// regression that dropped the check for only one of them would otherwise let
// that ecosystem's block silently win.
func TestParse_EcosystemsLegacyAndBlockBothDeclaredIsError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		doc       string
		legacyKey string
		block     string
	}{
		{
			name: "go-path",
			doc: `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
go-path = "/go"

[routes.ecosystems.go]
path = "/other"
`,
			legacyKey: "go-path",
			block:     "ecosystems.go",
		},
		{
			name: "gradle-path",
			doc: `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
gradle-path = "/maven"

[routes.ecosystems.gradle]
path = "/other"
`,
			legacyKey: "gradle-path",
			block:     "ecosystems.gradle",
		},
		{
			name: "cargo-registries",
			doc: `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
cargo-registries = ["a"]

[routes.ecosystems.cargo]
registries = ["b"]
`,
			legacyKey: "cargo-registries",
			block:     "ecosystems.cargo",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil {
				t.Fatalf("expected error for a route declaring both %s and [routes.%s], got nil", tc.legacyKey, tc.block)
			}
			if !strings.Contains(err.Error(), tc.legacyKey) {
				t.Errorf("expected error to name the retired key, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.block) {
				t.Errorf("expected error to name the block, got: %v", err)
			}
		})
	}
}

// TestParse_EcosystemsOldAndNewStyleProduceIdenticalBlock verifies that a
// routes file using the three retired top-level keys (cargo-registries,
// gradle-path, go-path) and an equivalent file using
// [routes.ecosystems.<name>] blocks produce byte-for-byte identical
// Route.Ecosystems values (issue #3403's back-compat requirement) -- an
// operator migrating from one spelling to the other changes nothing a
// downstream consumer (the manifest, the resolver) can observe.
func TestParse_EcosystemsOldAndNewStyleProduceIdenticalBlock(t *testing.T) {
	const oldStyle = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
cargo-registries = ["internal", "crates-remote"]
gradle-path = "/maven/"
go-path = "/go/"
`
	const newStyle = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.cargo]
registries = ["internal", "crates-remote"]

[routes.ecosystems.gradle]
path = "/maven/"

[routes.ecosystems.go]
path = "/go/"
`
	oldRoutes, err := Parse([]byte(oldStyle))
	if err != nil {
		t.Fatalf("unexpected error parsing old-style doc: %v", err)
	}
	newRoutes, err := Parse([]byte(newStyle))
	if err != nil {
		t.Fatalf("unexpected error parsing new-style doc: %v", err)
	}
	if !reflect.DeepEqual(oldRoutes[0].Ecosystems, newRoutes[0].Ecosystems) {
		t.Errorf("Ecosystems differ:\nold = %#v\nnew = %#v", oldRoutes[0].Ecosystems, newRoutes[0].Ecosystems)
	}
}

// TestParseRoutes_FakeRowRouteDeclarationHookSeesBlockKey drives parseRoutes
// -- the internal seam Parse wraps -- with a fake row rather than a real
// ecosystem, verifying the hook is called with exactly the key and value
// the block named, and that the block itself lands unchanged in
// Route.Ecosystems.
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
	routes, err := parseRoutes([]byte(doc), []ecosystem.Row{row})
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

// TestParseRoutes_FakeRowNilHookRejectsNonPathKey verifies that a row with a
// nil RouteDeclaration hook -- "this row's block accepts no key beyond
// path" -- rejects any other key, naming it in the error.
func TestParseRoutes_FakeRowNilHookRejectsNonPathKey(t *testing.T) {
	row := ecosystem.Row{Name: "fake"}
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.fake]
widget = "gizmo"
`
	_, err := parseRoutes([]byte(doc), []ecosystem.Row{row})
	if err == nil {
		t.Fatal("expected error for a key a nil-hook row doesn't accept, got nil")
	}
	if !strings.Contains(err.Error(), "widget") {
		t.Errorf("expected error to name the offending key, got: %v", err)
	}
}

// TestParseRoutes_FakeRowHookErrorIsWrappedWithBlockSpelling verifies that a
// hook error -- a bare noun-phrase per RouteDeclarationValidator's contract
// -- is wrapped with the block's own "ecosystems.<name>.<key>" spelling,
// not the hook's own words alone.
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
	_, err := parseRoutes([]byte(doc), []ecosystem.Row{row})
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

// TestParseRoutes_FakeRowsWithoutCargoRejectLegacyCargoRegistries pins that a
// retired top-level key whose ecosystem is missing from rows is rejected by
// the nil-hook path, naming the key the operator wrote, rather than panicking
// or being accepted unchecked.
func TestParseRoutes_FakeRowsWithoutCargoRejectLegacyCargoRegistries(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
cargo-registries = ["a"]
`
	_, err := parseRoutes([]byte(doc), []ecosystem.Row{{Name: "fake"}})
	if err == nil {
		t.Fatal("expected error for a legacy key whose row is absent, got nil")
	}
	if !strings.Contains(err.Error(), "cargo-registries") {
		t.Errorf("expected error to name the key the operator wrote, got: %v", err)
	}
	if !strings.Contains(err.Error(), "is not a key cargo's route declaration accepts") {
		t.Errorf("expected the nil-hook rejection naming cargo, got: %v", err)
	}
}

// TestParse_RetiredKeyStanzaEchoesEcosystemBlocks verifies that the
// replacement stanza carries the route's [routes.ecosystems.<name>] blocks
// too, not just its top-level keys: an operator who pastes back a stanza
// missing them silently loses every per-ecosystem declaration the route had.
func TestParse_RetiredKeyStanzaEchoesEcosystemBlocks(t *testing.T) {
	const doc = `
[[routes]]
upstream-base-url = "https://artifactory.example.com/artifactory"
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = "/go-modules"

[routes.ecosystems.cargo]
registries = ["internal"]
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired upstream-base-url, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"[routes.ecosystems.cargo]",
		`registries = ["internal"]`,
		"[routes.ecosystems.go]",
		`path = "/go-modules"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected stanza to contain %q, got: %v", want, err)
		}
	}
}

// TestParse_RetiredKeyStanzaRoundTripsThroughParse verifies the promise the
// retirement error actually makes -- "paste this stanza back" -- by feeding
// the printed stanza to Parse and checking the ecosystem declarations
// survive the round trip intact.
func TestParse_RetiredKeyStanzaRoundTripsThroughParse(t *testing.T) {
	const doc = `
[[routes]]
upstream-base-url = "https://artifactory.example.com:8443/artifactory"
match-host = "artifactory.example.com"
auth-scheme = "basic"
credential = { netrc = "~/.netrc" }
allow = ["/dl"]

[routes.ecosystems.go]
path = "/go-modules"

[routes.ecosystems.cargo]
registries = ["internal", "crates-remote"]
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired upstream-base-url, got nil")
	}
	msg := err.Error()
	idx := strings.Index(msg, "[[routes]]")
	if idx < 0 {
		t.Fatalf("expected a replacement stanza in the error, got: %v", err)
	}
	stanza := msg[idx:]

	routes, err := Parse([]byte(stanza))
	if err != nil {
		t.Fatalf("expected the replacement stanza to parse, got: %v\nstanza:\n%s", err, stanza)
	}
	if len(routes) != 1 {
		t.Fatalf("expected 1 route from the stanza, got %d", len(routes))
	}
	got := routes[0]
	if got.Ecosystems.Path("go") != "/go-modules" {
		t.Errorf("expected go path %q, got %q", "/go-modules", got.Ecosystems.Path("go"))
	}
	if want := []string{"internal", "crates-remote"}; !slices.Equal(got.Ecosystems.Strings("cargo", "registries"), want) {
		t.Errorf("expected cargo registries %v, got %v", want, got.Ecosystems.Strings("cargo", "registries"))
	}
	if got.UpstreamOrigin != "https://artifactory.example.com:8443" {
		t.Errorf("expected the stanza's upstream-origin to survive, got %q", got.UpstreamOrigin)
	}
}

// TestParse_RetiredKeyStanzaEchoesLegacyKeyAndBlockTogether verifies that a
// route mixing spellings -- a legacy top-level per-ecosystem key for one
// ecosystem, a block for another -- keeps both in the replacement stanza,
// and that the blocks are emitted after every top-level key, as TOML
// requires of a sub-table inside a [[routes]] entry.
func TestParse_RetiredKeyStanzaEchoesLegacyKeyAndBlockTogether(t *testing.T) {
	const doc = `
[[routes]]
enforce-allowlist = false
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
gradle-path = "/maven"

[routes.ecosystems.cargo]
registries = ["internal"]
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired enforce-allowlist, got nil")
	}
	msg := err.Error()
	gradle := strings.Index(msg, `gradle-path = "/maven"`)
	block := strings.Index(msg, "[routes.ecosystems.cargo]")
	if gradle < 0 {
		t.Errorf("expected stanza to keep the legacy gradle-path, got: %v", err)
	}
	if block < 0 {
		t.Errorf("expected stanza to keep the cargo block, got: %v", err)
	}
	if gradle >= 0 && block >= 0 && block < gradle {
		t.Errorf("expected the block after every top-level key, got: %v", err)
	}

	idx := strings.Index(msg, "[[routes]]")
	routes, err := Parse([]byte(msg[idx:]))
	if err != nil {
		t.Fatalf("expected the replacement stanza to parse, got: %v", err)
	}
	if routes[0].Ecosystems.Path("gradle") != "/maven" {
		t.Errorf("expected gradle path %q, got %q", "/maven", routes[0].Ecosystems.Path("gradle"))
	}
	if want := []string{"internal"}; !slices.Equal(routes[0].Ecosystems.Strings("cargo", "registries"), want) {
		t.Errorf("expected cargo registries %v, got %v", want, routes[0].Ecosystems.Strings("cargo", "registries"))
	}
}

// TestParse_RetiredKeyStanzaQuotesKeysThatNeedQuoting verifies that a stanza
// echoing a block whose ecosystem name or key was written as a quoted TOML
// key quotes it back, so the stanza an operator is told to paste back still
// parses. The pasted stanza is expected to fail validation here (nothing
// named "bad name" is an ecosystem), just not to fail to parse.
func TestParse_RetiredKeyStanzaQuotesKeysThatNeedQuoting(t *testing.T) {
	const doc = `
[[routes]]
upstream-base-url = "https://artifactory.example.com/artifactory"
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems."bad name"]
"weird key" = "gizmo"
`
	_, err := Parse([]byte(doc))
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

	_, err = Parse([]byte(stanza))
	if err != nil && strings.Contains(err.Error(), "parsing routes file") {
		t.Errorf("expected the replacement stanza to parse, got: %v\nstanza:\n%s", err, stanza)
	}
}

// TestParse_EcosystemsLegacyKeyErrorWording pins the full error text a
// retired top-level key's failed row-hook check produces: the hook returns a
// bare verb clause, so the route, the operator's own spelling of the key,
// and that clause have to read as one sentence.
func TestParse_EcosystemsLegacyKeyErrorWording(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
cargo-registries = [""]
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an empty cargo-registries name, got nil")
	}
	const want = `registryroutes: route "crates.example.com": cargo-registries names an empty string`
	if got := err.Error(); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// TestParse_EcosystemsEmptyDeclarationShapes pins what an empty declaration
// parses to in each of the three spellings. The old/new "identical block"
// equivalence
// (TestParse_EcosystemsOldAndNewStyleProduceIdenticalBlock) holds for a
// non-empty declaration; an empty one is preserved exactly as declared
// instead -- a retired key with an empty list declares nothing at all, while
// an empty block or an empty list under a block is a block the operator
// wrote.
func TestParse_EcosystemsEmptyDeclarationShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		want registryvocab.RouteEcosystems
	}{
		{
			name: "legacy empty list declares nothing",
			doc: `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
cargo-registries = []
`,
			want: nil,
		},
		{
			name: "block with an empty list keeps the key",
			doc: `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.cargo]
registries = []
`,
			want: registryvocab.RouteEcosystems{"cargo": registryvocab.RouteDeclaration{"registries": []any{}}},
		},
		{
			name: "empty block keeps the ecosystem",
			doc: `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
`,
			want: registryvocab.RouteEcosystems{"go": registryvocab.RouteDeclaration{}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routes, err := Parse([]byte(tc.doc))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(routes[0].Ecosystems, tc.want) {
				t.Errorf("Ecosystems = %#v, want %#v", routes[0].Ecosystems, tc.want)
			}
		})
	}
}
