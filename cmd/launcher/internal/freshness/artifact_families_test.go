package freshness_test

import (
	"testing"

	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
)

// A runner row naming an artifact freshness has no adapter for would only
// surface at dispatch; this fails it at test time instead.
func TestEveryRunnerFamilyArtifactHasAnAdapter(t *testing.T) {
	for _, f := range runner.Families {
		art, err := freshness.LookupArtifact(f.Artifact)
		if err != nil {
			t.Errorf("runner family %q artifact %q has no freshness adapter: %v", f.Name, f.Artifact, err)
			continue
		}
		if art.Name != f.Artifact {
			t.Errorf("runner family %q artifact %q resolved to adapter %q", f.Name, f.Artifact, art.Name)
		}
	}
}
