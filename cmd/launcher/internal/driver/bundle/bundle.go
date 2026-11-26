// Package bundle names every natively linked Driver's Strategy (claude is a
// Connector, supplied by its Driver row) for a binary to hand its Connector
// runtime, and in sibling files registers the bundled backends (backends.go,
// ADR 0068 decision 1), ecosystems (ecosystems.go) and runner families
// (runners.go). Only binaries import it (ADR 0066 decision 8); the registries
// and their readers import no Driver or concrete adapter. Each sibling file
// says which in-box binaries carry it.
package bundle

import (
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/driver/pi"
)

// Drivers returns the Strategies a binary links in, for driver.NewSet.
func Drivers() []driverkit.Strategy {
	return []driverkit.Strategy{opencode.Strategy, pi.Strategy}
}
