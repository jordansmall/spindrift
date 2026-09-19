package main

import (
	"os/exec"
	"strings"
	"testing"
)

// Code that resolves the repo root via os.Getwd() needs a real repo: the
// checked-out tree has no .git directory once copied into the Nix build
// sandbox that checks-inbox runs under, so `git rev-parse HEAD` against "."
// fails there even though it succeeds under a plain `go test` run.
func chdirToFreshGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "orchestrator-test@example.com")
	run("config", "user.name", "Orchestrator Test")
	run("commit", "--allow-empty", "-m", "init")
	t.Chdir(dir)
	return dir
}

func gitOutputT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
