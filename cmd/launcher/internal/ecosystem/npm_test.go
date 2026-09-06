package ecosystem

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// npmFamilyTaggedRoutes is one route declaring a tagged path for each of the
// three ecosystems, the only shape that binds all three vars at once.
func npmFamilyTaggedRoutes() []registrymanifest.Route {
	return []registrymanifest.Route{{
		Prefix: "r0",
		EnforcedPaths: []registryvocab.Subtree{
			{Ecosystem: "npm", Path: "/npm"},
			{Ecosystem: "pnpm", Path: "/pnpm"},
			{Ecosystem: "yarn", Path: "/yarn"},
		},
	}}
}

// TestNpmFamilyBindings_ThreeExportsCorrectURL pins that each var binds to
// its own ecosystem's tagged path under the route prefix.
func TestNpmFamilyBindings_ThreeExportsCorrectURL(t *testing.T) {
	got, warnings := NpmFamilyBindings(27182, "r0", npmFamilyTaggedRoutes())
	if len(got) != 3 {
		t.Fatalf("len(NpmFamilyBindings) = %d, want 3", len(got))
	}
	if len(warnings) != 0 {
		t.Fatalf("got warnings %v, want none", warnings)
	}
	want := map[string]string{
		"npm_config_registry":      "http://127.0.0.1:27182/r0/npm/",
		"pnpm_config_registry":     "http://127.0.0.1:27182/r0/pnpm/",
		"YARN_NPM_REGISTRY_SERVER": "http://127.0.0.1:27182/r0/yarn/",
	}
	for name, wantValue := range want {
		value, ok := ExportValue(got, name)
		if !ok {
			t.Errorf("%s missing from NpmFamilyBindings", name)
			continue
		}
		if value != wantValue {
			t.Errorf("%s = %q, want %q", name, value, wantValue)
		}
	}
}

func TestNpmFamilyBindings_DifferentPortDifferentURL(t *testing.T) {
	got, _ := NpmFamilyBindings(9999, "r0", npmFamilyTaggedRoutes())
	value, ok := ExportValue(got, "npm_config_registry")
	if !ok {
		t.Fatalf("npm_config_registry missing from NpmFamilyBindings")
	}
	if value != "http://127.0.0.1:9999/r0/npm/" {
		t.Errorf("npm_config_registry = %q, want %q", value, "http://127.0.0.1:9999/r0/npm/")
	}
}

// TestNpmFamilyBindings_DifferentPrefixDifferentURL pins that the route
// prefix, not just the port, lands in the rendered URL (issue #3142).
func TestNpmFamilyBindings_DifferentPrefixDifferentURL(t *testing.T) {
	got, _ := NpmFamilyBindings(27182, "artifactory-npm", npmFamilyTaggedRoutes())
	value, ok := ExportValue(got, "npm_config_registry")
	if !ok {
		t.Fatalf("npm_config_registry missing from NpmFamilyBindings")
	}
	if value != "http://127.0.0.1:27182/artifactory-npm/npm/" {
		t.Errorf("npm_config_registry = %q, want %q", value, "http://127.0.0.1:27182/artifactory-npm/npm/")
	}
}

// TestNpmFamilyBindings_SingleTaggedPath pins that a
// route with exactly one npm-tagged path binds npm_config_registry to the
// full-path URL, while pnpm/yarn -- which have no tagged path of their own
// on this route -- get no export at all (AC3's fallback), proving the three
// vars are decided independently rather than sharing npm's match.
func TestNpmFamilyBindings_SingleTaggedPath(t *testing.T) {
	routes := []registrymanifest.Route{{
		Prefix: "r0",
		EnforcedPaths: []registryvocab.Subtree{
			{Ecosystem: "npm", Path: "/artifactory/api/npm/npm-local"},
		},
	}}
	got, warnings := NpmFamilyBindings(27182, "r0", routes)
	if len(warnings) != 0 {
		t.Fatalf("got warnings %v, want none", warnings)
	}

	value, ok := ExportValue(got, "npm_config_registry")
	if !ok {
		t.Fatalf("npm_config_registry missing from NpmFamilyBindings")
	}
	want := "http://127.0.0.1:27182/r0/artifactory/api/npm/npm-local/"
	if value != want {
		t.Errorf("npm_config_registry = %q, want %q", value, want)
	}

	for _, name := range []string{"pnpm_config_registry", "YARN_NPM_REGISTRY_SERVER"} {
		if _, ok := ExportValue(got, name); ok {
			t.Errorf("%s exported, want no export (no tagged path for this ecosystem)", name)
		}
	}
}

