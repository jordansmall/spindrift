package forge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	run(other, "checkout", "-B", "main")
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
