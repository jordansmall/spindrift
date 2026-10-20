// Package bundle registers every bundled Driver's Strategy with the driver
// registry (here), every bundled backend's Strategy with the backendkit
// registry (backends.go, ADR 0068 decision 1), and every runner family's
// Strategy with the runner registry (runners.go). Only binaries import it
// (ADR 0066 decision 8); the registries and the libraries that read them import
// no Driver or concrete backend adapter. The in-box filesets
// (lib/mkHarness.nix driverTreeFiles) drop backends.go and runners.go; only the
// Box adds backends.go back, so the other in-box binaries register no backends
// and no runners.
package bundle

import (
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/driver/pi"
)

func init() {
	driver.Register(claude.Strategy)
	driver.Register(opencode.Strategy)
	driver.Register(pi.Strategy)
}
