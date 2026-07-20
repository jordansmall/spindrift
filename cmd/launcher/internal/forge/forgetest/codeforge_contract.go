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
	// Unreachable returns a CodeForge instance pointed at a backend Probe
	// cannot reach.
	Unreachable() forge.CodeForge
	// BranchPrefix returns the prefix AgentBranch bakes into its output.
	BranchPrefix() string
	// SeedLandable creates a landable artifact for issue num — one commit
	// ahead of the current base tip, carrying a marker unique to num — and
	// returns whatever ref Merge/Rebase expect for it (a branch name for
	// git/Fake, a PR URL for github).
	SeedLandable(num string) string
	// AdvanceBase adds a new commit to the base branch, so every
	// already-seeded ref is now behind it — the state Rebase exists to fix.
	AdvanceBase()
	// Landed reports whether num's marker has reached the base branch,
	// after a successful Merge.
	Landed(num string) bool
	// Rebased reports whether num's ref itself now incorporates the base
	// branch's latest commit, after a successful Rebase.
	Rebased(num string) bool
	// FailNextMerge arranges for ref's next Merge call to fail with
	// forge.ErrMergeConflict.
	FailNextMerge(ref string)
	// FailNextRebase arranges for ref's next Rebase call to fail with
	// forge.ErrMergeConflict.
	FailNextRebase(ref string)
}

// PushOnly is implemented by harnesses whose CodeForge has no PR concept —
// the git adapter and the Fake's push-only wrapper — so RunCodeForgeContract's
// push-only MERGE_MODE scenario (manual lands the raw agent branch directly,
// no PR indirection; auto has no meaning, CONTEXT.md) only runs against them.
// github implements PRForge and does not implement this marker.
type PushOnly interface {
	IsPushOnly()
}

// RunCodeForgeContract runs the shared CodeForge conformance suite against
// h. Every adapter package calls this from its own test file, backed by its
// own scripted-backend harness.
func RunCodeForgeContract(t *testing.T, h CodeForgeHarness) {
	t.Run("AgentBranchNaming", func(t *testing.T) { testAgentBranchNaming(t, h) })
	t.Run("MergeLandsRef", func(t *testing.T) { testMergeLandsRef(t, h) })
	t.Run("MergeConflict", func(t *testing.T) { testMergeConflict(t, h) })
	t.Run("RebaseIncorporatesBase", func(t *testing.T) { testRebaseIncorporatesBase(t, h) })
	t.Run("RebaseConflict", func(t *testing.T) { testRebaseConflict(t, h) })
	t.Run("Probe", func(t *testing.T) { testProbe(t, h) })
	t.Run("PushOnlyMergeModeMapping", func(t *testing.T) { testPushOnlyMergeModeMapping(t, h) })
}

// testAgentBranchNaming verifies AgentBranch concatenates the harness's
// configured prefix with the issue number — the seam's single owner of the
// branch-prefix rule (issue #444).
func testAgentBranchNaming(t *testing.T, h CodeForgeHarness) {
	got := h.Forge().AgentBranch("909")
	want := h.BranchPrefix() + "909"
	if got != want {
		t.Fatalf("AgentBranch(909) = %q, want %q", got, want)
	}
}

// testMergeLandsRef verifies Merge lands a seeded ref's changes onto the
// base branch — the MERGE_MODE=immediate mapping.
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

// testMergeConflict verifies Merge reports forge.ErrMergeConflict, not a
// generic error, when the ref cannot land automatically.
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

// testRebaseIncorporatesBase verifies Rebase pulls the base branch's latest
// commit into a seeded ref and force-pushes the result.
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

// testRebaseConflict verifies Rebase reports forge.ErrMergeConflict, not a
// generic error, when the ref cannot be rebased automatically.
func testRebaseConflict(t *testing.T, h CodeForgeHarness) {
	const num = "104"
	ref := h.SeedLandable(num)
	h.FailNextRebase(ref)
	err := h.Forge().Rebase(ref)
	if !errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Rebase(%q): want forge.ErrMergeConflict, got %v", ref, err)
	}
}

// testProbe verifies Probe succeeds against a reachable backend and fails
// against an unreachable one.
func testProbe(t *testing.T, h CodeForgeHarness) {
	if _, err := h.Forge().Probe(); err != nil {
		t.Fatalf("Probe on reachable backend: %v", err)
	}
	if _, err := h.Unreachable().Probe(); err == nil {
		t.Fatal("Probe on unreachable backend: want error, got nil")
	}
}

// testPushOnlyMergeModeMapping pins the push-only half of the MERGE_MODE
// mapping (CONTEXT.md) that lives at the CodeForge seam itself: auto has no
// meaning off github (no PRForge to enqueue it against), and Merge/Rebase
// take the raw agent branch name directly — no PR-URL indirection layer for
// a scripted ref to hide behind. (The manual-vs-immediate choice of whether
// to call Merge at all is the settle package's concern, not CodeForge's, so
// it isn't asserted here.) Runs only against harnesses that implement
// PushOnly (the git adapter, and the Fake's push-only wrapper) — github's
// own PRForge-backed auto mapping belongs to the sibling PRForge contract
// (issue #1546).
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
	// The identity above only pins a name. Prove it's actually the landing
	// artifact — immediate mode's Merge call — not just a name that happens
	// to match.
	if err := h.Forge().Merge(ref); err != nil {
		t.Fatalf("Merge(%q): %v", ref, err)
	}
	if !h.Landed(num) {
		t.Fatalf("Merge(%q) reported success but %s's marker never reached the base branch", ref, num)
	}
}
