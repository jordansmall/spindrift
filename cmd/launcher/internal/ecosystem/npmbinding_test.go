package ecosystem

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// TestNpmFamilyBindings_ThreeExportsCorrectURL pins the legacy (no route
// info at all) contract: all three vars bind to the bare route root.
func TestNpmFamilyBindings_ThreeExportsCorrectURL(t *testing.T) {
	got, warnings := NpmFamilyBindings(27182, "r0", nil)
	if len(got) != 3 {
		t.Fatalf("len(NpmFamilyBindings) = %d, want 3", len(got))
	}
	if len(warnings) != 0 {
		t.Fatalf("got warnings %v, want none", warnings)
	}
	want := "http://127.0.0.1:27182/r0/"
	for _, name := range []string{"npm_config_registry", "pnpm_config_registry", "YARN_NPM_REGISTRY_SERVER"} {
		value, ok := exportValue(got, name)
		if !ok {
			t.Errorf("%s missing from NpmFamilyBindings", name)
			continue
		}
		if value != want {
			t.Errorf("%s = %q, want %q", name, value, want)
		}
	}
}

func TestNpmFamilyBindings_DifferentPortDifferentURL(t *testing.T) {
	got, _ := NpmFamilyBindings(9999, "r0", nil)
	value, ok := exportValue(got, "npm_config_registry")
	if !ok {
		t.Fatalf("npm_config_registry missing from NpmFamilyBindings")
	}
	if value != "http://127.0.0.1:9999/r0/" {
		t.Errorf("npm_config_registry = %q, want %q", value, "http://127.0.0.1:9999/r0/")
	}
}

// TestNpmFamilyBindings_DifferentPrefixDifferentURL pins that the route
// prefix, not just the port, lands in the rendered URL (issue #3142).
func TestNpmFamilyBindings_DifferentPrefixDifferentURL(t *testing.T) {
	got, _ := NpmFamilyBindings(27182, "artifactory-npm", nil)
	value, ok := exportValue(got, "npm_config_registry")
	if !ok {
		t.Fatalf("npm_config_registry missing from NpmFamilyBindings")
	}
	if value != "http://127.0.0.1:27182/artifactory-npm/" {
		t.Errorf("npm_config_registry = %q, want %q", value, "http://127.0.0.1:27182/artifactory-npm/")
	}
}

// TestNpmFamilyBindings_NonHostRootedRouteUnchanged pins that a legacy
// (non-host-rooted) route with routes present still renders the bare route
// root for every var -- routes[0].HostRooted false takes the same branch as
// the no-routes-at-all case (issue #3259).
func TestNpmFamilyBindings_NonHostRootedRouteUnchanged(t *testing.T) {
	routes := []registrymanifest.Route{{Prefix: "r0", HostRooted: false}}
	got, warnings := NpmFamilyBindings(27182, "r0", routes)
	if len(warnings) != 0 {
		t.Fatalf("got warnings %v, want none", warnings)
	}
	for _, name := range []string{"npm_config_registry", "pnpm_config_registry", "YARN_NPM_REGISTRY_SERVER"} {
		value, ok := exportValue(got, name)
		if !ok {
			t.Errorf("%s missing from NpmFamilyBindings", name)
			continue
		}
		if value != "http://127.0.0.1:27182/r0/" {
			t.Errorf("%s = %q, want bare route root", name, value)
		}
	}
}

// TestNpmFamilyBindings_HostRootedSingleTaggedPath pins that a host-rooted
// route with exactly one npm-tagged path binds npm_config_registry to the
// full-path URL, while pnpm/yarn -- which have no tagged path of their own
// on this route -- get no export at all (AC3's fallback), proving the three
// vars are decided independently rather than sharing npm's match.
func TestNpmFamilyBindings_HostRootedSingleTaggedPath(t *testing.T) {
	routes := []registrymanifest.Route{{
		Prefix:     "r0",
		HostRooted: true,
		EnforcedPaths: []registrymanifest.EcosystemPath{
			{Ecosystem: "npm", Path: "/artifactory/api/npm/npm-local"},
		},
	}}
	got, warnings := NpmFamilyBindings(27182, "r0", routes)
	if len(warnings) != 0 {
		t.Fatalf("got warnings %v, want none", warnings)
	}

	value, ok := exportValue(got, "npm_config_registry")
	if !ok {
		t.Fatalf("npm_config_registry missing from NpmFamilyBindings")
	}
	want := "http://127.0.0.1:27182/r0/artifactory/api/npm/npm-local/"
	if value != want {
		t.Errorf("npm_config_registry = %q, want %q", value, want)
	}

	for _, name := range []string{"pnpm_config_registry", "YARN_NPM_REGISTRY_SERVER"} {
		if _, ok := exportValue(got, name); ok {
			t.Errorf("%s exported, want no export (no tagged path for this ecosystem)", name)
		}
	}
}

