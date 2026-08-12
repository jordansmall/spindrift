package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// singlePassFakeDriverArgv builds the argv mainRun needs to drive one
// end-to-end pass: the three required flags (-prompt-file, -driver-bin,
// -log-path) pointed at temp files/paths, plus whatever cap flags the
// caller appends (e.g. an incoherent -max-review-rounds/-max-slices pair).
func singlePassFakeDriverArgv(dir string, capFlags ...string) []string {
	argv := []string{
		"-prompt-file", filepath.Join(dir, "prompt.txt"),
		"-driver-bin", "claude",
		"-log-path", filepath.Join(dir, "stream.log"),
		"-heartbeat-log", filepath.Join(dir, "heartbeat.log"),
		"-state-file", filepath.Join(dir, "run-state.json"),
	}
	return append(argv, capFlags...)
}

// TestMainRunCoherentCapsNoWarning verifies mainRun's default cap pair
// (defaultMaxReviewRounds/defaultMaxSlices, review pass disabled since
// -review-prompt-file is unset) is coherent: no "cannot reach" warning
// reaches stderr, and a single clean driver-exec pass returns exit code 0.
func TestMainRunCoherentCapsNoWarning(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n'
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	rc := mainRun(singlePassFakeDriverArgv(dir), &stdout, &stderr)

	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
	if strings.Contains(stderr.String(), "cannot reach") {
		t.Errorf("stderr = %q, want no \"cannot reach\" incoherence warning for the coherent default cap pair", stderr.String())
	}
}

// TestMainRunIncoherentCapsWarnsButProceeds verifies the issue #2460 fix:
// an unsatisfiable (-max-review-rounds, -max-slices) pair is surfaced as a
// stderr warning ("cannot reach"), but does NOT abort the run -- mainRun
// still drives the single pass to completion and returns the same exit code
// a coherent pair would for an equivalent single pass.
func TestMainRunIncoherentCapsWarnsButProceeds(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n'
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	// Legacy loop (no -review-prompt-file): reaching max-review-rounds=3
	// needs max-slices >= 5 (maxReviewRounds+2); 2 is unreachable.
	rc := mainRun(singlePassFakeDriverArgv(dir, "-max-review-rounds=3", "-max-slices=2"), &stdout, &stderr)

	if !strings.Contains(stderr.String(), "cannot reach") {
		t.Errorf("stderr = %q, want it to contain %q (the incoherent-cap warning)", stderr.String(), "cannot reach")
	}
	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 -- the warning must not abort the run (stderr: %q)", rc, stderr.String())
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	if len(bytes.TrimSpace(calls)) == 0 {
		t.Fatalf("driver-exec was never invoked -- the incoherent-cap warning aborted the run instead of proceeding")
	}
}
