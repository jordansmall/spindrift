package local

import (
	"os/exec"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/testutil"
)

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse --abbrev-ref HEAD in %s: %v: %s", dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// SurfaceIntegrationBranch fetches parent's Integration branch from the
// Accumulation repo into pwd at the Integration tip, and leaves pwd on the
// branch it was already on (issue #1730 AC1, AC2).
func TestSurfaceIntegrationBranch_CreatesLocalBranchAtIntegrationTip(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1700", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	pwd := newCheckoutFixture(t, "main")

	surfaced, skipped, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent, parent.String())
	if err != nil {
		t.Fatalf("SurfaceIntegrationBranch: %v", err)
	}
	if skipped != "" {
		t.Fatalf("skipped = %q, want none", skipped)
	}
	if !surfaced {
		t.Fatalf("surfaced = false, want true")
	}

	want := testutil.GitOutput(t, repo.Bare, "rev-parse", "refs/heads/"+IntegrationBranch(parent))
	if got := testutil.GitOutput(t, pwd.dir, "rev-parse", "refs/heads/"+parent.String()); got != want {
		t.Errorf("refs/heads/%s = %s, want %s (Integration branch tip)", parent, got, want)
	}
	if got := currentBranch(t, pwd.dir); got != "main" {
		t.Errorf("current branch = %s, want main (unchanged)", got)
	}
}

// The local branch name and the Integration-branch key must be independently
// settable, so a parentless ticket can surface under a title-derived name while
// the Integration branch it fetches from stays keyed on the stable slug
// (issue #1811).
func TestSurfaceIntegrationBranch_BranchNameDecoupledFromIntegrationKey(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("42", "")
	const branchName = "seam-42"
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	pwd := newCheckoutFixture(t, "main")

	surfaced, skipped, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent, branchName)
	if err != nil {
		t.Fatalf("SurfaceIntegrationBranch: %v", err)
	}
	if skipped != "" {
		t.Fatalf("skipped = %q, want none", skipped)
	}
	if !surfaced {
		t.Fatalf("surfaced = false, want true")
	}

	want := testutil.GitOutput(t, repo.Bare, "rev-parse", "refs/heads/"+IntegrationBranch(parent))
	if got := testutil.GitOutput(t, pwd.dir, "rev-parse", "refs/heads/"+branchName); got != want {
		t.Errorf("refs/heads/%s = %s, want %s (Integration branch tip)", branchName, got, want)
	}
	if err := exec.Command("git", "-C", pwd.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+parent.String()).Run(); err == nil {
		t.Errorf("refs/heads/%s must not exist — the local branch name is branchName, not parent", parent)
	}
}

// Re-surfacing an already-surfaced, unchanged ticket reports surfaced=false
// instead of repeating the notice every run (issue #1730 AC5).
func TestSurfaceIntegrationBranch_UnchangedReRunIsNoOp(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1700", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	pwd := newCheckoutFixture(t, "main")

	if _, _, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent, parent.String()); err != nil {
		t.Fatalf("SurfaceIntegrationBranch (first run): %v", err)
	}

	surfaced, skipped, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent, parent.String())
	if err != nil {
		t.Fatalf("SurfaceIntegrationBranch (second run): %v", err)
	}
	if skipped != "" {
		t.Fatalf("skipped = %q, want none", skipped)
	}
	if surfaced {
		t.Errorf("surfaced = true, want false (unchanged tip)")
	}
}

// Fetching into the branch pwd currently has checked out would clobber the
// operator's working tree, so SurfaceIntegrationBranch reports the reason
// through skipped instead (issue #1730 AC1 safety clause, mirroring
// console_freshness.go's checkCheckoutSafe).
func TestSurfaceIntegrationBranch_RefusesWhenTargetBranchCheckedOut(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1700", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	pwd := newCheckoutFixture(t, parent.String())

	surfaced, skipped, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent, parent.String())
	if err != nil {
		t.Fatalf("SurfaceIntegrationBranch: %v", err)
	}
	if surfaced {
		t.Errorf("surfaced = true, want false (target branch is checked out)")
	}
	if skipped == "" {
		t.Errorf("skipped = %q, want a checked-out-branch reason", skipped)
	}
	if got := currentBranch(t, pwd.dir); got != parent.String() {
		t.Errorf("current branch = %s, want %s (untouched)", got, parent)
	}
}

// When no seam of parent has landed yet, the Integration branch does not exist
// in repoPath at all. That is a skip, not an error (issue #1730 AC3).
func TestSurfaceIntegrationBranch_SkipsWhenIntegrationBranchAbsent(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1700", "")
	repo := forgetest.NewGitRepoFixture(t, "main")
	pwd := newCheckoutFixture(t, "main")

	surfaced, skipped, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent, parent.String())
	if err != nil {
		t.Fatalf("SurfaceIntegrationBranch: %v", err)
	}
	if surfaced {
		t.Errorf("surfaced = true, want false (no Integration branch yet)")
	}
	if skipped == "" {
		t.Errorf("skipped = %q, want a no-seam-landed reason", skipped)
	}
}

// SurfaceIntegrationBranch never force-overwrites a local branch carrying
// commits the Integration branch does not know about. It reports the
// non-fast-forward through skipped and leaves the operator's commit in place
// (issue #1730 AC2).
func TestSurfaceIntegrationBranch_RefusesDivergedLocalBranch(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1700", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	pwd := newCheckoutFixture(t, "main")
	pwd.run("checkout", "-b", parent.String())
	pwd.commit("operator-work.txt", "operator work")
	pwd.run("checkout", "main")
	diverged := testutil.GitOutput(t, pwd.dir, "rev-parse", parent.String())

	surfaced, skipped, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent, parent.String())
	if err != nil {
		t.Fatalf("SurfaceIntegrationBranch: %v", err)
	}
	if surfaced {
		t.Errorf("surfaced = true, want false (diverged)")
	}
	if skipped == "" {
		t.Errorf("skipped = %q, want a diverged-branch reason", skipped)
	}
	if got := testutil.GitOutput(t, pwd.dir, "rev-parse", parent.String()); got != diverged {
		t.Errorf("refs/heads/%s = %s, want %s (untouched operator commit)", parent, got, diverged)
	}
}
