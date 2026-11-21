// Package bundle registers every natively linked Driver's Strategy (claude is
// a Connector, registered from its Driver row), and in sibling files the
// bundled backends (backends.go, ADR 0068 decision 1), ecosystems
// (ecosystems.go) and runner families (runners.go). Only binaries import it
// (ADR 0066 decision 8); the registries and their readers import no Driver or
// concrete adapter. Each sibling file says which in-box binaries carry it.
package bundle

import (
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/driver/pi"
)

func init() {
	driver.Register(opencode.Strategy)
	driver.Register(pi.Strategy)
}
