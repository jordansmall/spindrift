package driver

import "testing"

// nixDriverNames is generated from lib/drivers/default.nix by nix/checks.nix;
// see drivernames_gen.go. Edit lib/drivers/default.nix and run `nix run .#regen`.

func TestParityWithNixDriverRegistry(t *testing.T) {
	goNames := Names()

	nixSet := make(map[string]bool, len(nixDriverNames))
	for _, n := range nixDriverNames {
		nixSet[n] = true
	}
	goSet := make(map[string]bool, len(goNames))
	for _, n := range goNames {
		goSet[n] = true
	}

	for _, n := range nixDriverNames {
		if !goSet[n] {
			t.Errorf("nix Driver %q has no matching Go strategy", n)
		}
	}
	for _, n := range goNames {
		if !nixSet[n] {
			t.Errorf("Go Driver strategy %q has no matching nix Driver", n)
		}
	}
}
