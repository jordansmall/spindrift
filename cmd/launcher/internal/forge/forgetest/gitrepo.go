package forgetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitRepoFixture backs CodeForgeHarness implementations with a real bare git
// repo, so the git and github adapters share the same plumbing.
type GitRepoFixture struct {
	t *testing.T
	// Bare is the bare repo's filesystem path, the remote every clone in this
	// fixture and the adapter under test push to and pull from.
	Bare string
	base string
}

// NewGitRepoFixture creates a bare repo with a single base branch (one
// commit, "base.txt").
func NewGitRepoFixture(t *testing.T, base string) *GitRepoFixture {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")

	g := &GitRepoFixture{t: t, Bare: bare, base: base}
	g.run("", "init", "--bare", bare)
	// Git forks a detached `git gc --auto` once a push crosses the loose-object
	// threshold, and it can still be repacking when t.TempDir()'s RemoveAll
	// runs, failing with "directory not empty". gc.auto covers only
	// commands run directly against bare; pushes go through git-receive-pack,
	// whose own post-receive gc obeys receive.autogc, so both need disabling.
	g.run(bare, "config", "gc.auto", "0")
	g.run(bare, "config", "receive.autogc", "false")
	g.run("", "clone", bare, work)
	g.run(work, "checkout", "-B", base)
	g.writeFile(filepath.Join(work, "base.txt"), "base\n")
	g.run(work, "add", "base.txt")
	g.run(work, "commit", "-m", "base")
	g.run(work, "push", "-u", "origin", base)
	return g
}

func (g *GitRepoFixture) run(dir string, args ...string) {
	g.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		g.t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func (g *GitRepoFixture) writeFile(path, contents string) {
	g.t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		g.t.Fatal(err)
	}
}

// SeedBranch creates branch off the base tip, one commit ahead, carrying a
// marker file unique to num, and pushes it.
func (g *GitRepoFixture) SeedBranch(branch, num string) {
	g.t.Helper()
	work := g.t.TempDir()
	g.run("", "clone", g.Bare, work)
	g.run(work, "checkout", g.base)
	g.run(work, "checkout", "-b", branch)
	g.writeFile(filepath.Join(work, "feature-"+num+".txt"), "feature\n")
	g.run(work, "add", "feature-"+num+".txt")
	g.run(work, "commit", "-m", "feature "+num)
	g.run(work, "push", "-u", "origin", branch)
}

// AdvanceBranch commits onto branch's own tip and pushes it, as a fix pass
// does. AdvanceBase instead advances the base branch underneath it.
func (g *GitRepoFixture) AdvanceBranch(branch, marker string) {
	g.t.Helper()
	work := g.t.TempDir()
	g.run("", "clone", g.Bare, work)
	g.run(work, "checkout", branch)
	g.writeFile(filepath.Join(work, "advance-"+marker+".txt"), "advance\n")
	g.run(work, "add", "advance-"+marker+".txt")
	g.run(work, "commit", "-m", "advance "+marker)
	g.run(work, "push", "origin", branch)
}

// BranchSHA returns the commit SHA branch points to in the bare repo, the
// independent ground truth a HeadCommitSHA result is checked against.
func (g *GitRepoFixture) BranchSHA(branch string) string {
	g.t.Helper()
	out, err := exec.Command("git", "-C", g.Bare, "rev-parse", branch).Output()
	if err != nil {
		g.t.Fatalf("git -C %s rev-parse %s: %v", g.Bare, branch, err)
	}
	return strings.TrimSpace(string(out))
}

// AdvanceBase adds a new commit to the base branch, so every already-seeded
// branch is now behind it.
func (g *GitRepoFixture) AdvanceBase() {
	g.t.Helper()
	work := g.t.TempDir()
	g.run("", "clone", g.Bare, work)
	g.run(work, "checkout", g.base)
	g.writeFile(filepath.Join(work, "later.txt"), "later\n")
	g.run(work, "add", "later.txt")
	g.run(work, "commit", "-m", "advance base")
	g.run(work, "push", "origin", g.base)
}

// Landed reports whether num's marker file reached the base branch with the
// feature branch's own content. ConflictBase writes a same-named file onto
// base, so an existence check alone would mistake that for a merge.
func (g *GitRepoFixture) Landed(num string) bool {
	g.t.Helper()
	work := g.t.TempDir()
	g.run("", "clone", g.Bare, work)
	g.run(work, "checkout", g.base)
	got, err := os.ReadFile(filepath.Join(work, "feature-"+num+".txt"))
	return err == nil && string(got) == "feature\n"
}

// Rebased reports whether the base branch is an ancestor of ref, proving ref
// has incorporated the base branch's latest commit.
func (g *GitRepoFixture) Rebased(ref string) bool {
	g.t.Helper()
	work := g.t.TempDir()
	g.run("", "clone", g.Bare, work)
	cmd := exec.Command("git", "-C", work, "merge-base", "--is-ancestor", "origin/"+g.base, "origin/"+ref)
	return cmd.Run() == nil
}

// ConflictBase commits to base's copy of feature-<num>.txt so that a real
// merge or rebase with the seeded branch fails instead of succeeding.
func (g *GitRepoFixture) ConflictBase(num string) {
	g.t.Helper()
	work := g.t.TempDir()
	g.run("", "clone", g.Bare, work)
	g.run(work, "checkout", g.base)
	g.writeFile(filepath.Join(work, "feature-"+num+".txt"), "conflicting base change\n")
	g.run(work, "add", "feature-"+num+".txt")
	g.run(work, "commit", "-m", "conflicting base change")
	g.run(work, "push", "origin", g.base)
}
