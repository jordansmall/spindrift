package forge

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGH writes a fake gh script to dir and returns the augmented PATH that
// picks it up first. The script exits with exitCode and prints stderr to
// os.Stderr, then records its argv to dir/gh-args.txt.
func fakeGH(t *testing.T, exitCode int, fakeStderr string) (ghDir string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > "%s/gh-args.txt"
printf '%%s' %q >&2
exit %d
`, dir, fakeStderr, exitCode)
	ghPath := filepath.Join(dir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// withFakeGH prepends dir to PATH for the duration of the test.
func withFakeGH(t *testing.T, dir string) {
	t.Helper()
	old := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", old) })
	os.Setenv("PATH", dir+":"+old)
}

// TestProbe_PositionalSlug verifies that Probe passes the slug as a positional
// argument to `gh repo view` (no --repo flag).
func TestProbe_PositionalSlug(t *testing.T) {
	// We need two fake gh scripts: one for `gh auth status` (success) and one
	// for `gh repo view`. A single script handles both calls — we only inspect
	// the args file from the repo-view call, which is the second invocation.
	// Use a counting wrapper approach: write args to numbered files.
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
n=$(ls "%s"/call-*.txt 2>/dev/null | wc -l)
printf '%%s\n' "$@" > "%s/call-$(printf '%%02d' $n).txt"
`, dir, dir)
	ghPath := filepath.Join(dir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	withFakeGH(t, dir)

	c := NewExecClient("owner/repo", DispatchLabels{})
	// Both gh calls succeed (exit 0). Probe may return an error because the
	// output is empty — that's fine; we only care about the argv shape.
	it, ok := c.(interface{ Probe() (string, error) })
	if !ok {
		t.Fatal("Client does not expose Probe")
	}
	it.Probe() //nolint:errcheck

	// The second call (call-01.txt) is `gh repo view …`
	argsFile := filepath.Join(dir, "call-01.txt")
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("call-01.txt not written: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")

	// Must contain "owner/repo" as a positional arg.
	found := false
	for _, a := range args {
		if a == "owner/repo" {
			found = true
		}
		// Must NOT contain --repo flag.
		if a == "--repo" {
			t.Fatalf("Probe passed --repo flag to gh repo view; args: %q", args)
		}
	}
	if !found {
		t.Fatalf("slug not found as positional arg in gh repo view; args: %q", args)
	}
}

// TestProbe_StderrSurfaced verifies that when gh repo view fails, the returned
// error contains gh's actual stderr rather than just the configured slug.
func TestProbe_StderrSurfaced(t *testing.T) {
	// Call 0: gh auth status — succeed.
	// Call 1: gh repo view — fail with a distinctive stderr.
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
n=$(ls "%s"/call-*.txt 2>/dev/null | wc -l)
printf '%%s\n' "$@" > "%s/call-$(printf '%%02d' $n).txt"
if [ "$1" = "repo" ]; then
  printf 'unknown flag: --repo\n' >&2
  exit 1
fi
`, dir, dir)
	ghPath := filepath.Join(dir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	withFakeGH(t, dir)

	c := NewExecClient("owner/repo", DispatchLabels{})
	it, ok := c.(interface{ Probe() (string, error) })
	if !ok {
		t.Fatal("Client does not expose Probe")
	}
	_, err := it.Probe()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, ErrRepoNotFound) {
		t.Fatalf("want ErrRepoNotFound, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("error must contain gh's stderr; got: %v", err)
	}
}

// TestGitForcePush_CapturesStderr verifies that a rejected force-with-lease
// push returns an error containing git's stderr, not just the exit status.
func TestGitForcePush_CapturesStderr(t *testing.T) {
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
	// Base "other"'s main explicitly on origin/main: the bare repo's HEAD
	// symref may still point at the git installation's default branch name
	// (e.g. "master"), which wouldn't exist yet, leaving "checkout -B main"
	// with no start point to build on.
	run(other, "checkout", "-B", "main", "origin/main")
	run(other, "config", "user.email", "test@example.com")
	run(other, "config", "user.name", "Test")
	writeFile(filepath.Join(other, "b.txt"), "two\n")
	run(other, "add", "b.txt")
	run(other, "commit", "-m", "second")
	run(other, "push", "origin", "main")

	// work's remote-tracking ref is now stale relative to origin/main.
	run(work, "commit", "--allow-empty", "-m", "local change")

	err := gitForcePush(work)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "stale info") {
		t.Fatalf("want error to include git's stderr (stale info), got: %v", err)
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

	err := gitForcePush(work)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, ErrTransientPushFailure) {
		t.Fatalf("want ErrTransientPushFailure, got: %v", err)
	}
}

// TestGitForcePush_StaleLeaseIsNotTransient verifies that a genuine
// stale-lease rejection — the branch moved since the last fetch, so the
// rebase really is out of date — is NOT classified as transient: retrying it
// would be pointless, so callers must treat it as terminal.
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

	err := gitForcePush(work)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if errors.Is(err, ErrTransientPushFailure) {
		t.Fatalf("want a terminal (non-transient) error for a genuine stale-lease rejection, got: %v", err)
	}
}
