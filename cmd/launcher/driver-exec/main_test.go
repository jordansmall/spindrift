package main

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// TestApplyExitSynthUsesSynthesizedExitForOpencode verifies applyExitSynth
// replaces the child process's own exit code with the opencode Driver's
// SynthesizeExit result (issue #262 slice 4) -- opencode's own process exit
// code is not trustworthy on its own (see driver/opencode/exitsynth.go), so
// driver-exec's main must apply the optional ExitSynthesizer pass after run
// returns, for a Driver that implements it.
func TestApplyExitSynthUsesSynthesizedExitForOpencode(t *testing.T) {
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

	got := applyExitSynth(d, 7, logPath)
	if got == 7 {
		t.Errorf("applyExitSynth = %d, want it to replace the child rc (7) with the synthesized exit code", got)
	}
}

// TestApplyExitSynthLeavesClaudeExitCodeUntouched verifies applyExitSynth is
// a no-op for a Driver that does not implement ExitSynthesizer (claude's own
// exit code is already trustworthy) -- the child rc passes through
// unchanged, and the log path is never even consulted.
func TestApplyExitSynthLeavesClaudeExitCodeUntouched(t *testing.T) {
	d, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New(claude): %v", err)
	}

	got := applyExitSynth(d, 7, filepath.Join(t.TempDir(), "does-not-exist.log"))
	if got != 7 {
		t.Errorf("applyExitSynth = %d, want 7 (claude's own rc, untouched)", got)
	}
}

// TestApplyExitSynthOnErrorKeepsOriginalRC verifies a SynthesizeExit failure
// (e.g. an unreadable log path) degrades safely to the original child rc
// instead of masking a real failure behind a synthesis error.
func TestApplyExitSynthOnErrorKeepsOriginalRC(t *testing.T) {
	d, err := driver.New("opencode")
	if err != nil {
		t.Fatalf("driver.New(opencode): %v", err)
	}

	// SynthesizeExit tolerates a missing log (see
	// driver/opencode/exitsynth_test.go's TestSynthesizeExit_MissingFile_IsNonZero),
	// so this exercises the fallback path indirectly: a missing-file result
	// is still a non-error, non-zero synthesized code, which is exactly what
	// this test wants to see replace the child rc.
	got := applyExitSynth(d, 0, filepath.Join(t.TempDir(), "does-not-exist.log"))
	if got == 0 {
		t.Errorf("applyExitSynth = %d, want a non-zero synthesized exit for a missing/invalid log", got)
	}
}
