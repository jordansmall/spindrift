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

// reviewPassFakeDriverArgv is singlePassFakeDriverArgv plus -review-prompt-file,
// so mainRun dispatches to runWithReviewPass (run.go:131) and validateCaps
// sees reviewPassEnabled=true instead of false.
func reviewPassFakeDriverArgv(dir string, capFlags ...string) []string {
	argv := []string{
		"-prompt-file", filepath.Join(dir, "prompt.txt"),
		"-review-prompt-file", filepath.Join(dir, "review-prompt.txt"),
		"-driver-bin", "claude",
		"-log-path", filepath.Join(dir, "stream.log"),
		"-heartbeat-log", filepath.Join(dir, "heartbeat.log"),
		"-state-file", filepath.Join(dir, "run-state.json"),
	}
	return append(argv, capFlags...)
}

// TestMainRunReviewPassEnabledUsesReviewPassFormula pins the exact bug
// 698b5f3b fixed: mainRun must pass reviewPassEnabled = (*reviewPromptFile
// != "") to validateCaps, not its inverse. A (3, 5) -max-review-rounds/
// -max-slices pair is coherent under the legacy N+2 formula (5 == 3+2) but
// incoherent under the review-pass 2N+3 formula (needs -max-slices >= 9) --
// so setting -review-prompt-file must flip the warning on for this exact
// pair. Neither TestMainRunCoherentCapsNoWarning nor
// TestMainRunIncoherentCapsWarnsButProceeds sets -review-prompt-file, so
// both only ever exercise reviewPassEnabled=false; this test is the only
// one that would catch the wiring flipped to `== ""`.
func TestMainRunReviewPassEnabledUsesReviewPassFormula(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	// The implement/fix pass's own decision switch (run.go:286-317) has no
	// "no verdict" fallback like the legacy loop's does -- it stops only on
	// hasOutcome, so the outcome must land in $DRIVER_LOG_PATH as a real
	// stream-json line (matching run_test.go's streamJSONOutcomeLine), not
	// just printed to stdout, or pass 1 falls through to a review pass and
	// beyond it, into a land pass that needs a real prompt.txt on disk.
	writeFakeDriverExec(t, dir, callLog, `printf '%s' '`+streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc")+`' | tee -a "$DRIVER_LOG_PATH"
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := os.WriteFile(filepath.Join(dir, "review-prompt.txt"), []byte("review prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	rc := mainRun(reviewPassFakeDriverArgv(dir, "-max-review-rounds=3", "-max-slices=5"), &stdout, &stderr)

	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), "need -max-slices >= 9") {
		t.Errorf("stderr = %q, want the review-pass formula's minimum (9 = 2*3+3) -- a -max-review-rounds=3/-max-slices=5 pair is coherent under the legacy N+2 formula and must only warn once -review-prompt-file selects the review-pass formula", stderr.String())
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
