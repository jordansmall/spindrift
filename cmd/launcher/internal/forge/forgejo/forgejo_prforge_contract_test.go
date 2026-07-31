package forgejo_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// prforgeHarness is a forgetest.PRForgeHarness backed by fakeForgejo, an
// in-memory stand-in for the Forgejo REST API's pull/commit-status/repo
// endpoints.
type prforgeHarness struct {
	*fakeForgejo
	cf forge.CodeForge
}

func newPRForgeHarness(t *testing.T) *prforgeHarness {
	t.Helper()
	f := newFakeForgejo(t)
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      f.URL(),
		Repo:         "owner/repo",
		Token:        "tok",
		BaseBranch:   "main",
		BranchPrefix: "agent/issue-",
		GitRemoteURL: "unused",
	})
	return &prforgeHarness{fakeForgejo: f, cf: cf}
}

func (h *prforgeHarness) Forge() forge.PRForge       { return h.cf.(forge.PRForge) }
func (h *prforgeHarness) CodeForge() forge.CodeForge { return h.cf }

func TestForgejoCodeForge_PRForgeContract(t *testing.T) {
	forgetest.RunPRForgeContract(t, newPRForgeHarness(t))
}
