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

// Pins the minimal valid routes file of ADR 0045 and its mapping onto
// credresolver's netrc source.
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

// ADR 0045 defaults a missing auth-scheme to "bearer", not to an empty string
// that later code would have to special-case.
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

// The only valid schemes are "bearer", "basic", and "header:<Name>".
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
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a header: auth-scheme with an invalid header name, got nil")
	}
	if !strings.Contains(err.Error(), "artifactory.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// The optional [routes.ecosystems.cargo] registries key (ADR 0048, issue
// #3405) decodes unchanged, in file order.
func TestParse_CargoBlockRegistriesValidNamesAreParsed(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.cargo]
registries = ["example-remote", "another_one", "third-3"]
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"example-remote", "another_one", "third-3"}
	if !reflect.DeepEqual(routes[0].Ecosystems.Strings("cargo", "registries"), want) {
		t.Errorf("Ecosystems.Strings(cargo, registries) = %v, want %v", routes[0].Ecosystems.Strings("cargo", "registries"), want)
	}
}

// The [routes.ecosystems.cargo] block is optional.
func TestParse_CargoBlockRegistriesAbsentIsNil(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].Ecosystems.Strings("cargo", "registries") != nil {
		t.Errorf("Ecosystems.Strings(cargo, registries) = %v, want nil", routes[0].Ecosystems.Strings("cargo", "registries"))
	}
}

// An empty registry name would flow into a malformed CARGO_REGISTRIES__TOKEN
// env var name.
func TestParse_CargoBlockRegistriesEmptyNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.cargo]
registries = [""]
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an empty cargo-registries name, got nil")
	}
	if !strings.Contains(err.Error(), "crates.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ecosystems.cargo.registries") {
		t.Errorf("expected error to name the offending field, got: %v", err)
	}
}

// Names outside cargo's bare-key charset ([A-Za-z0-9_-]) flow into a
// CARGO_REGISTRIES_<NAME>_TOKEN shell env var name, so a name like "evil; rm"
// could otherwise smuggle shell metadata into a sourced env file.
func TestParse_CargoBlockRegistriesInvalidCharsIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.cargo]
registries = ["evil; rm"]
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for an invalid cargo-registries name, got nil")
	}
	if !strings.Contains(err.Error(), "evil; rm") {
		t.Errorf("expected error to name the offending value, got: %v", err)
	}
}

func TestParse_CargoBlockRegistriesDuplicateNameIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.cargo]
registries = ["example-remote", "example-remote"]
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a duplicate cargo-registries name, got nil")
	}
	if !strings.Contains(err.Error(), "example-remote") {
		t.Errorf("expected error to name the duplicated value, got: %v", err)
	}
}

// Omitting allow entirely stays valid for back-compat (ADR 0047).
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

// An allow pattern must already be in the canonical subtree-root form that
// validateAllowPatterns defines.

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

// The [routes.ecosystems.gradle] path key (ADR 0048, issue #3405; the retired
// gradle-path field it replaces was issue #3259) is stored with a trailing
// slash stripped, mirroring upstream-origin's own normalization.
func TestParse_GradleBlockPathValidIsNormalized(t *testing.T) {
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
		t.Errorf("Ecosystems.Path(gradle) = %q, want %q (trailing slash stripped)", routes[0].Ecosystems.Path("gradle"), want)
	}
}

// The [routes.ecosystems.gradle] block is optional.
func TestParse_GradleBlockPathAbsentIsEmpty(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].Ecosystems.Path("gradle") != "" {
		t.Errorf("Ecosystems.Path(gradle) = %q, want empty (omitted)", routes[0].Ecosystems.Path("gradle"))
	}
}

func TestParse_GradleBlockPathMissingLeadingSlashIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
path = "maven"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a gradle-path missing a leading slash, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

