package forgetest

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// CodeForgeHarness lets RunCodeForgeContract drive a CodeForge's scripted
// backend without knowing which adapter it is.
type CodeForgeHarness interface {
	// Forge returns the CodeForge under test, backed by a reachable remote.
	Forge() forge.CodeForge
	// Unreachable returns a CodeForge pointed at a backend Probe cannot reach.
	Unreachable() forge.CodeForge
	// BranchPrefix returns the prefix AgentBranch prepends to an issue number.
	BranchPrefix() string
	// SeedLandable creates a landable artifact for issue num, one commit ahead
	// of the current base tip and carrying a marker unique to num, and returns
	// whatever ref Merge and Rebase expect for it (a branch name for git and
	// the Fake, a PR URL for github and forgejo).
	SeedLandable(num string) string
	// AdvanceBase adds a commit to the base branch so every already-seeded ref
	// falls behind it, which is the state Rebase exists to fix.
	AdvanceBase()
	// Landed reports whether num's marker has reached the base branch.
	Landed(num string) bool
	// Rebased reports whether num's ref incorporates the base branch's latest
	// commit.
	Rebased(num string) bool
	// FailNextMerge arranges for ref's next Merge to fail with
	// forge.ErrMergeConflict.
	FailNextMerge(ref string)
	// FailNextRebase arranges for ref's next Rebase to fail with
	// forge.ErrMergeConflict.
	FailNextRebase(ref string)
}

// PushOnly marks a harness whose CodeForge has no PR concept, so
// RunCodeForgeContract runs the push-only MERGE_MODE scenario only against it
// (CONTEXT.md). github and forgejo implement PRForge and never implement this.
type PushOnly interface {
	IsPushOnly()
}

// LandingHarness marks a local-shaped harness, whose CodeForge also implements
// LandingContainmentQuery and LandingRepair (ADR 0029, ADR 0033, issue #1809,
// issue #2151). github and forgejo do not implement it.
type LandingHarness interface {
	// Parent returns the broad-ticket key the CodeForge checks IntegrationTip
	// against.
	Parent() string
	// Scope returns the forge.SeedScope LandingContained checks containment
	// against: this harness's parent paired with its adapter-rendered
	// Integration branch label (issue #2151).
	Scope() forge.SeedScope
	// MarkLanded merges num's SeedLandable ref and returns the resulting
	// IntegrationRef landing string in LandingContained's own "<branch>@<sha>"
	// grammar, so it can be handed straight back in.
	MarkLanded(num string) string
}

// RunCodeForgeContract runs the shared CodeForge conformance suite against h.
// Every adapter package calls it from its own test file with its own
// scripted-backend harness.
func RunCodeForgeContract(t *testing.T, h CodeForgeHarness) {
	t.Run("AgentBranchNaming", func(t *testing.T) { testAgentBranchNaming(t, h) })
	t.Run("MergeLandsRef", func(t *testing.T) { testMergeLandsRef(t, h) })
	t.Run("MergeConflict", func(t *testing.T) { testMergeConflict(t, h) })
	t.Run("RebaseIncorporatesBase", func(t *testing.T) { testRebaseIncorporatesBase(t, h) })
	t.Run("RebaseConflict", func(t *testing.T) { testRebaseConflict(t, h) })
	t.Run("Probe", func(t *testing.T) { testProbe(t, h) })
	t.Run("PushOnlyMergeModeMapping", func(t *testing.T) { testPushOnlyMergeModeMapping(t, h) })
	t.Run("LandingContainment", func(t *testing.T) { testLandingContainment(t, h) })
}

// testLandingContainment pins the collapsed LandingContained and LandingRepair
// contract (issue #1809, issue #2151): after MarkLanded, LandingContained
// agrees for both the resolved IntegrationRef and the raw BranchRef shape, and
// IntegrationTip resolves the same landing. The test skips a harness that does
// not implement LandingHarness.
func testLandingContainment(t *testing.T, h CodeForgeHarness) {
	lh, ok := h.(LandingHarness)
	if !ok {
		return
	}
	cf := h.Forge()
	q, ok := cf.(forge.LandingContainmentQuery)
	if !ok {
		t.Fatal("LandingHarness's Forge() does not implement forge.LandingContainmentQuery")
	}
	repair, ok := cf.(forge.LandingRepair)
	if !ok {
		t.Fatal("LandingHarness's Forge() does not implement forge.LandingRepair")
	}
	scope := lh.Scope()

	const num = "906"
	branch := h.SeedLandable(num)
	branchLanding := forge.Landing{Kind: forge.LandingBranchRef, Branch: branch}
	if contained, err := q.LandingContained(branchLanding, scope); err != nil || contained {
		t.Fatalf("LandingContained(%q) before merge = (%v, %v), want (false, nil)", branch, contained, err)
	}

	landingStr := lh.MarkLanded(num)
	landing, err := forge.ParseLanding(landingStr)
	if err != nil {
		t.Fatalf("ParseLanding(%q): %v", landingStr, err)
	}

	if contained, err := q.LandingContained(landing, scope); err != nil || !contained {
		t.Fatalf("LandingContained(%q) after merge = (%v, %v), want (true, nil)", landingStr, contained, err)
	}
	if contained, err := q.LandingContained(branchLanding, scope); err != nil || !contained {
		t.Fatalf("LandingContained(%q) after merge = (%v, %v), want (true, nil)", branch, contained, err)
	}
	tip, err := repair.IntegrationTip(lh.Parent())
	if err != nil {
		t.Fatalf("IntegrationTip(%q): %v", lh.Parent(), err)
	}
	if tip != landingStr {
		t.Fatalf("IntegrationTip(%q) = %q, want %q (the Integration branch's tip is unchanged since the merge)", lh.Parent(), tip, landingStr)
	}
}

