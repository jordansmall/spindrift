package forge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitClient_ImplementsCodeForge asserts that GitClient satisfies CodeForge.
func TestGitClient_ImplementsCodeForge(t *testing.T) {
	var _ CodeForge = NewGitClient("https://example.invalid/repo.git", "main")
}

// TestGitClient_NoPRConcept verifies that OpenPRForBranch and PRForBranch
// always report "not found" — the git Code Forge is push-only and has no PR
// concept at all.
func TestGitClient_NoPRConcept(t *testing.T) {
	g := NewGitClient("https://example.invalid/repo.git", "main")

	if pr, ok, err := g.OpenPRForBranch("agent/issue-1"); err != nil || ok {
		t.Errorf("OpenPRForBranch = (%+v, %v, %v), want (_, false, nil)", pr, ok, err)
	}
	if url, ok, err := g.PRForBranch("agent/issue-1"); err != nil || ok {
		t.Errorf("PRForBranch = (%q, %v, %v), want (_, false, nil)", url, ok, err)
	}
}

// TestGitClient_NoCIOrAutoMergeConcept verifies the remaining CodeForge
// methods that have no meaning off github: PRState and ListPRFiles report
// "not supported", CheckState reports no checks, CanAutoMerge always reports
// false, and EnqueueAutoMerge fails with an actionable message.
func TestGitClient_NoCIOrAutoMergeConcept(t *testing.T) {
	g := NewGitClient("https://example.invalid/repo.git", "main")

	if _, err := g.PRState("agent/issue-1"); err == nil {
		t.Error("PRState: want error, got nil")
	}
	if state, err := g.CheckState("agent/issue-1"); err != nil || state != StateNone {
		t.Errorf("CheckState = (%v, %v), want (StateNone, nil)", state, err)
	}
	if _, err := g.ListPRFiles("agent/issue-1"); err == nil {
		t.Error("ListPRFiles: want error, got nil")
	}
	if ok, err := g.CanAutoMerge(); err != nil || ok {
		t.Errorf("CanAutoMerge = (%v, %v), want (false, nil)", ok, err)
	}
	err := g.EnqueueAutoMerge("agent/issue-1")
	if err == nil {
		t.Fatal("EnqueueAutoMerge: want error, got nil")
	}
	if !strings.Contains(err.Error(), "CODE_FORGE=github") {
		t.Errorf("EnqueueAutoMerge error should point at CODE_FORGE=github, got: %v", err)
	}
}

// gitRun runs git in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func gitWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newBareRemoteWithBranches sets up a bare repo with a "main" branch (one
// commit) and a feature branch "agent/issue-1" (one additional commit on top
// of main), matching the shape a Box leaves behind: base branch plus a pushed
// per-issue branch. Returns the bare repo path.
func newBareRemoteWithBranches(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")

	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, "", "clone", bare, work)
	gitRun(t, work, "checkout", "-B", "main")
	gitRun(t, work, "config", "user.email", "test@example.com")
	gitRun(t, work, "config", "user.name", "Test")
	gitWriteFile(t, filepath.Join(work, "base.txt"), "base\n")
	gitRun(t, work, "add", "base.txt")
	gitRun(t, work, "commit", "-m", "base")
	gitRun(t, work, "push", "-u", "origin", "main")

	gitRun(t, work, "checkout", "-b", "agent/issue-1")
	gitWriteFile(t, filepath.Join(work, "feature.txt"), "feature\n")
	gitRun(t, work, "add", "feature.txt")
	gitRun(t, work, "commit", "-m", "feature")
	gitRun(t, work, "push", "-u", "origin", "agent/issue-1")

	return bare
}

// TestGitClient_Merge_PushOnlyLanding verifies that Merge lands the feature
// branch onto baseBranch and pushes the result — the MERGE_MODE=immediate
// mapping for a push-only forge.
func TestGitClient_Merge_PushOnlyLanding(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	g := NewGitClient(bare, "main")

	if err := g.Merge("agent/issue-1"); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	verify := t.TempDir()
	gitRun(t, "", "clone", bare, verify)
	gitRun(t, verify, "checkout", "main")
	if _, err := os.Stat(filepath.Join(verify, "feature.txt")); err != nil {
		t.Errorf("main does not contain feature.txt after Merge: %v", err)
	}
}

