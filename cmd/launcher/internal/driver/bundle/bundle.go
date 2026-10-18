// Package bundle registers every bundled Driver's Strategy with the driver
// registry. Only binaries import it (ADR 0066 decision 8); the registry and
// the libraries that read it import no Driver.
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
