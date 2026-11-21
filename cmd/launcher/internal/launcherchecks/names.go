package launcherchecks

import "spindrift.dev/launcher/internal/backend"

// TrackerNamesFromRegistry returns the Name of every backend.Registry entry
// valid as an ISSUE_TRACKER, in Registry's declaration order. cmd/launcher
// keeps its own list instead, because the backendkit registry can gain a
// Strategy at runtime (issue #2267).
func TrackerNamesFromRegistry() []string {
	var names []string
	for _, d := range backend.Registry {
		if d.ValidAsTracker {
			names = append(names, d.Name)
		}
	}
	return names
}

// CodeForgeNamesFromRegistry returns the Name of every backend.Registry entry
// valid as a CODE_FORGE, in Registry's declaration order.
func CodeForgeNamesFromRegistry() []string {
	var names []string
	for _, d := range backend.Registry {
		if d.ValidAsCodeForge {
			names = append(names, d.Name)
		}
	}
	return names
}
