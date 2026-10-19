package bundle_test

import (
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
)

// backend.Registry is generated from lib/backends/*.nix by nix/regen.nix; the
// Bundle must register a Strategy for every row, carrying the same Descriptor.
func TestBackendStrategiesMatchNixRows(t *testing.T) {
	for _, d := range backend.Registry {
		s, ok := backendkit.ByName(d.Name)
		if !ok {
			t.Errorf("nix backend row %q has no registered Go Strategy", d.Name)
			continue
		}
		if s.Descriptor != d {
			t.Errorf("backend %q: Strategy descriptor %+v differs from nix row %+v", d.Name, s.Descriptor, d)
		}
	}
	for _, s := range backendkit.Strategies() {
		if _, ok := backend.ByName(s.Name); !ok {
			t.Errorf("Go backend Strategy %q has no matching nix backend row", s.Name)
		}
	}
}
