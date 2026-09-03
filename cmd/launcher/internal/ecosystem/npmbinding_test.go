package ecosystem

import "testing"

func TestNpmFamilyBindings_ThreeExportsCorrectURL(t *testing.T) {
	got := NpmFamilyBindings(27182, "r0")
	if len(got) != 3 {
		t.Fatalf("len(NpmFamilyBindings) = %d, want 3", len(got))
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
	got := NpmFamilyBindings(9999, "r0")
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
	got := NpmFamilyBindings(27182, "artifactory-npm")
	value, ok := exportValue(got, "npm_config_registry")
	if !ok {
		t.Fatalf("npm_config_registry missing from NpmFamilyBindings")
	}
	if value != "http://127.0.0.1:27182/artifactory-npm/" {
		t.Errorf("npm_config_registry = %q, want %q", value, "http://127.0.0.1:27182/artifactory-npm/")
	}
}
