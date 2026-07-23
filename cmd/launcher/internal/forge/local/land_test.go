package local

import (
	"os/exec"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// gitOutput runs `git -C dir args...` and returns its trimmed stdout,
// failing t on error.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// parentCount returns the number of parent commits ref has inside repoPath —
// 1 for a linear history, 2+ for a merge commit.
func parentCount(t *testing.T, repoPath, ref string) int {
	t.Helper()
	fields := strings.Fields(gitOutput(t, repoPath, "rev-list", "--parents", "-n", "1", ref))
	if len(fields) == 0 {
		t.Fatalf("rev-list --parents -n 1 %s: empty output", ref)
	}
	return len(fields) - 1
}

// TestLocalCodeForge_Merge_ProducesNoMergeCommit asserts landing a single
// seam under CODE_FORGE=local advances the Integration branch by rebase and
// fast-forward, never a merge commit (issue #1889, ADR 0033): the branch's
// new tip has exactly one parent.
func TestLocalCodeForge_Merge_ProducesNoMergeCommit(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if err := cf.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if got := parentCount(t, repo.Bare, "refs/heads/"+IntegrationBranch(parent)); got != 1 {
		t.Errorf("integration branch tip has %d parents, want 1 (no merge commit)", got)
	}
}

// TestLocalCodeForge_TwoSeamChain_LandsLinearWithNoMergeCommits asserts a
// two-seam dependency chain lands with a fully linear Integration branch —
// no merge commits anywhere along it (issue #1889 acceptance criteria).
func TestLocalCodeForge_TwoSeamChain_LandsLinearWithNoMergeCommits(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)

	// Seam 2's bundle is built off the pre-seam-1 integration tip, before
	// seam 1 lands — mirroring a dependent seam in a chain whose own Box ran
	// against an earlier integration tip than the one it eventually lands
	// onto. Seeding it first, ahead of seam 1's land, is what makes seam 2's
	// own Merge do a genuine replay onto the now-advanced tip rather than a
	// no-op fast-forward.
	outbox2 := t.TempDir()
	branch2 := "agent/issue-1699"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox2, branch2, "1699")

	outbox1 := t.TempDir()
	branch1 := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox1, branch1, "1698")
	if err := br.RelayBundle(outbox1, branch1); err != nil {
		t.Fatalf("RelayBundle (seam 1): %v", err)
	}
	if err := cf.Merge(branch1); err != nil {
		t.Fatalf("Merge (seam 1): %v", err)
	}

	if err := br.RelayBundle(outbox2, branch2); err != nil {
		t.Fatalf("RelayBundle (seam 2): %v", err)
	}
	if err := cf.Merge(branch2); err != nil {
		t.Fatalf("Merge (seam 2): %v", err)
	}

	if out := gitOutput(t, repo.Bare, "rev-list", "--min-parents=2", "refs/heads/"+IntegrationBranch(parent)); out != "" {
		t.Errorf("integration branch has merge commit(s): %s", out)
	}
}
