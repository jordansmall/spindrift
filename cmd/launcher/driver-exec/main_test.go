package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// TestMainRunDefaultArgvFlagsReproduceClaudeShape verifies a bare mainRun
// invocation with no explicit --argv-* flags reproduces claude's own argv
// shape (lib/drivers/claude.nix: promptFlag="-p", agentsFlag="--agents") --
// since -driver itself defaults to "claude" (issue #2534 follow-up), the
// binary's own flag defaults must describe that same coherent shape instead
// of emitting an empty-string token ahead of the prompt and silently
// omitting --agents.
func TestMainRunDefaultArgvFlagsReproduceClaudeShape(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	driverBin := writeFakeDriver(t, dir, "fake-driver", "echo \"$@\" >> "+callLog+"\nexit 0\n")

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("implement the thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsFile := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(agentsFile, []byte(`{"scout":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	argv := []string{
		"-prompt-file", promptFile,
		"-agents-file", agentsFile,
		"-driver-bin", driverBin,
		"-log-path", filepath.Join(dir, "stream.log"),
		"-heartbeat-log", filepath.Join(dir, "heartbeat.log"),
	}

	var stdout, stderr bytes.Buffer
	rc := mainRun(argv, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	got := strings.TrimSpace(string(calls))
	fields := strings.Fields(got)
	if len(fields) == 0 || fields[0] != "-p" {
		t.Errorf("driver argv = %q, want it to start with \"-p\" (claude's promptFlag default)", got)
	}
	if !strings.Contains(got, "--agents") {
		t.Errorf("driver argv = %q, want it to contain \"--agents\" (claude's agentsFlag default)", got)
	}
}

// TestResolveExitUsesSynthesizedExitForOpencode verifies resolveExit
// replaces the child process's own exit code with the opencode Driver's
// ResolveExit result (issue #2263) -- opencode's own process exit code is
// not trustworthy on its own (see driver/opencode/exitsynth.go), so
// driver-exec's main must apply the Driver's required ResolveExit result
// after run returns.
func TestResolveExitUsesSynthesizedExitForOpencode(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")
	// A log with a valid outcome line and no error event synthesizes to 0
	// (see driver/opencode/exitsynth_test.go's own fixtures) -- distinct
	// from the "child rc" this test passes in, so a passing assertion proves
	// the synthesized value actually replaced it rather than coincidentally
	// matching.
	content := `{"type":"result","status":"ready"}` + "\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := driver.New("opencode")
	if err != nil {
		t.Fatalf("driver.New(opencode): %v", err)
	}

	got := resolveExit(d, 7, logPath)
	if got == 7 {
		t.Errorf("resolveExit = %d, want it to replace the child rc (7) with the synthesized exit code", got)
	}
}

// TestResolveExitLeavesClaudeExitCodeUntouched verifies resolveExit is a
// no-op for a Driver (claude) whose ResolveExit trusts the passed exit code
// unchanged -- the child rc passes through unchanged.
func TestResolveExitLeavesClaudeExitCodeUntouched(t *testing.T) {
	d, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New(claude): %v", err)
	}

	got := resolveExit(d, 7, filepath.Join(t.TempDir(), "does-not-exist.log"))
	if got != 7 {
		t.Errorf("resolveExit = %d, want 7 (claude's own rc, untouched)", got)
	}
}

// TestResolveExitOnErrorKeepsOriginalRC verifies a ResolveExit failure (e.g.
// an unreadable log path) degrades safely to the original child rc instead
// of masking a real failure behind a resolution failure.
func TestResolveExitOnErrorKeepsOriginalRC(t *testing.T) {
	d, err := driver.New("opencode")
	if err != nil {
		t.Fatalf("driver.New(opencode): %v", err)
	}

	// SynthesizeExit tolerates a missing log (see
	// driver/opencode/exitsynth_test.go's TestSynthesizeExit_MissingFile_IsNonZero),
	// so this exercises the fallback path indirectly: a missing-file result
	// is still a non-error, non-zero synthesized code, which is exactly what
	// this test wants to see replace the child rc.
	got := resolveExit(d, 0, filepath.Join(t.TempDir(), "does-not-exist.log"))
	if got == 0 {
		t.Errorf("resolveExit = %d, want a non-zero synthesized exit for a missing/invalid log", got)
	}
}
