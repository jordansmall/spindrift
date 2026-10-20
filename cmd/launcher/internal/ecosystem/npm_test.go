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

// npmFamilyCases pairs each npm-family row, whose name is also the ecosystem
// tag it reads, with the env var it binds. The strings are spelled out rather
// than read from the rows so a rename of either side fails here.
var npmFamilyCases = []struct {
	row, envVar string
}{
	{"npm", "npm_config_registry"},
	{"pnpm", "pnpm_config_registry"},
	{"yarn", "YARN_NPM_REGISTRY_SERVER"},
}

func taggedRoute(prefix string, paths ...registryvocab.Subtree) []registrymanifest.Route {
	return []registrymanifest.Route{{Prefix: prefix, EnforcedPaths: paths}}
}

func rowExports(t *testing.T, name string, port int, prefix string, routes []registrymanifest.Route) ([]EnvExport, []string) {
	t.Helper()
	row := rowByName(t, name)
	if row.EnvExports == nil {
		t.Fatalf("%s row has nil EnvExports", name)
	}
	return row.EnvExports(port, prefix, stubGetenv(nil), routes)
}

// Each row binds its BindingEnvVar and nothing else, from its own tagged path
// (issue #3259): the other two ecosystems' paths never reach it.
func TestNpmFamilyRows_BindOnlyOwnVarFromOwnTaggedPath(t *testing.T) {
	for _, tc := range npmFamilyCases {
		t.Run(tc.row, func(t *testing.T) {
			if got := rowByName(t, tc.row).BindingEnvVar; got != tc.envVar {
				t.Fatalf("BindingEnvVar = %q, want %q", got, tc.envVar)
			}
			got, warnings := rowExports(t, tc.row, 27182, "r0", npmFamilyTaggedRoutes())
			if len(warnings) != 0 {
				t.Fatalf("got warnings %v, want none", warnings)
			}
			want := []EnvExport{{Name: tc.envVar, Value: "http://127.0.0.1:27182/r0/" + tc.row + "/"}}
			if len(got) != 1 || got[0] != want[0] {
				t.Errorf("exports = %v, want %v", got, want)
			}
		})
	}
}

func TestNpmFamilyRows_DifferentPortDifferentURL(t *testing.T) {
	for _, tc := range npmFamilyCases {
		t.Run(tc.row, func(t *testing.T) {
			got, _ := rowExports(t, tc.row, 9999, "r0", npmFamilyTaggedRoutes())
			want := "http://127.0.0.1:9999/r0/" + tc.row + "/"
			if value, ok := ExportValue(got, tc.envVar); !ok || value != want {
				t.Errorf("%s = (%q, %v), want (%q, true)", tc.envVar, value, ok, want)
			}
		})
	}
}

// The route prefix, not just the port, must land in the rendered URL
// (issue #3142).
func TestNpmFamilyRows_DifferentPrefixDifferentURL(t *testing.T) {
	for _, tc := range npmFamilyCases {
		t.Run(tc.row, func(t *testing.T) {
			got, _ := rowExports(t, tc.row, 27182, "artifactory-npm", npmFamilyTaggedRoutes())
			want := "http://127.0.0.1:27182/artifactory-npm/" + tc.row + "/"
			if value, ok := ExportValue(got, tc.envVar); !ok || value != want {
				t.Errorf("%s = (%q, %v), want (%q, true)", tc.envVar, value, ok, want)
			}
		})
	}
}

// A route tagging only one ecosystem leaves the other two rows with no export
// at all (AC3's fallback).
func TestNpmFamilyRows_SingleTaggedPath(t *testing.T) {
	for _, tc := range npmFamilyCases {
		t.Run(tc.row, func(t *testing.T) {
			routes := taggedRoute("r0", registryvocab.Subtree{Ecosystem: tc.row, Path: "/artifactory/api/npm/npm-local"})
			got, warnings := rowExports(t, tc.row, 27182, "r0", routes)
			if len(warnings) != 0 {
				t.Fatalf("got warnings %v, want none", warnings)
			}
			want := "http://127.0.0.1:27182/r0/artifactory/api/npm/npm-local/"
			if value, ok := ExportValue(got, tc.envVar); !ok || value != want {
				t.Errorf("%s = (%q, %v), want (%q, true)", tc.envVar, value, ok, want)
			}

			for _, other := range npmFamilyCases {
				if other.row == tc.row {
					continue
				}
				otherGot, otherWarnings := rowExports(t, other.row, 27182, "r0", routes)
				if len(otherGot) != 0 || len(otherWarnings) != 0 {
					t.Errorf("%s row = (%v, %v) on a %s-only route, want nothing", other.row, otherGot, otherWarnings, tc.row)
				}
			}
		})
	}
}

