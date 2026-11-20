package bundle_test

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
)

// builtinBackends stay native: they need filesystem and git capabilities
// Connectors are denied (ADR 0073 rule 7).
var builtinBackends = []string{backend.Local.Name, backend.Git.Name}

// pinnedNativeNetwork is the native network adapters still registered while
// their deletion is open: #6868 (forgejo's forge half) and #7122 (its tracker
// half). Each of those tickets removes this entry; nothing else belongs here.
var pinnedNativeNetwork = []string{"forgejo"}

// nativeNetworkBackends names every Strategy that is native yet neither a
// Built-in backend, connector-only, nor a pinned exception.
func nativeNetworkBackends(strats []backendkit.Strategy) []string {
	var native []string
	for _, s := range strats {
		if s.ConnectorOnly || slices.Contains(builtinBackends, s.Name) || slices.Contains(pinnedNativeNetwork, s.Name) {
			continue
		}
		native = append(native, s.Name)
	}
	return native
}

// ADR 0073 rule 7: after the migration window the Bundle registers no native
// network backend. The row's connectorOnly flag is the one definition of
// "native" (lib/connector-names.nix reads the same flag).
func TestBundleRegistersNoNativeNetworkAdapter(t *testing.T) {
	if got := nativeNetworkBackends(backendkit.Strategies()); len(got) != 0 {
		t.Errorf("Bundle registers native network adapter(s) %v: network backends are Connectors (ADR 0073), so set connectorOnly on the row or delete the adapter", got)
	}
}

func TestNativeNetworkBackendsReportsPlantedRegistration(t *testing.T) {
	t.Cleanup(backendkit.ReplaceRegistryForTest(backendkit.Strategies()))
	backendkit.Register(backendkit.Strategy{Descriptor: backend.Descriptor{Name: "planted", ValidAsTracker: true}})
	got := nativeNetworkBackends(backendkit.Strategies())
	if want := []string{"planted"}; !slices.Equal(got, want) {
		t.Errorf("nativeNetworkBackends = %v, want %v", got, want)
	}
}

// ConnectorRouted is the Strategy-side half of the row's connectorOnly: a
// connector-only row's adapter reads through its Connector.
func TestConnectorOnlyStrategiesAreConnectorRouted(t *testing.T) {
	for _, s := range backendkit.Strategies() {
		if s.ConnectorOnly && !s.ConnectorRouted {
			t.Errorf("connector-only backend %q is not ConnectorRouted", s.Name)
		}
	}
}
