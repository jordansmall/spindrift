package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGitT runs `git <args...>` with its working directory set to dir,
// failing the test immediately on error -- a small test-only convenience
// wrapper around the same convention integrate.go's own runGitIn uses.
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// currentBranchT returns the short name of the branch currently checked
// out in dir -- chdirToFreshWorkerRepo never pins a specific default branch
// name (it depends on the ambient git/init.defaultBranch config), so tests
// that need to return to "whatever branch HEAD started on" must discover
// it rather than assume "main" or "master".
func currentBranchT(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGitT(t, dir, "symbolic-ref", "--short", "HEAD"))
}

// TestIntegrateSliceBranchCleanCommit verifies a branch carrying one clean,
// non-conflicting commit ahead of HEAD integrates: integrateSliceBranch
// returns integrateOK, and the change is actually present on the
// integrating repo's own HEAD afterward as a real commit (issue #2060).
func TestIntegrateSliceBranchCleanCommit(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)
	base := currentBranchT(t, repoRoot)

	runGitT(t, repoRoot, "checkout", "-b", "orchestrator-worker/clean-slice")
	if err := os.WriteFile(filepath.Join(repoRoot, "new.txt"), []byte("hello from the worker\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitT(t, repoRoot, "add", "new.txt")
	runGitT(t, repoRoot, "commit", "-m", "add new.txt")
	runGitT(t, repoRoot, "checkout", base)

	status, out, err := integrateSliceBranch(repoRoot, "clean-slice", "orchestrator-worker/clean-slice")
	if err != nil {
		t.Fatalf("integrateSliceBranch() error = %v", err)
	}
	if status != integrateOK {
		t.Fatalf("integrateSliceBranch() status = %q, want %q (output: %q)", status, integrateOK, out)
	}

	got, err := os.ReadFile(filepath.Join(repoRoot, "new.txt"))
	if err != nil {
		t.Fatalf("ReadFile(new.txt) after integration: %v", err)
	}
	if string(got) != "hello from the worker\n" {
		t.Errorf("new.txt content = %q, want the worker's own content present on HEAD", got)
	}

	log := runGitT(t, repoRoot, "log", "--oneline", "-1")
	if !strings.Contains(log, "integrate slice clean-slice") {
		t.Errorf("git log -1 = %q, want the integration commit as HEAD's own tip", log)
	}
}

// TestIntegrateSliceBranchNoNewCommits verifies a branch identical to HEAD
// (no commits past the merge-base) returns integrateEmpty, leaves HEAD
// unchanged, and creates no commit (issue #2060).
func TestIntegrateSliceBranchNoNewCommits(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)

	runGitT(t, repoRoot, "branch", "orchestrator-worker/idle-slice")

	before := strings.TrimSpace(runGitT(t, repoRoot, "rev-parse", "HEAD"))

	status, out, err := integrateSliceBranch(repoRoot, "idle-slice", "orchestrator-worker/idle-slice")
	if err != nil {
		t.Fatalf("integrateSliceBranch() error = %v", err)
	}
	if status != integrateEmpty {
		t.Fatalf("integrateSliceBranch() status = %q, want %q (output: %q)", status, integrateEmpty, out)
	}

	after := strings.TrimSpace(runGitT(t, repoRoot, "rev-parse", "HEAD"))
	if before != after {
		t.Errorf("HEAD = %q after integration, want unchanged %q", after, before)
	}
}

// TestIntegrateSliceBranchConflictAbortsCleanly verifies a branch whose own
// commit conflicts with a change already on HEAD returns integrateConflict
// with non-empty output describing the conflict, and leaves the working
// tree clean afterward -- no half-finished cherry-pick left behind (issue
// #2060).
func TestIntegrateSliceBranchConflictAbortsCleanly(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)
	base := currentBranchT(t, repoRoot)

	// Branch off the initial commit (before either side ever touches
	// conflict.txt), write conflict.txt with one content on the branch...
	runGitT(t, repoRoot, "checkout", "-b", "orchestrator-worker/conflict-slice")
	if err := os.WriteFile(filepath.Join(repoRoot, "conflict.txt"), []byte("worker's own content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitT(t, repoRoot, "add", "conflict.txt")
	runGitT(t, repoRoot, "commit", "-m", "worker adds conflict.txt")

	// ...then, back on the original branch, write conflict.txt with
	// DIFFERENT content -- an add/add conflict against the same merge-base.
	runGitT(t, repoRoot, "checkout", base)
	if err := os.WriteFile(filepath.Join(repoRoot, "conflict.txt"), []byte("HEAD's own different content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitT(t, repoRoot, "add", "conflict.txt")
	runGitT(t, repoRoot, "commit", "-m", "HEAD adds conflict.txt independently")

	status, out, err := integrateSliceBranch(repoRoot, "conflict-slice", "orchestrator-worker/conflict-slice")
	if err != nil {
		t.Fatalf("integrateSliceBranch() error = %v", err)
	}
	if status != integrateConflict {
		t.Fatalf("integrateSliceBranch() status = %q, want %q (output: %q)", status, integrateConflict, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("integrateSliceBranch() output is empty, want non-empty conflict detail")
	}

	statusOut := runGitT(t, repoRoot, "status", "--short")
	if strings.TrimSpace(statusOut) != "" {
		t.Errorf("git status --short after conflict = %q, want empty (working tree must be left clean, no half-finished cherry-pick)", statusOut)
	}

	// A stray CHERRY_PICK_HEAD is the more precise signal that the
	// cherry-pick was genuinely aborted, not just that the working tree
	// happens to look clean.
	if _, err := os.Stat(filepath.Join(repoRoot, ".git", "CHERRY_PICK_HEAD")); !os.IsNotExist(err) {
		t.Errorf("CHERRY_PICK_HEAD exists (stat err = %v), want no in-progress cherry-pick left behind", err)
	}
}
