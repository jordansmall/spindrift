//go:build integration

package inputdoc_test

import (
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/seamtest"
)

// TestSeamLoadRenderedRunInput feeds Load the document Nix actually renders
// (the bats harness's run document), pinning the Nix-to-Go schema seam that
// the hand-written Document struct and the nix renderer must agree on.
func TestSeamLoadRenderedRunInput(t *testing.T) {
	doc, err := inputdoc.Load(seamtest.Path(t, "launcher-run-input.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, c := range []struct {
		section map[string]string
		name    string
		key     string
		want    string
	}{
		{doc.Settings, "settings", "COMPLETE_LABEL", "agent-complete"},
		{doc.Artifacts, "artifacts", "RUNTIME", "podman"},
		{doc.Artifacts, "artifacts", "RUNNER_KIND", "oci"},
	} {
		if got := c.section[c.key]; got != c.want {
			t.Errorf("%s[%q] = %q, want %q", c.name, c.key, got, c.want)
		}
	}
}
