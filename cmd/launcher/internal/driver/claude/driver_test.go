package claude_test

import (
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// newDriver builds the claude Driver from the package's own Strategy, the seam
// the reader tests in this package exercise.
func newDriver(t *testing.T) driverkit.Driver {
	t.Helper()
	return driverkit.New(claude.Strategy)
}
