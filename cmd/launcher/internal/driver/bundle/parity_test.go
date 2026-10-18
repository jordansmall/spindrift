package bundle_test

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// The descriptors behind DescriptorNames are generated from lib/drivers/*.nix
// by nix/regen.nix; see descriptor_gen.go. Edit the Driver rows and run
// `nix run .#regen`.
func TestParityWithNixDriverRegistry(t *testing.T) {
	goNames := driver.Names()
	nixNames := driver.DescriptorNames()

	for _, n := range nixNames {
		if !slices.Contains(goNames, n) {
			t.Errorf("nix Driver %q has no matching Go strategy", n)
		}
	}
	for _, n := range goNames {
		if !slices.Contains(nixNames, n) {
			t.Errorf("Go Driver strategy %q has no matching nix Driver", n)
		}
	}
}

// TestNewDefaultsToClaude verifies that an empty DRIVER value resolves to the
// "claude" strategy, matching the nix side's default (ADR 0009).
func TestNewDefaultsToClaude(t *testing.T) {
	d, err := driver.New("")
	if err != nil {
		t.Fatalf("New(\"\"): %v", err)
	}
	if d.Name() != "claude" {
		t.Errorf("Name(): got %q, want %q", d.Name(), "claude")
	}
}
