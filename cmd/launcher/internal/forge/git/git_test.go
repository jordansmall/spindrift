package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestGitClient_ImplementsCodeForge asserts that GitClient satisfies forge.CodeForge.
func TestGitClient_ImplementsCodeForge(t *testing.T) {
	var _ forge.CodeForge = NewGitClient("https://example.invalid/repo.git", "main", "Test Bot", "bot@example.com", "agent/issue-")
}

// TestGitClient_NoPRForgeConcept verifies that the git Code Forge implements
// no PR/CI/auto-merge surface at all — a type assertion against forge.PRForge
// reports absence, the mechanism callers use instead of a removed PushOnly()
// flag.
func TestGitClient_NoPRForgeConcept(t *testing.T) {
	g := NewGitClient("https://example.invalid/repo.git", "main", "Test Bot", "bot@example.com", "agent/issue-")
	if _, ok := g.(forge.PRForge); ok {
		t.Error("gitClient satisfies forge.PRForge, want it to implement forge.CodeForge only")
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
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")

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
// reports forge.ErrMergeConflict when the feature branch conflicts with base,
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

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	err := g.Merge("agent/issue-1")
	if err != forge.ErrMergeConflict {
		t.Fatalf("Merge: want forge.ErrMergeConflict, got: %v", err)
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

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
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

// TestGitClient_Merge_RejectsFlagLikeRef verifies that Merge refuses a landing
// ref starting with "-" instead of passing it to git, where it would be
// parsed as an option (e.g. a maliciously crafted outcome line's landing=
// field — the outcome line is untrusted input per CLAUDE.md's
// comment-injection trust boundary). Regression test for argument-injection
// RCE via `git fetch origin <branch>`.
func TestGitClient_Merge_RejectsFlagLikeRef(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")

	canary := filepath.Join(t.TempDir(), "pwned")
	err := g.Merge("--upload-pack=touch " + canary)
	if err == nil {
		t.Fatal("Merge: want error for a flag-like ref, got nil")
	}
	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatal("Merge executed the injected command — argument injection succeeded")
	}
}

// TestGitClient_Rebase_RejectsFlagLikeRef is Rebase's counterpart to
// TestGitClient_Merge_RejectsFlagLikeRef.
func TestGitClient_Rebase_RejectsFlagLikeRef(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")

	if err := g.Rebase("--upload-pack=touch /tmp/should-not-run"); err == nil {
		t.Fatal("Rebase: want error for a flag-like ref, got nil")
	}
}

// TestGitClient_Merge_SetsCommitIdentityOnTempClone verifies that Merge
// configures the launcher-supplied commit identity on its throwaway clone
// rather than depending on ambient host git config, which may be unset on a
// bare CI runner (a real merge commit needs a committer identity).
func TestGitClient_Merge_SetsCommitIdentityOnTempClone(t *testing.T) {
	bare := newBareRemoteWithBranches(t)
	g := NewGitClient(bare, "main", "Spindrift Bot", "bot@example.com", "agent/issue-")

	if err := g.Merge("agent/issue-1"); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	verify := t.TempDir()
	gitRun(t, "", "clone", bare, verify)
	gitRun(t, verify, "checkout", "main")
	out := exec.Command("git", "-C", verify, "log", "-1", "--format=%an <%ae>", "main")
	got, err := out.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if strings.TrimSpace(string(got)) != "Spindrift Bot <bot@example.com>" {
		t.Errorf("merge commit identity = %q, want \"Spindrift Bot <bot@example.com>\"", strings.TrimSpace(string(got)))
	}
}

// TestGitClient_Merge_CloneFailureDoesNotLeakCredentials verifies that a
// clone failure against a credential-bearing remote URL (the
// oauth2:<token>@host form CODE_FORGE_REMOTE_URL uses for hosts without a
// credential helper, docs/reference.md) never echoes the credential back
// into the returned error — that error flows unmodified into a public
// GitHub issue comment (settle.mergeImmediate).
func TestGitClient_Merge_CloneFailureDoesNotLeakCredentials(t *testing.T) {
	const secret = "sometoken123"
	g := NewGitClient("https://oauth2:"+secret+"@127.0.0.1:1/does-not-exist.git", "main", "Test Bot", "bot@example.com", "agent/issue-")

	err := g.Merge("agent/issue-1")
	if err == nil {
		t.Fatal("Merge against unreachable credential-bearing remote: want error, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Merge error leaks embedded credential: %v", err)
	}
}

// TestGitClient_Probe verifies Probe succeeds against a reachable remote and
// fails against an unreachable one.
func TestGitClient_Probe(t *testing.T) {
	bare := newBareRemoteWithBranches(t)

	g := NewGitClient(bare, "main", "Test Bot", "bot@example.com", "agent/issue-")
	if _, err := g.Probe(); err != nil {
		t.Errorf("Probe on reachable remote: %v", err)
	}

	bad := NewGitClient(filepath.Join(t.TempDir(), "does-not-exist.git"), "main", "Test Bot", "bot@example.com", "agent/issue-")
	if _, err := bad.Probe(); err == nil {
		t.Error("Probe on unreachable remote: want error, got nil")
	}
}

// TestGitClient_Probe_DoesNotLeakCredentials verifies that Probe's error
// against a credential-bearing remote URL never echoes the credential back
// — Probe's error can reach `doctor` output, and any error text derived
// from remoteURL must stay redacted the same way Merge/Rebase's do.
func TestGitClient_Probe_DoesNotLeakCredentials(t *testing.T) {
	const secret = "sometoken123"
	g := NewGitClient("https://oauth2:"+secret+"@127.0.0.1:1/does-not-exist.git", "main", "Test Bot", "bot@example.com", "agent/issue-")

	_, err := g.Probe()
	if err == nil {
		t.Fatal("Probe against unreachable credential-bearing remote: want error, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Probe error leaks embedded credential: %v", err)
	}
}
