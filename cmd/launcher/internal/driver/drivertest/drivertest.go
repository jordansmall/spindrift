// Package drivertest holds the assertion a binary's test runs to prove it
// links the Driver bundle.
package drivertest

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// RequireBundled fails t unless the calling binary registers exactly the
// Drivers the Nix registry declares and refuses any other name.
func RequireBundled(t testing.TB) {
	t.Helper()
	if got, want := driver.Names(), driver.DescriptorNames(); !slices.Equal(got, want) {
		t.Errorf("registered Drivers = %v, want %v: the binary must import internal/driver/bundle", got, want)
	}
	if _, err := driver.New("no-such-driver"); err == nil {
		t.Error(`driver.New("no-such-driver") succeeded, want an unknown-DRIVER error`)
	}
}
