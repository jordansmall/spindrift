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

// TestIntegrateSliceBranchRefusesDirtyRepoRoot verifies integrateSliceBranch
// refuses to run at all -- never attempting merge-base/rev-list/cherry-pick
// -- when repoRoot already has pre-existing uncommitted/staged changes
// before the call, and that those changes survive untouched (issue #2060
// review finding: `git cherry-pick --abort` on the conflict path can
// destroy pre-existing staged work, since it resets the index/tree to
// whatever they were when the cherry-pick started, not necessarily clean).
func TestIntegrateSliceBranchRefusesDirtyRepoRoot(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)
	base := currentBranchT(t, repoRoot)

	// Branch off the initial commit, write conflict.txt with one content on
	// the branch...
	runGitT(t, repoRoot, "checkout", "-b", "orchestrator-worker/conflict-slice")
	if err := os.WriteFile(filepath.Join(repoRoot, "conflict.txt"), []byte("worker's own content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitT(t, repoRoot, "add", "conflict.txt")
	runGitT(t, repoRoot, "commit", "-m", "worker adds conflict.txt")

	// ...then, back on the original branch, write conflict.txt with
	// DIFFERENT content, so integrating conflict-slice would conflict.
	runGitT(t, repoRoot, "checkout", base)
	if err := os.WriteFile(filepath.Join(repoRoot, "conflict.txt"), []byte("HEAD's own different content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitT(t, repoRoot, "add", "conflict.txt")
	runGitT(t, repoRoot, "commit", "-m", "HEAD adds conflict.txt independently")

	// Now stage unrelated pre-existing work in repoRoot, simulating the
	// coordinator's own in-flight edit sitting in the index before this
	// call ever runs.
	preexistingPath := filepath.Join(repoRoot, "preexisting.txt")
	if err := os.WriteFile(preexistingPath, []byte("coordinator's own staged work\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitT(t, repoRoot, "add", "preexisting.txt")

	before := strings.TrimSpace(runGitT(t, repoRoot, "rev-parse", "HEAD"))

	status, _, err := integrateSliceBranch(repoRoot, "conflict-slice", "orchestrator-worker/conflict-slice")
	if err == nil {
		t.Fatal("integrateSliceBranch() error = nil, want a non-nil error refusing to run on a dirty repoRoot")
	}
	if status != integrateFailed {
		t.Errorf("integrateSliceBranch() status = %q, want %q", status, integrateFailed)
	}

	after := strings.TrimSpace(runGitT(t, repoRoot, "rev-parse", "HEAD"))
	if before != after {
		t.Errorf("HEAD = %q after refused integration, want unchanged %q", after, before)
	}

	got, readErr := os.ReadFile(preexistingPath)
	if readErr != nil {
		t.Fatalf("ReadFile(preexisting.txt) after refused integration: %v", readErr)
	}
	if string(got) != "coordinator's own staged work\n" {
		t.Errorf("preexisting.txt content = %q, want the pre-existing staged content to survive untouched", got)
	}

	staged := runGitT(t, repoRoot, "diff", "--cached", "--name-only")
	if strings.TrimSpace(staged) != "preexisting.txt" {
		t.Errorf("git diff --cached --name-only after refused integration = %q, want preexisting.txt still staged", staged)
	}
}

// TestIntegrateSliceBranchMergeCommitInRangeIsFailedNotConflict verifies
// that a cherry-pick failure NOT caused by a content conflict -- here, the
// cherry-picked range contains a merge commit, so `git cherry-pick
// --no-commit` errors with "is a merge but no -m option was given" without
// ever entering a real conflict/unmerged-paths state -- is reported as
// integrateFailed with a non-nil error, not integrateConflict (issue #2060
// review finding: misclassifying this as integrateConflict sends the
// coordinator the literal command that just failed --
// `git cherry-pick --no-commit <merge-base>..<branch>` -- as "resolve
// manually" guidance, an unbreakable retry loop since there's no conflict
// to resolve).
func TestIntegrateSliceBranchMergeCommitInRangeIsFailedNotConflict(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)
	base := currentBranchT(t, repoRoot)

	// Build a branch whose own history -- within the range that will be
	// cherry-picked -- contains a real merge commit: branch off, fork a
	// side branch, commit on both, then merge the side branch back in with
	// --no-ff so a genuine two-parent merge commit lands on the branch.
	runGitT(t, repoRoot, "checkout", "-b", "orchestrator-worker/merge-slice")
	runGitT(t, repoRoot, "checkout", "-b", "side-branch")
	if err := os.WriteFile(filepath.Join(repoRoot, "side.txt"), []byte("side content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitT(t, repoRoot, "add", "side.txt")
	runGitT(t, repoRoot, "commit", "-m", "side commit")

	runGitT(t, repoRoot, "checkout", "orchestrator-worker/merge-slice")
	if err := os.WriteFile(filepath.Join(repoRoot, "main.txt"), []byte("main content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitT(t, repoRoot, "add", "main.txt")
	runGitT(t, repoRoot, "commit", "-m", "main commit")
	runGitT(t, repoRoot, "merge", "--no-ff", "side-branch", "-m", "merge side into merge-slice")

	runGitT(t, repoRoot, "checkout", base)

	before := strings.TrimSpace(runGitT(t, repoRoot, "rev-parse", "HEAD"))

	status, out, err := integrateSliceBranch(repoRoot, "merge-slice", "orchestrator-worker/merge-slice")
	if err == nil {
		t.Fatalf("integrateSliceBranch() error = nil, want a non-nil error (output: %q)", out)
	}
	if status != integrateFailed {
		t.Errorf("integrateSliceBranch() status = %q, want %q (err: %v, output: %q)", status, integrateFailed, err, out)
	}

	after := strings.TrimSpace(runGitT(t, repoRoot, "rev-parse", "HEAD"))
	if before != after {
		t.Errorf("HEAD = %q after failed integration, want unchanged %q", after, before)
	}

	statusOut := runGitT(t, repoRoot, "status", "--short")
	if strings.TrimSpace(statusOut) != "" {
		t.Errorf("git status --short after failed integration = %q, want empty (no half-finished cherry-pick left behind)", statusOut)
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, ".git", "CHERRY_PICK_HEAD")); !os.IsNotExist(statErr) {
		t.Errorf("CHERRY_PICK_HEAD exists (stat err = %v), want no in-progress cherry-pick left behind", statErr)
	}
}

// TestIntegrateSliceBranchCommitHookFailureCleansUp verifies that when a
// cherry-pick applies cleanly but the subsequent `git commit` fails (here,
// because a rejecting commit-msg hook is installed), integrateSliceBranch
// still cleans up the in-progress cherry-pick before returning
// integrateFailed -- so the staged changes never survive to trip the
// dirty-tree guard on a later slice's own integration call (issue #2060
// review finding: without this cleanup, one bad commit-msg hook cascades
// into every subsequent slice's integration failing too).
func TestIntegrateSliceBranchCommitHookFailureCleansUp(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)
	base := currentBranchT(t, repoRoot)

	runGitT(t, repoRoot, "checkout", "-b", "orchestrator-worker/hook-slice")
	if err := os.WriteFile(filepath.Join(repoRoot, "new.txt"), []byte("hello from the worker\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitT(t, repoRoot, "add", "new.txt")
	runGitT(t, repoRoot, "commit", "-m", "add new.txt")
	runGitT(t, repoRoot, "checkout", base)

	// Install a commit-msg hook that always rejects the commit, only now --
	// after the worker's own setup commit above -- so it fires only on the
	// integration commit integrateSliceBranch itself is about to attempt.
	hookPath := filepath.Join(repoRoot, ".git", "hooks", "commit-msg")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho \"commit-msg hook rejects all commits\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(commit-msg hook): %v", err)
	}

	before := strings.TrimSpace(runGitT(t, repoRoot, "rev-parse", "HEAD"))

	status, out, err := integrateSliceBranch(repoRoot, "hook-slice", "orchestrator-worker/hook-slice")
	if err == nil {
		t.Fatalf("integrateSliceBranch() error = nil, want a non-nil error from the rejected commit (output: %q)", out)
	}
	if status != integrateFailed {
		t.Errorf("integrateSliceBranch() status = %q, want %q (err: %v)", status, integrateFailed, err)
	}

	after := strings.TrimSpace(runGitT(t, repoRoot, "rev-parse", "HEAD"))
	if before != after {
		t.Errorf("HEAD = %q after failed integration, want unchanged %q", after, before)
	}

	statusOut := runGitT(t, repoRoot, "status", "--porcelain")
	if strings.TrimSpace(statusOut) != "" {
		t.Errorf("git status --porcelain after failed commit = %q, want empty (cherry-pick's staged changes must be cleaned up, not left to cascade into later slices' dirty-tree guard)", statusOut)
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, ".git", "CHERRY_PICK_HEAD")); !os.IsNotExist(statErr) {
		t.Errorf("CHERRY_PICK_HEAD exists (stat err = %v), want no in-progress cherry-pick left behind", statErr)
	}
}

// TestIntegrateSliceBranchAlreadyAppliedIsEmptyNotFailed verifies that when
// two branches independently produce an identical net diff -- the first
// integrated normally, the second's cherry-pick then applies cleanly but
// stages nothing because the tree already matches -- integrateSliceBranch
// reports integrateEmpty rather than treating `git commit`'s "nothing to
// commit" exit as a hard failure (issue #2060 review finding).
func TestIntegrateSliceBranchAlreadyAppliedIsEmptyNotFailed(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)
	base := currentBranchT(t, repoRoot)

	// First branch: adds dup.txt with some content.
	runGitT(t, repoRoot, "checkout", "-b", "orchestrator-worker/first-slice")
	if err := os.WriteFile(filepath.Join(repoRoot, "dup.txt"), []byte("identical content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitT(t, repoRoot, "add", "dup.txt")
	runGitT(t, repoRoot, "commit", "-m", "first slice adds dup.txt")
	runGitT(t, repoRoot, "checkout", base)

	// Second branch: also adds dup.txt with the exact same content, off the
	// same original base -- an independent commit producing an identical
	// net diff.
	runGitT(t, repoRoot, "checkout", "-b", "orchestrator-worker/second-slice")
	if err := os.WriteFile(filepath.Join(repoRoot, "dup.txt"), []byte("identical content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitT(t, repoRoot, "add", "dup.txt")
	runGitT(t, repoRoot, "commit", "-m", "second slice adds dup.txt")
	runGitT(t, repoRoot, "checkout", base)

	// Integrate the first slice normally.
	status, out, err := integrateSliceBranch(repoRoot, "first-slice", "orchestrator-worker/first-slice")
	if err != nil {
		t.Fatalf("integrateSliceBranch(first-slice) error = %v", err)
	}
	if status != integrateOK {
		t.Fatalf("integrateSliceBranch(first-slice) status = %q, want %q (output: %q)", status, integrateOK, out)
	}

	before := strings.TrimSpace(runGitT(t, repoRoot, "rev-parse", "HEAD"))

	// Integrating the second slice now cherry-picks a commit whose net
	// diff is already present on HEAD via the first slice's integration --
	// the cherry-pick applies cleanly but stages nothing.
	status, out, err = integrateSliceBranch(repoRoot, "second-slice", "orchestrator-worker/second-slice")
	if err != nil {
		t.Fatalf("integrateSliceBranch(second-slice) error = %v", err)
	}
	if status != integrateEmpty {
		t.Fatalf("integrateSliceBranch(second-slice) status = %q, want %q (output: %q)", status, integrateEmpty, out)
	}

	after := strings.TrimSpace(runGitT(t, repoRoot, "rev-parse", "HEAD"))
	if before != after {
		t.Errorf("HEAD = %q after already-applied integration, want unchanged %q", after, before)
	}

	statusOut := runGitT(t, repoRoot, "status", "--short")
	if strings.TrimSpace(statusOut) != "" {
		t.Errorf("git status --short after already-applied integration = %q, want empty (working tree must be left clean)", statusOut)
	}

	if _, statErr := os.Stat(filepath.Join(repoRoot, ".git", "CHERRY_PICK_HEAD")); !os.IsNotExist(statErr) {
		t.Errorf("CHERRY_PICK_HEAD exists (stat err = %v), want no in-progress cherry-pick left behind", statErr)
	}
}
