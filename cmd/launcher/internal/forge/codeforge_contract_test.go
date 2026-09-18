package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// trackingCodeForge wraps a forge.CodeForge, recording which refs Rebase
// landed successfully. Fake's own RebasedURLs records every attempt,
// including ones that error, which is what the settle package's call-count
// assertions need but not what this harness's Rebased(num) landing check
// needs.
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
// It exposes the Fake's full CodeForge API, the github-shaped side of the
// contract's PushOnly split.
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
// existing MergeErrs/RebaseErrs, a FIFO drained in call order rather than
// keyed by ref, so the contract's scenarios (which always Merge/Rebase the
// ref immediately after failing it) never need ref itself here.
func (h *fakeCodeForgeHarness) FailNextMerge(_ string) {
	h.f.MergeErrs = append(h.f.MergeErrs, forge.ErrMergeConflict)
}

func (h *fakeCodeForgeHarness) FailNextRebase(_ string) {
	h.f.RebaseErrs = append(h.f.RebaseErrs, forge.ErrMergeConflict)
}

// fakePushOnlyCodeForgeHarness wraps fakeCodeForgeHarness's Fake with
// AsPushOnly(), the git adapter's shape with no PRForge, and adds the
// IsPushOnly marker so RunCodeForgeContract's MERGE_MODE mapping scenario
// exercises it, keeping the Fake and the git adapter from drifting apart on
// it (CONTEXT.md).
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

// fakeLocalCodeForgeHarness adapts forge.Fake to forgetest.CodeForgeHarness
// via AsLocal(), CODE_FORGE=local's shape, plus forgetest.LandingHarness, so
// RunCodeForgeContract's landing scenario (issue #1809) drives the Fake's
// scripted LandingContainmentQuery/LandingRepair with the same assertions it
// drives against the real local adapter's git-backed one.
type fakeLocalCodeForgeHarness struct {
	t      *testing.T
	f      *forge.Fake
	cf     *trackingCodeForge
	parent string
}

func newFakeLocalCodeForgeHarness(t *testing.T) *fakeLocalCodeForgeHarness {
	f := forge.NewFake(testLabels)
	f.BranchPrefix = "agent/issue-"
	f.ProbeRepo = "owner/repo"
	return &fakeLocalCodeForgeHarness{
		t:      t,
		f:      f,
		cf:     &trackingCodeForge{CodeForge: f.AsLocal(), rebasedOK: map[string]bool{}},
		parent: "1694",
	}
}

// localTrackingCodeForge forwards the local-shaped optional interfaces
// (LandingContainmentQuery, LandingRepair) that trackingCodeForge's interface
// embedding hides from type assertions: Go promotes only an embedded
// interface field's declared methods, not its dynamic value's. It stays
// local-only so the github-shaped wrapper claims no capability its Fake lacks.
type localTrackingCodeForge struct {
	*trackingCodeForge
}

func (w localTrackingCodeForge) LandingContained(landing forge.Landing, scope forge.SeedScope) (bool, error) {
	return w.CodeForge.(forge.LandingContainmentQuery).LandingContained(landing, scope)
}

func (w localTrackingCodeForge) IntegrationTip(parent string) (string, error) {
	return w.CodeForge.(forge.LandingRepair).IntegrationTip(parent)
}

func (h *fakeLocalCodeForgeHarness) Forge() forge.CodeForge { return localTrackingCodeForge{h.cf} }

func (h *fakeLocalCodeForgeHarness) Unreachable() forge.CodeForge {
	f := forge.NewFake(testLabels)
	f.BranchPrefix = h.f.BranchPrefix
	f.ProbeErr = forge.ErrRepoNotFound
	return f.AsLocal()
}

func (h *fakeLocalCodeForgeHarness) BranchPrefix() string { return h.f.BranchPrefix }

func (h *fakeLocalCodeForgeHarness) SeedLandable(num string) string {
	return h.f.AgentBranch(num)
}

func (h *fakeLocalCodeForgeHarness) AdvanceBase() {}

func (h *fakeLocalCodeForgeHarness) Landed(num string) bool {
	return h.f.Merged == h.f.AgentBranch(num)
}

func (h *fakeLocalCodeForgeHarness) Rebased(num string) bool {
	return h.cf.rebasedOK[h.f.AgentBranch(num)]
}

func (h *fakeLocalCodeForgeHarness) FailNextMerge(_ string) {
	h.f.MergeErrs = append(h.f.MergeErrs, forge.ErrMergeConflict)
}

func (h *fakeLocalCodeForgeHarness) FailNextRebase(_ string) {
	h.f.RebaseErrs = append(h.f.RebaseErrs, forge.ErrMergeConflict)
}

func (h *fakeLocalCodeForgeHarness) Parent() string { return h.parent }

// Scope implements forgetest.LandingHarness (issue #2151): the harness's own
// parent paired with an operator-facing Integration branch label, mirroring
// local.IntegrationBranch's "integration/<parent>" grammar closely enough
// for diagnostics, though the Fake never actually renders it.
func (h *fakeLocalCodeForgeHarness) Scope() forge.SeedScope {
	return forge.NewSeedScope(h.parent, "integration/"+h.parent)
}

// MarkLanded implements forgetest.LandingHarness: it merges num's branch as
// scripted bookkeeping, since the Fake has no real git backing, and scripts
// the Fake's LandingContainmentQuery/LandingRepair to agree with the landing
// string it returns, as the real local adapter's git ancestry check would
// once Merge lands the branch onto the Integration branch.
func (h *fakeLocalCodeForgeHarness) MarkLanded(num string) string {
	h.t.Helper()
	branch := h.f.AgentBranch(num)
	if err := h.cf.Merge(branch); err != nil {
		h.t.Fatalf("Merge(%q): %v", branch, err)
	}
	landing := "integration/" + h.parent + "@" + num + "sha"
	h.f.SetLandingContained(landing, h.parent, true, nil)
	h.f.SetLandingContained(branch, h.parent, true, nil)
	h.f.SetIntegrationTip(h.parent, landing)
	return landing
}

func TestFake_LocalCodeForgeContract(t *testing.T) {
	forgetest.RunCodeForgeContract(t, newFakeLocalCodeForgeHarness(t))
}
