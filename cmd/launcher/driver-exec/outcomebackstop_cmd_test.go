package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRunOutcomeBackstop_ParsesFlagsAndEmits verifies the outcome-backstop
// subcommand's flag parsing reaches outcomebackstop.Run with the right
// Config: a CODE_FORGE=local repo with a base and a branch commit emits a
// single SPINDRIFT_OUTCOME line carrying landing=<branch>, without ever
// attempting a push (issue #2157). The commit on the branch and the
// host-mediated relay are real, git-verified evidence, so Run now resolves
// status=ready rather than the always-blocked default (issue #2380).
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

// TestRunOutcomeBackstop_RunStateFileFlagBlocksVerdict verifies the
// -run-state-file flag reaches outcomebackstop.Config: pointing it at a
// run-state artifact recording last_verdict=BLOCK keeps the emitted status
// at "blocked" even though the git-observed evidence (a real commit pushed
// via the host-mediated relay path) would otherwise resolve to "ready"
// (issue #2459).
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

// TestRunOutcomeBackstop_DefaultRunStateFilePathIsTmpRunState verifies that
// omitting -run-state-file falls back to /tmp/run-state.json, matching the
// orchestrator's own --state-file default (issue #1997), rather than
// leaving the backstop with no verdict-known path at all (issue #2459).
func TestRunOutcomeBackstop_DefaultRunStateFilePathIsTmpRunState(t *testing.T) {
	const defaultPath = "/tmp/run-state.json"
	// This path is shared with a real orchestrator run on the same host
	// (issue #1997's default --state-file); save and restore whatever is
	// there rather than blindly overwriting/deleting it, so this test can
	// never clobber a live run's own handoff artifact.
	if prior, err := os.ReadFile(defaultPath); err == nil {
		t.Cleanup(func() { os.WriteFile(defaultPath, prior, 0o644) })
	} else {
		t.Cleanup(func() { os.Remove(defaultPath) })
	}
	writeTestFile(t, defaultPath, `{"last_verdict":"BLOCK"}`)

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
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runOutcomeBackstop exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	out := stdout.String()
	if !bytes.Contains([]byte(out), []byte("status=blocked")) {
		t.Fatalf("expected the default run-state path to be read and block the verdict, got %q", out)
	}
}

// TestRunOutcomeBackstop_MissingRequiredFlagReturnsNonZero verifies a
// missing -base fails loudly (exit 1) instead of running
// outcomebackstop.Run against a zero-value Config.
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

// TestIsOutcomeBackstopInvocation verifies the outcome-backstop subcommand's
// dispatch guard: a bare "outcome-backstop" first arg selects it, while
// every other invocation shape falls through to the default Driver-invocation
// path (or, for "bundle-out", to that other subcommand).
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
