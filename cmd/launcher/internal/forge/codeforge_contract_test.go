package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// trackingCodeForge wraps a forge.CodeForge, recording which refs Rebase
// landed successfully. Fake's own RebasedURLs records every attempt
// (including ones that error) — exactly what the settle package's call-count
// assertions need, but not what this harness's Rebased(num) landing check
// needs, so success is tracked separately rather than repurposing
// RebasedURLs.
type trackingCodeForge struct {
	forge.CodeForge
	rebasedOK map[string]bool
}

func (w *trackingCodeForge) Rebase(ref string) error {
	err := w.CodeForge.Rebase(ref)
	if err == nil {
		w.rebasedOK[ref] = true
	}
	return err
}

// fakeCodeForgeHarness adapts forge.Fake to forgetest.CodeForgeHarness. The
// Fake has no real git content, so Landed/Rebased read back bookkeeping
// (Merged, the tracking wrapper's rebasedOK) instead of inspecting a repo.
// It exposes the Fake's full CodeForge surface — the github-shaped side of
// the contract's PushOnly split.
type fakeCodeForgeHarness struct {
	f  *forge.Fake
	cf *trackingCodeForge
}

func newFakeCodeForgeHarness() *fakeCodeForgeHarness {
	f := forge.NewFake(testLabels)
	f.BranchPrefix = "agent/issue-"
	f.ProbeRepo = "owner/repo"
	return &fakeCodeForgeHarness{f: f, cf: &trackingCodeForge{CodeForge: f, rebasedOK: map[string]bool{}}}
}

func (h *fakeCodeForgeHarness) Forge() forge.CodeForge { return h.cf }

func (h *fakeCodeForgeHarness) Unreachable() forge.CodeForge {
	f := forge.NewFake(testLabels)
	f.BranchPrefix = h.f.BranchPrefix
	f.ProbeErr = forge.ErrRepoNotFound
	return f
}

func (h *fakeCodeForgeHarness) BranchPrefix() string { return h.f.BranchPrefix }

func (h *fakeCodeForgeHarness) SeedLandable(num string) string {
	return h.f.AgentBranch(num)
}

func (h *fakeCodeForgeHarness) AdvanceBase() {}

func (h *fakeCodeForgeHarness) Landed(num string) bool {
	return h.f.Merged == h.f.AgentBranch(num)
}

func (h *fakeCodeForgeHarness) Rebased(num string) bool {
	return h.cf.rebasedOK[h.f.AgentBranch(num)]
}

// FailNextMerge/FailNextRebase queue a scripted conflict on the Fake's
// existing MergeErrs/RebaseErrs — a FIFO drained in call order, not keyed by
// ref, so the contract's scenarios (which always Merge/Rebase the ref
// immediately after failing it) never need ref itself here.
func (h *fakeCodeForgeHarness) FailNextMerge(_ string) {
	h.f.MergeErrs = append(h.f.MergeErrs, forge.ErrMergeConflict)
}

func (h *fakeCodeForgeHarness) FailNextRebase(_ string) {
	h.f.RebaseErrs = append(h.f.RebaseErrs, forge.ErrMergeConflict)
}

// fakePushOnlyCodeForgeHarness wraps fakeCodeForgeHarness's Fake with
// AsPushOnly() — the git adapter's shape (no PRForge) — and adds the
// IsPushOnly marker so RunCodeForgeContract's MERGE_MODE mapping scenario
// exercises it (CONTEXT.md: "so the Fake and the git adapter cannot drift
// apart on it").
type fakePushOnlyCodeForgeHarness struct {
	*fakeCodeForgeHarness
}

func newFakePushOnlyCodeForgeHarness() *fakePushOnlyCodeForgeHarness {
	h := newFakeCodeForgeHarness()
	h.cf = &trackingCodeForge{CodeForge: h.f.AsPushOnly(), rebasedOK: map[string]bool{}}
	return &fakePushOnlyCodeForgeHarness{fakeCodeForgeHarness: h}
}

func (h *fakePushOnlyCodeForgeHarness) Unreachable() forge.CodeForge {
	f := forge.NewFake(testLabels)
	f.BranchPrefix = h.f.BranchPrefix
	f.ProbeErr = forge.ErrRepoNotFound
	return f.AsPushOnly()
}

func (h *fakePushOnlyCodeForgeHarness) IsPushOnly() {}

func TestFake_CodeForgeContract(t *testing.T) {
	forgetest.RunCodeForgeContract(t, newFakeCodeForgeHarness())
}

func TestFake_CodeForgeContract_PushOnly(t *testing.T) {
	forgetest.RunCodeForgeContract(t, newFakePushOnlyCodeForgeHarness())
}
