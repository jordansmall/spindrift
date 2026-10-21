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

// A Code Forge row without a LandedBanner would panic main's forgeLandedLine at
// the end of a run, so the gap is caught here instead.
func TestCodeForgeStrategiesHaveLandedBanner(t *testing.T) {
	for _, s := range backendkit.Strategies() {
		if s.ValidAsCodeForge && s.LandedBanner == nil {
			t.Errorf("Code Forge Strategy %q has no LandedBanner", s.Name)
		}
	}
}

// HostRepoDir is how core learns the Accumulation repo path, so a backend
// declares it exactly when its repo is host-mediated.
func TestHostRepoDirIffHostMediatedRemote(t *testing.T) {
	for _, s := range backendkit.Strategies() {
		if s.HostMediatedRemote != (s.HostRepoDir != nil) {
			t.Errorf("backend %q: HostMediatedRemote=%v but HostRepoDir set=%v", s.Name, s.HostMediatedRemote, s.HostRepoDir != nil)
		}
	}
}