func TestParse_GradleBlockPathWhitespaceIsError(t *testing.T) {
	for _, path := range []string{" /maven", "/maven ", "/mav en"} {
		t.Run(path, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
path = "` + path + `"
`
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for gradle-path %q with whitespace, got nil", path)
			}
		})
	}
}

func TestParse_GradleBlockPathDotDotSegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
path = "/maven/../etc"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a gradle-path with a \"..\" segment, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// The path.Clean-based consumers downstream can never produce or match a "."
// segment.
func TestParse_GradleBlockPathDotSegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
path = "/maven/./release"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a gradle-path with a \".\" segment, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// The path.Clean-based consumers downstream can never produce or match an
// interior doubled slash. The trailing-slash case is covered by
// TestParse_GradleBlockPathTrailingDoubleSlashIsNormalized instead.
func TestParse_GradleBlockPathEmptySegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
path = "/maven//release"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a gradle-path with an interior doubled slash, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// The path key is operator-declared but flows into gradleRedirectScript's
// Groovy double-quoted string literal (ecosystem.GradleInitScript), where an
// unescaped "$" triggers GString interpolation at init-script load time. Both
// cases splice tc.path into a TOML basic string, so the "\" case lives in
// TestParse_GradleBlockPathBackslashIsError, which uses a TOML literal string.
func TestParse_GradleBlockPathShellMetacharacterIsError(t *testing.T) {
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
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
path = "` + tc.path + `"
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

// The fixture uses a TOML literal (single-quoted) string so the backslash
// reaches Parse unescaped rather than being consumed as a TOML basic-string
// escape sequence.
func TestParse_GradleBlockPathBackslashIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
path = '/maven/\release'
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a gradle-path containing \"\\\", got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// The path key only adds a subtree on top of an already-resolved host-rooted
// route, so "the whole host" needs no special field and declaring it is an
// error naming that limitation.
func TestParse_GradleBlockPathBareRootIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
path = "/"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for gradle-path = \"/\", got nil")
	}
	if !strings.Contains(err.Error(), "whole host") {
		t.Errorf("expected error to explain the whole-host limitation, got: %v", err)
	}
}

// TrimSuffix strips only one trailing slash, so a naive normalization would
// leave "/", a specific-looking path that is really the same rejected
// whole-host value, rather than collapsing to "" and hitting the bare-root
// check.
func TestParse_GradleBlockPathDoubleSlashWholeHostIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
path = "//"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for gradle-path = \"//\", got nil")
	}
	if !strings.Contains(err.Error(), "whole host") {
		t.Errorf("expected error to explain the whole-host limitation, got: %v", err)
	}
}

// "/foo//" must normalize all the way to "/foo", not the "/foo/" a single
// TrimSuffix leaves behind, which would render a double-slash init-script URL
// that strict Maven registries 404 on.
func TestParse_GradleBlockPathTrailingDoubleSlashIsNormalized(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.gradle]
path = "/foo//"
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/foo"; routes[0].Ecosystems.Path("gradle") != want {
		t.Errorf("Ecosystems.Path(gradle) = %q, want %q (all trailing slashes stripped)", routes[0].Ecosystems.Path("gradle"), want)
	}
}

// The [routes.ecosystems.go] path key (ADR 0048, issue #3405; the retired
// go-path field it replaces was issue #3260) is stored with a trailing slash
// stripped, mirroring [routes.ecosystems.gradle] path.
func TestParse_GoBlockPathValidIsNormalized(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = "/go/"
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/go"; routes[0].Ecosystems.Path("go") != want {
		t.Errorf("Ecosystems.Path(go) = %q, want %q (trailing slash stripped)", routes[0].Ecosystems.Path("go"), want)
	}
}

// The [routes.ecosystems.go] block is optional.
func TestParse_GoBlockPathAbsentIsEmpty(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routes[0].Ecosystems.Path("go") != "" {
		t.Errorf("Ecosystems.Path(go) = %q, want empty (omitted)", routes[0].Ecosystems.Path("go"))
	}
}

func TestParse_GoBlockPathMissingLeadingSlashIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = "go"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a go-path missing a leading slash, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

func TestParse_GoBlockPathWhitespaceIsError(t *testing.T) {
	for _, path := range []string{" /go", "/go ", "/g o"} {
		t.Run(path, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = "` + path + `"
`
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for go-path %q with whitespace, got nil", path)
			}
		})
	}
}

func TestParse_GoBlockPathDotDotSegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = "/go/../etc"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a go-path with a \"..\" segment, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// The path.Clean-based consumers downstream can never produce or match a "."
// segment.
func TestParse_GoBlockPathDotSegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = "/go/./release"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a go-path with a \".\" segment, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// The path.Clean-based consumers downstream can never produce or match an
// interior doubled slash. The trailing-slash case is covered by
// TestParse_GoBlockPathTrailingDoubleSlashIsNormalized instead.
func TestParse_GoBlockPathEmptySegmentIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = "/go//release"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a go-path with an interior doubled slash, got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// The path key is operator-declared but flows into a shell-sourced
// "export GOPROXY='<value>'" line (bindregistry_cmd.go, registrymanifest.go).
// A GOPROXY URL path has no legitimate use for those bytes, and the shared ban
// keeps this key and [routes.ecosystems.gradle] path from drifting via
// validateDeclaredPath.
func TestParse_GoBlockPathShellMetacharacterIsError(t *testing.T) {
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
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = "` + tc.path + `"
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

// The fixture uses a TOML literal (single-quoted) string so the backslash
// reaches Parse unescaped rather than being consumed as a TOML basic-string
// escape sequence.
func TestParse_GoBlockPathBackslashIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = '/go/\release'
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a go-path containing \"\\\", got nil")
	}
	if !strings.Contains(err.Error(), "repo.example.com") {
		t.Errorf("expected error to name the route, got: %v", err)
	}
}

// The path key only adds a subtree on top of an already-resolved host-rooted
// route, so "the whole host" needs no special field and declaring it is an
// error naming that limitation.
func TestParse_GoBlockPathBareRootIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = "/"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for go-path = \"/\", got nil")
	}
	if !strings.Contains(err.Error(), "whole host") {
		t.Errorf("expected error to explain the whole-host limitation, got: %v", err)
	}
}

// TrimSuffix strips only one trailing slash, so a naive normalization would
// leave "/", a specific-looking path that is really the same rejected
// whole-host value, rather than collapsing to "" and hitting the bare-root
// check.
func TestParse_GoBlockPathDoubleSlashWholeHostIsError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = "//"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for go-path = \"//\", got nil")
	}
	if !strings.Contains(err.Error(), "whole host") {
		t.Errorf("expected error to explain the whole-host limitation, got: %v", err)
	}
}

// "/foo//" must normalize all the way to "/foo", not the "/foo/" a single
// TrimSuffix leaves behind, which would render a double-slash GOPROXY URL that
// some proxies 404 on.
func TestParse_GoBlockPathTrailingDoubleSlashIsNormalized(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }

