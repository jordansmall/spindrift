package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/runstate"
)

// singlePassFakeDriverArgv builds the argv mainRun needs for one end-to-end
// pass. The driver and cap facts that used to be CLI flags now live inside the
// handoff file this helper writes (issue #2975).
func singlePassFakeDriverArgv(t *testing.T, dir string, caps promptassembly.Caps) []string {
	handoffFile := writeHandoffFile(t, dir, promptassembly.Handoff{
		Driver:    "claude",
		DriverBin: "claude",
		Caps:      caps,
	})
	return []string{
		"-handoff-file", handoffFile,
		"-prompt-file", filepath.Join(dir, "prompt.txt"),
		"-log-path", filepath.Join(dir, "stream.log"),
		"-state-file", filepath.Join(dir, "run-state.json"),
	}
}

// reviewPassFakeDriverArgv is singlePassFakeDriverArgv plus a
// Handoff.ReviewPromptFile. That field, not a -review-prompt-file CLI flag, is
// what now enables the review pass (issue #2975).
func reviewPassFakeDriverArgv(t *testing.T, dir string, caps promptassembly.Caps) []string {
	handoffFile := writeHandoffFile(t, dir, promptassembly.Handoff{
		Driver:           "claude",
		DriverBin:        "claude",
		ReviewPromptFile: filepath.Join(dir, "review-prompt.txt"),
		Caps:             caps,
	})
	return []string{
		"-handoff-file", handoffFile,
		"-prompt-file", filepath.Join(dir, "prompt.txt"),
		"-log-path", filepath.Join(dir, "stream.log"),
		"-state-file", filepath.Join(dir, "run-state.json"),
	}
}

// TestMainRunCoherentCapsNoWarning pins that the shipped default cap pair is
// coherent with the review pass off, so no "cannot reach" warning reaches
// stderr.
func TestMainRunCoherentCapsNoWarning(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n'
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	rc := mainRun(singlePassFakeDriverArgv(t, dir, promptassembly.Caps{MaxReviewRounds: defaultMaxReviewRounds, MaxSlices: defaultMaxSlices}), &stdout, &stderr)

	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
	if strings.Contains(stderr.String(), "cannot reach") {
		t.Errorf("stderr = %q, want no \"cannot reach\" incoherence warning for the coherent default cap pair", stderr.String())
	}
}

// TestMainRunReviewPassEnabledUsesReviewPassFormula pins the bug 698b5f3b
// fixed: mainRun must pass reviewPassEnabled = (handoff.ReviewPromptFile != "")
// to validateCaps, not its inverse. The (3, 5) cap pair warns only under the
// review-pass 2N+3 formula, not the legacy N+2 one, and no other test here has
// caps that trip it, so this one alone catches the wiring flipped to `== ""`.
func TestMainRunReviewPassEnabledUsesReviewPassFormula(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	// The implement/fix decision switch in run.go has no "no verdict" fallback
	// like the legacy loop's, so it stops only on hasOutcome. The outcome must
	// land in $DRIVER_LOG_PATH as a real stream-json line, not just on stdout,
	// or pass 1 falls through to a review pass and then a land pass that needs a
	// real prompt.txt on disk.
	writeFakeDriverExec(t, dir, callLog, `printf '%s' '`+streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc")+`' | tee -a "$DRIVER_LOG_PATH"
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := os.WriteFile(filepath.Join(dir, "review-prompt.txt"), []byte("review prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	rc := mainRun(reviewPassFakeDriverArgv(t, dir, promptassembly.Caps{MaxReviewRounds: 3, MaxSlices: 5}), &stdout, &stderr)

	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), "need -max-slices >= 9") {
		t.Errorf("stderr = %q, want the review-pass formula's minimum (9 = 2*3+3) -- a max-review-rounds=3/max-slices=5 pair is coherent under the legacy N+2 formula and must only warn once the handoff's ReviewPromptFile selects the review-pass formula", stderr.String())
	}
}

// TestMainRunIncoherentCapsWarnsButProceeds pins the issue #2460 fix: an
// unsatisfiable (max-review-rounds, max-slices) pair warns on stderr with
// "cannot reach" but does not abort the run.
func TestMainRunIncoherentCapsWarnsButProceeds(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n'
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	// Legacy loop (no ReviewPromptFile): reaching max-review-rounds=3 needs
	// max-slices >= 5 (maxReviewRounds+2); 2 is unreachable.
	rc := mainRun(singlePassFakeDriverArgv(t, dir, promptassembly.Caps{MaxReviewRounds: 3, MaxSlices: 2}), &stdout, &stderr)

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

// TestMainRunToleratesNegativeBudgetCaps proves mainRun clamps a negative
// budget cap to 0 and runs the Box to completion rather than aborting (issues
// #2694, #2975). Caps arrive already typed from the handoff, so a malformed
// value fails in LoadHandoffFile first, but a negative one is valid JSON and
// must clamp to disabled with one stderr line naming the value it clamped.
func TestMainRunToleratesNegativeBudgetCaps(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n'
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	rc := mainRun(singlePassFakeDriverArgv(t, dir, promptassembly.Caps{MaxBudgetTokens: -1, MaxBudgetUSD: -1}), &stdout, &stderr)

	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q) -- a negative budget cap value must degrade to disabled, not abort the run", rc, stderr.String())
	}
	if _, err := os.ReadFile(callLog); err != nil {
		t.Fatalf("driver-exec was never invoked (%v) -- a negative budget cap value must not abort the run before any pass runs", err)
	}
	if !strings.Contains(stderr.String(), "max-budget-tokens=-1 is negative") {
		t.Errorf("stderr = %q, want it to name the degraded max-budget-tokens value", stderr.String())
	}
	if !strings.Contains(stderr.String(), "max-budget-usd=-1 is negative") {
		t.Errorf("stderr = %q, want it to name the degraded max-budget-usd value", stderr.String())
	}
}

