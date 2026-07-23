package local

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestLocalCodeForge_BranchMergedIntoIntegration_TrueForRebasedLanding asserts
// BranchMergedIntoIntegration reports merged=true for a seam that landed via
// rebase (issue #1889) even though its own branch ref in the Accumulation
// repo still points at its pre-rebase tip — the state a lost or malformed
// `landing:` record leaves reconcile's healing path to re-derive from patch
// content, since rebasing onto a since-advanced integration tip gives the
// landed commit a new sha the branch ref's own (stale) ancestry can no
// longer see (issue #1890).
func TestLocalCodeForge_BranchMergedIntoIntegration_TrueForRebasedLanding(t *testing.T) {
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
	preLandSHA := revParse(t, repo.Bare, "refs/heads/"+branch)

	// Advance the integration branch with an unrelated commit, so replaying
	// branch's own commit onto it is a genuine rebase (a new sha), not a
	// no-op fast-forward — mirroring land_test.go's own two-seam setup.
	other := t.TempDir()
	run(t, "", "clone", repo.Bare, other)
	run(t, other, "checkout", IntegrationBranch(parent))
	if err := os.WriteFile(filepath.Join(other, "other.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, other, "add", "other.txt")
	run(t, other, "config", "user.email", "test@example.com")
	run(t, other, "config", "user.name", "Test")
	run(t, other, "commit", "-m", "other seam")
	run(t, other, "push", "origin", IntegrationBranch(parent))

	// Land branch by rebasing it onto the now-advanced integration tip and
	// fast-forwarding integration to the result directly, deliberately
	// bypassing cf.Merge — which would resync refs/heads/branch to the
	// rebased result and defeat the point of this test — standing in for a
	// landing whose branch ref never got resynced in the Accumulation repo.
	rebaseWork := t.TempDir()
	run(t, "", "clone", repo.Bare, rebaseWork)
	run(t, rebaseWork, "checkout", branch)
	run(t, rebaseWork, "rebase", "origin/"+IntegrationBranch(parent))
	run(t, rebaseWork, "push", "origin", "HEAD:refs/heads/"+IntegrationBranch(parent))

	if got := revParse(t, repo.Bare, "refs/heads/"+branch); got != preLandSHA {
		t.Fatalf("refs/heads/%s = %s, want unchanged %s", branch, got, preLandSHA)
	}
	if err := exec.Command("git", "-C", repo.Bare, "merge-base", "--is-ancestor", preLandSHA, "refs/heads/"+IntegrationBranch(parent)).Run(); err == nil {
		t.Fatal("branch's pre-rebase tip is an ancestor of integration branch, want not (test setup didn't force a rebase)")
	}

	merged, err := repair.BranchMergedIntoIntegration(branch, parent.String())
	if err != nil {
		t.Fatalf("BranchMergedIntoIntegration: %v", err)
	}
	if !merged {
		t.Error("BranchMergedIntoIntegration for a rebased-and-landed seam = false, want true")
	}
}

// writeAndCommit writes name=contents inside dir and commits it — shared
// scaffolding for the multi-commit rebase fixtures below, which need more
// control over individual commit boundaries than seedBundleBranch's
// single-commit shape gives.
func writeAndCommit(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", name)
	run(t, dir, "commit", "-m", name)
}

// TestLocalCodeForge_BranchMergedIntoIntegration_TrueForMultiCommitRebasedLanding
// asserts a multi-commit seam that lands via rebase — every commit replayed
// as a new sha — still reports merged=true when every one of them is
// patch-equivalent to the integration branch, not just the oldest one (issue
// #1890): a bundle relays a branch's entire base..branch range, so a real
// seam is routinely more than one commit.
func TestLocalCodeForge_BranchMergedIntoIntegration_TrueForMultiCommitRebasedLanding(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	branch := "agent/issue-1698"

	work := t.TempDir()
	run(t, "", "clone", repo.Bare, work)
	run(t, work, "checkout", IntegrationBranch(parent))
	run(t, work, "checkout", "-b", branch)
	run(t, work, "config", "user.email", "test@example.com")
	run(t, work, "config", "user.name", "Test")
	writeAndCommit(t, work, "feature-1698-a.txt", "a")
	writeAndCommit(t, work, "feature-1698-b.txt", "b")
	run(t, work, "push", "origin", branch)
	preLandSHA := revParse(t, repo.Bare, "refs/heads/"+branch)

	other := t.TempDir()
	run(t, "", "clone", repo.Bare, other)
	run(t, other, "checkout", IntegrationBranch(parent))
	run(t, other, "config", "user.email", "test@example.com")
	run(t, other, "config", "user.name", "Test")
	writeAndCommit(t, other, "other.txt", "other")
	run(t, other, "push", "origin", IntegrationBranch(parent))

	rebaseWork := t.TempDir()
	run(t, "", "clone", repo.Bare, rebaseWork)
	run(t, rebaseWork, "checkout", branch)
	run(t, rebaseWork, "rebase", "origin/"+IntegrationBranch(parent))
	run(t, rebaseWork, "push", "origin", "HEAD:refs/heads/"+IntegrationBranch(parent))

	if got := revParse(t, repo.Bare, "refs/heads/"+branch); got != preLandSHA {
		t.Fatalf("refs/heads/%s = %s, want unchanged %s", branch, got, preLandSHA)
	}

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	repair := cf.(forge.LandingRepair)

	merged, err := repair.BranchMergedIntoIntegration(branch, parent.String())
	if err != nil {
		t.Fatalf("BranchMergedIntoIntegration: %v", err)
	}
	if !merged {
		t.Error("BranchMergedIntoIntegration for a fully rebased multi-commit seam = false, want true")
	}
}

// TestLocalCodeForge_BranchMergedIntoIntegration_FalseWhenLaterCommitNeverLanded
// asserts BranchMergedIntoIntegration still reports merged=false for a
// multi-commit seam whose oldest commit's patch reached the integration
// branch but whose newest commit's never did — patch-equivalence must clear
// every commit `git cherry` reports on the branch, not just the first line,
// or a genuinely-unlanded seam would self-heal to closed (issue #1890).
func TestLocalCodeForge_BranchMergedIntoIntegration_FalseWhenLaterCommitNeverLanded(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	branch := "agent/issue-1698"

	work := t.TempDir()
	run(t, "", "clone", repo.Bare, work)
	run(t, work, "checkout", IntegrationBranch(parent))
	run(t, work, "checkout", "-b", branch)
	run(t, work, "config", "user.email", "test@example.com")
	run(t, work, "config", "user.name", "Test")
	writeAndCommit(t, work, "feature-1698-a.txt", "a")
	oldestSHA := revParse(t, work, "HEAD")
	writeAndCommit(t, work, "feature-1698-b.txt", "b")
	run(t, work, "push", "origin", branch)

	other := t.TempDir()
	run(t, "", "clone", repo.Bare, other)
	run(t, other, "checkout", IntegrationBranch(parent))
	run(t, other, "config", "user.email", "test@example.com")
	run(t, other, "config", "user.name", "Test")
	writeAndCommit(t, other, "other.txt", "other")
	run(t, other, "push", "origin", IntegrationBranch(parent))

	// Land only the oldest commit's patch onto integration — the newest
	// commit genuinely never lands.
	partial := t.TempDir()
	run(t, "", "clone", repo.Bare, partial)
	run(t, partial, "checkout", IntegrationBranch(parent))
	run(t, partial, "config", "user.email", "test@example.com")
	run(t, partial, "config", "user.name", "Test")
	run(t, partial, "cherry-pick", oldestSHA)
	run(t, partial, "push", "origin", "HEAD:refs/heads/"+IntegrationBranch(parent))

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	repair := cf.(forge.LandingRepair)

	merged, err := repair.BranchMergedIntoIntegration(branch, parent.String())
	if err != nil {
		t.Fatalf("BranchMergedIntoIntegration: %v", err)
	}
	if merged {
		t.Error("BranchMergedIntoIntegration with a genuinely-unlanded later commit = true, want false")
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

// TestPatchEquivalentToIntegration_FalseForUnknownSHA asserts
// patchEquivalentToIntegration reports merged=false, no error, when sha is
// unknown to repoPath — `git cherry` exits nonzero ("fatal: unknown commit")
// rather than reporting a genuine "+"/"-" verdict, the same "not merged"
// posture isMergedIntoIntegration itself gives an unknown sha, not a hard
// error (issue #1890).
func TestPatchEquivalentToIntegration_FalseForUnknownSHA(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))

	merged, err := patchEquivalentToIntegration(repo.Bare, strings.Repeat("0", 40), IntegrationBranch(parent))
	if err != nil {
		t.Fatalf("patchEquivalentToIntegration: %v", err)
	}
	if merged {
		t.Error("patchEquivalentToIntegration(unknown sha) = true, want false")
	}
}

// TestPatchEquivalentToIntegration_ErrorsOnGenuineGitFailure asserts
// patchEquivalentToIntegration returns a real error — not merged=false —
// when git itself cannot even run, distinct from the "unknown sha" outcome
// above, which comes from git running fine and reporting no such commit.
func TestPatchEquivalentToIntegration_ErrorsOnGenuineGitFailure(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))

	t.Setenv("PATH", "")
	if _, err := patchEquivalentToIntegration(repo.Bare, strings.Repeat("0", 40), IntegrationBranch(parent)); err == nil {
		t.Fatal("patchEquivalentToIntegration with no git on PATH: got nil error, want one")
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
