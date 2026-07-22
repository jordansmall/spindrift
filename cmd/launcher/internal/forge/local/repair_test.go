package local

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// TestLocalCodeForge_BranchMergedIntoIntegration_FalseBeforeMerge asserts
// BranchMergedIntoIntegration reports merged=false, no error, for a branch
// relayed into the Accumulation repo but never merged onto the Integration
// branch — the pre-merge state Reconcile's healing path must never mistake
// for a genuine repair opportunity.
func TestLocalCodeForge_BranchMergedIntoIntegration_FalseBeforeMerge(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)
	repair, ok := cf.(forge.LandingRepair)
	if !ok {
		t.Fatal("local CodeForge does not implement forge.LandingRepair")
	}
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}

	merged, err := repair.BranchMergedIntoIntegration(branch, parent.String())
	if err != nil {
		t.Fatalf("BranchMergedIntoIntegration: %v", err)
	}
	if merged {
		t.Error("BranchMergedIntoIntegration before merge = true, want false")
	}
}

// TestLocalCodeForge_BranchMergedIntoIntegration_TrueAfterMerge asserts
// BranchMergedIntoIntegration reports merged=true once branch has actually
// landed onto parent's Integration branch — the healing path's confirmation
// that a stuck BranchRef landing really did merge.
func TestLocalCodeForge_BranchMergedIntoIntegration_TrueAfterMerge(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)
	repair := cf.(forge.LandingRepair)
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if err := cf.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	merged, err := repair.BranchMergedIntoIntegration(branch, parent.String())
	if err != nil {
		t.Fatalf("BranchMergedIntoIntegration: %v", err)
	}
	if !merged {
		t.Error("BranchMergedIntoIntegration after merge = false, want true")
	}
}

// TestLocalCodeForge_BranchMergedIntoIntegration_FalseForNonexistentBranch
// asserts BranchMergedIntoIntegration reports merged=false, no error, for a
// branch name the Accumulation repo has never seen — never relayed, or a
// since-abandoned attempt — the same "stays open" posture as a genuinely
// unmerged one, not a hard error.
func TestLocalCodeForge_BranchMergedIntoIntegration_FalseForNonexistentBranch(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	repair := cf.(forge.LandingRepair)

	merged, err := repair.BranchMergedIntoIntegration("agent/issue-9999", parent.String())
	if err != nil {
		t.Fatalf("BranchMergedIntoIntegration: %v", err)
	}
	if merged {
		t.Error("BranchMergedIntoIntegration(nonexistent branch) = true, want false")
	}
}

// TestLocalCodeForge_BranchMergedIntoIntegration_ErrorsOnGenuineGitFailure
// asserts BranchMergedIntoIntegration returns a real error — not
// merged=false — when git itself cannot even run, distinct from the
// "branch not found" outcome above, which comes from git running fine and
// reporting no such ref.
func TestLocalCodeForge_BranchMergedIntoIntegration_ErrorsOnGenuineGitFailure(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	repair := cf.(forge.LandingRepair)

	t.Setenv("PATH", "")
	if _, err := repair.BranchMergedIntoIntegration("agent/issue-1698", parent.String()); err == nil {
		t.Fatal("BranchMergedIntoIntegration with no git on PATH: got nil error, want one")
	}
}

// TestLocalCodeForge_IntegrationTip_ResolvesNamedParentsBranch asserts
// IntegrationTip resolves parent's own Integration branch — explicitly, not
// the adapter's own construction-time parent — mirroring
// VerifyLanding/BranchMergedIntoIntegration's instance-agnostic contract
// (issue #1734): a single shared reconcile-time instance must resolve every
// parent in a mixed batch correctly, not just the one it was built with.
func TestLocalCodeForge_IntegrationTip_ResolvesNamedParentsBranch(t *testing.T) {
	setGitIdentityEnv(t)

	parent1, parent2 := ResolveParent("1694", ""), ResolveParent("2200", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent1))
	cf1 := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent1), parent1, "Test Bot", "bot@example.com", "agent/issue-")

	cf2 := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent1), parent2, "Test Bot", "bot@example.com", "agent/issue-")
	outbox := t.TempDir()
	branch := "agent/issue-2201"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent1), outbox, branch, "2201")
	if err := cf2.(forge.BundleRelay).RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if err := cf2.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	wantLanding, err := cf2.(forge.LandingRef).LandingRef()
	if err != nil {
		t.Fatalf("LandingRef: %v", err)
	}

	got, err := cf1.(forge.LandingRepair).IntegrationTip(parent2.String())
	if err != nil {
		t.Fatalf("IntegrationTip: %v", err)
	}
	if got != wantLanding {
		t.Errorf("IntegrationTip(%q) via a differently-parented instance = %q, want %q", parent2, got, wantLanding)
	}
}
