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

// singlePassFakeDriverArgv builds the argv mainRun needs to drive one
// end-to-end pass: the required -handoff-file (pointed at a handoff.json this
// helper writes from caps, since the driver/cap facts that used to be CLI
// flags now live inside the handoff -- issue #2975) plus the per-pass
// -prompt-file / -log-path / -state-file flags pointed at temp files/paths.
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
// Handoff.ReviewPromptFile, so mainRun dispatches to runWithReviewPass
// (run.go's run()) and validateCaps sees reviewPassEnabled=true instead of
// false. The review pass's master switch is the handoff field now, not a
// -review-prompt-file CLI flag (issue #2975).
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

// TestMainRunCoherentCapsNoWarning verifies the shipped default cap pair
// (defaultMaxReviewRounds/defaultMaxSlices, review pass disabled since the
// handoff carries no ReviewPromptFile) is coherent: no "cannot reach" warning
// reaches stderr, and a single clean driver-exec pass returns exit code 0.
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

// TestMainRunReviewPassEnabledUsesReviewPassFormula pins the exact bug
// 698b5f3b fixed: mainRun must pass reviewPassEnabled = (handoff.ReviewPromptFile
// != "") to validateCaps, not its inverse. A (3, 5) max-review-rounds/
// max-slices pair is coherent under the legacy N+2 formula (5 == 3+2) but
// incoherent under the review-pass 2N+3 formula (needs max-slices >= 9) --
// so a handoff carrying a ReviewPromptFile must flip the warning on for this
// exact pair. Neither TestMainRunCoherentCapsNoWarning nor
// TestMainRunIncoherentCapsWarnsButProceeds sets ReviewPromptFile, so both
// only ever exercise reviewPassEnabled=false; this test is the only one that
// would catch the wiring flipped to `== ""`.
func TestMainRunReviewPassEnabledUsesReviewPassFormula(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	// The implement/fix pass's own decision switch (run.go) has no "no
	// verdict" fallback like the legacy loop's does -- it stops only on
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
	rc := mainRun(reviewPassFakeDriverArgv(t, dir, promptassembly.Caps{MaxReviewRounds: 3, MaxSlices: 5}), &stdout, &stderr)

	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), "need -max-slices >= 9") {
		t.Errorf("stderr = %q, want the review-pass formula's minimum (9 = 2*3+3) -- a max-review-rounds=3/max-slices=5 pair is coherent under the legacy N+2 formula and must only warn once the handoff's ReviewPromptFile selects the review-pass formula", stderr.String())
	}
}

// TestMainRunIncoherentCapsWarnsButProceeds verifies the issue #2460 fix:
// an unsatisfiable (max-review-rounds, max-slices) pair is surfaced as a
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
// budget cap to 0 (disabled) and runs the Box to completion rather than
// aborting (issue #2694 / #2975). The caps arrive already typed from the
// handoff (int/float64), so a malformed value can no longer reach mainRun at
// all -- LoadHandoffFile's JSON unmarshal would have failed first -- but a
// negative value is still valid JSON and must degrade the same way the host
// launcher's own atoiNonneg/floatNonneg have always tolerated a negative
// MAX_BUDGET_TOKENS/MAX_BUDGET_USD, with one stderr line naming the degrade.
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

// TestMainRunDrivesFullReviewSequenceFromHandoffFixture is
// TestRunWithReviewPassSequenceOnBlockThenApprove (run_test.go) one layer up:
// it drives the same 5-pass implement -> review(BLOCK) -> fix ->
// review(APPROVE) -> land sequence through mainRun's own flag parsing and
// handoff loading, rather than a hand-built config{} literal, proving the
// full handoff -> config -> multi-round-loop chain (issue #2975's
// "Orchestrator loop tests drive the real loop from handoff fixtures" DoD
// line) -- not just the config -> multi-round-loop half of it the reference
// test already covers. Assertions here are deliberately lighter than the
// reference test's exhaustive per-pass flag checks, which this test does not
// duplicate.
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

// TestMainRunThreadsMaxBudgetTokensFromHandoffIntoTheReviewLoop verifies that
// a low Handoff.Caps.MaxBudgetTokens threads all the way through config into
// the review loop's own Caps.MaxBudgetTokens and actually caps the run -- the
// same fake-driver body and assertions as
// TestRunWithReviewPassTerminatesOnMaxBudgetTokensCap (run_test.go), but
// driven through mainRun's own handoff loading instead of a hand-built config
// literal, so this is the one test proving the full handoff -> config -> Caps
// -> behavior chain, not just the config -> Caps -> behavior half of it.
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
