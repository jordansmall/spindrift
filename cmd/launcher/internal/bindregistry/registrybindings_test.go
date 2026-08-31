package bindregistry

import (
	"fmt"
	"testing"
)

func TestNpmFamilyBindings_ThreeExportsCorrectURL(t *testing.T) {
	got := NpmFamilyBindings(27182)
	if len(got) != 3 {
		t.Fatalf("len(NpmFamilyBindings) = %d, want 3", len(got))
	}
	want := "http://127.0.0.1:27182/"
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
	got := NpmFamilyBindings(9999)
	value, ok := exportValue(got, "npm_config_registry")
	if !ok {
		t.Fatalf("npm_config_registry missing from NpmFamilyBindings")
	}
	if value != "http://127.0.0.1:9999/" {
		t.Errorf("npm_config_registry = %q, want %q", value, "http://127.0.0.1:9999/")
	}
}

func TestCargoConfigTOML_ExactContent(t *testing.T) {
	got := CargoConfigTOML(27182)
	want := `[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:27182/"
`
	if got != want {
		t.Errorf("CargoConfigTOML(27182) = %q, want %q", got, want)
	}
}

func TestCargoConfigTOML_PortInterpolated(t *testing.T) {
	for _, port := range []int{9999, 12345} {
		got := CargoConfigTOML(port)
		want := fmt.Sprintf(`[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:%d/"
`, port)
		if got != want {
			t.Errorf("CargoConfigTOML(%d) = %q, want %q", port, got, want)
		}
	}
}
