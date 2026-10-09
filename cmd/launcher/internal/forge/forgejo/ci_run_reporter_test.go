package forgejo_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
)

// Forgejo has no CI-run URL to report (issue #4962); a CIRunReporter
// implementation would silently light up a dashboard link it cannot back.
func TestForgejoCodeForge_DoesNotImplementCIRunReporter(t *testing.T) {
	cfg := forgejo.ForgejoCodeForgeConfig{BaseURL: "https://codeberg.org", Repo: "owner/repo", Token: "tok"}
	for name, cf := range map[string]forge.CodeForge{
		"base":     forgejo.NewForgejoCodeForge(cfg, nil),
		"readonly": forgejo.NewReadOnlyForgejoCodeForge(cfg, nil),
	} {
		if _, ok := cf.(forge.CIRunReporter); ok {
			t.Errorf("%s forgejo code forge implements forge.CIRunReporter, want it github-only", name)
		}
	}
}