// Whole-host regression (issue #3259 review finding): a "/" tagged path,
// registrypathset's whole-host marker, must render as the bare-root URL with
// exactly one trailing slash. A double slash can 404 on a strict registry.
// Mirrors cargo's ""-means-"no path" convention (see cargoIndexPath).
func TestNpmFamilyRows_WholeHostTaggedPath(t *testing.T) {
	for _, tc := range npmFamilyCases {
		t.Run(tc.row, func(t *testing.T) {
			routes := taggedRoute("r0", registryvocab.Subtree{Ecosystem: tc.row, Path: "/"})
			got, warnings := rowExports(t, tc.row, 27182, "r0", routes)
			if len(warnings) != 0 {
				t.Fatalf("got warnings %v, want none", warnings)
			}
			want := "http://127.0.0.1:27182/r0/"
			if value, ok := ExportValue(got, tc.envVar); !ok || value != want {
				t.Errorf("%s = (%q, %v), want (%q, true)", tc.envVar, value, ok, want)
			}
		})
	}
}

// AC3's fallback: absence of declaration is absence of binding, not an
// error, so a missing tagged path warns about nothing.
func TestNpmFamilyRows_ZeroTaggedPaths(t *testing.T) {
	for _, tc := range npmFamilyCases {
		t.Run(tc.row, func(t *testing.T) {
			got, warnings := rowExports(t, tc.row, 27182, "r0", []registrymanifest.Route{{Prefix: "r0"}})
			if len(got) != 0 {
				t.Errorf("got exports %v, want none", got)
			}
			if len(warnings) != 0 {
				t.Errorf("got warnings %v, want none", warnings)
			}
		})
	}
}

// Two or more same-tagged paths on the matched route leave no way to tell
// which is the default registry, so the row's var gets no export and a
// warning naming the ecosystem, the var, the ambiguous paths, and the prefix.
func TestNpmFamilyRows_AmbiguousTaggedPaths(t *testing.T) {
	for _, tc := range npmFamilyCases {
		t.Run(tc.row, func(t *testing.T) {
			routes := taggedRoute("r0",
				registryvocab.Subtree{Ecosystem: tc.row, Path: "/artifactory/api/npm/npm-local"},
				registryvocab.Subtree{Ecosystem: tc.row, Path: "/artifactory/api/npm/npm-other"},
			)
			got, warnings := rowExports(t, tc.row, 27182, "r0", routes)
			if _, ok := ExportValue(got, tc.envVar); ok {
				t.Errorf("%s exported, want no export (ambiguous)", tc.envVar)
			}
			if len(warnings) != 1 {
				t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
			}
			w := warnings[0]
			if !strings.HasPrefix(w, "==> WARNING: ") {
				t.Errorf("warning %q missing \"==> WARNING: \" prefix", w)
			}
			for _, want := range []string{tc.row, tc.envVar, "r0", "/artifactory/api/npm/npm-local", "/artifactory/api/npm/npm-other"} {
				if !strings.Contains(w, want) {
					t.Errorf("warning %q missing %q", w, want)
				}
			}
		})
	}
}

// A scoped "@scope:registry=" path is not the default registry, so a route
// holding only scoped paths binds nothing and warns about nothing (#5720).
func TestNpmFamilyRows_ScopedOnlyPathBindsNothing(t *testing.T) {
	for _, tc := range npmFamilyCases {
		t.Run(tc.row, func(t *testing.T) {
			routes := taggedRoute("r0", registryvocab.Subtree{Ecosystem: tc.row, Path: "/acme", Scoped: true})
			got, warnings := rowExports(t, tc.row, 27182, "r0", routes)
			if len(got) != 0 || len(warnings) != 0 {
				t.Errorf("got (%v, %v), want no export and no warning", got, warnings)
			}
		})
	}
}

// A scoped path beside one unscoped path neither binds nor makes the unscoped
// one ambiguous.
func TestNpmFamilyRows_ScopedPathDoesNotMakeUnscopedAmbiguous(t *testing.T) {
	for _, tc := range npmFamilyCases {
		t.Run(tc.row, func(t *testing.T) {
			routes := taggedRoute("r0",
				registryvocab.Subtree{Ecosystem: tc.row, Path: "/acme", Scoped: true},
				registryvocab.Subtree{Ecosystem: tc.row, Path: "/default"},
			)
			got, warnings := rowExports(t, tc.row, 27182, "r0", routes)
			if len(warnings) != 0 {
				t.Fatalf("got warnings %v, want none", warnings)
			}
			if value, ok := ExportValue(got, tc.envVar); !ok || value != "http://127.0.0.1:27182/r0/default/" {
				t.Errorf("%s = (%q, %v), want the unscoped path", tc.envVar, value, ok)
			}
		})
	}
}

