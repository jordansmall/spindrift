package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// fakePRForgeHarness adapts forge.Fake to forgetest.PRForgeHarness. It
// exposes the Fake's full PRForge surface — the github-shaped side of the
// contract — and also its AsPushOnly() wrapper, so the same harness proves
// both halves of the optional-interface discovery scenario.
type fakePRForgeHarness struct {
	f *forge.Fake
}

func newFakePRForgeHarness() *fakePRForgeHarness {
	f := forge.NewFake(testLabels)
	f.BranchPrefix = "agent/issue-"
	return &fakePRForgeHarness{f: f}
}

func (h *fakePRForgeHarness) Forge() forge.PRForge       { return h.f }
func (h *fakePRForgeHarness) CodeForge() forge.CodeForge { return h.f }

func (h *fakePRForgeHarness) PushOnlyCodeForge() forge.CodeForge { return h.f.AsPushOnly() }

func (h *fakePRForgeHarness) SeedOpenPR(num string) string {
	branch := h.f.AgentBranch(num)
	url := "https://github.com/owner/repo/pull/" + num
	h.f.SetPR(branch, forge.PR{URL: url, IsDraft: false})
	return url
}

func (h *fakePRForgeHarness) SeedCheckStates(url string, states []forge.RollupState) {
	h.f.SetCheckStates(url, states)
}

func (h *fakePRForgeHarness) SeedFailingCheck(url, name, conclusion, summary string) {
	h.f.SetFailureDetail(url, name+": "+conclusion+"\n"+summary)
}

func (h *fakePRForgeHarness) SeedAutoMergeAllowed(allowed bool) {
	h.f.AutoMergeAllowed = allowed
}

func (h *fakePRForgeHarness) AutoMergeEnqueued(url string) bool {
	for _, u := range h.f.EnqueueAutoMergeCalls {
		if u == url {
			return true
		}
	}
	return false
}

func TestFake_PRForgeContract(t *testing.T) {
	forgetest.RunPRForgeContract(t, newFakePRForgeHarness())
}
