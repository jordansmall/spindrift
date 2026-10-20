package bundle

import (
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/forge/local"
)

// Registers the bundled backends' Strategies. Kept in its own file because
// lib/harness-core.nix driverTreeFiles drops it from every in-box fileset
// and only boxBin adds it back: only the Box looks a backend up at runtime
// (ADR 0068 decision 1), so backend churn never moves the other in-box
// binaries' drvPaths.
func init() {
	// Registration order is the launcher's valid-name order in its error messages.
	backendkit.Register(github.Strategy)
	backendkit.Register(forgejo.Strategy)
	backendkit.Register(jira.Strategy)
	backendkit.Register(local.Strategy)
	backendkit.Register(git.Strategy)
}
