package driver

import "testing"

// nixDriverNames mirrors the keys of lib/drivers/default.nix. Keep this list
// in sync by hand — mkHarness.nix throws on an unknown `driver` option value,
// and this test throws on a nix Driver name with no matching Go strategy (or
// vice versa), so the two registries cannot silently drift (ADR 0009).
var nixDriverNames = []string{"claude"}

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
