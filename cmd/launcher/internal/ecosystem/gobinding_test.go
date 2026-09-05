package ecosystem

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
)

func containsWarningSubstring(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestComputeGoBindings is table-driven over every GoBindingInput combination
// ComputeGoBindings branches on. Each case's assertions mirror one (or more)
// of the previously-separate single-shape test functions this table
// replaces -- see the case name for which behavior it pins.
func TestComputeGoBindings(t *testing.T) {
	cases := []struct {
		name   string
		port   int
		prefix string
		// routes defaults to nil, the pre-#3260 legacy contract -- most
		// cases below leave it unset since they're pinning the env-driven
		// GOTOOLCHAIN/GONOPROXY/GOSUMDB decisions, which routes never
		// affects; only the GOPROXY-route cases below set it.
		routes []registrymanifest.Route
		input  GoBindingInput

		// wantExports asserts an export is present with exactly this value.
		wantExports map[string]string
		// wantAbsentExports asserts an export is entirely absent (not just
		// empty-valued).
		wantAbsentExports []string
		// wantWarningSubstrings asserts some warning contains this substring.
		wantWarningSubstrings []string
		// wantNoWarningSubstrings asserts no warning contains this substring.
		wantNoWarningSubstrings []string
	}{
		{
			name:        "GOPROXY always present",
			port:        27182,
			prefix:      "r0",
			input:       GoBindingInput{},
			wantExports: map[string]string{"GOPROXY": "http://127.0.0.1:27182/r0"},
		},
		{
			name:                    "no prior GOTOOLCHAIN pins local without warning",
			port:                    27182,
			prefix:                  "r0",
			input:                   GoBindingInput{},
			wantExports:             map[string]string{"GOTOOLCHAIN": "local"},
			wantNoWarningSubstrings: []string{"GOTOOLCHAIN"},
		},
		{
			name:                    "prior GOTOOLCHAIN already local warns nothing",
			port:                    27182,
			prefix:                  "r0",
			input:                   GoBindingInput{GOTOOLCHAIN: "local"},
			wantExports:             map[string]string{"GOTOOLCHAIN": "local"},
			wantNoWarningSubstrings: []string{"GOTOOLCHAIN"},
		},
		{
			name:                  "prior GOTOOLCHAIN=auto warns",
			port:                  27182,
			prefix:                "r0",
			input:                 GoBindingInput{GOTOOLCHAIN: "auto"},
			wantExports:           map[string]string{"GOTOOLCHAIN": "local"},
			wantWarningSubstrings: []string{"auto"},
		},
		{
			name:                    "no prior GONOPROXY or GOPRIVATE forces none without warning",
			port:                    27182,
			prefix:                  "r0",
			input:                   GoBindingInput{},
			wantExports:             map[string]string{"GONOPROXY": "none"},
			wantNoWarningSubstrings: []string{"GONOPROXY"},
		},
		{
			name:                  "prior GONOPROXY set warns",
			port:                  27182,
			prefix:                "r0",
			input:                 GoBindingInput{GONOPROXY: "example.com/*"},
			wantWarningSubstrings: []string{"GONOPROXY"},
		},
		{
			name:                  "prior GOPRIVATE set warns GONOPROXY override",
			port:                  27182,
			prefix:                "r0",
			input:                 GoBindingInput{GOPRIVATE: "example.com/*"},
			wantWarningSubstrings: []string{"GONOPROXY"},
		},
		{
			name:                    "GOSUMDB off when no exemption declared",
			port:                    27182,
			prefix:                  "r0",
			input:                   GoBindingInput{},
			wantExports:             map[string]string{"GOSUMDB": "off"},
			wantNoWarningSubstrings: []string{"GOSUMDB"},
		},
		{
			name:                  "GOSUMDB off warns when prior value set",
			port:                  27182,
			prefix:                "r0",
			input:                 GoBindingInput{GOSUMDB: "sum.golang.org"},
			wantExports:           map[string]string{"GOSUMDB": "off"},
			wantWarningSubstrings: []string{"sum.golang.org"},
		},
		{
			name:                    "GOPRIVATE set leaves GOSUMDB alone",
			port:                    27182,
			prefix:                  "r0",
			input:                   GoBindingInput{GOPRIVATE: "example.com/*", GOSUMDB: "sum.golang.org"},
			wantAbsentExports:       []string{"GOSUMDB"},
			wantNoWarningSubstrings: []string{"GOSUMDB"},
		},
		{
			name:                    "GONOSUMDB set leaves GOSUMDB alone",
			port:                    27182,
			prefix:                  "r0",
			input:                   GoBindingInput{GONOSUMDB: "example.com/*", GOSUMDB: "sum.golang.org"},
			wantAbsentExports:       []string{"GOSUMDB"},
			wantNoWarningSubstrings: []string{"GOSUMDB"},
		},
		{
			name:   "GOPROXY interpolates the given port",
			port:   9999,
			prefix: "r0",
			input: GoBindingInput{
				GOTOOLCHAIN: "auto",
				GONOPROXY:   "example.com/*",
				GOSUMDB:     "sum.golang.org",
			},
			// GOTOOLCHAIN/GONOPROXY/GOSUMDB overrides here are the same
			// forced values pinned by the dedicated single-field cases
			// above; asserting them here too confirms the port-only change
			// doesn't perturb the other three exports this input also
			// populates. The warnings those same inputs trigger are
			// already precisely covered by "prior GOTOOLCHAIN=auto warns",
			// "prior GONOPROXY set warns", and "GOSUMDB off warns when
			// prior value set" above, so this case stays Exports-only.
			wantExports: map[string]string{
				"GOPROXY":     "http://127.0.0.1:9999/r0",
				"GOTOOLCHAIN": "local",
				"GONOPROXY":   "none",
				"GOSUMDB":     "off",
			},
		},
		{
			// Distinct from the port-interpolation case above: pins that the
			// route prefix, not just the port, lands in GOPROXY (issue #3142
			// -- bindings mode has no per-ecosystem route mapping, so it
			// binds to the first manifest route's prefix).
			name:        "GOPROXY interpolates the given prefix",
			port:        27182,
			prefix:      "artifactory-go",
			input:       GoBindingInput{},
			wantExports: map[string]string{"GOPROXY": "http://127.0.0.1:27182/artifactory-go"},
		},
		{
			// A legacy (non-host-rooted) route with routes present still
			// renders the bare prefix, the same branch the nil-routes cases
			// above take (issue #3260, mirroring NpmFamilyBindings' own
			// non-host-rooted case).
			name:        "non-host-rooted route renders bare prefix unchanged",
			port:        27182,
			prefix:      "r0",
			routes:      []registrymanifest.Route{{Prefix: "r0", HostRooted: false}},
			input:       GoBindingInput{},
			wantExports: map[string]string{"GOPROXY": "http://127.0.0.1:27182/r0"},
		},
		{
			name:   "host-rooted route with one go-tagged path renders full-path GOPROXY",
			port:   27182,
			prefix: "r0",
			routes: []registrymanifest.Route{{
				Prefix:     "r0",
				HostRooted: true,
				EnforcedPaths: []registrymanifest.EcosystemPath{
					{Ecosystem: "go", Path: "/artifactory/api/go/go-local"},
				},
			}},
			input:       GoBindingInput{},
			wantExports: map[string]string{"GOPROXY": "http://127.0.0.1:27182/r0/artifactory/api/go/go-local"},
		},
		{
			// AC3's fallback: a host-rooted route declaring no "go"-tagged
			// path leaves GOPROXY entirely unexported (not the bare-root
			// URL, which was never declared for it). GONOPROXY=none and
			// GOSUMDB=off go with it: with nothing routed through the
			// Forwarder, GONOPROXY=none would force a repo's private paths
			// out to Go's own default public proxy, and GOSUMDB=off would
			// drop checksum-database verification for fetches no longer
			// passing through a controlled mirror. GOTOOLCHAIN=local
			// survives -- it is a fact about this Box's single baked
			// toolchain, not about routing.
			name:              "host-rooted route with no go-tagged path leaves GOPROXY, GONOPROXY and GOSUMDB unset",
			port:              27182,
			prefix:            "r0",
			routes:            []registrymanifest.Route{{Prefix: "r0", HostRooted: true}},
			input:             GoBindingInput{},
			wantAbsentExports: []string{"GOPROXY", "GONOPROXY", "GOSUMDB"},
			wantExports: map[string]string{
				"GOTOOLCHAIN": "local",
			},
		},
		{
			// Each override warning is gated with the export it announces:
			// with no GOPROXY export there is no override to report, and
			// the GONOPROXY wording ("every module path, private or not,
			// now routes through the Forwarder") would be a lie.
			name:   "host-rooted route with no go-tagged path suppresses the GONOPROXY and GOSUMDB warnings",
			port:   27182,
			prefix: "r0",
			routes: []registrymanifest.Route{{Prefix: "r0", HostRooted: true}},
			input: GoBindingInput{
				GOTOOLCHAIN: "auto",
				GONOPROXY:   "example.com/*",
				GOSUMDB:     "sum.golang.org",
			},
			wantAbsentExports:       []string{"GOPROXY", "GONOPROXY", "GOSUMDB"},
			wantExports:             map[string]string{"GOTOOLCHAIN": "local"},
			wantWarningSubstrings:   []string{"GOTOOLCHAIN"},
			wantNoWarningSubstrings: []string{"GONOPROXY", "GOSUMDB"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeGoBindings(tc.port, tc.prefix, tc.routes, tc.input)

			for name, want := range tc.wantExports {
				value, ok := ExportValue(got.Exports, name)
				if !ok || value != want {
					t.Errorf("%s export = (%q, %v), want (%q, true)", name, value, ok, want)
				}
			}
			for _, name := range tc.wantAbsentExports {
				if value, ok := ExportValue(got.Exports, name); ok {
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