// TestMainRunDrivesFullReviewSequenceFromHandoffFixture drives the same 5-pass
// sequence as TestRunWithReviewPassSequenceOnBlockThenApprove (run_test.go) but
// through mainRun's own flag parsing and handoff loading, so it also covers the
// handoff-to-config half of the chain (issue #2975). Assertions stay lighter
// than that reference test's per-pass flag checks on purpose.
func TestMainRunDrivesFullReviewSequenceFromHandoffFixture(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, reviewPassFakeDriverBody(callLog))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("REVIEW PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(sessionFile, []byte("--session-id fake-id"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(dir, "run-state.json")

	handoffFile := writeHandoffFile(t, dir, promptassembly.Handoff{
		Driver:           "claude",
		DriverBin:        "claude",
		ReviewPromptFile: reviewPromptFile,
		Caps:             promptassembly.Caps{MaxReviewRounds: 3, MaxSlices: 10},
	})

	var stdout, stderr bytes.Buffer
	rc := mainRun([]string{
		"-handoff-file", handoffFile,
		"-prompt-file", promptFile,
		"-session-file", sessionFile,
		"-log-path", filepath.Join(dir, "stream.log"),
		"-state-file", stateFile,
	}, &stdout, &stderr)

	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("driver-exec invocation count = %d, want 5 (log: %q)", len(lines), calls)
	}

	for _, want := range []string{
		`"spindrift_op":{"op":"pass_start","pass":1,"role":"implement"}`,
		`"spindrift_op":{"op":"pass_start","pass":2,"role":"review"}`,
		`"spindrift_op":{"op":"pass_start","pass":3,"role":"fix"}`,
		`"spindrift_op":{"op":"pass_start","pass":4,"role":"review"}`,
		`"spindrift_op":{"op":"pass_start","pass":5,"role":"land"}`,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
		}
	}

	if !strings.Contains(stdout.String(), "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") {
		t.Errorf("stdout = %q, want the final pass's own outcome line present unchanged", stdout.String())
	}

	got, err := runstate.ReadRunState(stateFile)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.LastVerdict != "APPROVE" {
		t.Errorf("LastVerdict = %q, want %q -- proving the review verdict reached run-state through the handoff-driven path too", got.LastVerdict, "APPROVE")
	}
}

// TestMainRunThreadsMaxBudgetTokensFromHandoffIntoTheReviewLoop is
// TestRunWithReviewPassTerminatesOnMaxBudgetTokensCap (run_test.go) driven
// through mainRun's own handoff loading instead of a hand-built config literal,
// so it is the one test covering the handoff-to-config-to-Caps chain rather
// than only the config-to-Caps half.
func TestMainRunThreadsMaxBudgetTokensFromHandoffIntoTheReviewLoop(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	body := `: > "$DRIVER_LOG_PATH"
n=$(wc -l < "` + callLog + `")
if [ $((n % 2)) -eq 0 ]; then
  printf '%s' '` + streamJSONOutcomeLine("VERDICT: BLOCK") + `' >> "$DRIVER_LOG_PATH"
fi
printf '%s' '` + streamJSONResultLine(70, 30, 0.01) + `' >> "$DRIVER_LOG_PATH"
exit 0
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review-prompt.txt"), []byte("review prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	argv := reviewPassFakeDriverArgv(t, dir, promptassembly.Caps{MaxReviewRounds: 0, MaxSlices: 0, MaxBudgetTokens: 350, MaxBudgetUSD: 0})

	var stdout, stderr bytes.Buffer
	rc := mainRun(argv, &stdout, &stderr)

	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}

	if !strings.Contains(stdout.String(), `"decision":"continue","reason":"budget exceeded; running terminal land pass"`) {
		t.Errorf("stdout = %q, want the budget-cap-fired continue reason naming the cap, proving Handoff.Caps.MaxBudgetTokens threaded through config into Caps.MaxBudgetTokens", stdout.String())
	}
}
