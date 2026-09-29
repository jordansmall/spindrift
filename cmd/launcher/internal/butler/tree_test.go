package butler

import (
	"os/exec"
	"sort"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ledger/ledgertest"
)

// newTreeBareRepo builds a bare repo with one commit on "main" holding the
// given paths, via the shared ledgertest.NewRepo fixture, and also returns
// the commit sha the tree tests assert against.
func newTreeBareRepo(t *testing.T, paths ...string) (repo, commit string) {
	t.Helper()
	bare := ledgertest.NewRepo(t, paths...)
	out, err := exec.Command("git", "-C", bare, "rev-parse", "refs/heads/main").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return bare, strings.TrimSpace(string(out))
}

func TestGitTreeHead(t *testing.T) {
	bare, commit := newTreeBareRepo(t, "a.go", "b.go")
	tree := GitTree{Repo: bare}

	got, err := tree.Head("main")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if got != commit {
		t.Fatalf("Head(main) = %s, want %s", got, commit)
	}

	if _, err := tree.Head("no-such-branch"); err == nil {
		t.Fatal("Head(no-such-branch): got nil error, want one")
	}
}

func TestGitTreeTrackedFiles(t *testing.T) {
	// mktree builds one flat tree (no --batch nesting), so this fixture
	// sticks to top-level paths; ls-tree -r's recursion into subtrees is
	// exercised by git itself, not this thin wrapper.
	bare, commit := newTreeBareRepo(t, "c.go", "a.go", "b.go")
	tree := GitTree{Repo: bare}

	got, err := tree.TrackedFiles(commit)
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	sort.Strings(got)
	want := []string{"a.go", "b.go", "c.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("TrackedFiles = %v, want %v", got, want)
	}
}

func TestGitTreeTrackedFilesEmptyTree(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	bare := ledgertest.NewRepo(t)

	mktreeCmd := exec.Command("git", "-C", bare, "mktree")
	mktreeCmd.Stdin = strings.NewReader("")
	treeOut, err := mktreeCmd.Output()
	if err != nil {
		t.Fatalf("mktree (empty): %v", err)
	}
	tree := strings.TrimSpace(string(treeOut))
	commitOut, err := exec.Command("git", "-C", bare, "commit-tree", tree, "-m", "empty").Output()
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	commit := strings.TrimSpace(string(commitOut))

	got, err := GitTree{Repo: bare}.TrackedFiles(commit)
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("TrackedFiles on an empty tree = %v, want empty", got)
	}
}

// TestFetchTree_FetchesBranchAndReturnsTreeOverScratch drives FetchTree
// against a bare repo standing in for a hosted forge's remote: an
// already-initialized bare scratch repo (mirroring the state remoteLedger
// hands FetchTree after ledger.NewRemote's own git init --bare), then
// FetchTree fetches "main" into it exactly once.
func TestFetchTree_FetchesBranchAndReturnsTreeOverScratch(t *testing.T) {
	remote, head := newTreeBareRepo(t, "a.go", "b.go")
	scratch := ledgertest.NewRepo(t)

	tree, err := FetchTree(scratch, remote, "main")
	if err != nil {
		t.Fatalf("FetchTree: %v", err)
	}

	got, err := tree.Head("main")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if got != head {
		t.Fatalf("Head(main) = %s, want remote head %s", got, head)
	}

	files, err := tree.TrackedFiles(got)
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	sort.Strings(files)
	want := []string{"a.go", "b.go"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("TrackedFiles = %v, want %v", files, want)
	}
}

// TestFetchTree_RedactsCredentialsOnError pins that a failed fetch's error
// never leaks a URL's embedded token, the same guarantee ledger.Remote's own
// git wrappers give (forge.RedactURLCredentials) -- the port at 1 refuses
// the connection immediately rather than hanging on GIT_TERMINAL_PROMPT=0.
func TestFetchTree_RedactsCredentialsOnError(t *testing.T) {
	scratch := ledgertest.NewRepo(t)
	const secret = "secret-token"
	url := "https://user:" + secret + "@127.0.0.1:1/x.git"

	_, err := FetchTree(scratch, url, "main")
	if err == nil {
		t.Fatal("FetchTree: got nil error, want one (unreachable remote)")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("FetchTree error leaked the credential: %v", err)
	}
}
