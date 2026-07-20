package forgetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// GitRepoFixture is shared scaffolding for CodeForgeHarness implementations
// backed by a real bare git repo — the git and github adapters both root
// their harness in one, so SeedBranch, AdvanceBase, Landed, Rebased, and
// ConflictBase reduce to the same git plumbing regardless of which adapter
// is driving it.
type GitRepoFixture struct {
	t *testing.T
	// Bare is the bare repo's filesystem path — the "remote" every clone in
	// this fixture, and the adapter under test, pushes to and pulls from.
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
// feature branch's own content — not merely present, since ConflictBase
// also writes a same-named file straight onto base to provoke a conflict,
// and a bare existence check couldn't tell that placeholder apart from a
// genuine merge.
func (g *GitRepoFixture) Landed(num string) bool {
	g.t.Helper()
	work := g.t.TempDir()
	g.run("", "clone", g.Bare, work)
	g.run(work, "checkout", g.base)
	got, err := os.ReadFile(filepath.Join(work, "feature-"+num+".txt"))
	return err == nil && string(got) == "feature\n"
}

// Rebased reports whether the base branch is an ancestor of ref — proof ref
// has incorporated the base branch's latest commit.
func (g *GitRepoFixture) Rebased(ref string) bool {
	g.t.Helper()
	work := g.t.TempDir()
	g.run("", "clone", g.Bare, work)
	cmd := exec.Command("git", "-C", work, "merge-base", "--is-ancestor", "origin/"+g.base, "origin/"+ref)
	return cmd.Run() == nil
}

// ConflictBase commits a change to base's copy of feature-<num>.txt that
// conflicts with a seeded branch's own commit to the same file, so a real
// git merge/rebase between them fails instead of succeeding automatically.
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
