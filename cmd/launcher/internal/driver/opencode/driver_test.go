package opencode_test

import (
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
)

// newDriver builds the opencode Driver from the package's own Strategy, the seam
// the reader tests in this package exercise.
func newDriver() driverkit.Driver {
	return driverkit.New(opencode.Strategy)
}

func sessionFlags(t *testing.T, d driverkit.Driver, mode, slug, issue, home string) string {
	t.Helper()
	got, err := d.SessionFlags(mode, slug, issue, home)
	if err != nil {
		t.Fatalf("SessionFlags(%q): %v", mode, err)
	}
	return got
}