[routes.ecosystems.go]
path = "/foo//"
`
	routes, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/foo"; routes[0].Ecosystems.Path("go") != want {
		t.Errorf("Ecosystems.Path(go) = %q, want %q (all trailing slashes stripped)", routes[0].Ecosystems.Path("go"), want)
	}
}

// npmrcFileResolver keys its lookup on the route's match host, not
// UpstreamURL, since npmrc has no analogous upstream-URL concept
// (credresolver.go).
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

// ADR 0045 shapes the gradle-properties source as a path plus a required "key"
// companion, which lands in Credential.PropertyKey.
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

// Mirrors cargo-credentials' registry-name requirement (ADR 0045).
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

// "key" is documented as gradle-properties' companion only, mirroring
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

// The match host rides along as Credential.MatchHost because execResolver
// names the route in a failed command's error, not to select behavior.
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

// An empty argv would reach exec.Command with no program name at all.
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

// A non-string element must be rejected, not silently coerced.
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

// parseExecArgv's v.([]any) type assertion is what rejects a TOML string here.
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

// An empty argv[0] would reach exec.Command as an empty program name and fail
// with a bare OS error that never names the offending route.
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

// Decoding into map[string]any means go-toml no longer rejects this shape at
// decode time, so Parse has to.
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

// parseCredential sets MatchHost for every source, not only exec and npmrc:
// harmless for the sources that ignore it, and one unconditional assignment is
// simpler to reason about than one gated per source.
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

// Silently dropping a typo'd top-level key would mean the operator's intended
// config never took effect.
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

// An empty routes file is indistinguishable from a typo, such as a stray
// top-level table name, and silently disabling the registry proxy that way
// would surprise the operator.
func TestParse_ZeroRoutesIsError(t *testing.T) {
	_, err := Parse([]byte(""))
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
	_, err := Parse([]byte(doc))
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
	_, err := Parse([]byte(doc))
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
	_, err := Parse([]byte(doc))
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
	_, err := Parse([]byte(doc))
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
	_, err := Parse([]byte(doc))
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
	_, err := Parse([]byte(doc))
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

// Two messages can fire for a wrong registry-name. When it is present but "",
// the generic empty-value check every credential key goes through fires first,
// not the companion-required message that fires only when registry-name is
// absent entirely (TestParse_CargoCredentialsWithoutRegistryNameIsError).
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

// Pins the companion-required message's exact text, which fires only when
// registry-name is absent from the table rather than merely empty.
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

// The accepted set is credentialSourceKeys plus the registry-name and key
// companions.
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

// registry-name is documented as cargo-credentials' companion key only, and
// silently dropping it for other sources would contradict that without telling
// the operator.
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

// Parse phrases this in TOML-key vocabulary rather than credresolver's own
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

// The registry-name companion rides along as Credential.RegistryName rather
// than being rejected as a second source.
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
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for both retired keys, got nil")
	}
	if !strings.Contains(err.Error(), "upstream-base-url") || !strings.Contains(err.Error(), "enforce-allowlist") {
		t.Errorf("expected error to name both retired keys, got: %v", err)
	}
}

// The replacement stanza is built from the offending route's own remaining
// keys, not a generic template: auth-scheme and allow survive as top-level
// keys, and the three retired per-ecosystem keys (also retired here, ADR 0048)
// survive as their equivalent [routes.ecosystems.<name>] blocks.
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
		`allow = ["/dl"]`,
		"[routes.ecosystems.cargo]",
		`registries = ["artifactory"]`,
		"[routes.ecosystems.gradle]",
		`path = "/maven"`,
		"[routes.ecosystems.go]",
		`path = "/go"`,
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

// The retirement error reaches stderr and CI logs, so it must never echo a
// credential embedded in the retired URL.
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

// Every route is host-rooted since ADR 0047 (issue #3261), so allow,
// gradle-path and go-path coexist freely with a minimal route of match-host
// plus a credential; the legacy-route rejections those three keys used to hit
// are gone.
func TestParse_MinimalHostRootedRouteParses(t *testing.T) {
	const doc = `
[[routes]]
match-host = "artifactory.example.com"
credential = { netrc = "~/.netrc" }
allow = ["/dl"]

[routes.ecosystems.gradle]
path = "/maven"

[routes.ecosystems.go]
path = "/go"
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
	if r.Ecosystems.Path("gradle") != "/maven" || r.Ecosystems.Path("go") != "/go" {
		t.Errorf("Ecosystems.Path(gradle) = %q, Ecosystems.Path(go) = %q, want /maven and /go", r.Ecosystems.Path("gradle"), r.Ecosystems.Path("go"))
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
	routes, err := Parse([]byte(doc))
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

// A [routes.ecosystems.<name>] block's "path" key follows the same
// canonical-path rules gradle-path and go-path always used (issue #3403).
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
}

