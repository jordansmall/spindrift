package ledgertest

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// NewRepo builds a bare git repo with one commit on refs/heads/main, the
// shared fixture every package that needs a Ledger store or a remote
// stand-in builds against (issue #3995). With no paths the commit holds one
// tracked file (a.go, content "package a\n"); given paths, the commit holds
// exactly those paths instead (each blob's content is its own path), for
// tests that care about the tree shape rather than a specific file's
// content. gc.auto and receive.autogc are both off since a detached `git gc
// --auto` can still be repacking when t.TempDir()'s RemoveAll runs (same
// rationale as forgetest.NewGitRepoFixture). Git identity env is set via
// t.Setenv so a caller's own further plumbing commands against the repo
// (e.g. a second commit-tree) inherit it without setting it again.
func NewRepo(t *testing.T, paths ...string) string {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	dir := t.TempDir()
	bare := filepath.Join(dir, "repo.git")
	runGit(t, "", "init", "--bare", "-q", bare)
	runGit(t, bare, "config", "gc.auto", "0")
	runGit(t, bare, "config", "receive.autogc", "false")

	entries := make([]string, 0, len(paths))
	if len(paths) == 0 {
		entries = append(entries, treeEntry(t, bare, "a.go", "package a\n"))
	} else {
		for _, p := range paths {
			entries = append(entries, treeEntry(t, bare, p, p))
		}
	}

	mktreeCmd := exec.Command("git", "-C", bare, "mktree")
	mktreeCmd.Stdin = strings.NewReader(strings.Join(entries, "\n") + "\n")
	treeOut, err := mktreeCmd.Output()
	if err != nil {
		t.Fatalf("mktree: %v", err)
	}
	tree := strings.TrimSpace(string(treeOut))

	commitOut, err := exec.Command("git", "-C", bare, "commit-tree", tree, "-m", "base").Output()
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	commit := strings.TrimSpace(string(commitOut))
	runGit(t, bare, "update-ref", "refs/heads/main", commit)
	return bare
}

// treeEntry hash-objects content into bare and returns its mktree line for
// path.
func treeEntry(t *testing.T, bare, path, content string) string {
	t.Helper()
	hashCmd := exec.Command("git", "-C", bare, "hash-object", "-w", "--stdin")
	hashCmd.Stdin = strings.NewReader(content)
	out, err := hashCmd.Output()
	if err != nil {
		t.Fatalf("hash-object %s: %v", path, err)
	}
	blob := strings.TrimSpace(string(out))
	return "100644 blob " + blob + "\t" + path
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", full, err, out)
	}
}
