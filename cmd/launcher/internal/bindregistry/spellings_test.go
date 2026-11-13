package bindregistry

import (
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
)

// A discovery-only Strategy has nothing to rewrite at bind time.
func TestInTreeBindings_ExcludesDiscoveryConfigPathsOnlyStrategy(t *testing.T) {
	discoveryOnly := synthetic.Spellings("discovery-only", "a.cfg", "A.cfg")

	got := InTreeBindings([]ecosystem.Strategy{discoveryOnly, synthetic.Strategy})

	if len(got) != 1 || got[0].Name != synthetic.Strategy.Name {
		t.Errorf("InTreeBindings = %+v, want only %s", got, synthetic.Strategy.Name)
	}
}