// Cargo's own RouteDeclaration hook is what validates the "registries" key.
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
	if !reflect.DeepEqual(routes[0].Ecosystems.Strings("cargo", "registries"), want) {
		t.Errorf("Ecosystems.Strings(cargo, registries) = %v, want %v", routes[0].Ecosystems.Strings("cargo", "registries"), want)
	}
}

// "registries" is cargo's own key, and a nil RouteDeclaration hook (gradle's)
// accepts no key beyond "path" at all.
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

// An ecosystem is unknown when it has no ecosystem.Table row.
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

// The retirement gate refuses the retired key before either side is validated,
// and the printed stanza merges its value into the ecosystem's existing block
// rather than dropping either. One case per retired key: all three resolve
// through the same retiredRouteKeys table, and a regression isolated to one of
// them would otherwise let its value silently vanish from the stanza.
func TestParse_RetiredKeyAlongsideItsBlockIsError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		doc        string
		retiredKey string
		block      string
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
			retiredKey: "go-path",
			block:      "ecosystems.go",
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
			retiredKey: "gradle-path",
			block:      "ecosystems.gradle",
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
			retiredKey: "cargo-registries",
			block:      "ecosystems.cargo",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil {
				t.Fatalf("expected error for a route declaring both %s and [routes.%s], got nil", tc.retiredKey, tc.block)
			}
			if !strings.Contains(err.Error(), tc.retiredKey) {
				t.Errorf("expected error to name the retired key, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.block) {
				t.Errorf("expected error to name the block, got: %v", err)
			}
		})
	}
}

// The three retired top-level keys are refused (ADR 0048, issue #3405), and
// the printed stanza must parse to the same Route.Ecosystems as a file already
// written with [routes.ecosystems.<name>] blocks, so an operator who pastes it
// back gets the route a downstream consumer (the manifest, the resolver) would
// have seen either way.
func TestParse_RetiredKeyStanzaMatchesBlockDoc(t *testing.T) {
	const retiredDoc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
cargo-registries = ["internal", "crates-remote"]
gradle-path = "/maven/"
go-path = "/go/"
`
	const blockDoc = `
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
	_, err := Parse([]byte(retiredDoc))
	if err == nil {
		t.Fatal("expected error for a route spelling the retired top-level keys, got nil")
	}
	stanza := err.Error()[strings.Index(err.Error(), "[[routes]]"):]

	stanzaRoutes, err := Parse([]byte(stanza))
	if err != nil {
		t.Fatalf("unexpected error parsing the printed replacement stanza: %v", err)
	}
	blockRoutes, err := Parse([]byte(blockDoc))
	if err != nil {
		t.Fatalf("unexpected error parsing new-style doc: %v", err)
	}
	if !reflect.DeepEqual(stanzaRoutes[0].Ecosystems, blockRoutes[0].Ecosystems) {
		t.Errorf("Ecosystems differ:\nstanza = %#v\nblock  = %#v", stanzaRoutes[0].Ecosystems, blockRoutes[0].Ecosystems)
	}
}

// Drives parseRoutes, the internal seam Parse wraps, with a fake row rather
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
	_, err := parseRoutes([]byte(doc), []ecosystem.Row{row})
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

