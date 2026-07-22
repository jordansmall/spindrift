package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/seambundle"
)

func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s): %v: %s", args, dir, err, out)
	}
	return string(out)
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunBundleOut_ParsesFlagsAndDelegates verifies the bundle-out
// subcommand's flag parsing reaches bundleout.Run with the right Config: a
// non-empty base..branch range produces a bundle at outbox/seam.bundle
// (issue #1808).
func TestRunBundleOut_ParsesFlagsAndDelegates(t *testing.T) {
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "-b", "main")
	runGitCmd(t, dir, "config", "user.name", "Test Bot")
	runGitCmd(t, dir, "config", "user.email", "bot@example.com")
	writeTestFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runGitCmd(t, dir, "add", "base.txt")
	runGitCmd(t, dir, "commit", "-m", "base")
	runGitCmd(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(dir, "feature.txt"), "feature\n")
	runGitCmd(t, dir, "add", "feature.txt")
	runGitCmd(t, dir, "commit", "-m", "feature")

	outbox := t.TempDir()
	var stdout bytes.Buffer
	rc := runBundleOut([]string{
		"--repo", dir,
		"--base", "main",
		"--branch", "feature",
		"--outbox", outbox,
		"--issue", "7",
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runBundleOut exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outbox, seambundle.FileName)); err != nil {
		t.Fatalf("bundle not created: %v", err)
	}
}

// TestRunBundleOut_MissingRequiredFlagReturnsNonZero verifies a missing
// -branch fails loudly (exit 1) instead of running bundleout.Run against a
// zero-value Config.
func TestRunBundleOut_MissingRequiredFlagReturnsNonZero(t *testing.T) {
	var stdout bytes.Buffer
	rc := runBundleOut([]string{
		"--repo", t.TempDir(),
		"--base", "main",
		"--outbox", t.TempDir(),
	}, &stdout)
	if rc == 0 {
		t.Fatal("runBundleOut exit = 0, want non-zero for a missing -branch")
	}
}

// TestIsBundleOutInvocation verifies the bundle-out subcommand's dispatch
// guard: a bare "bundle-out" first arg selects it, while the ordinary
// "--"-flag invocation (and no args at all) fall through to the default
// Driver-invocation path.
func TestIsBundleOutInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"bundle-out first arg", []string{"bundle-out", "--repo", "x"}, true},
		{"ordinary flag invocation", []string{"--prompt-file", "x"}, false},
		{"no args", nil, false},
	}
	for _, c := range cases {
		if got := isBundleOutInvocation(c.args); got != c.want {
			t.Errorf("%s: isBundleOutInvocation(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}
