package driver

import (
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// TestReasonAliases_CoverAllDriverkitReasons pins the launcher-facing
// re-export surface (classification.go) to alias every driverkit.Reason.
// If driverkit gains a new Reason without a matching alias here, this test
// fails instead of the new Reason silently vanishing from launcher-facing
// code (issue #2269).
func TestReasonAliases_CoverAllDriverkitReasons(t *testing.T) {
	aliased := map[driverkit.Reason]bool{
		RateLimit:       true,
		Overloaded:      true,
		Network:         true,
		TaskFailed:      true,
		UnsupportedFlag: true,
	}
	for _, r := range driverkit.AllReasons {
		if !aliased[r] {
			t.Errorf("driverkit.Reason %q has no matching alias in classification.go", r)
		}
	}
	if len(aliased) != len(driverkit.AllReasons) {
		t.Errorf("classification.go aliases %d reasons, driverkit declares %d via AllReasons — update the alias block and this pin together", len(aliased), len(driverkit.AllReasons))
	}
}
