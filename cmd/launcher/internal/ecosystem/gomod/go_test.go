package gomod

import (
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// GOPROXY binds only when a route's ecosystems block declares a go path, and
// GONOPROXY and GOSUMDB ride along with that export, so every case pinning an
// env-driven decision needs this shape.
var goDeclaredRoutes = []registrymanifest.Route{{
	Prefix:     "r0",
	Ecosystems: registryvocab.RouteEcosystems{"go": registryvocab.RouteDeclaration{"path": "/go"}},
}}

func containsWarningSubstring(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestComputeBindings(t *testing.T) {
	cases := []struct {
		name   string
		port   int
		prefix string
		// A case that leaves routes unset gets goDeclaredRoutes: the
		// env-driven GONOPROXY/GOSUMDB decisions only apply alongside a
		// bound GOPROXY. A case pinning the routes axis sets it.
		routes []registrymanifest.Route
		input  bindingInput

		wantExports map[string]string
		// The export must be entirely absent, not just empty-valued.
		wantAbsentExports       []string
		wantWarningSubstrings   []string
		wantNoWarningSubstrings []string
	}{
		{
			name:        "GOPROXY bound to the go path the route's ecosystems block declares",
			port:        27182,
			prefix:      "r0",
			input:       bindingInput{},
			wantExports: map[string]string{"GOPROXY": "http://127.0.0.1:27182/r0/go"},
		},
		{
			name:                    "no prior GOTOOLCHAIN pins local without warning",
			port:                    27182,
			prefix:                  "r0",
			input:                   bindingInput{},
			wantExports:             map[string]string{"GOTOOLCHAIN": "local"},
			wantNoWarningSubstrings: []string{"GOTOOLCHAIN"},
		},
		{
			name:                    "prior GOTOOLCHAIN already local warns nothing",
			port:                    27182,
			prefix:                  "r0",
			input:                   bindingInput{GOTOOLCHAIN: "local"},
			wantExports:             map[string]string{"GOTOOLCHAIN": "local"},
			wantNoWarningSubstrings: []string{"GOTOOLCHAIN"},
		},
		{
			name:                  "prior GOTOOLCHAIN=auto warns",
			port:                  27182,
			prefix:                "r0",
			input:                 bindingInput{GOTOOLCHAIN: "auto"},
			wantExports:           map[string]string{"GOTOOLCHAIN": "local"},
			wantWarningSubstrings: []string{"auto"},
		},
		{
			name:                    "no prior GONOPROXY or GOPRIVATE forces none without warning",
			port:                    27182,
			prefix:                  "r0",
			input:                   bindingInput{},
			wantExports:             map[string]string{"GONOPROXY": "none"},
			wantNoWarningSubstrings: []string{"GONOPROXY"},
		},
		{
			name:                  "prior GONOPROXY set warns",
			port:                  27182,
			prefix:                "r0",
			input:                 bindingInput{GONOPROXY: "example.com/*"},
			wantWarningSubstrings: []string{"GONOPROXY"},
		},
		{
			name:                  "prior GOPRIVATE set warns GONOPROXY override",
			port:                  27182,
			prefix:                "r0",
			input:                 bindingInput{GOPRIVATE: "example.com/*"},
			wantWarningSubstrings: []string{"GONOPROXY"},
		},
		{
			name:                    "GOSUMDB off when no exemption declared",
			port:                    27182,
			prefix:                  "r0",
			input:                   bindingInput{},
			wantExports:             map[string]string{"GOSUMDB": "off"},
			wantNoWarningSubstrings: []string{"GOSUMDB"},
		},
		{
			name:                  "GOSUMDB off warns when prior value set",
			port:                  27182,
			prefix:                "r0",
			input:                 bindingInput{GOSUMDB: "sum.golang.org"},
			wantExports:           map[string]string{"GOSUMDB": "off"},
			wantWarningSubstrings: []string{"sum.golang.org"},
		},
		{
			name:                    "GOPRIVATE set leaves GOSUMDB alone",
			port:                    27182,
			prefix:                  "r0",
			input:                   bindingInput{GOPRIVATE: "example.com/*", GOSUMDB: "sum.golang.org"},
			wantAbsentExports:       []string{"GOSUMDB"},
			wantNoWarningSubstrings: []string{"GOSUMDB"},
		},
		{
			name:                    "GONOSUMDB set leaves GOSUMDB alone",
			port:                    27182,
			prefix:                  "r0",
			input:                   bindingInput{GONOSUMDB: "example.com/*", GOSUMDB: "sum.golang.org"},
			wantAbsentExports:       []string{"GOSUMDB"},
			wantNoWarningSubstrings: []string{"GOSUMDB"},
		},
		{
			// Pins that the route prefix lands in GOPROXY (issue #3142:
			// bindings mode has no per-ecosystem route mapping, so it binds
			// to the first manifest route's prefix).
			name:        "GOPROXY interpolates the given prefix",
			port:        27182,
			prefix:      "artifactory-go",
			input:       bindingInput{},
			wantExports: map[string]string{"GOPROXY": "http://127.0.0.1:27182/artifactory-go/go"},
		},
		{
			// The declared path is concatenated as-is: nothing between
			// the routes file and here normalizes it.
			name:   "route declaring a go path in its ecosystems block renders full-path GOPROXY",
			port:   27182,
			prefix: "r0",
			routes: []registrymanifest.Route{{
				Prefix:     "r0",
				Ecosystems: registryvocab.RouteEcosystems{"go": registryvocab.RouteDeclaration{"path": "/artifactory/api/go/go-remote"}},
			}},
			input:       bindingInput{},
			wantExports: map[string]string{"GOPROXY": "http://127.0.0.1:27182/r0/artifactory/api/go/go-remote"},
		},
		{
			// AC3's fallback: a route declaring no go path leaves GOPROXY
			// unexported, not set to the bare-root URL. GONOPROXY and GOSUMDB
			// stay unset too, since GONOPROXY=none would push private paths to
			// Go's public proxy and GOSUMDB=off would drop checksum
			// verification. GOTOOLCHAIN=local is about the toolchain, not routing.
			name:              "route with no go declaration leaves GOPROXY, GONOPROXY and GOSUMDB unset",
			port:              27182,
			prefix:            "r0",
			routes:            []registrymanifest.Route{{Prefix: "r0"}},
			input:             bindingInput{},
			wantAbsentExports: []string{"GOPROXY", "GONOPROXY", "GOSUMDB"},
			wantExports: map[string]string{
				"GOTOOLCHAIN": "local",
			},
		},
		{
			// Each override warning is gated with the export it announces:
			// with no GOPROXY export there is no override to report, and the
			// GONOPROXY wording would claim routing that does not happen.
			name:   "route with no go declaration suppresses the GONOPROXY and GOSUMDB warnings",
			port:   27182,
			prefix: "r0",
			routes: []registrymanifest.Route{{Prefix: "r0"}},
			input: bindingInput{
				GOTOOLCHAIN: "auto",
				GONOPROXY:   "example.com/*",
				GOSUMDB:     "sum.golang.org",
			},
			wantAbsentExports:       []string{"GOPROXY", "GONOPROXY", "GOSUMDB"},
			wantExports:             map[string]string{"GOTOOLCHAIN": "local"},
			wantWarningSubstrings:   []string{"GOTOOLCHAIN"},
			wantNoWarningSubstrings: []string{"GONOPROXY", "GOSUMDB"},
		},
		{
			// A manifest can carry blocks for ecosystems this Box's
			// renderers know nothing about; an unrecognized block is just
			// another route with nothing declared for go, never an error.
			name:   "route declaring only another ecosystem's block leaves GOPROXY unset",
			port:   27182,
			prefix: "r0",
			routes: []registrymanifest.Route{{
				Prefix:     "r0",
				Ecosystems: registryvocab.RouteEcosystems{"pypi": registryvocab.RouteDeclaration{"path": "/pypi"}},
			}},
			input:             bindingInput{},
			wantAbsentExports: []string{"GOPROXY", "GONOPROXY", "GOSUMDB"},
			wantExports:       map[string]string{"GOTOOLCHAIN": "local"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := tc.routes
			if routes == nil {
				routes = goDeclaredRoutes
			}
			got := computeBindings(tc.port, tc.prefix, routes, tc.input)

			for name, want := range tc.wantExports {
				value, ok := ecosystem.ExportValue(got.Exports, name)
				if !ok || value != want {
					t.Errorf("%s export = (%q, %v), want (%q, true)", name, value, ok, want)
				}
			}
			for _, name := range tc.wantAbsentExports {
				if value, ok := ecosystem.ExportValue(got.Exports, name); ok {
					t.Errorf("%s should be absent from Exports, got %q", name, value)
				}
			}
			for _, substr := range tc.wantWarningSubstrings {
				if !containsWarningSubstring(got.Warnings, substr) {
					t.Errorf("expected a warning mentioning %q, got %v", substr, got.Warnings)
				}
			}
			for _, substr := range tc.wantNoWarningSubstrings {
				if containsWarningSubstring(got.Warnings, substr) {
					t.Errorf("unexpected warning mentioning %q: %v", substr, got.Warnings)
				}
			}
		})
	}
}

// The go Strategy must pass its getenv parameter into computeBindings rather
// than discard the snapshot it is handed.
func TestStrategyEnvExports(t *testing.T) {
	if Strategy.EnvExports == nil {
		t.Fatal("go Strategy has nil EnvExports")
	}

	overrideEnv := stubGetenv(map[string]string{"GOTOOLCHAIN": "auto"})
	_, gotWarnings := Strategy.EnvExports(27182, "r0", overrideEnv, goDeclaredRoutes)
	if !containsWarningSubstring(gotWarnings, "GOTOOLCHAIN") {
		t.Errorf("expected a GOTOOLCHAIN override warning, got %v", gotWarnings)
	}

	emptyEnv := stubGetenv(nil)
	gotExports, gotWarnings2 := Strategy.EnvExports(27182, "r0", emptyEnv, goDeclaredRoutes)
	if len(gotWarnings2) != 0 {
		t.Errorf("expected no warnings with an empty env snapshot, got %v", gotWarnings2)
	}
	if value, ok := ecosystem.ExportValue(gotExports, "GOPROXY"); !ok || value != "http://127.0.0.1:27182/r0/go" {
		t.Errorf("GOPROXY export = (%q, %v), want (%q, true)", value, ok, "http://127.0.0.1:27182/r0/go")
	}
}

// The pre-#3260 Strategy discarded its routes parameter. A route declaring a go
// path renders a full-path GOPROXY the Strategy can only produce by passing
// routes through to computeBindings.
func TestStrategyEnvExports_ThreadsRoutes(t *testing.T) {
	routes := []registrymanifest.Route{{
		Prefix:     "r0",
		Ecosystems: registryvocab.RouteEcosystems{"go": registryvocab.RouteDeclaration{"path": "/artifactory/api/go/go-local"}},
	}}

	gotExports, _ := Strategy.EnvExports(27182, "r0", stubGetenv(nil), routes)

	want := "http://127.0.0.1:27182/r0/artifactory/api/go/go-local"
	if value, ok := ecosystem.ExportValue(gotExports, "GOPROXY"); !ok || value != want {
		t.Errorf("GOPROXY export = (%q, %v), want (%q, true)", value, ok, want)
	}
}

// stubGetenv stands in for os.Getenv so these tests never read the real
// process environment.
func stubGetenv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// The package is gomod because go is a keyword, but the Name must stay "go":
// routes files, discovery output and the [routes.ecosystems.go] block key on it.
func TestStrategy_Shape(t *testing.T) {
	s := Strategy
	if s.Name != "go" || s.Classification != "go mod" {
		t.Errorf("Name, Classification = %q, %q, want go, go mod", s.Name, s.Classification)
	}
	if s.EnvExports == nil || s.BindingEnvVar != "GOPROXY" {
		t.Errorf("go binds via env exports, got EnvExports set=%v BindingEnvVar=%q", s.EnvExports != nil, s.BindingEnvVar)
	}
	if s.HomeConfig != nil || s.RepoAwareHomeConfig != nil || s.InTreeConfigPath != "" {
		t.Errorf("go has no config file, got HomeConfig=%v RepoAwareHomeConfig=%v InTreeConfigPath=%q",
			s.HomeConfig != nil, s.RepoAwareHomeConfig != nil, s.InTreeConfigPath)
	}
	if want := []string{"netrc", "npmrc", "gradle-properties"}; !slices.Equal(s.CredentialStores, want) {
		t.Errorf("CredentialStores = %v, want %v", s.CredentialStores, want)
	}
	if s.RetiredRouteKey != "go-path" {
		t.Errorf("RetiredRouteKey = %q, want go-path", s.RetiredRouteKey)
	}
	if want := []string{"go.sum"}; !slices.Equal(s.LockfileNames, want) {
		t.Errorf("LockfileNames = %v, want %v", s.LockfileNames, want)
	}
}

// registryroutes resolves the retired go-path key through the registry.
func TestStrategy_ResolvesByRetiredRouteKey(t *testing.T) {
	row, ok := ecosystem.RowByRetiredRouteKey("go-path", []ecosystem.Strategy{Strategy})
	if !ok || row.Name != "go" {
		t.Errorf(`RowByRetiredRouteKey("go-path") = (%q, %v), want (go, true)`, row.Name, ok)
	}
}
