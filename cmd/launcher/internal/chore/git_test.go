package chore_test

import (
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/chore"
)

// newBareRepoWithFiles builds a bare repo with one commit on "main" holding
// the given paths (each blob's content is its own path), same exec-git style
// as ledger/local_test.go's newBareRepo.
func newBareRepoWithFiles(t *testing.T, paths ...string) (repo, commit string) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	dir := t.TempDir()
	bare := filepath.Join(dir, "repo.git")
	runGit(t, "", "init", "--bare", "-q", bare)
	runGit(t, bare, "config", "gc.auto", "0")

	var entries []string
	for _, p := range paths {
		hashCmd := exec.Command("git", "-C", bare, "hash-object", "-w", "--stdin")
		hashCmd.Stdin = strings.NewReader(p)
		out, err := hashCmd.Output()
		if err != nil {
			t.Fatalf("hash-object %s: %v", p, err)
		}
		blob := strings.TrimSpace(string(out))
		entries = append(entries, "100644 blob "+blob+"\t"+p)
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
	commit = strings.TrimSpace(string(commitOut))
	runGit(t, bare, "update-ref", "refs/heads/main", commit)
	return bare, commit
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

func TestHead(t *testing.T) {
	bare, commit := newBareRepoWithFiles(t, "a.go", "b.go")

	got, err := chore.Head(bare, "main")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if got != commit {
		t.Fatalf("Head(main) = %s, want %s", got, commit)
	}

	if _, err := chore.Head(bare, "no-such-branch"); err == nil {
		t.Fatal("Head(no-such-branch): got nil error, want one")
	}
}

func TestTrackedFiles(t *testing.T) {
	// mktree builds one flat tree (no --batch nesting), so this fixture
	// sticks to top-level paths; ls-tree -r's recursion into subtrees is
	// exercised by git itself, not this package's thin wrapper.
	bare, commit := newBareRepoWithFiles(t, "c.go", "a.go", "b.go")

	got, err := chore.TrackedFiles(bare, commit)
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	sort.Strings(got)
	want := []string{"a.go", "b.go", "c.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("TrackedFiles = %v, want %v", got, want)
	}
}

func TestTrackedFilesEmptyTree(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	bare := t.TempDir()
	bareRepo := filepath.Join(bare, "repo.git")
	runGit(t, "", "init", "--bare", "-q", bareRepo)
	runGit(t, bareRepo, "config", "gc.auto", "0")

	mktreeCmd := exec.Command("git", "-C", bareRepo, "mktree")
	mktreeCmd.Stdin = strings.NewReader("")
	treeOut, err := mktreeCmd.Output()
	if err != nil {
		t.Fatalf("mktree (empty): %v", err)
	}
	tree := strings.TrimSpace(string(treeOut))
	commitOut, err := exec.Command("git", "-C", bareRepo, "commit-tree", tree, "-m", "empty").Output()
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	commit := strings.TrimSpace(string(commitOut))

	got, err := chore.TrackedFiles(bareRepo, commit)
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("TrackedFiles on an empty tree = %v, want empty", got)
	}
}