// TestNpmFamilyBindings_HostRootedWholeHostTaggedPath pins the whole-host
// regression (issue #3259 review finding): a host-rooted route whose single
// npm-tagged path is "/" (registrypathset's own whole-host marker, e.g. from
// a committed .npmrc with no path at all) must render npm_config_registry as
// the plain bare-root URL with exactly one trailing slash, not
// ".../r0//" -- a double slash a strict registry can 404 on. Mirrors cargo's
// own ""-means-"no path" convention (see cargoIndexPath).
func TestNpmFamilyBindings_HostRootedWholeHostTaggedPath(t *testing.T) {
	routes := []registrymanifest.Route{{
		Prefix:     "r0",
		HostRooted: true,
		EnforcedPaths: []registrymanifest.EcosystemPath{
			{Ecosystem: "npm", Path: "/"},
		},
	}}
	got, warnings := NpmFamilyBindings(27182, "r0", routes)
	if len(warnings) != 0 {
		t.Fatalf("got warnings %v, want none", warnings)
	}

	value, ok := exportValue(got, "npm_config_registry")
	if !ok {
		t.Fatalf("npm_config_registry missing from NpmFamilyBindings")
	}
	want := "http://127.0.0.1:27182/r0/"
	if value != want {
		t.Errorf("npm_config_registry = %q, want %q", value, want)
	}
}

// TestNpmFamilyBindings_HostRootedZeroTaggedPaths pins AC3's fallback: a
// host-rooted route declaring no tagged path for an ecosystem exports
// nothing for that var, and warns about none of it -- absence of
// declaration is absence of binding, not an error.
func TestNpmFamilyBindings_HostRootedZeroTaggedPaths(t *testing.T) {
	routes := []registrymanifest.Route{{Prefix: "r0", HostRooted: true}}
	got, warnings := NpmFamilyBindings(27182, "r0", routes)
	if len(got) != 0 {
		t.Errorf("got exports %v, want none", got)
	}
	if len(warnings) != 0 {
		t.Errorf("got warnings %v, want none", warnings)
	}
}

// TestNpmFamilyBindings_HostRootedAmbiguousTaggedPaths pins the ambiguous
// case: two or more npm-tagged paths on the matched host-rooted route mean
// there's no way to tell which is the default registry, so
// npm_config_registry gets no export and a warning naming the ecosystem,
// the ambiguous paths, and the route's prefix.
func TestNpmFamilyBindings_HostRootedAmbiguousTaggedPaths(t *testing.T) {
	routes := []registrymanifest.Route{{
		Prefix:     "r0",
		HostRooted: true,
		EnforcedPaths: []registrymanifest.EcosystemPath{
			{Ecosystem: "npm", Path: "/artifactory/api/npm/npm-local"},
			{Ecosystem: "npm", Path: "/artifactory/api/npm/npm-other"},
		},
	}}
	got, warnings := NpmFamilyBindings(27182, "r0", routes)
	if _, ok := exportValue(got, "npm_config_registry"); ok {
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
		Prefix:     "r0",
		HostRooted: true,
		EnforcedPaths: []registrymanifest.EcosystemPath{
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
		value, ok := exportValue(got, name)
		if !ok {
			t.Errorf("%s missing from NpmFamilyBindings", name)
			continue
		}
		if value != want {
			t.Errorf("%s = %q, want %q", name, value, want)
		}
	}
}