// The retirement gate (ADR 0048, issue #3405) runs before parseRoutes looks at
// rows: a legacy cargo-registries key is refused even when rows omits cargo
// entirely, rather than reaching the row-lookup rejection buildRouteEcosystems
// would otherwise produce.
func TestParseRoutes_RetiredCargoRegistriesFiresBeforeRowsAreConsulted(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
cargo-registries = ["a"]
`
	_, err := parseRoutes([]byte(doc), []ecosystem.Row{{Name: "fake"}})
	if err == nil {
		t.Fatal("expected error for a retired cargo-registries key, got nil")
	}
	if !strings.Contains(err.Error(), "cargo-registries") {
		t.Errorf("expected error to name the key the operator wrote, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ADR 0048") {
		t.Errorf("expected the retirement error, got: %v", err)
	}
}

// The stanza carries the route's [routes.ecosystems.<name>] blocks too, not
// just its top-level keys: an operator who pastes back a stanza missing them
// silently loses every per-ecosystem declaration the route had.
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

// A route mixing spellings, a legacy top-level key for one ecosystem and a
// block for another, carries both as blocks in the replacement stanza, after
// every top-level key, as TOML requires of a sub-table inside a [[routes]]
// entry.
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
	gradle := strings.Index(msg, "[routes.ecosystems.gradle]")
	cargo := strings.Index(msg, "[routes.ecosystems.cargo]")
	credential := strings.Index(msg, `credential = { netrc = "~/.netrc" }`)
	if gradle < 0 {
		t.Errorf("expected stanza to carry the legacy gradle-path as a block, got: %v", err)
	}
	if cargo < 0 {
		t.Errorf("expected stanza to keep the cargo block, got: %v", err)
	}
	if credential >= 0 && (gradle < credential || cargo < credential) {
		t.Errorf("expected both blocks after every top-level key, got: %v", err)
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

// Pins the full error text a retired top-level key produces: cargo-registries
// = [""] would fail cargo's own empty-name check past the gate, but the gate
// refuses the key itself first, so that check is never reached.
func TestParse_EcosystemsLegacyKeyErrorWording(t *testing.T) {
	const doc = `
[[routes]]
match-host = "crates.example.com"
cargo-registries = [""]
credential = { netrc = "~/.netrc" }
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired cargo-registries key, got nil")
	}
	const want = "registryroutes: route \"crates.example.com\": cargo-registries is retired (ADR 0048, issue #3405): the routes file's per-ecosystem keys become one [routes.ecosystems.<name>] block with one typed key, path -- the row validates any further keys; equivalent routes-file stanza:\n\n[[routes]]\nmatch-host = \"crates.example.com\"\ncredential = { netrc = \"~/.netrc\" }\n\n[routes.ecosystems.cargo]\nregistries = [\"\"]\n"
	if got := err.Error(); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// An empty block, and an empty list under a block, are both blocks the
