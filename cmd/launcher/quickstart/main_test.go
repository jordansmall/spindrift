package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestForceFlagUsage_NoBakPromise(t *testing.T) {
	if !strings.Contains(forceFlagUsage, "backing each up") {
		t.Errorf("forceFlagUsage = %q, want it to mention backing each up", forceFlagUsage)
	}
	if strings.Contains(forceFlagUsage, "*.bak") {
		t.Errorf("forceFlagUsage = %q, must not promise a fixed *.bak backup name", forceFlagUsage)
	}
}

// TestHostEnvironment_InsideGitWorkTree covers issue #2567: the finish-line
// git-add reminder relies on hostEnvironment.InsideGitWorkTree correctly
// distinguishing a real git work tree from a plain directory.
func TestHostEnvironment_InsideGitWorkTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	workTree := t.TempDir()
	if err := exec.Command("git", "init", workTree).Run(); err != nil {
		t.Fatalf("git init %s: %v", workTree, err)
	}
	if got := (hostEnvironment{}).InsideGitWorkTree(workTree); !got {
		t.Errorf("InsideGitWorkTree(%q) = false, want true for a git-init'd dir", workTree)
	}

	plainDir := t.TempDir()
	// t.TempDir() is not guaranteed to sit outside every git work tree (e.g.
	// if TMPDIR is itself under a repo checkout) — set GIT_CEILING_DIRECTORIES
	// so `git rev-parse --is-inside-work-tree` cannot walk up past plainDir's
	// parent and find an ancestor .git, keeping this assertion deterministic.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(plainDir))
	if got := (hostEnvironment{}).InsideGitWorkTree(plainDir); got {
		t.Errorf("InsideGitWorkTree(%q) = true, want false for a dir that was never git-init'd", plainDir)
	}
}
