package main

import (
	"os/exec"
	"strings"
	"testing"
)

// chdirToFreshGitRepo creates a disposable git repo with one empty commit,
// chdirs the test into it, and returns its path. Tests that exercise code
// resolving the repo root via os.Getwd() need this: the checked-out repo has
// no `.git` directory once copied into the Nix build sandbox `checks-inbox`
// runs under, so a bare `git rev-parse HEAD` against "." would fail there
// even though it succeeds in a plain `go test` run from a real working tree.
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

// gitOutputT runs `git <args...>` with its working directory set to dir,
// failing the test on a non-zero exit, and returns its trimmed combined
// output.
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
