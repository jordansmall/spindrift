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

// TestMainRunToleratesMalformedOrNegativeBudgetCaps proves mainRun's own
// wiring -- flag parse (as a plain string, not fs.Int/fs.Float64), then
// parseNonnegBudgetTokens/parseNonnegBudgetUSD -- actually runs a Box to
// completion on a negative or malformed -max-budget-tokens/-max-budget-usd,
// rather than the fs.Int/fs.Float64-typed flag.Parse failure (exit code 2,
// before any pass runs at all) that shape would have produced (issue #2694
// review finding): entrypoint.sh forwards MAX_BUDGET_TOKENS/MAX_BUDGET_USD
// unconditionally now, so a stale or mistyped operator value -- one the
// host launcher's own atoiNonneg/floatNonneg have always tolerated
// silently -- must not newly kill the Box.
func TestMainRunToleratesMalformedOrNegativeBudgetCaps(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n'
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	rc := mainRun(singlePassFakeDriverArgv(dir, "-max-budget-tokens=-1", "-max-budget-usd=not-a-number"), &stdout, &stderr)

	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q) -- a bad budget cap value must degrade to disabled, not abort the run", rc, stderr.String())
	}
	if _, err := os.ReadFile(callLog); err != nil {
		t.Fatalf("driver-exec was never invoked (%v) -- a bad budget cap value must not abort the run before any pass runs", err)
	}
	if !strings.Contains(stderr.String(), `-max-budget-tokens="-1"`) {
		t.Errorf("stderr = %q, want it to name the degraded -max-budget-tokens value", stderr.String())
	}
	if !strings.Contains(stderr.String(), `-max-budget-usd="not-a-number"`) {
		t.Errorf("stderr = %q, want it to name the degraded -max-budget-usd value", stderr.String())
	}
}

// TestMainRunAcceptsMaxBudgetFlagsAndThreadsThemIntoTheReviewLoop verifies
// two things the flag declaration alone (main.go:45-46) doesn't prove
// (issue #2694 review finding): that mainRun's FlagSet actually declares
// -max-budget-tokens/-max-budget-usd (entrypoint.sh now forwards both on
// every orchestrator run, unconditionally -- a FlagSet missing either one
// fails fs.Parse and kills the Box, the same failure mode
// TestMainRunAcceptsArgvShapeFlags guards for the 7 argv-shape flags), and
// that a low -max-budget-tokens value threads all the way through config
// into the review loop's own Caps.MaxBudgetTokens and actually caps the run
// -- the same fake-driver body and assertions as
// TestRunWithReviewPassTerminatesOnMaxBudgetTokensCap (run_test.go), but
// driven through mainRun's own flag parsing instead of a hand-built config
// literal, so this is the one test proving the full flag -> config -> Caps
// -> behavior chain, not just the config -> Caps -> behavior half of it.
func TestMainRunAcceptsMaxBudgetFlagsAndThreadsThemIntoTheReviewLoop(t *testing.T) {
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
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("review prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	argv := append(singlePassFakeDriverArgv(dir),
		"-review-prompt-file", reviewPromptFile,
		"-max-review-rounds=0",
		"-max-slices=0",
		"-max-budget-tokens=350",
		"-max-budget-usd=0",
	)

	var stdout, stderr bytes.Buffer
	rc := mainRun(argv, &stdout, &stderr)

	if rc == 2 {
		t.Fatalf("mainRun exit code = 2 (flag parse failure), stderr: %q -- FlagSet must declare -max-budget-tokens/-max-budget-usd", stderr.String())
	}
	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}

	if !strings.Contains(stdout.String(), `"decision":"continue","reason":"budget exceeded; running terminal land pass"`) {
		t.Errorf("stdout = %q, want the budget-cap-fired continue reason naming the cap, proving -max-budget-tokens threaded through config into Caps.MaxBudgetTokens", stdout.String())
	}
}

// TestMainRunAcceptsArgvShapeFlags verifies mainRun's FlagSet declares all 7
// argv-shape flags entrypoint.sh's orchestrator invocation always passes
// (agent/entrypoint.sh's $_driver_invoker call, issue #2534 follow-up): a
// FlagSet missing any of these fails fs.Parse with "flag provided but not
// defined" and mainRun returns exit code 2 before any driver-exec pass runs.
// This pins that an orchestrator-driven run works at all, not just that the
// flags happen to be accepted.
func TestMainRunAcceptsArgvShapeFlags(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n'
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	argv := append(singlePassFakeDriverArgv(dir),
		"-argv-prompt-style", "flag",
		"-argv-prompt-flag", "-p",
		"-argv-model-flag", "--model",
		"-argv-model-omit-empty",
		"-argv-agents-flag", "--agents",
		"-argv-effort-flag", "--effort",
		"-argv-order", "prompt model",
	)

	var stdout, stderr bytes.Buffer
	rc := mainRun(argv, &stdout, &stderr)

	if rc == 2 {
		t.Fatalf("mainRun exit code = 2 (flag parse failure), stderr: %q -- FlagSet must declare all 7 argv-shape flags", stderr.String())
	}
	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
}

// TestMainRunDefaultArgvFlagsReproduceClaudeShape verifies a bare mainRun
// invocation with no explicit --argv-prompt-flag/--argv-agents-flag flags
// forwards claude's own argv shape (lib/drivers/claude.nix:
// promptFlag="-p", agentsFlag="--agents") into its driver-exec invocation --
// since -driver itself defaults to "claude" (issue #2534 follow-up), the
// orchestrator's own flag defaults must describe that same coherent shape
// instead of forwarding an empty-string --argv-prompt-flag/--argv-agents-flag
// value.
func TestMainRunDefaultArgvFlagsReproduceClaudeShape(t *testing.T) {
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

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	got := strings.TrimSpace(string(calls))
	if !strings.Contains(got, "--argv-prompt-flag -p") {
		t.Errorf("driver-exec argv = %q, want it to contain \"--argv-prompt-flag -p\" (claude's promptFlag default)", got)
	}
	if !strings.Contains(got, "--argv-agents-flag --agents") {
		t.Errorf("driver-exec argv = %q, want it to contain \"--argv-agents-flag --agents\" (claude's agentsFlag default)", got)
	}
}
