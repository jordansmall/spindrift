package forge_test

import (
	"os/exec"
	"testing"

	"spindrift.dev/launcher/internal/fixturebuild"
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
	return &fakePRForgeHarness{f: f}
}

func (h *fakePRForgeHarness) Forge() forge.PRForge       { return h.f }
func (h *fakePRForgeHarness) CodeForge() forge.CodeForge { return h.f }
func (h *fakePRForgeHarness) BranchPrefix() string       { return "agent/issue-" }

func (h *fakePRForgeHarness) PushOnlyCodeForge() forge.CodeForge { return h.f.AsPushOnly() }

func (h *fakePRForgeHarness) SeedOpenPR(num string) string {
	branch := forge.AgentBranchName(h.BranchPrefix(), num)
	url := "https://github.com/owner/repo/pull/" + num
	h.f.SetPR(branch, forge.PR{URL: url})
	return url
}

func (h *fakePRForgeHarness) SeedDraftPR(num string) string {
	url := h.SeedOpenPR(num)
	h.f.SetDraft(url, true)
	return url
}

func (h *fakePRForgeHarness) IsDraft(url string) bool { return h.f.IsDraft(url) }

func (h *fakePRForgeHarness) SeedCheckStates(url string, states []forge.RollupState) {
	h.f.SetCheckStates(url, states)
}

func (h *fakePRForgeHarness) SeedFailingCheck(url, name, conclusion, summary string) {
	h.f.SetFailureDetail(url, name+": "+conclusion+"\n"+summary)
}

func (h *fakePRForgeHarness) SeedFailingCheckLog(url, check, log string) {
	h.f.SetFailureDetail(url, forge.RenderFailureDetail([]forge.FailureDetailEntry{{Name: check, State: "FAILURE", Log: log}}))
}

func (h *fakePRForgeHarness) SeedAutoMergeAllowed(allowed bool) {
	h.f.AutoMergeAllowed = allowed
}

func (h *fakePRForgeHarness) SeedNeedsUpdate(url string, needsUpdate bool) {
	h.f.SetNeedsUpdate(url, needsUpdate)
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

// TestFake_PRForgeContract_RemoteEngine runs the PR-forge contract with the
// Fake's Connector in a child connector-host.
func TestFake_PRForgeContract_RemoteEngine(t *testing.T) {
	forge.UseRemoteSeam(t, func() *exec.Cmd { return fixturebuild.ConnectorHostCmd(t) })
	forgetest.RunPRForgeContract(t, newFakePRForgeHarness())
}
