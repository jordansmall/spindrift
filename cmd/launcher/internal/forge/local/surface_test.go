package local

import (
	"os/exec"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge/forgetest"
)

// currentBranch returns dir's currently checked-out branch name.
func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse --abbrev-ref HEAD in %s: %v: %s", dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestSurfaceIntegrationBranch_CreatesLocalBranchAtIntegrationTip verifies
// SurfaceIntegrationBranch fetches parent's Integration branch from the
// Accumulation repo into pwd as a local branch named after parent, at the
// Integration branch's current tip, without switching pwd off its current
// branch (issue #1730 AC1, AC2).
func TestSurfaceIntegrationBranch_CreatesLocalBranchAtIntegrationTip(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1700", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	pwd := newCheckoutFixture(t, "main")

	surfaced, skipped, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent)
	if err != nil {
		t.Fatalf("SurfaceIntegrationBranch: %v", err)
	}
	if skipped != "" {
		t.Fatalf("skipped = %q, want none", skipped)
	}
	if !surfaced {
		t.Fatalf("surfaced = false, want true")
	}

	want := revParse(t, repo.Bare, "refs/heads/"+IntegrationBranch(parent))
	if got := revParse(t, pwd.dir, "refs/heads/"+parent.String()); got != want {
		t.Errorf("refs/heads/%s = %s, want %s (Integration branch tip)", parent, got, want)
	}
	if got := currentBranch(t, pwd.dir); got != "main" {
		t.Errorf("current branch = %s, want main (unchanged)", got)
	}
}

// TestSurfaceIntegrationBranch_UnchangedReRunIsNoOp verifies re-surfacing an
// already-surfaced, unchanged ticket reports surfaced=false — the idempotent
// no-op AC (issue #1730 AC5) — rather than repeating the notice every run.
func TestSurfaceIntegrationBranch_UnchangedReRunIsNoOp(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1700", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	pwd := newCheckoutFixture(t, "main")

	if _, _, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent); err != nil {
		t.Fatalf("SurfaceIntegrationBranch (first run): %v", err)
	}

	surfaced, skipped, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent)
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

// TestSurfaceIntegrationBranch_RefusesWhenTargetBranchCheckedOut verifies
// SurfaceIntegrationBranch refuses to fetch into parent's branch when it is
// pwd's currently checked-out branch, reporting the reason through skipped
// rather than clobbering the operator's working tree (issue #1730 AC1 safety
// clause, mirroring console_freshness.go's checkCheckoutSafe).
func TestSurfaceIntegrationBranch_RefusesWhenTargetBranchCheckedOut(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1700", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	pwd := newCheckoutFixture(t, parent.String())

	surfaced, skipped, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent)
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

// TestSurfaceIntegrationBranch_SkipsWhenIntegrationBranchAbsent verifies
// SurfaceIntegrationBranch reports a skip, not an error, when no seam of
// parent has landed yet — the Integration branch doesn't exist in repoPath
// at all (issue #1730 AC3: nothing surfaced for an incomplete ticket).
func TestSurfaceIntegrationBranch_SkipsWhenIntegrationBranchAbsent(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1700", "")
	repo := forgetest.NewGitRepoFixture(t, "main")
	pwd := newCheckoutFixture(t, "main")

	surfaced, skipped, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent)
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

// TestSurfaceIntegrationBranch_RefusesDivergedLocalBranch verifies
// SurfaceIntegrationBranch never force-overwrites a local branch named
// parent that has commits of its own the Integration branch doesn't know
// about — a non-fast-forward is reported through skipped, and the local
// branch is left exactly as the operator left it (issue #1730 AC2: never
// clobbering operator work).
func TestSurfaceIntegrationBranch_RefusesDivergedLocalBranch(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1700", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	pwd := newCheckoutFixture(t, "main")
	pwd.run("checkout", "-b", parent.String())
	pwd.commit("operator-work.txt", "operator work")
	pwd.run("checkout", "main")
	diverged := revParse(t, pwd.dir, parent.String())

	surfaced, skipped, err := SurfaceIntegrationBranch(repo.Bare, pwd.dir, parent)
	if err != nil {
		t.Fatalf("SurfaceIntegrationBranch: %v", err)
	}
	if surfaced {
		t.Errorf("surfaced = true, want false (diverged)")
	}
	if skipped == "" {
		t.Errorf("skipped = %q, want a diverged-branch reason", skipped)
	}
	if got := revParse(t, pwd.dir, parent.String()); got != diverged {
		t.Errorf("refs/heads/%s = %s, want %s (untouched operator commit)", parent, got, diverged)
	}
}
