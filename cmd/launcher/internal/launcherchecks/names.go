package launcherchecks

import "spindrift.dev/launcher/internal/backend"

// TrackerNamesFromRegistry returns the Name of every backend.Registry entry
// valid as an ISSUE_TRACKER, in Registry's declaration order — the list the
// issue-tracker-config row renders its "must be ..." remedy from.
//
// cmd/launcher keeps its own backendRows-sourced list for Deps.TrackerNames
// rather than calling this: backendRows is a package-level slice a caller
// can append to at runtime (the issue #2267 AC5 extensibility guarantee),
// and validate()'s ISSUE_TRACKER-invalid message renders through that same
// seam, so a Registry-sourced list there would silently drop the
// runtime-registered backend. Quickstart has no such extension point, so
// the registry is the whole story for it.
func TrackerNamesFromRegistry() []string {
	var names []string
	for _, d := range backend.Registry {
		if d.ValidAsTracker {
			names = append(names, d.Name)
		}
	}
	return names
}

// CodeForgeNamesFromRegistry returns the Name of every backend.Registry
// entry valid as a CODE_FORGE, in Registry's declaration order. See
// TrackerNamesFromRegistry for why cmd/launcher doesn't share it.
func CodeForgeNamesFromRegistry() []string {
	var names []string
	for _, d := range backend.Registry {
		if d.ValidAsCodeForge {
			names = append(names, d.Name)
		}
	}
	return names
}
