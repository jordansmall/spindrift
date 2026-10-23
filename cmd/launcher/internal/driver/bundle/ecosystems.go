package bundle

import (
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/gomod"
	"spindrift.dev/launcher/internal/ecosystem/gradle"
)

// Registers the bundled ecosystems' Strategies. Kept in its own file because
// lib/harness-core.nix driverTreeFiles drops it from every in-box fileset and
// only boxBin and driverExecBin add it back: of the in-box binaries only the
// Box and driver-exec bind an ecosystem, and the orchestrator must vendor
// none (its vendorHash). The host binaries build from the whole tree.
func init() {
	// The argument order is both the lockfile first-hit precedence and the
	// env-export file's line order.
	ecosystem.Register(ecosystem.Cargo, ecosystem.Npm, ecosystem.Yarn, ecosystem.Pnpm, gomod.Strategy, gradle.Strategy)
}