// TestGitClient_Merge_ConflictReturnsErrMergeConflict verifies that Merge
// reports ErrMergeConflict when the feature branch conflicts with base,
// leaving base unpushed.
func TestGitClient_Merge_ConflictReturnsErrMergeConflict(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")

	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, "", "clone", bare, work)
	gitRun(t, work, "checkout", "-B", "main")
	gitRun(t, work, "config", "user.email", "test@example.com")
	gitRun(t, work, "config", "user.name", "Test")
	gitWriteFile(t, filepath.Join(work, "shared.txt"), "base\n")
	gitRun(t, work, "add", "shared.txt")
	gitRun(t, work, "commit", "-m", "base")
	gitRun(t, work, "push", "-u", "origin", "main")

	gitRun(t, work, "checkout", "-b", "agent/issue-1")
	gitWriteFile(t, filepath.Join(work, "shared.txt"), "feature change\n")
	gitRun(t, work, "add", "shared.txt")
	gitRun(t, work, "commit", "-m", "feature")
	gitRun(t, work, "push", "-u", "origin", "agent/issue-1")

	gitRun(t, work, "checkout", "main")
	gitWriteFile(t, filepath.Join(work, "shared.txt"), "conflicting main change\n")
	gitRun(t, work, "add", "shared.txt")
	gitRun(t, work, "commit", "-m", "conflicting")
	gitRun(t, work, "push", "origin", "main")

	g := NewGitClient(bare, "main")
	err := g.Merge("agent/issue-1")
	if err != ErrMergeConflict {
		t.Fatalf("Merge: want ErrMergeConflict, got: %v", err)
	}
}

// TestGitClient_Rebase_ForcePushesRebasedBranch verifies that Rebase rebases
// the feature branch onto the latest base and force-pushes it back.
func TestGitClient_Rebase_ForcePushesRebasedBranch(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")

	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, "", "clone", bare, work)
	gitRun(t, work, "checkout", "-B", "main")
	gitRun(t, work, "config", "user.email", "test@example.com")
	gitRun(t, work, "config", "user.name", "Test")
	gitWriteFile(t, filepath.Join(work, "base.txt"), "base\n")
	gitRun(t, work, "add", "base.txt")
	gitRun(t, work, "commit", "-m", "base")
	gitRun(t, work, "push", "-u", "origin", "main")

	gitRun(t, work, "checkout", "-b", "agent/issue-1")
	gitWriteFile(t, filepath.Join(work, "feature.txt"), "feature\n")
	gitRun(t, work, "add", "feature.txt")
	gitRun(t, work, "commit", "-m", "feature")
	gitRun(t, work, "push", "-u", "origin", "agent/issue-1")

	// Advance main so the feature branch is now behind.
	gitRun(t, work, "checkout", "main")
	gitWriteFile(t, filepath.Join(work, "later.txt"), "later\n")
	gitRun(t, work, "add", "later.txt")
	gitRun(t, work, "commit", "-m", "later main commit")
	gitRun(t, work, "push", "origin", "main")

	g := NewGitClient(bare, "main")
	if err := g.Rebase("agent/issue-1"); err != nil {
		t.Fatalf("Rebase: %v", err)
	}

	verify := t.TempDir()
	gitRun(t, "", "clone", bare, verify)
	gitRun(t, verify, "checkout", "agent/issue-1")
	if _, err := os.Stat(filepath.Join(verify, "later.txt")); err != nil {
		t.Errorf("rebased branch does not contain later.txt from base: %v", err)
	}
	if _, err := os.Stat(filepath.Join(verify, "feature.txt")); err != nil {
		t.Errorf("rebased branch lost feature.txt: %v", err)
	}
}

// TestGitClient_Probe verifies Probe succeeds against a reachable remote and
// fails against an unreachable one.
func TestGitClient_Probe(t *testing.T) {
	bare := newBareRemoteWithBranches(t)

	g := NewGitClient(bare, "main")
	if _, err := g.Probe(); err != nil {
		t.Errorf("Probe on reachable remote: %v", err)
	}

	bad := NewGitClient(filepath.Join(t.TempDir(), "does-not-exist.git"), "main")
	if _, err := bad.Probe(); err == nil {
		t.Error("Probe on unreachable remote: want error, got nil")
	}
}