// TestNpmFamilyBindings_WholeHostTaggedPath pins the whole-host
// regression (issue #3259 review finding): a route whose single
// npm-tagged path is "/" (registrypathset's own whole-host marker, e.g. from
// a committed .npmrc with no path at all) must render npm_config_registry as
// the plain bare-root URL with exactly one trailing slash, not
// ".../r0//" -- a double slash a strict registry can 404 on. Mirrors cargo's
// own ""-means-"no path" convention (see cargoIndexPath).
func TestNpmFamilyBindings_WholeHostTaggedPath(t *testing.T) {
	routes := []registrymanifest.Route{{
		Prefix: "r0",
		EnforcedPaths: []registryvocab.Subtree{
			{Ecosystem: "npm", Path: "/"},
		},
	}}
	got, warnings := NpmFamilyBindings(27182, "r0", routes)
	if len(warnings) != 0 {
		t.Fatalf("got warnings %v, want none", warnings)
	}

	value, ok := ExportValue(got, "npm_config_registry")
	if !ok {
		t.Fatalf("npm_config_registry missing from NpmFamilyBindings")
	}
	want := "http://127.0.0.1:27182/r0/"
	if value != want {
		t.Errorf("npm_config_registry = %q, want %q", value, want)
	}
}

// TestNpmFamilyBindings_ZeroTaggedPaths pins AC3's fallback: a
// route declaring no tagged path for an ecosystem exports
// nothing for that var, and warns about none of it -- absence of
// declaration is absence of binding, not an error.
func TestNpmFamilyBindings_ZeroTaggedPaths(t *testing.T) {
	routes := []registrymanifest.Route{{Prefix: "r0"}}
	got, warnings := NpmFamilyBindings(27182, "r0", routes)
	if len(got) != 0 {
		t.Errorf("got exports %v, want none", got)
	}
	if len(warnings) != 0 {
		t.Errorf("got warnings %v, want none", warnings)
	}
}

// TestNpmFamilyBindings_AmbiguousTaggedPaths pins the ambiguous
// case: two or more npm-tagged paths on the matched route mean
// there's no way to tell which is the default registry, so
// npm_config_registry gets no export and a warning naming the ecosystem,
// the ambiguous paths, and the route's prefix.
func TestNpmFamilyBindings_AmbiguousTaggedPaths(t *testing.T) {
	routes := []registrymanifest.Route{{
		Prefix: "r0",
		EnforcedPaths: []registryvocab.Subtree{
			{Ecosystem: "npm", Path: "/artifactory/api/npm/npm-local"},
			{Ecosystem: "npm", Path: "/artifactory/api/npm/npm-other"},
		},
	}}
	got, warnings := NpmFamilyBindings(27182, "r0", routes)
	if _, ok := ExportValue(got, "npm_config_registry"); ok {
		t.Errorf("npm_config_registry exported, want no export (ambiguous)")
	}
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
	}
	w := warnings[0]
	if !strings.HasPrefix(w, "==> WARNING: ") {
		t.Errorf("warning %q missing \"==> WARNING: \" prefix", w)
	}
	for _, want := range []string{"npm", "r0", "/artifactory/api/npm/npm-local", "/artifactory/api/npm/npm-other"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q missing %q", w, want)
		}
	}
}

// TestNpmFamilyBindings_IndependentPerEcosystem pins that npm, pnpm, and
// yarn each resolve their own single tagged path independently in one call
// -- proving the three vars aren't computed from one shared match.
func TestNpmFamilyBindings_IndependentPerEcosystem(t *testing.T) {
	routes := []registrymanifest.Route{{
		Prefix: "r0",
		EnforcedPaths: []registryvocab.Subtree{
			{Ecosystem: "npm", Path: "/npm-repo"},
			{Ecosystem: "pnpm", Path: "/pnpm-repo"},
			{Ecosystem: "yarn", Path: "/yarn-repo"},
		},
	}}
	got, warnings := NpmFamilyBindings(27182, "r0", routes)
	if len(warnings) != 0 {
		t.Fatalf("got warnings %v, want none", warnings)
	}

	cases := map[string]string{
		"npm_config_registry":      "http://127.0.0.1:27182/r0/npm-repo/",
		"pnpm_config_registry":     "http://127.0.0.1:27182/r0/pnpm-repo/",
		"YARN_NPM_REGISTRY_SERVER": "http://127.0.0.1:27182/r0/yarn-repo/",
	}
	for name, want := range cases {
		value, ok := ExportValue(got, name)
		if !ok {
			t.Errorf("%s missing from NpmFamilyBindings", name)
			continue
		}
		if value != want {
			t.Errorf("%s = %q, want %q", name, value, want)
		}
	}
}
