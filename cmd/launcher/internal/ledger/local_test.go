package ledger_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
)

// setGitIdentityEnv gives ambient git commands a commit identity, matching
// internal/forge/local's convention: production code (commitIdentityEnv)
// always sets identity explicitly on the commits it makes, but plumbing
// commands like `git init` still consult the ambient environment.
func setGitIdentityEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
}

// newBareRepo creates a bare repo with a single "main" branch (one commit),
// so a contract case's refs/heads/* snapshot is non-empty and the
// no-branch-moved assertion actually bites.
func newBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "repo.git")
	runGit(t, "", "init", "--bare", "-q", bare)
	// Same rationale as forgetest.NewGitRepoFixture: a detached `git gc
	// --auto` can still be repacking when t.TempDir()'s RemoveAll runs.
	runGit(t, bare, "config", "gc.auto", "0")
	runGit(t, bare, "config", "receive.autogc", "false")

	tree := runGitOutputWithStdin(t, bare, "", "mktree")
	commit := runGitOutputWithStdin(t, bare, "", "commit-tree", tree, "-m", "base")
	runGit(t, bare, "update-ref", "refs/heads/main", commit)
	return bare
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	var full []string
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	} else {
		full = args
	}
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", full, err, out)
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", full, err)
	}
	return strings.TrimSpace(string(out))
}

func runGitOutputWithStdin(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", full, err)
	}
	return strings.TrimSpace(string(out))
}

func newHarness(t *testing.T) ledgertest.Harness {
	t.Helper()
	setGitIdentityEnv(t)
	return localHarness{bare: newBareRepo(t)}
}

// localHarness is the ledgertest.Harness for Local: Backend and Rival are
// two independent Local handles over the same bare repo.
type localHarness struct {
	bare string
}

func (h localHarness) Backend() ledger.Backend { return ledger.Local{Repo: h.bare} }
func (h localHarness) Rival() ledger.Backend   { return ledger.Local{Repo: h.bare} }

func (h localHarness) Branches() map[string]string {
	out, err := exec.Command("git", "-C", h.bare, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/").Output()
	if err != nil {
		return nil
	}
	branches := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		branches[fields[0]] = fields[1]
	}
	return branches
}

func TestLocalLedgerContract(t *testing.T) {
	ledgertest.RunContract(t, newHarness)
}

// TestLocalChain asserts what the contract can't in a backend-agnostic way:
// a done commit's parent is the claim commit, and the ref lives under
// refs/spindrift/butler/, never refs/heads/.
func TestLocalChain(t *testing.T) {
	setGitIdentityEnv(t)
	bare := newBareRepo(t)
	b := ledger.Local{Repo: bare}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(b, "chore-1", ledger.Tip{}, ledger.ClaimedBy{Host: "worker", Start: start})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	done, err := ledger.Finish(b, "chore-1", claim, ledger.State{}, start.Add(time.Minute))
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	parent := runGitOutput(t, bare, "rev-parse", done.Commit+"^")
	if parent != claim.Commit {
		t.Fatalf("done commit's parent = %s, want the claim commit %s", parent, claim.Commit)
	}

	if _, err := exec.Command("git", "-C", bare, "rev-parse", "-q", "--verify", ledger.RefPrefix+"chore-1").Output(); err != nil {
		t.Fatalf("ref %s does not resolve: %v", ledger.RefPrefix+"chore-1", err)
	}
	if _, err := exec.Command("git", "-C", bare, "rev-parse", "-q", "--verify", "refs/heads/chore-1").Output(); err == nil {
		t.Fatal("a branch refs/heads/chore-1 was created; the Ledger must never write under refs/heads/")
	}
}
