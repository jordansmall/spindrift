package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

// A CODE_FORGE=local repo with a base and a branch commit emits one
// SPINDRIFT_OUTCOME line carrying landing=<branch> and never pushes (issue
// #2157). The branch commit and the host-mediated relay are git-verified
// evidence, so Run resolves status=ready, not the always-blocked default
// (issue #2380).
func TestRunOutcomeBackstop_ParsesFlagsAndEmits(t *testing.T) {
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "-b", "main")
	runGitCmd(t, dir, "config", "user.name", "Test Bot")
	runGitCmd(t, dir, "config", "user.email", "bot@example.com")
	writeTestFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runGitCmd(t, dir, "add", "base.txt")
	runGitCmd(t, dir, "commit", "-m", "base")
	runGitCmd(t, dir, "checkout", "-b", "agent/issue-42")
	writeTestFile(t, filepath.Join(dir, "feature.txt"), "feature\n")
	runGitCmd(t, dir, "add", "feature.txt")
	runGitCmd(t, dir, "commit", "-m", "feature")

	var stdout bytes.Buffer
	rc := runOutcomeBackstop([]string{
		"--repo", dir,
		"--issue", "42",
		"--branch", "agent/issue-42",
		"--base", "main",
		"--host-mediated-remote", "1",
		"--run-state-file", filepath.Join(t.TempDir(), "no-run-state.json"),
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runOutcomeBackstop exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	out := stdout.String()
	if got := len(bytes.Split(bytes.TrimRight([]byte(out), "\n"), []byte("\n"))); got != 1 {
		t.Fatalf("expected exactly one output line, got %d: %q", got, out)
	}
	if !bytes.Contains([]byte(out), []byte("SPINDRIFT_OUTCOME")) {
		t.Fatalf("expected SPINDRIFT_OUTCOME line, got %q", out)
	}
	if !bytes.Contains([]byte(out), []byte("status=ready")) {
		t.Fatalf("expected status=ready, got %q", out)
	}
	if !bytes.Contains([]byte(out), []byte("landing=agent/issue-42")) {
		t.Fatalf("expected landing=agent/issue-42, got %q", out)
	}
	if bytes.Contains([]byte(out), []byte("nonce=")) {
		t.Fatalf("expected no nonce field, got %q", out)
	}
	if !bytes.Contains([]byte(out), []byte("no writable remote under CODE_FORGE=local")) {
		t.Fatalf("expected the local no-writable-remote note, got %q", out)
	}
}

// A run-state artifact recording last_verdict=BLOCK keeps the emitted status
// at "blocked" even though the git-observed evidence (a real commit on the
// host-mediated relay path) would otherwise resolve to "ready" (issue #2459).
func TestRunOutcomeBackstop_RunStateFileFlagBlocksVerdict(t *testing.T) {
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "-b", "main")
	runGitCmd(t, dir, "config", "user.name", "Test Bot")
	runGitCmd(t, dir, "config", "user.email", "bot@example.com")
	writeTestFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runGitCmd(t, dir, "add", "base.txt")
	runGitCmd(t, dir, "commit", "-m", "base")
	runGitCmd(t, dir, "checkout", "-b", "agent/issue-42")
	writeTestFile(t, filepath.Join(dir, "feature.txt"), "feature\n")
	runGitCmd(t, dir, "add", "feature.txt")
	runGitCmd(t, dir, "commit", "-m", "feature")

	runStatePath := filepath.Join(dir, "run-state.json")
	writeTestFile(t, runStatePath, `{"last_verdict":"BLOCK"}`)

	var stdout bytes.Buffer
	rc := runOutcomeBackstop([]string{
		"--repo", dir,
		"--issue", "42",
		"--branch", "agent/issue-42",
		"--base", "main",
		"--host-mediated-remote", "1",
		"--run-state-file", runStatePath,
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runOutcomeBackstop exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	out := stdout.String()
	if !bytes.Contains([]byte(out), []byte("status=blocked")) {
		t.Fatalf("expected status=blocked, got %q", out)
	}
	if bytes.Contains([]byte(out), []byte("status=ready")) {
		t.Fatalf("expected status not to be ready, got %q", out)
	}
}

// Omitting -run-state-file defaults to /tmp/run-state.json, matching the
// orchestrator's own --state-file default (issue #1997), so the backstop is
// never left without a verdict-known path (issue #2459). The test reads
// DefValue and never touches that path: the Box bakes sandbox = false, so a
// live orchestrator artifact can sit there and an earlier version clobbered it.
func TestRunOutcomeBackstop_DefaultRunStateFilePathIsTmpRunState(t *testing.T) {
	fs, _ := newOutcomeBackstopFlagSet()

	got := fs.Lookup("run-state-file")
	if got == nil {
		t.Fatal("run-state-file flag not registered")
	}
	if want := "/tmp/run-state.json"; got.DefValue != want {
		t.Fatalf("run-state-file default = %q, want %q", got.DefValue, want)
	}
}

// A missing -base must fail loudly instead of running outcomebackstop.Run
// against a zero-value Config.
func TestRunOutcomeBackstop_MissingRequiredFlagReturnsNonZero(t *testing.T) {
	var stdout bytes.Buffer
	rc := runOutcomeBackstop([]string{
		"--repo", t.TempDir(),
		"--branch", "agent/issue-42",
	}, &stdout)
	if rc == 0 {
		t.Fatal("runOutcomeBackstop exit = 0, want non-zero for a missing -base")
	}
}

// Only a bare "outcome-backstop" first arg selects the subcommand; every other
// shape, "bundle-out" included, falls through to the default Driver path.
func TestIsOutcomeBackstopInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"outcome-backstop first arg", []string{"outcome-backstop", "--repo", "x"}, true},
		{"no args", nil, false},
		{"ordinary flag invocation", []string{"--driver", "claude"}, false},
		{"bundle-out", []string{"bundle-out"}, false},
	}
	for _, c := range cases {
		if got := isOutcomeBackstopInvocation(c.args); got != c.want {
			t.Errorf("%s: isOutcomeBackstopInvocation(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}