// operator wrote, so each is preserved exactly as declared rather than
// collapsing to "declared nothing". The retired spelling of the same thing has
// no parse at all (TestParse_RetiredEcosystemKeyEmptyValueIsStillRefused).
func TestParse_EcosystemsEmptyDeclarationShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		want registryvocab.RouteEcosystems
	}{
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

// mergeRetiredRouteEcosystems panics on a key that resolves to no
// ecosystem.Table row, so this test is what keeps that panic unreachable: only
// a row dropping its RetiredRouteKey out from under retiredRouteKeys could
// trip it.
func TestRetiredRouteKeysResolveToRows(t *testing.T) {
	for _, entry := range retiredRouteKeys {
		if _, ok := ecosystem.RowByRetiredRouteKey(entry.key); !ok {
			t.Errorf("retiredRouteKeys key %q does not resolve to any ecosystem.Table row", entry.key)
		}
	}
}

// A Go struct tag cannot reference a const, so this test is the only thing
// keeping rawRoute's toml tags for the three retired keys and the ecosystem
// consts that own their spelling from drifting apart.
func TestRawRouteRetiredKeyTagsMatchEcosystemConsts(t *testing.T) {
	cases := []struct {
		field string
		want  string
	}{
		{"CargoRegistries", ecosystem.CargoRetiredRouteKey},
		{"GradlePath", ecosystem.GradleRetiredRouteKey},
		{"GoPath", ecosystem.GoRetiredRouteKey},
	}

	rt := reflect.TypeOf(rawRoute{})
	for _, tc := range cases {
		field, ok := rt.FieldByName(tc.field)
		if !ok {
			t.Errorf("rawRoute has no field %s", tc.field)
			continue
		}
		if got := field.Tag.Get("toml"); got != tc.want {
			t.Errorf("rawRoute.%s toml tag = %q, want %q", tc.field, got, tc.want)
		}
	}
}

// Each of the three retired per-ecosystem top-level keys (ADR 0048, issue
// #3405) is refused on its own, and the printed stanza must parse and carry
// the equivalent [routes.ecosystems.<name>] block.
func TestParse_RetiredEcosystemKeyIsError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		key      string
		doc      string
		row      string
		wantPath string
		wantRegs []string
	}{
		{
			name: "go-path",
			key:  "go-path",
			doc: `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
go-path = "/go-modules"
`,
			row:      "go",
			wantPath: "/go-modules",
		},
		{
			name: "gradle-path",
			key:  "gradle-path",
			doc: `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
gradle-path = "/maven"
`,
			row:      "gradle",
			wantPath: "/maven",
		},
		{
			name: "cargo-registries",
			key:  "cargo-registries",
			doc: `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
cargo-registries = ["a", "b"]
`,
			row:      "cargo",
			wantRegs: []string{"a", "b"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil {
				t.Fatalf("expected error for a retired %s, got nil", tc.key)
			}
			msg := err.Error()
			for _, want := range []string{tc.key, "repo.example.com", "ADR 0048", "#3405"} {
				if !strings.Contains(msg, want) {
					t.Errorf("expected error to contain %q, got: %v", want, err)
				}
			}

			idx := strings.Index(msg, "[[routes]]")
			if idx < 0 {
				t.Fatalf("expected a replacement stanza in the error, got: %v", err)
			}
			stanza := msg[idx:]
			routes, err := Parse([]byte(stanza))
			if err != nil {
				t.Fatalf("expected the replacement stanza to parse, got: %v\nstanza:\n%s", err, stanza)
			}
			if tc.wantPath != "" {
				if got := routes[0].Ecosystems.Path(tc.row); got != tc.wantPath {
					t.Errorf("Ecosystems.Path(%s) = %q, want %q", tc.row, got, tc.wantPath)
				}
			}
			if tc.wantRegs != nil {
				if got := routes[0].Ecosystems.Strings(tc.row, "registries"); !slices.Equal(got, tc.wantRegs) {
					t.Errorf("Ecosystems.Strings(%s, registries) = %v, want %v", tc.row, got, tc.wantRegs)
				}
			}
		})
	}
}

// When a route declares an ecosystem both ways, this is a single retirement
// error (there is no separate "both declared" rejection) and the printed
// stanza keeps only the explicit block's value, dropping the legacy one rather
// than emitting a block with the key twice.
func TestParse_RetiredKeyStanzaExplicitBlockWinsOnConflict(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
gradle-path = "/legacy"

[routes.ecosystems.gradle]
path = "/explicit"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired gradle-path, got nil")
	}
	msg := err.Error()
	idx := strings.Index(msg, "[[routes]]")
	if idx < 0 {
		t.Fatalf("expected a replacement stanza in the error, got: %v", err)
	}
	stanza := msg[idx:]
	if strings.Contains(stanza, "/legacy") {
		t.Errorf("expected stanza to drop the legacy value entirely, got:\n%s", stanza)
	}

	routes, err := Parse([]byte(stanza))
	if err != nil {
		t.Fatalf("expected the replacement stanza to parse, got: %v\nstanza:\n%s", err, stanza)
	}
	if got := routes[0].Ecosystems.Path("gradle"); got != "/explicit" {
		t.Errorf("Ecosystems.Path(gradle) = %q, want %q (explicit block wins)", got, "/explicit")
	}
}

