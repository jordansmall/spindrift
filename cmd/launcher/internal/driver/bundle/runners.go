package bundle

import (
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/bwrap"
	"spindrift.dev/launcher/internal/runner/oci"
)

// Registers the runner families' Strategies. Kept in its own file because the
// in-box filesets (lib/harness-core.nix driverTreeFiles) drop it: the Box
// binaries link no runner, so runner churn never moves the image drvPath.
func init() {
	runner.Register(oci.Strategy)
	runner.Register(bwrap.Strategy)
}
