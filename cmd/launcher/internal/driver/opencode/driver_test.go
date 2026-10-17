package opencode_test

import (
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
)

// newDriver builds the opencode Driver from the package's own Strategy, the seam
// the reader tests in this package exercise.
func newDriver() driverkit.Driver {
	return driverkit.New(opencode.Strategy)
}
