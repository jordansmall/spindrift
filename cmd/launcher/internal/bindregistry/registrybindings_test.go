package bindregistry

import (
	"fmt"
	"testing"
)

func TestCargoConfigTOML_ExactContent(t *testing.T) {
	got := CargoConfigTOML(27182, "r0")
	want := `[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:27182/r0/"
`
	if got != want {
		t.Errorf("CargoConfigTOML(27182, %q) = %q, want %q", "r0", got, want)
	}
}

func TestCargoConfigTOML_PortInterpolated(t *testing.T) {
	for _, port := range []int{9999, 12345} {
		got := CargoConfigTOML(port, "r0")
		want := fmt.Sprintf(`[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:%d/r0/"
`, port)
		if got != want {
			t.Errorf("CargoConfigTOML(%d, %q) = %q, want %q", port, "r0", got, want)
		}
	}
}

// TestCargoConfigTOML_PrefixInterpolated pins that the route prefix, not
// just the port, lands in the rendered registry URL (issue #3142).
func TestCargoConfigTOML_PrefixInterpolated(t *testing.T) {
	got := CargoConfigTOML(27182, "artifactory-cargo")
	want := `[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:27182/artifactory-cargo/"
`
	if got != want {
		t.Errorf("CargoConfigTOML(27182, %q) = %q, want %q", "artifactory-cargo", got, want)
	}
}
