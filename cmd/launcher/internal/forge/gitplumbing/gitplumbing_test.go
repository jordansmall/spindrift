package gitplumbing

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func TestIsMergeConflict_DetectsMergeConflictMarker(t *testing.T) {
	if !IsMergeConflict("error: merge conflict in file.go") {
		t.Fatal("want true for stderr containing 'merge conflict'")
	}
}

func TestIsMergeConflict_IgnoresUnrelatedError(t *testing.T) {
	if IsMergeConflict("error: permission denied") {
		t.Fatal("want false for unrelated stderr")
	}
}

// TestGitForcePush_StaleLeaseIsNotTransient verifies that a genuine
// stale-lease rejection — the branch moved since the last fetch, so the
// rebase really is out of date — is NOT classified as transient: retrying it
// would be pointless, so callers must treat it as terminal.
//
// This also subsumes the former TestGitForcePush_CapturesStderr (previously
// in cmd/launcher/internal/forge/github/exec_test.go, removed in b1d0489 /
// #684): the stderr-substring assertion below is that test's check, merged
// into this one alongside the non-transient classification.
func TestGitForcePush_StaleLeaseIsNotTransient(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")
	other := filepath.Join(dir, "other")

	run := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeFile := func(path, contents string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("", "init", "--bare", bare)
	run("", "clone", bare, work)
	run(work, "checkout", "-B", "main")
	run(work, "config", "user.email", "test@example.com")
	run(work, "config", "user.name", "Test")
	writeFile(filepath.Join(work, "a.txt"), "one\n")
	run(work, "add", "a.txt")
	run(work, "commit", "-m", "first")
	run(work, "push", "-u", "origin", "main")

	run("", "clone", bare, other)
	run(other, "checkout", "-B", "main", "origin/main")
	run(other, "config", "user.email", "test@example.com")
	run(other, "config", "user.name", "Test")
	writeFile(filepath.Join(other, "b.txt"), "two\n")
	run(other, "add", "b.txt")
	run(other, "commit", "-m", "second")
	run(other, "push", "origin", "main")

	// work's remote-tracking ref is now stale relative to origin/main.
	run(work, "commit", "--allow-empty", "-m", "local change")

	err := GitForcePush(context.Background(), work)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "stale info") {
		t.Fatalf("want error to include git's stderr (stale info), got: %v", err)
	}
	if errors.Is(err, forge.ErrTransientPushFailure) {
		t.Fatalf("want a terminal (non-transient) error for a genuine stale-lease rejection, got: %v", err)
	}
}

// TestGitForcePush_TransientFailureIsRetryable verifies that a push failure
// with no ref-rejection markers in its stderr — e.g. a network or forge
// outage — is classified as transient so callers can retry it.
func TestGitForcePush_TransientFailureIsRetryable(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")

	run := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	run("", "init", work)
	run(work, "checkout", "-B", "main")
	run(work, "config", "user.email", "test@example.com")
	run(work, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "a.txt")
	run(work, "commit", "-m", "first")
	// No real remote: the push fails on a generic infra-shaped error, with
	// no stale-lease/rejection markers in stderr.
	run(work, "remote", "add", "origin", filepath.Join(dir, "does-not-exist"))

	err := GitForcePush(context.Background(), work)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, forge.ErrTransientPushFailure) {
		t.Fatalf("want forge.ErrTransientPushFailure, got: %v", err)
	}
}

// TestWrapForcePushError_RedactsCredentialsFromStderr verifies that
// wrapForcePushError never echoes a credential embedded in git's stderr
// into the returned error. Exercised directly against a crafted stderr
// string rather than a real subprocess: modern git already anonymizes URLs
// in its own connect/access diagnostics, which would make a subprocess-level
// test pass regardless of whether our own formatting redacts — this proves
// the code path itself redacts, independent of that git-version behavior.
// git's stderr for a credential-bearing remote CAN echo the full URL for
// other failure shapes (e.g. server-side auth rejection messages), and this
// error flows unmodified into a public GitHub issue comment
// (settle.mergeImmediate).
func TestWrapForcePushError_RedactsCredentialsFromStderr(t *testing.T) {
	const secret = "sometoken123"
	stderr := "fatal: unable to access 'https://oauth2:" + secret + "@git.example.com/org/repo.git/': The requested URL returned error: 403"

	err := wrapForcePushError(errors.New("exit status 128"), stderr)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("wrapForcePushError leaks embedded credential: %v", err)
	}
}
