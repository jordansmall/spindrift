package local

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

// checkoutFixture is a plain (non-bare) git checkout standing in for the
// operator's local working repo. It deliberately configures no remote, so
// tests exercise SeedAccumulationRepo's no-remote-required contract.
type checkoutFixture struct {
	t   *testing.T
	dir string
}

func newCheckoutFixture(t *testing.T, branch string) *checkoutFixture {
	t.Helper()
	dir := t.TempDir()
	c := &checkoutFixture{t: t, dir: dir}
	c.run("init", "-b", branch)
	c.run("config", "user.email", "test@example.com")
	c.run("config", "user.name", "Test")
	c.commit("base.txt", "base")
	return c
}

func (c *checkoutFixture) run(args ...string) {
	c.t.Helper()
	testutil.GitRun(c.t, c.dir, args...)
}

func (c *checkoutFixture) commit(name, contents string) {
	c.t.Helper()
	if err := os.WriteFile(filepath.Join(c.dir, name), []byte(contents), 0o644); err != nil {
		c.t.Fatal(err)
	}
	c.run("add", name)
	c.run("commit", "-m", name)
}

func (c *checkoutFixture) headRev() string {
	c.t.Helper()
	return testutil.GitOutput(c.t, c.dir, "rev-parse", "HEAD")
}

// This test also covers the "no remote at all" acceptance criterion
// (ADR 0033): newCheckoutFixture never configures one, and
// SeedAccumulationRepo reads only pwd's own baseBranch ref, never `origin`.
func TestSeedAccumulationRepo_CreatesBareRepoWhenAbsent(t *testing.T) {
	checkout := newCheckoutFixture(t, "main")
	repoPath := filepath.Join(t.TempDir(), "repo.git")

	if out, err := exec.Command("git", "-C", checkout.dir, "remote").CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("checkout fixture must have no remote configured, got %q (err %v)", out, err)
	}

	if err := SeedAccumulationRepo(repoPath, checkout.dir, "main"); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	if _, err := os.Stat(repoPath); err != nil {
		t.Fatalf("repoPath not created: %v", err)
	}
	if got, want := testutil.GitOutput(t, repoPath, "rev-parse", "refs/heads/main"), checkout.headRev(); got != want {
		t.Errorf("refs/heads/main = %s, want %s", got, want)
	}
}

// baseBranch here ("trunk") is deliberately atypical, so the test fails if
// SeedAccumulationRepo inherits git's own init.defaultBranch rather than
// setting HEAD itself. A wrong HEAD leaves a later `git clone` of the
// Accumulation repo checking out nothing (issue #1697's mount ticket).
func TestSeedAccumulationRepo_SetsHEADToBaseBranch(t *testing.T) {
	checkout := newCheckoutFixture(t, "trunk")
	repoPath := filepath.Join(t.TempDir(), "repo.git")

	if err := SeedAccumulationRepo(repoPath, checkout.dir, "trunk"); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("symbolic-ref HEAD: %v: %s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), "refs/heads/trunk"; got != want {
		t.Errorf("HEAD = %s, want %s", got, want)
	}
}

// Re-seeding must not disturb refs other tickets already wrote into the
// Accumulation repo, meaning agent branches and Integration branches
// (ADR 0033). Seeding touches baseBranch's own ref and nothing else.
func TestSeedAccumulationRepo_PreservesOtherRefs(t *testing.T) {
	checkout := newCheckoutFixture(t, "main")
	repoPath := filepath.Join(t.TempDir(), "repo.git")

	if err := SeedAccumulationRepo(repoPath, checkout.dir, "main"); err != nil {
		t.Fatalf("SeedAccumulationRepo (first run): %v", err)
	}

	// Stand in for a landed agent branch left in the Accumulation repo by
	// prior seam work.
	work := t.TempDir()
	testutil.GitRun(t, work, "clone", repoPath, ".")
	testutil.GitRun(t, work, "checkout", "-b", "agent/1696")
	if err := os.WriteFile(filepath.Join(work, "seam.txt"), []byte("seam"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.GitRun(t, work, "add", "seam.txt")
	testutil.GitRun(t, work, "config", "user.email", "test@example.com")
	testutil.GitRun(t, work, "config", "user.name", "Test")
	testutil.GitRun(t, work, "commit", "-m", "seam work")
	testutil.GitRun(t, work, "push", "origin", "agent/1696")
	agentRev := testutil.GitOutput(t, work, "rev-parse", "agent/1696")

	if err := SeedAccumulationRepo(repoPath, checkout.dir, "main"); err != nil {
		t.Fatalf("SeedAccumulationRepo (second run): %v", err)
	}

	if got := testutil.GitOutput(t, repoPath, "rev-parse", "refs/heads/agent/1696"); got != agentRev {
		t.Errorf("refs/heads/agent/1696 = %s, want %s (untouched by re-seed)", got, agentRev)
	}
}

// A second seeding run must pick up new commits the operator's local
// checkout has since made on baseBranch. This is the "sync" half of
// ADR 0033's seed contract, not just first creation.
func TestSeedAccumulationRepo_UpdatesBaseWhenCheckoutAdvances(t *testing.T) {
	checkout := newCheckoutFixture(t, "main")
	repoPath := filepath.Join(t.TempDir(), "repo.git")

	if err := SeedAccumulationRepo(repoPath, checkout.dir, "main"); err != nil {
		t.Fatalf("SeedAccumulationRepo (first run): %v", err)
	}

	checkout.commit("later.txt", "later")

	if err := SeedAccumulationRepo(repoPath, checkout.dir, "main"); err != nil {
		t.Fatalf("SeedAccumulationRepo (second run): %v", err)
	}

	if got, want := testutil.GitOutput(t, repoPath, "rev-parse", "refs/heads/main"), checkout.headRev(); got != want {
		t.Errorf("refs/heads/main = %s, want %s (checkout's new tip)", got, want)
	}
}

// The base ref mirrors the local checkout rather than only fast-forwarding,
// so a `git reset --hard` to an earlier commit must carry through the force
// refspec as well.
func TestSeedAccumulationRepo_MirrorsCheckoutRewind(t *testing.T) {
	checkout := newCheckoutFixture(t, "main")
	checkout.commit("later.txt", "later")
	repoPath := filepath.Join(t.TempDir(), "repo.git")

	if err := SeedAccumulationRepo(repoPath, checkout.dir, "main"); err != nil {
		t.Fatalf("SeedAccumulationRepo (first run): %v", err)
	}

	checkout.run("reset", "--hard", "HEAD~1")

	if err := SeedAccumulationRepo(repoPath, checkout.dir, "main"); err != nil {
		t.Fatalf("SeedAccumulationRepo (second run): %v", err)
	}

	if got, want := testutil.GitOutput(t, repoPath, "rev-parse", "refs/heads/main"), checkout.headRev(); got != want {
		t.Errorf("refs/heads/main = %s, want %s (checkout's rewound tip)", got, want)
	}
}
