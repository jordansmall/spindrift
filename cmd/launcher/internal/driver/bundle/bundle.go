// Package bundle registers every bundled Driver's Strategy with the driver
// registry and every bundled backend's Strategy with the backendkit registry
// (ADR 0068 decision 1). Only binaries import it (ADR 0066 decision 8); the
// registries and the libraries that read them import no Driver or concrete
// backend adapter.
package bundle

import (
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/driver/pi"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/forge/local"
)

func init() {
	driver.Register(claude.Strategy)
	driver.Register(opencode.Strategy)
	driver.Register(pi.Strategy)

	// Registration order is the launcher's valid-name order in its error messages.
	backendkit.Register(github.Strategy)
	backendkit.Register(forgejo.Strategy)
	backendkit.Register(jira.Strategy)
	backendkit.Register(local.Strategy)
	backendkit.Register(git.Strategy)
}
