package main

import (
	"bytes"
	"fmt"
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

// TestMainRunRejectsNonPositiveMaxParallelWorkers verifies the issue #2495
// fix: unlike the cap-coherence warning above (which never aborts the run),
// a non-positive -max-parallel-workers is fatal -- mainRun returns exit code
// 1, writes an actionable message to stderr, and never invokes driver-exec
// at all.
func TestMainRunRejectsNonPositiveMaxParallelWorkers(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n'
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	rc := mainRun(singlePassFakeDriverArgv(dir, "-max-parallel-workers=0"), &stdout, &stderr)

	if rc != 1 {
		t.Fatalf("mainRun exit code = %d, want 1 (stderr: %q)", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-max-parallel-workers=0") {
		t.Errorf("stderr = %q, want it to name the offending -max-parallel-workers=0 value", stderr.String())
	}

	if _, err := os.ReadFile(callLog); err == nil {
		t.Fatalf("driver-exec was invoked -- a non-positive -max-parallel-workers must abort the run before any pass runs")
	}
}

// TestMainRunRejectsNegativeBudgetCaps is
// TestMainRunRejectsNonPositiveMaxParallelWorkers's own twin for
// validateBudgetCaps (issue #2694 review finding): proves mainRun's own
// wiring -- flag parse, then the validateBudgetCaps call, then the fatal
// abort -- actually runs, not just that validateBudgetCaps itself rejects a
// negative value in isolation (caps_test.go already covers that).
func TestMainRunRejectsNegativeBudgetCaps(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n'
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	rc := mainRun(singlePassFakeDriverArgv(dir, "-max-budget-tokens=-1"), &stdout, &stderr)

	if rc != 1 {
		t.Fatalf("mainRun exit code = %d, want 1 (stderr: %q)", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-max-budget-tokens=-1") {
		t.Errorf("stderr = %q, want it to name the offending -max-budget-tokens=-1 value", stderr.String())
	}

	if _, err := os.ReadFile(callLog); err == nil {
		t.Fatalf("driver-exec was invoked -- a negative -max-budget-tokens must abort the run before any pass runs")
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

// TestMainRunPositiveMaxParallelWorkersFlagBoundsWorkerConcurrency proves
// the whole -max-parallel-workers pipeline end to end: the flag parsed by
// mainRun (main.go), threaded into config.maxParallelWorkers (the
// "maxParallelWorkers: *maxParallelWorkers" wiring at main.go), and from
// there into WorkerOptions.MaxParallel at the LaunchWorkers call site
// (dispatch.go's "MaxParallel: cfg.maxParallelWorkers"). A cap of 3 is
// deliberately picked to differ from defaultMaxParallelWorkers (2): if
// either wiring line were ever deleted, LaunchWorkers would silently fall
// back to the default and this test's observed peak would read 2, not 3,
// even though every existing test (including the ones proving runBounded's
// own admission control and LaunchWorkers' zero-value fallback) would stay
// green (issue #2495 review finding).
//
// Every slice declares its own distinct FileLeases entry so scheduleSlices
// (issue #2060) places all of them into the same batch -- an
// undeclared-lease slice is scheduled conservatively solo (schedule.go),
// which would otherwise flatten this test's observed peak to 1 regardless
// of maxParallel, proving nothing about the concurrency cap this test
// exists to pin.
func TestMainRunPositiveMaxParallelWorkersFlagBoundsWorkerConcurrency(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	workerWorkDir := filepath.Join(dir, "worker-work-dir")
	if err := os.MkdirAll(workerWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	countFile := filepath.Join(dir, "running-count")
	peakFile := filepath.Join(dir, "peak-count")
	lockDir := filepath.Join(dir, "count.lockdir")
	coordCountFile := filepath.Join(dir, "coord-count")
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)
	t.Setenv("COUNT_FILE", countFile)
	t.Setenv("PEAK_FILE", peakFile)
	t.Setenv("LOCK_DIR", lockDir)
	t.Setenv("COORD_COUNT_FILE", coordCountFile)

	const numSlices = 6
	const maxParallel = 3 // deliberately != defaultMaxParallelWorkers (2)

	slices := make([]ManifestSlice, numSlices)
	for i := range slices {
		slices[i] = ManifestSlice{
			Name:       fmt.Sprintf("slice-%d", i),
			Task:       "implement seam",
			FileLeases: []string{fmt.Sprintf("path/slice-%d.txt", i)},
		}
	}
	manifestLine, err := SliceManifest{Slices: slices}.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	// A worker invocation's own $DRIVER_LOG_PATH lands under
	// $WORKER_WORK_DIR (workers.go); the coordinator's own lands under
	// $PWD/stream.log. Every worker bumps a shared, mkdir-locked counter
	// (flock isn't guaranteed available in the box), records the running
	// peak, holds briefly to force overlap with its siblings, then
	// decrements before signaling done via its sentinel file -- the same
	// technique TestRunBoundedNeverExceedsMaxParallel uses in-process,
	// reproduced here across real subprocesses so it proves the config
	// wiring too, not just runBounded's own admission control.
	body := `case "$DRIVER_LOG_PATH" in
  "$WORKER_WORK_DIR"*)
    : > "$DRIVER_LOG_PATH"
    while ! mkdir "$LOCK_DIR" 2>/dev/null; do sleep 0.01; done
    n=$(( $(cat "$COUNT_FILE" 2>/dev/null || echo 0) + 1 ))
    echo "$n" > "$COUNT_FILE"
    peak=$(cat "$PEAK_FILE" 2>/dev/null || echo 0)
    if [ "$n" -gt "$peak" ]; then echo "$n" > "$PEAK_FILE"; fi
    rmdir "$LOCK_DIR"
    sleep 0.2
    while ! mkdir "$LOCK_DIR" 2>/dev/null; do sleep 0.01; done
    n=$(( $(cat "$COUNT_FILE") - 1 ))
    echo "$n" > "$COUNT_FILE"
    rmdir "$LOCK_DIR"
    : > "${DRIVER_LOG_PATH%.log}.done"
    exit 0
    ;;
esac
: > "$DRIVER_LOG_PATH"
n=$(cat "$COORD_COUNT_FILE" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "$COORD_COUNT_FILE"
case "$n" in
  1)
    printf '%s' '` + streamJSONOutcomeLine(strings.TrimSpace(manifestLine)) + `' >> "$DRIVER_LOG_PATH"
    ;;
  2)
    printf '%s' '` + streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") + `' >> "$DRIVER_LOG_PATH"
    ;;
esac
exit 0
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Manifest dispatch (dispatchManifestIfPresent) is only reachable
	// through the review-pass loop (run.go), so -review-prompt-file must be
	// set to route mainRun there -- the legacy loop never calls it at all.
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("REVIEW PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}

	argv := append(reviewPassFakeDriverArgv(dir),
		"-worker-prompt-file", workerPromptFile,
		"-worker-work-dir", workerWorkDir,
		"-worker-timeout", "10s",
		fmt.Sprintf("-max-parallel-workers=%d", maxParallel),
	)

	var stdout, stderr bytes.Buffer
	rc := mainRun(argv, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}

	peakBytes, err := os.ReadFile(peakFile)
	if err != nil {
		t.Fatalf("read peakFile: %v (workers may never have run)", err)
	}
	peak := strings.TrimSpace(string(peakBytes))
	if peak != fmt.Sprintf("%d", maxParallel) {
		t.Errorf("peak concurrent workers = %s, want exactly %d -- either the cap was not honored (peak > %d) or -max-parallel-workers never reached LaunchWorkers at all (peak = 2, the hardcoded default)", peak, maxParallel, maxParallel)
	}
}