// testAgentBranchNaming pins the seam as the single owner of the branch-prefix
// rule (issue #444).
func testAgentBranchNaming(t *testing.T, h CodeForgeHarness) {
	got := h.Forge().AgentBranch("909")
	want := h.BranchPrefix() + "909"
	if got != want {
		t.Fatalf("AgentBranch(909) = %q, want %q", got, want)
	}
}

// testMergeLandsRef pins the MERGE_MODE=immediate mapping.
func testMergeLandsRef(t *testing.T, h CodeForgeHarness) {
	const num = "101"
	ref := h.SeedLandable(num)
	if err := h.Forge().Merge(ref); err != nil {
		t.Fatalf("Merge(%q): %v", ref, err)
	}
	if !h.Landed(num) {
		t.Fatalf("Merge(%q) reported success but %s's marker never reached the base branch", ref, num)
	}
}

// testMergeConflict requires forge.ErrMergeConflict, not a generic error, so a
// caller can tell a conflict from a broken backend.
func testMergeConflict(t *testing.T, h CodeForgeHarness) {
	const num = "102"
	ref := h.SeedLandable(num)
	h.FailNextMerge(ref)
	err := h.Forge().Merge(ref)
	if !errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Merge(%q): want forge.ErrMergeConflict, got %v", ref, err)
	}
	if h.Landed(num) {
		t.Fatalf("Merge(%q) reported a conflict but %s's marker landed on the base branch anyway", ref, num)
	}
}

func testRebaseIncorporatesBase(t *testing.T, h CodeForgeHarness) {
	const num = "103"
	ref := h.SeedLandable(num)
	h.AdvanceBase()
	if err := h.Forge().Rebase(ref); err != nil {
		t.Fatalf("Rebase(%q): %v", ref, err)
	}
	if !h.Rebased(num) {
		t.Fatalf("Rebase(%q) reported success but %s never incorporated the base branch's latest commit", ref, num)
	}
}

// testRebaseConflict requires forge.ErrMergeConflict, not a generic error.
func testRebaseConflict(t *testing.T, h CodeForgeHarness) {
	const num = "104"
	ref := h.SeedLandable(num)
	h.FailNextRebase(ref)
	err := h.Forge().Rebase(ref)
	if !errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Rebase(%q): want forge.ErrMergeConflict, got %v", ref, err)
	}
}

func testProbe(t *testing.T, h CodeForgeHarness) {
	if _, err := h.Forge().Probe(); err != nil {
		t.Fatalf("Probe on reachable backend: %v", err)
	}
	if _, err := h.Unreachable().Probe(); err == nil {
		t.Fatal("Probe on unreachable backend: want error, got nil")
	}
}

// testPushOnlyMergeModeMapping pins the push-only half of the MERGE_MODE
// mapping that lives at the CodeForge seam (CONTEXT.md): auto has no meaning
// without a PRForge to enqueue against, and Merge and Rebase take the raw agent
// branch. Whether manual calls Merge at all belongs to the settle package, and
// github and forgejo's auto mapping to the PRForge contract (issue #1546).
func testPushOnlyMergeModeMapping(t *testing.T, h CodeForgeHarness) {
	if _, ok := h.(PushOnly); !ok {
		return
	}
	if _, isPR := h.Forge().(forge.PRForge); isPR {
		t.Fatal("push-only harness's CodeForge satisfies forge.PRForge, want MERGE_MODE=auto to have no meaning here")
	}
	const num = "905"
	ref := h.SeedLandable(num)
	if want := h.Forge().AgentBranch(num); ref != want {
		t.Fatalf("SeedLandable(%s) = %q, want the raw agent branch %q — manual mode lands the feature branch directly, not a PR indirection", num, ref, want)
	}
	// The identity above pins only a name. Merging proves the branch is the
	// landing artifact itself.
	if err := h.Forge().Merge(ref); err != nil {
		t.Fatalf("Merge(%q): %v", ref, err)
	}
	if !h.Landed(num) {
		t.Fatalf("Merge(%q) reported success but %s's marker never reached the base branch", ref, num)
	}
}
