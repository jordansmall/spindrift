package main

import (
	"os"
	"os/exec"
	"testing"
)

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// Git forks a detached `git gc --auto` / `git maintenance run --auto` once
	// a commit crosses the loose-object threshold, and it can still be
	// repacking when t.TempDir()'s RemoveAll runs, failing the test with
	// "directory not empty" on .git/objects.
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s): %v: %s", args, dir, err, out)
	}
	return string(out)
}