// Each row resolves its own tagged path, so distinct paths per ecosystem do
// not bleed into one another and one row's ambiguity does not silence another.
func TestNpmFamilyRows_IndependentPerEcosystem(t *testing.T) {
	routes := taggedRoute("r0",
		registryvocab.Subtree{Ecosystem: "npm", Path: "/npm-repo"},
		registryvocab.Subtree{Ecosystem: "pnpm", Path: "/pnpm-repo"},
		registryvocab.Subtree{Ecosystem: "yarn", Path: "/yarn-repo"},
		registryvocab.Subtree{Ecosystem: "yarn", Path: "/yarn-other"},
	)
	for _, tc := range npmFamilyCases {
		t.Run(tc.row, func(t *testing.T) {
			got, warnings := rowExports(t, tc.row, 27182, "r0", routes)
			if tc.row == "yarn" {
				if len(got) != 0 || len(warnings) != 1 {
					t.Errorf("yarn = (%v, %v), want no export and one ambiguity warning", got, warnings)
				}
				return
			}
			if len(warnings) != 0 {
				t.Fatalf("got warnings %v, want none", warnings)
			}
			want := "http://127.0.0.1:27182/r0/" + tc.row + "-repo/"
			if value, ok := ExportValue(got, tc.envVar); !ok || value != want {
				t.Errorf("%s = (%q, %v), want (%q, true)", tc.envVar, value, ok, want)
			}
		})
	}
}

// The fixture carries a "#" comment line and a ";" comment line because the
// parser has to ignore both styles. Simplifying them away stops testing that.
func TestParseNpmRegistryConfig_DefaultAndScopedRegistry(t *testing.T) {
	content := `
registry=https://npm.example.com/
@myorg:registry=https://scoped.example.com/npm/
# a comment
; another comment style
`
	decls, namedAny, err := npmRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if !namedAny {
		t.Error("namedAny = false, want true")
	}
	want := []Declaration{
		{Host: "npm.example.com", UpstreamBaseURL: "https://npm.example.com"},
		{Host: "scoped.example.com", UpstreamBaseURL: "https://scoped.example.com/npm", Scoped: true},
	}
	if len(decls) != len(want) {
		t.Fatalf("decls = %+v, want %+v", decls, want)
	}
	for i, w := range want {
		if decls[i] != w {
			t.Errorf("decls[%d] = %+v, want %+v", i, decls[i], w)
		}
	}
}

func TestParseNpmRegistryConfig_RepeatedURLDeduped(t *testing.T) {
	content := "registry=https://npm.example.com/\n@myorg:registry=https://npm.example.com/\n"
	decls, _, err := npmRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1 (repeated URL deduped)", decls)
	}
}

// A credential in the URL's userinfo must never come back as a Declaration,
// since that would persist it into a written routes file.
func TestParseNpmRegistryConfig_UserinfoURLIsSkippedButNamed(t *testing.T) {
	decls, namedAny, err := npmRow.ConfigParser("registry=https://ci:s3cr3t@npm.example.com/\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("decls = %+v, want none (userinfo URL must never be returned)", decls)
	}
	if !namedAny {
		t.Error("namedAny = false, want true")
	}
}

func TestParseNpmRegistryConfig_PortOnlyHostIsSkippedButNamed(t *testing.T) {
	decls, namedAny, err := npmRow.ConfigParser("registry=http://:8080/\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("decls = %+v, want none (a port-only host has no hostname a route can match on)", decls)
	}
	if !namedAny {
		t.Error("namedAny = false, want true")
	}
}

// No registry key at all is distinct from named but unusable, so namedAny
// must be false rather than true with zero declarations.
func TestParseNpmRegistryConfig_NoDeclarationYieldsNamedAnyFalse(t *testing.T) {
	decls, namedAny, err := npmRow.ConfigParser("# just a comment\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("decls = %+v, want none", decls)
	}
	if namedAny {
		t.Error("namedAny = true, want false (no registry key at all)")
	}
}

// ConfigParser is pure: registrydiscover's walker stamps Ecosystem and
// ConfigPath after the call returns, so a parser that set them itself would
// fight the walker.
func TestParseNpmRegistryConfig_NeverStampsEcosystemOrConfigPath(t *testing.T) {
	decls, _, err := npmRow.ConfigParser("registry=https://npm.example.com/\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	if decls[0].Ecosystem != "" || decls[0].ConfigPath != "" {
		t.Errorf("decls[0] = %+v, want Ecosystem and ConfigPath both unset", decls[0])
	}
}