// A retired ADR 0047 key alongside a retired ADR 0048 key produces one error
// naming every offending key, with both ADRs' rationale, not two separate
// rejections a caller could only observe one at a time.
func TestParse_RetiredKeysAcrossBothADRsNamesAllInOneError(t *testing.T) {
	const doc = `
[[routes]]
match-host = "repo.example.com"
enforce-allowlist = true
credential = { netrc = "~/.netrc" }
go-path = "/go"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired enforce-allowlist and go-path, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"enforce-allowlist", "go-path", "ADR 0047", "#3261", "ADR 0048", "#3405"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}
}

// Dropping a declared upstream-origin would print a stanza that parses clean
// but silently falls back to the origin derived from match-host, losing the
// operator's port and scheme.
func TestParse_RetiredEcosystemKeyStanzaKeepsDeclaredUpstreamOrigin(t *testing.T) {
	const origin = "https://acme.example:8443"
	const doc = `
[[routes]]
match-host = "acme.example"
upstream-origin = "` + origin + `"
credential = { netrc = "~/.netrc" }
go-path = "/go-modules"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("expected error for a retired go-path, got nil")
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
	if got := routes[0].UpstreamOrigin; got != origin {
		t.Errorf("UpstreamOrigin = %q, want %q\nstanza:\n%s", got, origin, stanza)
	}
	if got := routes[0].Ecosystems.Path("go"); got != "/go-modules" {
		t.Errorf("Ecosystems.Path(go) = %q, want %q", got, "/go-modules")
	}
}

// Detection is by presence, not truthiness: a retired key spelled with an
// empty value is as retired as one with a real value, the same rule
// enforce-allowlist = false already follows.
func TestParse_RetiredEcosystemKeyEmptyValueIsStillRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		decl string
	}{
		{name: "gradle-path", key: ecosystem.GradleRetiredRouteKey, decl: `gradle-path = ""`},
		{name: "go-path", key: ecosystem.GoRetiredRouteKey, decl: `go-path = ""`},
		{name: "cargo-registries", key: ecosystem.CargoRetiredRouteKey, decl: `cargo-registries = []`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
` + tc.decl + "\n"
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for an empty %s, got nil", tc.key)
			}
			msg := err.Error()
			for _, want := range []string{tc.key, "repo.example.com", "ADR 0048", "#3405"} {
				if !strings.Contains(msg, want) {
					t.Errorf("expected error to contain %q, got: %v", want, err)
				}
			}
			idx := strings.Index(msg, "[[routes]]")
			if idx < 0 {
				t.Fatalf("expected a replacement stanza in the error, got: %v", err)
			}
			stanza := msg[idx:]
			if _, err := Parse([]byte(stanza)); err != nil {
				t.Fatalf("expected the replacement stanza to parse, got: %v\nstanza:\n%s", err, stanza)
			}
		})
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
	"cargo-credentials": {credentialTOML: `cargo-credentials = "~/.cargo/credentials.toml", registry-name = "my-registry"`, wantValue: "~/.cargo/credentials.toml", wantCompanion: "my-registry"},
	"exec":              {credentialTOML: `exec = ["op", "read", "op://vault/item"]`, wantExecArgv: []string{"op", "read", "op://vault/item"}},
	"npmrc":             {credentialTOML: `npmrc = "~/.npmrc"`, wantValue: "~/.npmrc"},
	"gradle-properties": {credentialTOML: `gradle-properties = "/home/build/.gradle/gradle.properties", key = "mavenToken"`, wantValue: "/home/build/.gradle/gradle.properties", wantCompanion: "mavenToken"},
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
			_, err := Parse([]byte(doc))
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

			routes, err := Parse([]byte(stanza))
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

// kindMissingCompanionFixtures supplies, for each kind with a CompanionKey,
// a credential inline table naming the source but omitting its companion.
var kindMissingCompanionFixtures = map[string]string{
	"cargo-credentials": `cargo-credentials = "~/.cargo/credentials.toml"`,
	"gradle-properties": `gradle-properties = "/home/build/.gradle/gradle.properties"`,
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
			_, err := Parse([]byte(doc))
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
