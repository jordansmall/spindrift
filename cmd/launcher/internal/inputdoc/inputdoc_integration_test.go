//go:build integration

package inputdoc_test

import (
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/seamtest"
)

// TestSeamLoadRenderedRunInput feeds Load the document Nix actually renders
// (the bats harness's run document), pinning the Nix-to-Go schema seam that
// the Document struct and the nix renderer must agree on.
func TestSeamLoadRenderedRunInput(t *testing.T) {
	doc, err := inputdoc.Load(seamtest.Path(t, "launcher-run-input.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := doc.Artifacts
	for _, c := range []struct {
		name string
		got  string
		want string
	}{
		{"settings[COMPLETE_LABEL]", doc.Settings["COMPLETE_LABEL"], "agent-complete"},
		{"artifacts.Shared.Runtime", a.Shared.Runtime.String(), "podman"},
		{"artifacts.Shared.RunnerKind", a.Shared.RunnerKind.String(), "oci"},
		{"artifacts.Shared.BoxArtifact", a.Shared.BoxArtifact.String(), "oci-image"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if a.OCI == nil || a.Bwrap != nil {
		t.Errorf("family parts: OCI set = %v, Bwrap set = %v; want only OCI", a.OCI != nil, a.Bwrap != nil)
	}
}
