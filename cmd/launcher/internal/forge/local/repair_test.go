package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// writeAndCommit writes name=contents inside dir and commits it — shared
// scaffolding for the multi-commit rebase fixtures below (and
// containment_test.go's own), which need more control over individual commit
// boundaries than seedBundleBranch's single-commit shape gives.
func writeAndCommit(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", name)
	run(t, dir, "commit", "-m", name)
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
// the adapter's own construction-time parent — mirroring LandingContained's
// own instance-agnostic contract (issue #1734): a single shared
// reconcile-time instance must resolve every parent in a mixed batch
// correctly, not just the one it was built with.
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
