package launcherchecks

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/backend"
)

// registryIndex reports the position of name in backend.Registry, or -1.
func registryIndex(name string) int {
	for i, d := range backend.Registry {
		if d.Name == name {
			return i
		}
	}
	return -1
}

// assertRegistryFilter checks got against the registry entries whose axis
// flag (read by flag) is set: same membership, no duplicates, no extras,
// and Registry's own declaration order preserved.
func assertRegistryFilter(t *testing.T, got []string, flag func(backend.Descriptor) bool) {
	t.Helper()

	seen := map[string]bool{}
	prev := -1
	for _, name := range got {
		if seen[name] {
			t.Errorf("%q appears twice in %v", name, got)
		}
		seen[name] = true

		i := registryIndex(name)
		if i < 0 {
			t.Errorf("%q is not a backend.Registry entry, got %v", name, got)
			continue
		}
		if !flag(backend.Registry[i]) {
			t.Errorf("%q is in %v but its Registry descriptor doesn't declare that axis", name, got)
		}
		if i <= prev {
			t.Errorf("%q breaks backend.Registry's declaration order in %v", name, got)
		}
		prev = i
	}
	for _, d := range backend.Registry {
		if flag(d) && !seen[d.Name] {
			t.Errorf("%q declares the axis in backend.Registry but is missing from %v", d.Name, got)
		}
	}
}

// TestTrackerNamesFromRegistry_MirrorsRegistry proves the shared helper is a
// faithful, order-preserving filter of backend.Registry — the single source
// both the launcher-startup rows' "must be ..." list and any other consumer
// read, rather than a per-caller copy of the loop.
func TestTrackerNamesFromRegistry_MirrorsRegistry(t *testing.T) {
	got := TrackerNamesFromRegistry()
	if len(got) == 0 {
		t.Fatal("TrackerNamesFromRegistry() = empty, want the registry's tracker-valid backends")
	}
	assertRegistryFilter(t, got, func(d backend.Descriptor) bool { return d.ValidAsTracker })
}

// TestCodeForgeNamesFromRegistry_MirrorsRegistry is the CODE_FORGE axis's
// half of TestTrackerNamesFromRegistry_MirrorsRegistry.
func TestCodeForgeNamesFromRegistry_MirrorsRegistry(t *testing.T) {
	got := CodeForgeNamesFromRegistry()
	if len(got) == 0 {
		t.Fatal("CodeForgeNamesFromRegistry() = empty, want the registry's forge-valid backends")
	}
	assertRegistryFilter(t, got, func(d backend.Descriptor) bool { return d.ValidAsCodeForge })
}

// TestNamesFromRegistry_AxesDiffer guards the two helpers against being
// wired to the same flag: jira is tracker-only and git is forge-only, so a
// copy-paste of one filter into the other shows up here.
func TestNamesFromRegistry_AxesDiffer(t *testing.T) {
	trackers := TrackerNamesFromRegistry()
	forges := CodeForgeNamesFromRegistry()
	if slices.Contains(forges, "jira") {
		t.Errorf("CodeForgeNamesFromRegistry() = %v, want it to exclude the tracker-only %q", forges, "jira")
	}
	if slices.Contains(trackers, "git") {
		t.Errorf("TrackerNamesFromRegistry() = %v, want it to exclude the forge-only %q", trackers, "git")
	}
}
