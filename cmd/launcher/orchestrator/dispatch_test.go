package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/runstate"
)

// gitOutputT runs `git <args...>` with its working directory set to dir,
// failing the test on a non-zero exit, and returns its trimmed combined
// output -- shared by the squash tests below to inspect repoRoot's HEAD/log
// the same way chdirToFreshWorkerRepo's own unexported `run` closure sets it
// up (workers_test.go).
func gitOutputT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestTruncateRunesNeverSplitsAMultiByteRune verifies truncateRunes cuts on
// a rune boundary rather than a byte index: a naive s[:max] byte slice can
// land mid-way through a multi-byte UTF-8 rune, producing an invalid
// encoding that decodes as U+FFFD once written out (issue #2059 review
// finding).
func TestTruncateRunesNeverSplitsAMultiByteRune(t *testing.T) {
	// "café" is 4 runes but 5 bytes -- the trailing "é" is a 2-byte rune, so
	// a byte cutoff at 4 would split it.
	got := truncateRunes("café", 4)
	if !strings.HasPrefix(got, "café") {
		t.Fatalf("truncateRunes(%q, 4) = %q, want a valid, unsplit \"café\" prefix", "café", got)
	}
	if strings.ContainsRune(got, '�') {
		t.Errorf("truncateRunes(%q, 4) = %q, want no replacement-character corruption", "café", got)
	}
}

// TestDispatchManifestIfPresentNoopWhenWorkerPromptFileUnset verifies
// dispatchManifestIfPresent is a no-op -- returning false and leaving state
// untouched -- when cfg.workerPromptFile is unset and its log carries no
// manifest either, matching every other "empty disables this feature" field
// on config (issue #2059).
func TestDispatchManifestIfPresentNoopWhenWorkerPromptFileUnset(t *testing.T) {
	cfg := config{workerPromptFile: "", logPath: filepath.Join(t.TempDir(), "nonexistent.log")}
	state := runstate.RunState{}
	want := runstate.RunState{}

	got, _, _ := dispatchManifestIfPresent(cfg, &state, io.Discard)
	if got {
		t.Errorf("dispatchManifestIfPresent() = true, want false")
	}
	if !reflect.DeepEqual(state, want) {
		t.Errorf("state = %+v, want untouched %+v", state, want)
	}
}

// TestDispatchManifestIfPresentPreservesPriorWorkerFindingsWhenWorkerPromptFileUnset
// verifies dispatchManifestIfPresent leaves a NON-empty state.WorkerFindings
// (set by an earlier dispatch, possibly in a prior Box invocation reading
// the same on-disk run-state) untouched when cfg.workerPromptFile is unset
// and this pass's own log carries no manifest of its own -- the routine
// fix-pass/review-only case (issue #2495 review finding). A zero-valued
// state, as TestDispatchManifestIfPresentNoopWhenWorkerPromptFileUnset above
// uses, can't distinguish "left untouched" from "cleared then happens to
// still be empty"; this test seeds a non-empty value so a regression to
// unconditional clearing is actually caught.
func TestDispatchManifestIfPresentPreservesPriorWorkerFindingsWhenWorkerPromptFileUnset(t *testing.T) {
	cfg := config{workerPromptFile: "", logPath: filepath.Join(t.TempDir(), "nonexistent.log")}
	state := runstate.RunState{WorkerFindings: "findings from an earlier dispatch, in this or a prior Box"}
	want := state

	got, _, _ := dispatchManifestIfPresent(cfg, &state, io.Discard)
	if got {
		t.Errorf("dispatchManifestIfPresent() = true, want false")
	}
	if !reflect.DeepEqual(state, want) {
		t.Errorf("state = %+v, want untouched %+v", state, want)
	}
}

// TestDispatchManifestIfPresentPreservesFindingsWhenManifestFoundButNoWorkerPromptFile
// verifies that when cfg.workerPromptFile is empty (a fix-pass or
// review-only invocation, which never renders worker-prompt.md), a manifest
// appearing in this pass's own log anyway -- e.g. a custom
// SPINDRIFT_PROMPT_DIR fix-prompt.md that still carries the manifest step,
// a legitimate, supported shape, not just a rogue/hallucinated marker --
// must NOT be scanned for or acted on at all: dispatchManifestIfPresent must
// return false and leave a prior state.WorkerFindings (set by an earlier
// pass's dispatch) untouched, exactly like the no-manifest case, rather than
// discarding it with a stderr note. Runtime coherence checks for this knob
// belong at orchestrator startup (issue #2495 AC3), not as mid-run handling
// here; this function's own job is only to seed/preserve state for the next
// pass's seedPromptFromState (issue #2495 review finding: this branch
// previously cleared state.WorkerFindings unconditionally here, destroying
// an earlier pass's dispatch results).
func TestDispatchManifestIfPresentPreservesFindingsWhenManifestFoundButNoWorkerPromptFile(t *testing.T) {
	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "orphaned", Task: "implement seam a"}}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "manifest-stream.log")
	if err := os.WriteFile(logPath, []byte(streamJSONOutcomeLine(strings.TrimSpace(line))), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: "",
	}
	state := runstate.RunState{WorkerFindings: "findings from an earlier dispatch, in this or a prior Box"}
	want := state

	got, _, _ := dispatchManifestIfPresent(cfg, &state, io.Discard)
	if got {
		t.Errorf("dispatchManifestIfPresent() = true, want false (no worker-prompt-file to dispatch against)")
	}
	if !reflect.DeepEqual(state, want) {
		t.Errorf("state = %+v, want untouched %+v", state, want)
	}
}

// TestDispatchManifestIfPresentNoopWhenNoManifestInLog verifies
// dispatchManifestIfPresent returns false and leaves state untouched when
// cfg.workerPromptFile is set but the pass's own log carries no manifest
// marker.
func TestDispatchManifestIfPresentNoopWhenNoManifestInLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine("Just narration, no manifest.")
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		workerPromptFile: filepath.Join(dir, "worker-prompt.txt"),
		logPath:          logPath,
		driver:           "claude",
	}
	state := runstate.RunState{}
	want := runstate.RunState{}

	got, _, _ := dispatchManifestIfPresent(cfg, &state, io.Discard)
	if got {
		t.Errorf("dispatchManifestIfPresent() = true, want false")
	}
	if !reflect.DeepEqual(state, want) {
		t.Errorf("state = %+v, want untouched %+v", state, want)
	}
}

// TestDispatchManifestIfPresentClearsStaleWorkerFindingsWhenNoManifest
// verifies that a pass with no manifest clears any state.WorkerFindings left
// over from an earlier dispatch, mirroring run.go's own unconditional
// per-pass state.ReviewFindings reassignment -- otherwise a later pass that
// dispatches no manifest of its own would still seed the next prompt with a
// stale worker report from a prior pass as though it were still fresh
// (issue #2058 review finding A).
func TestDispatchManifestIfPresentClearsStaleWorkerFindingsWhenNoManifest(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "done-fast", Task: "implement seam a"}}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	manifestLogPath := filepath.Join(dir, "manifest-stream.log")
	if err := os.WriteFile(manifestLogPath, []byte(streamJSONOutcomeLine(strings.TrimSpace(line))), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          manifestLogPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    2 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	if got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout); !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}
	if state.WorkerFindings == "" {
		t.Fatal("state.WorkerFindings is empty after first dispatch, want non-empty")
	}

	noManifestLogPath := filepath.Join(dir, "no-manifest-stream.log")
	if err := os.WriteFile(noManifestLogPath, []byte(streamJSONOutcomeLine("plain text, no manifest")), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.logPath = noManifestLogPath

	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if got {
		t.Errorf("dispatchManifestIfPresent() = true, want false (no manifest in second pass's log)")
	}
	if state.WorkerFindings != "" {
		t.Errorf("state.WorkerFindings = %q after a no-manifest pass, want empty (stale findings must not survive a pass that dispatched nothing)", state.WorkerFindings)
	}
}

// TestDispatchManifestIfPresentQuarantinesStrayWorkerOutcome proves issue
// #2491 AC2: a misbehaving worker that ignores its own prompt (which, per
// worker-prompt.md, teaches it no SPINDRIFT_OUTCOME grammar at all -- see
// the sibling test pinning that) and emits a stray SPINDRIFT_OUTCOME-shaped
// line into its own quarantined log anyway cannot terminate the run, cannot
// satisfy the launcher's own outcome scanner, and -- by the same structural
// argument, since the synthetic-outcome backstop in outcomebackstop.Run
// reads only cfg.logPath just like scanPassLog does -- cannot trip the
// backstop either. The stray line genuinely lands on disk in the worker's
// own --log-path (proving this isn't a vacuous test: the worker really did
// misbehave), but the coordinator's own pass log (cfg.logPath, the file
// dispatchManifestIfPresent itself was invoked against) never contains it,
// because launchOneWorker's structural quarantine (issue #2059) never
// forwards a worker's log content into the coordinator's own log or gives
// any worker a way to write to cfg.logPath. The join itself is unaffected:
// the worker still resolves to WorkerDone, exactly like "done-fast".
func TestDispatchManifestIfPresentQuarantinesStrayWorkerOutcome(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "emits-stray-outcome", Task: "implement seam a"}}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	manifestLogPath := filepath.Join(dir, "manifest-stream.log")
	if err := os.WriteFile(manifestLogPath, []byte(streamJSONOutcomeLine(strings.TrimSpace(line))), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          manifestLogPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    2 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	if got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout); !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}
	if state.WorkerFindings == "" {
		t.Fatal("state.WorkerFindings is empty, want the no-result-reported summary dispatchManifestIfPresent records for a WorkerDone slice (it must join as WorkerDone, not derail)")
	}

	workerLogPath := filepath.Join(cfg.workerWorkDir, "emits-stray-outcome.log")
	workerLog, err := os.ReadFile(workerLogPath)
	if err != nil {
		t.Fatalf("ReadFile(workerLogPath): %v", err)
	}
	if !strings.Contains(string(workerLog), outcome.Token) {
		t.Fatalf("worker's own log %q = %q, want it to contain the stray %s line (otherwise this test proves nothing)", workerLogPath, workerLog, outcome.Token)
	}

	verdict, hasOutcome := scanPassLog(cfg.logPath, cfg.driver)
	if hasOutcome {
		t.Errorf("scanPassLog(cfg.logPath) hasOutcome = true, want false -- the coordinator's own pass log must never see a worker's stray outcome line")
	}
	if verdict != "" {
		t.Errorf("scanPassLog(cfg.logPath) verdict = %q, want empty", verdict)
	}
}

// TestDispatchManifestIfPresentMovesRemainingSliceToDoneOnRetrySuccess
// verifies a slice that timed out on a prior dispatch (already present in
// state.RemainingSlices) is removed from RemainingSlices and appended to
// DoneSlices -- not left in both lists simultaneously -- once a later
// dispatch reports it WorkerDone (issue #2058 review finding B.1).
func TestDispatchManifestIfPresentMovesRemainingSliceToDoneOnRetrySuccess(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "done-fast", Task: "implement seam a"}}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	if err := os.WriteFile(logPath, []byte(streamJSONOutcomeLine(strings.TrimSpace(line))), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    2 * time.Second,
	}
	// Seed state as though "done-fast" timed out on a prior dispatch pass.
	state := runstate.RunState{RemainingSlices: []string{"done-fast"}}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	if !reflect.DeepEqual(state.DoneSlices, []string{"done-fast"}) {
		t.Errorf("state.DoneSlices = %v, want [done-fast]", state.DoneSlices)
	}
	if len(state.RemainingSlices) != 0 {
		t.Errorf("state.RemainingSlices = %v, want empty (done-fast must not remain listed as still-remaining once it succeeds)", state.RemainingSlices)
	}
}

// TestDispatchManifestIfPresentDoesNotDuplicateDoneSliceAcrossDispatches
// verifies dispatching the same slice name as WorkerDone across two separate
// calls appends it to state.DoneSlices exactly once, not twice (issue #2058
// review finding B.2).
func TestDispatchManifestIfPresentDoesNotDuplicateDoneSliceAcrossDispatches(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "done-fast", Task: "implement seam a"}}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	if err := os.WriteFile(logPath, []byte(streamJSONOutcomeLine(strings.TrimSpace(line))), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    2 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	if got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout); !got {
		t.Fatalf("dispatchManifestIfPresent() (first call) = false, want true")
	}
	if got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout); !got {
		t.Fatalf("dispatchManifestIfPresent() (second call) = false, want true")
	}

	if !reflect.DeepEqual(state.DoneSlices, []string{"done-fast"}) {
		t.Errorf("state.DoneSlices = %v, want [done-fast] exactly once", state.DoneSlices)
	}
}

// TestRunWithReviewPassDispatchesManifestThenContinuesImplementFixLoop
// verifies runWithReviewPass wires dispatchManifestIfPresent into its own
// implement/fix pass block end to end (issue #2059 AC1): a pass whose log
// carries a slice manifest dispatches the one declared worker, skips the
// review pass entirely for that iteration, and seeds the next pass's own
// prompt with the worker findings -- rather than treating the manifest pass
// as though it produced no outcome and stopping, or running a review pass
// against a manifest declaration there is nothing yet to review.
func TestRunWithReviewPassDispatchesManifestThenContinuesImplementFixLoop(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	coordCountFile := filepath.Join(dir, "coord-count")
	workerWorkDir := filepath.Join(dir, "worker-work-dir")
	if err := os.MkdirAll(workerWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)
	t.Setenv("COORD_COUNT_FILE", coordCountFile)

	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "slice-a", Task: "implement seam a"}}}
	manifestLine, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	body := `case "$DRIVER_LOG_PATH" in
  "$WORKER_WORK_DIR"*)
    : > "$DRIVER_LOG_PATH"
    sentinel="${DRIVER_LOG_PATH%.log}.done"
    : > "$sentinel"
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
    printf '%s' '` + streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") + `' | tee -a "$DRIVER_LOG_PATH"
    ;;
esac
exit 0
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("REVIEW PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		driverBin:        "claude",
		issue:            "7",
		logPath:          filepath.Join(dir, "stream.log"),
		heartbeatLog:     filepath.Join(dir, "heartbeat.log"),
		stateFile:        filepath.Join(dir, "run-state.json"),
		maxReviewRounds:  3,
		maxSlices:        10,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    workerWorkDir,
		workerTimeout:    2 * time.Second,
	}

	var stdout strings.Builder
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Errorf("exit code = %d, want 0", rc)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	// exactly two coordinator invocations (implement, fix) plus one worker
	// invocation (slice-a) -- three lines total.
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("driver-exec invocation count = %d, want 3 (log: %q)", len(lines), calls)
	}

	fixPromptFile := flagValue(lines[2], "--prompt-file")
	if fixPromptFile == "" || fixPromptFile == promptFile {
		t.Fatalf("pass 2 (fix) --prompt-file = %q, want a fresh seeded file", fixPromptFile)
	}
	seeded, err := os.ReadFile(fixPromptFile)
	if err != nil {
		t.Fatalf("read seeded fix prompt: %v", err)
	}
	for _, want := range []string{"Worker dispatch results", "slice-a"} {
		if !strings.Contains(string(seeded), want) {
			t.Errorf("fix pass seeded prompt = %q, want it to contain %q", seeded, want)
		}
	}

	out := stdout.String()
	if !strings.Contains(out, "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") {
		t.Errorf("stdout = %q, want the final pass's own outcome line present unchanged", out)
	}
	if !strings.Contains(out, `"spindrift_op":{"op":"pass_start","pass":1,"role":"implement"}`) {
		t.Errorf("stdout = %q, want pass 1's own pass_start with role \"implement\"", out)
	}
	if !strings.Contains(out, `"spindrift_op":{"op":"pass_start","pass":2,"role":"fix"}`) {
		t.Errorf("stdout = %q, want pass 2's own pass_start with role \"fix\"", out)
	}
	if strings.Contains(out, `"role":"review"`) {
		t.Errorf("stdout = %q, want no review pass_start for this manifest-dispatch cycle", out)
	}
}

// TestRunWithReviewPassTerminatesOnMaxBudgetTokensCapFromWorkerSpend covers
// the issue's own motivating end-to-end path (issue #2694 review finding):
// every other budget-cap test drives cumulative usage entirely from
// coordinator (implement/fix/review) pass logs, never a dispatched worker's
// own spend -- despite the issue's motivation naming the worker/reviewer
// loop as ~67% of a run's total cost. Here the coordinator's own passes
// report zero usage; only the dispatched worker's log carries a
// usage.Report (200 tokens), and -max-budget-tokens is set to 100 -- low
// enough that only the worker's own contribution can trip it. If
// dispatchManifestIfPresent's returned workerTokens/workerUSD were dropped
// on the floor before reaching cumulativeTokens/cumulativeUSD (run.go's own
// call site), this run would sail past the cap all the way to its plain
// SPINDRIFT_OUTCOME instead of landing via the budget cap.
func TestRunWithReviewPassTerminatesOnMaxBudgetTokensCapFromWorkerSpend(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	coordCountFile := filepath.Join(dir, "coord-count")
	workerWorkDir := filepath.Join(dir, "worker-work-dir")
	if err := os.MkdirAll(workerWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)
	t.Setenv("COORD_COUNT_FILE", coordCountFile)

	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "slice-a", Task: "implement seam a"}}}
	manifestLine, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	// Coordinator pass sequence: 1 implement (dispatches the manifest), 2
	// fix (no manifest this time, falls through to review), 3 review
	// (BLOCK -- the budget cap fires here once the worker's own 200-token
	// spend from pass 1's dispatch is folded in), 4 land (reaches its own
	// outcome, stopping the loop). The worker branch reports 200 tokens via
	// a stream-json result event before signaling done, same shape
	// streamJSONResultLine produces.
	body := `case "$DRIVER_LOG_PATH" in
  "$WORKER_WORK_DIR"*)
    : > "$DRIVER_LOG_PATH"
    printf '%s' '` + streamJSONResultLine(200, 0, 0) + `' >> "$DRIVER_LOG_PATH"
    sentinel="${DRIVER_LOG_PATH%.log}.done"
    : > "$sentinel"
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
  3)
    printf '%s' '` + streamJSONOutcomeLine("VERDICT: BLOCK") + `' >> "$DRIVER_LOG_PATH"
    ;;
  4)
    printf '%s' '` + streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") + `' | tee -a "$DRIVER_LOG_PATH"
    ;;
esac
exit 0
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("REVIEW PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		driverBin:        "claude",
		issue:            "7",
		logPath:          filepath.Join(dir, "stream.log"),
		heartbeatLog:     filepath.Join(dir, "heartbeat.log"),
		stateFile:        filepath.Join(dir, "run-state.json"),
		maxReviewRounds:  0,
		maxSlices:        0,
		maxBudgetTokens:  100,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    workerWorkDir,
		workerTimeout:    2 * time.Second,
	}

	var stdout strings.Builder
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Errorf("exit code = %d, want 0", rc)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	// 4 coordinator invocations (implement, fix, review, land) + 1 worker
	// invocation (slice-a) -- 5 lines total.
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("driver-exec invocation count = %d, want 5 (log: %q)", len(lines), calls)
	}

	out := stdout.String()
	if !strings.Contains(out, `"decision":"continue","reason":"budget exceeded; running terminal land pass"`) {
		t.Errorf("stdout = %q, want the budget-cap-fired continue reason -- the worker's own 200-token spend, alone, must be enough to trip -max-budget-tokens=100", out)
	}
	if !strings.Contains(out, `"spindrift_op":{"op":"pass_start","pass":4,"role":"land"}`) {
		t.Errorf("stdout = %q, want the terminal land pass's own pass_start with role \"land\"", out)
	}
	if !strings.Contains(out, "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") {
		t.Errorf("stdout = %q, want the land pass's own outcome line present unchanged", out)
	}
}

// TestRunWithReviewPassMaxSlicesCapNotShadowedByRepeatedManifestDispatch
// verifies the maxSlices cap (issue #2457) takes priority over a slice
// manifest dispatch in the pass-decision switch (issue #2058 review
// finding): a coordinator that re-emits a slice manifest on every single
// pass must not defeat the cap by matching the manifestDispatched case
// first, forever. With maxSlices tuned to 2, the loop must stop after
// exactly 3 coordinator invocations (2 manifest-dispatch passes, plus the
// terminal land pass the cap commits to) -- not run unbounded past that,
// even though this fake coordinator keeps declaring a manifest well past
// pass 3 (it only gives up and emits a terminal outcome on pass 7, purely
// as a backstop so a still-buggy case ordering doesn't hang this test
// forever).
func TestRunWithReviewPassMaxSlicesCapNotShadowedByRepeatedManifestDispatch(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	coordCountFile := filepath.Join(dir, "coord-count")
	workerWorkDir := filepath.Join(dir, "worker-work-dir")
	if err := os.MkdirAll(workerWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)
	t.Setenv("COORD_COUNT_FILE", coordCountFile)

	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "slice-a", Task: "implement seam a"}}}
	manifestLine, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	body := `case "$DRIVER_LOG_PATH" in
  "$WORKER_WORK_DIR"*)
    : > "$DRIVER_LOG_PATH"
    sentinel="${DRIVER_LOG_PATH%.log}.done"
    : > "$sentinel"
    exit 0
    ;;
esac
: > "$DRIVER_LOG_PATH"
n=$(cat "$COORD_COUNT_FILE" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "$COORD_COUNT_FILE"
if [ "$n" -le 6 ]; then
  printf '%s' '` + streamJSONOutcomeLine(strings.TrimSpace(manifestLine)) + `' >> "$DRIVER_LOG_PATH"
else
  printf '%s' '` + streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") + `' | tee -a "$DRIVER_LOG_PATH"
fi
exit 0
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("REVIEW PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		driverBin:        "claude",
		issue:            "7",
		logPath:          filepath.Join(dir, "stream.log"),
		heartbeatLog:     filepath.Join(dir, "heartbeat.log"),
		stateFile:        filepath.Join(dir, "run-state.json"),
		maxReviewRounds:  3,
		maxSlices:        2,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    workerWorkDir,
		workerTimeout:    2 * time.Second,
	}

	var stdout strings.Builder
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	coordCount, err := os.ReadFile(coordCountFile)
	if err != nil {
		t.Fatalf("read coordCountFile: %v", err)
	}
	if got := strings.TrimSpace(string(coordCount)); got != "3" {
		t.Fatalf("coordinator invocation count = %s, want 3 (maxSlices=2 cap plus its terminal land pass -- a repeated manifest dispatch must not shadow the cap and run it unbounded)", got)
	}

	got, err := runstate.ReadRunState(cfg.stateFile)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if !got.TerminalLand {
		t.Errorf("TerminalLand = %v, want true (the cap must still fire and win, even though this pass also dispatched a manifest)", got.TerminalLand)
	}
	if got.CapFired != "max slices reached" {
		t.Errorf("CapFired = %q, want %q", got.CapFired, "max slices reached")
	}
}

// TestDispatchManifestIfPresentSkipsRedispatchOfAlreadyDoneSlice verifies a
// manifest slice whose name already appears in state.DoneSlices is filtered
// out before LaunchWorkers is ever called -- launchOneWorker's own `git
// worktree add -B <branch>` unconditionally force-resets that slice's
// branch to the orchestrator's current HEAD (workers.go), which would
// silently destroy a completed worker's commits if re-dispatched (issue
// #2059 review finding). A second, genuinely-new slice name in the same
// manifest must still dispatch normally, and the skipped slice gets a
// skip-notice line in state.WorkerFindings instead.
func TestDispatchManifestIfPresentSkipsRedispatchOfAlreadyDoneSlice(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "already-done", Task: "implement seam a"},
		{Name: "done-fast", Task: "implement seam b"},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine(strings.TrimSpace(line))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    2 * time.Second,
	}
	state := runstate.RunState{DoneSlices: []string{"already-done"}}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	if strings.Contains(string(calls), "already-done") {
		t.Errorf("callLog = %q, want no invocation for the already-done slice (its worktree/branch must never be touched)", calls)
	}
	if !strings.Contains(string(calls), "done-fast") {
		t.Errorf("callLog = %q, want an invocation for the genuinely-new done-fast slice", calls)
	}

	if !reflect.DeepEqual(state.DoneSlices, []string{"already-done", "done-fast"}) {
		t.Errorf("state.DoneSlices = %v, want [already-done done-fast]", state.DoneSlices)
	}
	if !strings.Contains(state.WorkerFindings, "already-done: already done, skipped redispatch") {
		t.Errorf("state.WorkerFindings = %q, want a skip notice for already-done", state.WorkerFindings)
	}
	if !strings.Contains(state.WorkerFindings, "done-fast: done") {
		t.Errorf("state.WorkerFindings = %q, want a done notice for done-fast", state.WorkerFindings)
	}
}

// TestDispatchManifestIfPresentSkipsLaunchWorkersWhenAllSlicesAlreadyDone
// verifies dispatchManifestIfPresent never calls LaunchWorkers at all when
// every slice in the manifest is already in state.DoneSlices (issue #2059
// review finding) -- LaunchWorkers' first action is to create
// cfg.workerWorkDir, so asserting that directory is never created proves it
// was never invoked. dispatchManifestIfPresent still returns true (a
// manifest was present) and still records the skip-notice findings.
func TestDispatchManifestIfPresentSkipsLaunchWorkersWhenAllSlicesAlreadyDone(t *testing.T) {
	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "already-done", Task: "implement seam a"}}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine(strings.TrimSpace(line))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	workerWorkDir := filepath.Join(dir, "workdir-that-should-not-be-created")
	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: filepath.Join(dir, "worker-prompt.txt"),
		workerWorkDir:    workerWorkDir,
		workerTimeout:    2 * time.Second,
	}
	state := runstate.RunState{DoneSlices: []string{"already-done"}}

	got, _, _ := dispatchManifestIfPresent(cfg, &state, io.Discard)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}
	if !strings.Contains(state.WorkerFindings, "already-done: already done, skipped redispatch") {
		t.Errorf("state.WorkerFindings = %q, want a skip notice for already-done", state.WorkerFindings)
	}
	if _, err := os.Stat(workerWorkDir); !os.IsNotExist(err) {
		t.Errorf("workerWorkDir exists (stat err = %v), want it never created -- LaunchWorkers must never run when every slice is already done", err)
	}
}

// TestRunWithReviewPassSkipsManifestDispatchWhenPassAlreadyReachedOutcome
// verifies a pass whose log carries both a valid SPINDRIFT_OUTCOME line AND a
// valid SPINDRIFT_SLICE_MANIFEST line never invokes the worker-dispatch path
// at all (issue #2059 review finding): dispatchManifestIfPresent (which
// blocks for up to the full worker timeout) previously ran unconditionally,
// before the loop's own switch decided the pass had already reached a
// terminal outcome -- wasting a full worker fan-out on a pass the loop was
// about to stop on anyway. The fake driver-exec used for a dispatched worker
// would append to workerCallLog if ever invoked; asserting it stays empty
// proves LaunchWorkers, and therefore launchOneWorker's own `git worktree
// add`, never ran.
func TestRunWithReviewPassSkipsManifestDispatchWhenPassAlreadyReachedOutcome(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	workerCallLog := filepath.Join(dir, "worker-calls.log")
	if err := os.WriteFile(workerCallLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	workerWorkDir := filepath.Join(dir, "worker-work-dir")
	if err := os.MkdirAll(workerWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)
	t.Setenv("WORKER_CALL_LOG", workerCallLog)

	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "slice-a", Task: "implement seam a"}}}
	manifestLine, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	// A single coordinator pass whose own log carries BOTH a manifest and a
	// terminal outcome line. If a worker were ever dispatched, it would
	// exec through this same fake driver-exec with a --log-path under
	// WORKER_WORK_DIR, and record itself into WORKER_CALL_LOG.
	body := `case "$DRIVER_LOG_PATH" in
  "$WORKER_WORK_DIR"*)
    echo "$@" >> "$WORKER_CALL_LOG"
    : > "$DRIVER_LOG_PATH"
    sentinel="${DRIVER_LOG_PATH%.log}.done"
    : > "$sentinel"
    exit 0
    ;;
esac
: > "$DRIVER_LOG_PATH"
printf '%s%s' '` + streamJSONOutcomeLine(strings.TrimSpace(manifestLine)) + `' '` + streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") + `' | tee -a "$DRIVER_LOG_PATH"
exit 0
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("REVIEW PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		driverBin:        "claude",
		issue:            "7",
		logPath:          filepath.Join(dir, "stream.log"),
		heartbeatLog:     filepath.Join(dir, "heartbeat.log"),
		stateFile:        filepath.Join(dir, "run-state.json"),
		maxReviewRounds:  3,
		maxSlices:        10,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    workerWorkDir,
		workerTimeout:    2 * time.Second,
	}

	var stdout strings.Builder
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Errorf("exit code = %d, want 0", rc)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("driver-exec invocation count = %d, want 1 (the loop must stop right after this pass's own outcome, never fanning out a worker) (log: %q)", len(lines), calls)
	}

	workerCalls, err := os.ReadFile(workerCallLog)
	if err != nil {
		t.Fatalf("read workerCallLog: %v", err)
	}
	if len(strings.TrimSpace(string(workerCalls))) != 0 {
		t.Errorf("workerCallLog = %q, want empty -- a pass that already reached its own outcome must never dispatch a worker", workerCalls)
	}

	out := stdout.String()
	if !strings.Contains(out, "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") {
		t.Errorf("stdout = %q, want the pass's own outcome line present unchanged", out)
	}
	if !strings.Contains(out, `"spindrift_op":{"op":"decision","decision":"stop","reason":"outcome reached"}`) {
		t.Errorf("stdout = %q, want a stop decision with reason \"outcome reached\"", out)
	}
}

// TestDispatchManifestIfPresentDispatchesAndMergesResults verifies
// dispatchManifestIfPresent, when the pass's own log carries a manifest,
// dispatches LaunchWorkers and merges every WorkerResult into state: done
// slices into state.DoneSlices, timed-out/crashed slices into
// state.RemainingSlices, and a per-slice summary into state.WorkerFindings
// (issue #2059).
func TestDispatchManifestIfPresentDispatchesAndMergesResults(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "done-fast", Task: "implement seam a"},
		{Name: "crash-now", Task: "implement seam b"},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine(strings.TrimSpace(line))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    200 * time.Millisecond,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	if !reflect.DeepEqual(state.DoneSlices, []string{"done-fast"}) {
		t.Errorf("state.DoneSlices = %v, want [done-fast]", state.DoneSlices)
	}
	if !reflect.DeepEqual(state.RemainingSlices, []string{"crash-now"}) {
		t.Errorf("state.RemainingSlices = %v, want [crash-now]", state.RemainingSlices)
	}
	if state.WorkerFindings == "" {
		t.Fatal("state.WorkerFindings is empty, want non-empty")
	}
	for _, want := range []string{"done-fast", "crash-now"} {
		if !strings.Contains(state.WorkerFindings, want) {
			t.Errorf("state.WorkerFindings = %q, want it to contain %q", state.WorkerFindings, want)
		}
	}
}

// TestDispatchManifestIfPresentReturnsCumulativeWorkerUsage verifies
// dispatchManifestIfPresent sums every dispatched slice's own token/USD
// usage into its returned workerTokens/workerUSD (issue #2694 review
// finding): the budget cap's own motivating measurement names the worker
// loop as the majority of a run's spend, so this dispatch's own contribution
// must be visible to the caller's cumulative accumulator, not just each
// slice's pass/fail outcome.
func TestDispatchManifestIfPresentReturnsCumulativeWorkerUsage(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	body := `echo "worker log" > "$DRIVER_LOG_PATH"
base=$(basename "$DRIVER_LOG_PATH")
case "$base" in
  alpha.log)
    printf '%s' '` + streamJSONResultLine(100, 50, 1.0) + `' >> "$DRIVER_LOG_PATH"
    : > "${DRIVER_LOG_PATH%.log}.done"
    exit 0
    ;;
  beta.log)
    printf '%s' '` + streamJSONResultLine(200, 25, 2.5) + `' >> "$DRIVER_LOG_PATH"
    : > "${DRIVER_LOG_PATH%.log}.done"
    exit 0
    ;;
esac
`
	writeFakeDriverExec(t, fakeDir, callLog, body)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "alpha", Task: "implement seam a"},
		{Name: "beta", Task: "implement seam b"},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine(strings.TrimSpace(line))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    200 * time.Millisecond,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, workerTokens, workerUSD := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}
	if wantTokens := 100 + 50 + 200 + 25; workerTokens != wantTokens {
		t.Errorf("workerTokens = %d, want %d", workerTokens, wantTokens)
	}
	if wantUSD := 1.0 + 2.5; workerUSD != wantUSD {
		t.Errorf("workerUSD = %v, want %v", workerUSD, wantUSD)
	}
}

// TestDispatchManifestIfPresentIncludesWorkerResultInFindings verifies
// dispatchManifestIfPresent's WorkerDone findings line includes the
// worker's own reported Result text, not just the literal word "done" --
// otherwise the next coordinator pass never sees what a worker actually
// reported (issue #2059 review finding).
func TestDispatchManifestIfPresentIncludesWorkerResultInFindings(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	body := `: > "$DRIVER_LOG_PATH"
result="${DRIVER_LOG_PATH%.log}.result"
printf '%s' 'REPORT: slice-a implemented the parser fix.' > "$result"
sentinel="${DRIVER_LOG_PATH%.log}.done"
: > "$sentinel"
exit 0
`
	writeFakeDriverExec(t, fakeDir, callLog, body)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "slice-a", Task: "implement seam a"}}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine(strings.TrimSpace(line))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    2 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	want := "REPORT: slice-a implemented the parser fix."
	if !strings.Contains(state.WorkerFindings, want) {
		t.Errorf("state.WorkerFindings = %q, want it to contain the worker's own reported result %q", state.WorkerFindings, want)
	}
	wantBranch := "orchestrator-worker/slice-a"
	if !strings.Contains(state.WorkerFindings, wantBranch) {
		t.Errorf("state.WorkerFindings = %q, want it to name the branch %q to cherry-pick (issue #2059 review finding)", state.WorkerFindings, wantBranch)
	}
}

// TestDispatchManifestIfPresentReportsCrashErrNotMisleadingExitCode
// verifies dispatchManifestIfPresent's WorkerCrashed findings line
// surfaces r.Err (the orchestrator-side launch/join error) when present,
// rather than falsely reporting "exit 0" -- workers.go's own early
// launch-failure paths (e.g. seedWorkerPrompt) set Err but leave ExitCode
// at its zero value (issue #2059 review finding).
func TestDispatchManifestIfPresentReportsCrashErrNotMisleadingExitCode(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "slice-a", Task: "implement seam a"}}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine(strings.TrimSpace(line))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:  "claude",
		logPath: logPath,
		// A nonexistent workerPromptFile makes seedWorkerPrompt fail before
		// any process is ever launched, setting WorkerResult.Err with
		// ExitCode left at 0.
		workerPromptFile: filepath.Join(dir, "nonexistent-worker-prompt.txt"),
		workerWorkDir:    t.TempDir(),
		workerTimeout:    2 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	if strings.Contains(state.WorkerFindings, "exit 0") {
		t.Errorf("state.WorkerFindings = %q, want no misleading \"exit 0\" when r.Err is set", state.WorkerFindings)
	}
	if !strings.Contains(state.WorkerFindings, "seed worker prompt") {
		t.Errorf("state.WorkerFindings = %q, want it to contain the orchestrator-side error message", state.WorkerFindings)
	}
}

// TestDispatchManifestIfPresentTruncatesLongWorkerResult verifies
// dispatchManifestIfPresent caps how much of a worker's own reported
// Result text it includes in findings, so one runaway worker report can't
// blow out the next pass's prompt size (issue #2059 review finding).
func TestDispatchManifestIfPresentTruncatesLongWorkerResult(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	body := `: > "$DRIVER_LOG_PATH"
result="${DRIVER_LOG_PATH%.log}.result"
awk 'BEGIN { for (i = 0; i < 6000; i++) printf "a" }' > "$result"
sentinel="${DRIVER_LOG_PATH%.log}.done"
: > "$sentinel"
exit 0
`
	writeFakeDriverExec(t, fakeDir, callLog, body)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "slice-a", Task: "implement seam a"}}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine(strings.TrimSpace(line))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    2 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	if len(state.WorkerFindings) >= 6000 {
		t.Errorf("len(state.WorkerFindings) = %d, want it capped well under the 6000-byte raw result", len(state.WorkerFindings))
	}
	if !strings.Contains(state.WorkerFindings, "(truncated)") {
		t.Errorf("state.WorkerFindings = %q, want a truncation marker", state.WorkerFindings)
	}
}

// TestDispatchManifestIfPresentIntegratesDisjointLeaseSlicesOntoHead
// verifies dispatchManifestIfPresent (issue #2060): a manifest of two
// slices declaring disjoint FileLeases dispatch through scheduleSlices'
// batching, and once dispatch finishes, both branches' own commits are
// integrated automatically onto the orchestrator's own repo HEAD -- both
// worker-committed files are present on disk, state.DoneSlices lists both
// slice names, and state.WorkerFindings reports "integrated" for both,
// rather than the old "ready to cherry-pick" wording a human/coordinator
// pass previously had to act on by hand.
func TestDispatchManifestIfPresentIntegratesDisjointLeaseSlicesOntoHead(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	body := `case "$DRIVER_LOG_PATH" in
  "$WORKER_WORK_DIR"*)
    : > "$DRIVER_LOG_PATH"
    base=$(basename "$DRIVER_LOG_PATH" .log)
    echo "content for $base" > "$base.txt"
    git add "$base.txt"
    git commit -m "worker commit for $base"
    : > "${DRIVER_LOG_PATH%.log}.done"
    exit 0
    ;;
esac
: > "$DRIVER_LOG_PATH"
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workerWorkDir := t.TempDir()
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)

	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "alpha", Task: "implement seam alpha", FileLeases: []string{"alpha.txt"}},
		{Name: "beta", Task: "implement seam beta", FileLeases: []string{"beta.txt"}},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	if err := os.WriteFile(logPath, []byte(streamJSONOutcomeLine(strings.TrimSpace(line))), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    workerWorkDir,
		workerTimeout:    5 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	for _, want := range []string{"alpha", "beta"} {
		if !containsSlice(state.DoneSlices, want) {
			t.Errorf("state.DoneSlices = %v, want it to contain %q", state.DoneSlices, want)
		}
	}

	for _, f := range []string{"alpha.txt", "beta.txt"} {
		content, err := os.ReadFile(filepath.Join(repoRoot, f))
		if err != nil {
			t.Errorf("ReadFile(%s) after integration: %v", f, err)
			continue
		}
		if !strings.Contains(string(content), "content for") {
			t.Errorf("%s content = %q, want the worker's own committed content present on HEAD", f, content)
		}
	}

	for _, want := range []string{"alpha: done, integrated", "beta: done, integrated"} {
		if !strings.Contains(state.WorkerFindings, want) {
			t.Errorf("state.WorkerFindings = %q, want it to contain %q", state.WorkerFindings, want)
		}
	}
}

// TestDispatchManifestIfPresentSquashesMultipleSliceIntegrationsIntoOneCommit
// verifies dispatchManifestIfPresent (issue #2060 review finding): two
// independent slices (disjoint declared FileLeases, so they batch and
// integrate within the same LaunchWorkers call) each land their own
// per-slice integration commit onto repoRoot's HEAD during the batch loop,
// but once every batch in the manifest has been dispatched and integrated,
// those per-slice commits must be squashed into exactly ONE final commit on
// HEAD -- a single coherent change, not one "chore(orchestrator): integrate
// slice <name>" commit per slice -- naming every integrated slice in its
// own message.
func TestDispatchManifestIfPresentSquashesMultipleSliceIntegrationsIntoOneCommit(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)
	preCallHead := gitOutputT(t, repoRoot, "rev-parse", "HEAD")

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	body := `case "$DRIVER_LOG_PATH" in
  "$WORKER_WORK_DIR"*)
    : > "$DRIVER_LOG_PATH"
    base=$(basename "$DRIVER_LOG_PATH" .log)
    echo "content for $base" > "$base.txt"
    git add "$base.txt"
    git commit -m "worker commit for $base"
    : > "${DRIVER_LOG_PATH%.log}.done"
    exit 0
    ;;
esac
: > "$DRIVER_LOG_PATH"
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workerWorkDir := t.TempDir()
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)

	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "alpha", Task: "implement seam alpha", FileLeases: []string{"alpha.txt"}},
		{Name: "beta", Task: "implement seam beta", FileLeases: []string{"beta.txt"}},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	if err := os.WriteFile(logPath, []byte(streamJSONOutcomeLine(strings.TrimSpace(line))), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    workerWorkDir,
		workerTimeout:    5 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	newCommits := gitOutputT(t, repoRoot, "rev-list", preCallHead+"..HEAD")
	commitCount := 0
	if newCommits != "" {
		commitCount = len(strings.Split(newCommits, "\n"))
	}
	if commitCount != 1 {
		t.Fatalf("rev-list %s..HEAD has %d commits, want exactly 1 (per-slice integration commits should be squashed into one)", preCallHead, commitCount)
	}

	headMsg := gitOutputT(t, repoRoot, "log", "-1", "--format=%B", "HEAD")
	if !strings.Contains(headMsg, "alpha") || !strings.Contains(headMsg, "beta") {
		t.Errorf("squashed commit message = %q, want it to name both integrated slices alpha and beta", headMsg)
	}

	for _, f := range []string{"alpha.txt", "beta.txt"} {
		content, err := os.ReadFile(filepath.Join(repoRoot, f))
		if err != nil {
			t.Errorf("ReadFile(%s) after integration: %v", f, err)
			continue
		}
		if !strings.Contains(string(content), "content for") {
			t.Errorf("%s content = %q, want the worker's own committed content present on the squashed HEAD", f, content)
		}
	}
}

// TestDispatchManifestIfPresentSquashPreservesConflictGuidanceAcrossBatches
// verifies dispatchManifestIfPresent (issue #2060 review finding): a
// conflicted slice dispatched in a batch AFTER an earlier batch has already
// landed its own per-slice integration commit must still get a usable
// cherry-pick range in its finding's manual-resolution guidance, even
// though squashIntegrationCommits (run once, after every batch) later
// rewrites HEAD via `git reset --soft` and orphans that earlier batch's own
// interim commit -- exactly the commit a later batch's worker branch (`git
// worktree add ... HEAD`, workers.go) was rooted on. Three slices: "alpha"
// (batch 0, unrelated FileLeases, integrates cleanly) landing an interim
// commit; "beta" and "gamma" (batch 1, both DependsOn alpha, disjoint
// declared FileLeases so they share a batch and both root their own
// worktrees on alpha's interim commit) each independently create shared.txt
// with different content -- beta integrates first and lands cleanly, then
// gamma's own cherry-pick, replayed against beta's now-different shared.txt,
// conflicts. Recomputing merge-base(HEAD, gamma's branch) AFTER the squash
// below resolves to startHead (alpha's interim commit is unreachable from
// the squashed HEAD), which would replay alpha's own already-squashed diff
// a second time and halt with git's own empty-cherry-pick guard -- this
// test proves the guidance's own frozen range instead applies cleanly.
func TestDispatchManifestIfPresentSquashPreservesConflictGuidanceAcrossBatches(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	body := `case "$DRIVER_LOG_PATH" in
  "$WORKER_WORK_DIR"*)
    : > "$DRIVER_LOG_PATH"
    base=$(basename "$DRIVER_LOG_PATH" .log)
    case "$base" in
      alpha)
        echo "content for alpha" > alpha.txt
        git add alpha.txt
        ;;
      beta)
        echo "content B" > shared.txt
        git add shared.txt
        ;;
      gamma)
        echo "content C" > shared.txt
        git add shared.txt
        ;;
    esac
    git commit -m "worker commit for $base"
    : > "${DRIVER_LOG_PATH%.log}.done"
    exit 0
    ;;
esac
: > "$DRIVER_LOG_PATH"
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workerWorkDir := t.TempDir()
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)

	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "alpha", Task: "implement seam alpha", FileLeases: []string{"alpha.txt"}},
		{Name: "beta", Task: "implement seam beta", FileLeases: []string{"lease-beta"}, DependsOn: []string{"alpha"}},
		{Name: "gamma", Task: "implement seam gamma", FileLeases: []string{"lease-gamma"}, DependsOn: []string{"alpha"}},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	if err := os.WriteFile(logPath, []byte(streamJSONOutcomeLine(strings.TrimSpace(line))), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    workerWorkDir,
		workerTimeout:    5 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !containsSlice(state.DoneSlices, want) {
			t.Errorf("state.DoneSlices = %v, want it to contain %q", state.DoneSlices, want)
		}
	}

	if !strings.Contains(state.WorkerFindings, "alpha: done, integrated") {
		t.Errorf("state.WorkerFindings = %q, want alpha reported as cleanly integrated", state.WorkerFindings)
	}
	if !strings.Contains(state.WorkerFindings, "beta: done, integrated") {
		t.Errorf("state.WorkerFindings = %q, want beta reported as cleanly integrated", state.WorkerFindings)
	}
	if !strings.Contains(state.WorkerFindings, "gamma: done, but integration conflicted") {
		t.Errorf("state.WorkerFindings = %q, want gamma reported as conflicted", state.WorkerFindings)
	}

	// Squash landed alpha and beta's own interim commits as exactly one
	// commit on HEAD; gamma's conflict never landed anything.
	headMsg := gitOutputT(t, repoRoot, "log", "-1", "--format=%B", "HEAD")
	if !strings.Contains(headMsg, "alpha") || !strings.Contains(headMsg, "beta") {
		t.Errorf("squashed commit message = %q, want it to name both integrated slices alpha and beta", headMsg)
	}
	if strings.Contains(headMsg, "gamma") {
		t.Errorf("squashed commit message = %q, want it to NOT name gamma (its own integration conflicted, nothing landed)", headMsg)
	}

	// Pull the exact `git cherry-pick <range>` command the rendered
	// fallback guidance names for gamma. gamma's own change genuinely
	// conflicts with beta's (both touch shared.txt), so replaying the
	// range is still expected to conflict -- the regression this guards is
	// a DIFFERENT failure: naming a range recomputed against post-squash
	// HEAD would fold alpha's own already-squashed interim commit into the
	// range too (rev-list count 2, not 1), and replaying THAT commit first
	// halts on git's own empty-cherry-pick guard before gamma's own commit
	// (the one that's actually supposed to conflict) is ever reached.
	const marker = "git cherry-pick --no-commit "
	idx := strings.Index(state.WorkerFindings, marker)
	if idx == -1 {
		t.Fatalf("state.WorkerFindings = %q, want it to carry a %q command for gamma's own conflict", state.WorkerFindings, marker)
	}
	rest := state.WorkerFindings[idx+len(marker):]
	revRange := rest[:strings.IndexAny(rest, " \n")]
	if !strings.Contains(revRange, "..") {
		t.Fatalf("extracted revRange = %q, want a <sha>..<branch> range", revRange)
	}

	revListOut := gitOutputT(t, repoRoot, "rev-list", revRange)
	revListCount := 0
	if revListOut != "" {
		revListCount = len(strings.Split(revListOut, "\n"))
	}
	if revListCount != 1 {
		t.Fatalf("rev-list %s has %d commits, want exactly 1 (gamma's own commit only) -- a count of 2 means the range wrongly folded in alpha's own already-squashed interim commit", revRange, revListCount)
	}

	cherryCmd := exec.Command("git", "cherry-pick", "--no-commit", revRange)
	cherryCmd.Dir = repoRoot
	cherryOut, cherryErr := cherryCmd.CombinedOutput()
	if cherryErr == nil {
		t.Fatalf("git cherry-pick --no-commit %s (frozen guidance range) against post-squash HEAD unexpectedly succeeded, want gamma's genuine content conflict with beta's own change to shared.txt", revRange)
	}
	if strings.Contains(string(cherryOut), "previous cherry-pick is now empty") {
		t.Fatalf("git cherry-pick --no-commit %s failed on git's own empty-cherry-pick guard (alpha's already-squashed commit got replayed a second time) instead of gamma's genuine conflict:\n%s", revRange, cherryOut)
	}
	if !strings.Contains(string(cherryOut), "CONFLICT") {
		t.Fatalf("git cherry-pick --no-commit %s failed for an unexpected reason, want a CONFLICT on shared.txt:\n%s", revRange, cherryOut)
	}
}

// TestDispatchManifestIfPresentDoesNotSquashWhenNothingIntegrated verifies
// dispatchManifestIfPresent (issue #2060 review finding): when a dispatched
// slice's own branch carries no new commits (integrateEmpty), HEAD is never
// advanced, so squashIntegrationCommits must be a strict no-op -- it must
// never create a spurious commit on top of an unchanged HEAD.
func TestDispatchManifestIfPresentDoesNotSquashWhenNothingIntegrated(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)
	preCallHead := gitOutputT(t, repoRoot, "rev-parse", "HEAD")

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// The fake worker succeeds but never commits anything of its own, so
	// its branch carries no commits past its merge-base with HEAD --
	// integrateSliceBranch reports integrateEmpty, and HEAD stays put.
	body := `case "$DRIVER_LOG_PATH" in
  "$WORKER_WORK_DIR"*)
    : > "$DRIVER_LOG_PATH"
    : > "${DRIVER_LOG_PATH%.log}.done"
    exit 0
    ;;
esac
: > "$DRIVER_LOG_PATH"
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workerWorkDir := t.TempDir()
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)

	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "noop-slice", Task: "implement seam that makes no changes"},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	if err := os.WriteFile(logPath, []byte(streamJSONOutcomeLine(strings.TrimSpace(line))), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    workerWorkDir,
		workerTimeout:    5 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	if !strings.Contains(state.WorkerFindings, "noop-slice: done, nothing to integrate") {
		t.Fatalf("state.WorkerFindings = %q, want noop-slice reported as integrateEmpty", state.WorkerFindings)
	}

	postCallHead := gitOutputT(t, repoRoot, "rev-parse", "HEAD")
	if postCallHead != preCallHead {
		t.Errorf("HEAD moved from %s to %s with nothing integrated, want it unchanged (no spurious squash commit)", preCallHead, postCallHead)
	}
}

// TestDispatchManifestIfPresentDispatchesSeparateBatchesSequentially
// verifies dispatchManifestIfPresent (issue #2060): two slices declaring no
// FileLeases at all are each scheduled into their own separate, sequential
// batch by scheduleSlices (an undeclared-lease slice is always solo,
// schedule.go) -- and, crucially, the SECOND slice's own worktree (created
// via `git worktree add ... HEAD`, workers.go) is created only after the
// FIRST slice's own branch has already been integrated onto HEAD, so the
// second worker's own worktree already carries the first worker's
// committed file. The fake worker driver below deliberately crashes
// (never writes its own sentinel) if it does not see that file, proving
// the batches genuinely ran sequentially with integration in between --
// not merely dispatched in manifest order with no ordering guarantee
// between them.
func TestDispatchManifestIfPresentDispatchesSeparateBatchesSequentially(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	body := `case "$DRIVER_LOG_PATH" in
  "$WORKER_WORK_DIR"*)
    : > "$DRIVER_LOG_PATH"
    base=$(basename "$DRIVER_LOG_PATH" .log)
    case "$base" in
      first)
        echo "marker from first" > first.marker
        git add first.marker
        git commit -m "first commit"
        ;;
      second)
        if [ -f first.marker ]; then
          echo "second saw first" > second.marker
          git add second.marker
          git commit -m "second commit"
        else
          exit 1
        fi
        ;;
    esac
    : > "${DRIVER_LOG_PATH%.log}.done"
    exit 0
    ;;
esac
: > "$DRIVER_LOG_PATH"
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workerWorkDir := t.TempDir()
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)

	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "first", Task: "implement seam first"},
		{Name: "second", Task: "implement seam second"},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	if err := os.WriteFile(logPath, []byte(streamJSONOutcomeLine(strings.TrimSpace(line))), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    workerWorkDir,
		workerTimeout:    5 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	if !reflect.DeepEqual(state.DoneSlices, []string{"first", "second"}) {
		t.Fatalf("state.DoneSlices = %v, want [first second] -- \"second\" missing/crashed means its worktree was created before \"first\"'s batch was integrated (batches did not run strictly sequentially)", state.DoneSlices)
	}
}

// TestDispatchManifestIfPresentReportsIntegrationConflictWithoutLosingDoneSlice
// verifies dispatchManifestIfPresent (issue #2060): two slices with
// disjoint DECLARED FileLeases (so scheduleSlices dispatches them
// concurrently in the same batch) both modify the same real file with
// different content -- the first slice's branch integrates cleanly and
// advances HEAD; the second slice's own branch, built from the ORIGINAL
// pre-integration HEAD, then conflicts on integration. Both workers
// themselves still succeeded (WorkerDone), so state.DoneSlices must list
// both regardless -- but state.WorkerFindings must report the conflict
// with manual-resolution guidance for the second slice, not silently
// swallow it. That guidance is sourced from the real
// conflict-resolve-cherry-pick-prompt.md template (issue #2060 review
// finding), rendered via conflictResolveGuidance -- PROMPTS_DIR is pointed
// at this repo's own templates/default/prompts dir (resolved to an
// absolute path before chdirToFreshWorkerRepo below changes the process's
// cwd out from under a relative lookup) so the test exercises that real
// render path rather than conflictResolveGuidance's own missing-template
// fallback string.
func TestDispatchManifestIfPresentReportsIntegrationConflictWithoutLosingDoneSlice(t *testing.T) {
	promptsDirAbs, err := filepath.Abs(filepath.Join("..", "..", "..", "templates", "default", "prompts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROMPTS_DIR", promptsDirAbs)

	chdirToFreshWorkerRepo(t)

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	body := `case "$DRIVER_LOG_PATH" in
  "$WORKER_WORK_DIR"*)
    : > "$DRIVER_LOG_PATH"
    base=$(basename "$DRIVER_LOG_PATH" .log)
    case "$base" in
      conflict-a)
        echo "content A" > shared.txt
        ;;
      conflict-b)
        echo "content B" > shared.txt
        ;;
    esac
    git add shared.txt
    git commit -m "worker commit for $base"
    : > "${DRIVER_LOG_PATH%.log}.done"
    exit 0
    ;;
esac
: > "$DRIVER_LOG_PATH"
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workerWorkDir := t.TempDir()
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)

	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "conflict-a", Task: "implement seam a", FileLeases: []string{"lease-a"}},
		{Name: "conflict-b", Task: "implement seam b", FileLeases: []string{"lease-b"}},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	if err := os.WriteFile(logPath, []byte(streamJSONOutcomeLine(strings.TrimSpace(line))), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    workerWorkDir,
		workerTimeout:    5 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	for _, want := range []string{"conflict-a", "conflict-b"} {
		if !containsSlice(state.DoneSlices, want) {
			t.Errorf("state.DoneSlices = %v, want it to contain %q (the worker itself succeeded regardless of integration outcome)", state.DoneSlices, want)
		}
	}

	if !strings.Contains(state.WorkerFindings, "conflict-a: done, integrated") {
		t.Errorf("state.WorkerFindings = %q, want conflict-a reported as cleanly integrated", state.WorkerFindings)
	}
	if !strings.Contains(state.WorkerFindings, "conflict-b: done, but integration conflicted") {
		t.Errorf("state.WorkerFindings = %q, want conflict-b reported as conflicted", state.WorkerFindings)
	}
	if !strings.Contains(state.WorkerFindings, "orchestrator-worker/conflict-b") {
		t.Errorf("state.WorkerFindings = %q, want it to name conflict-b's own branch", state.WorkerFindings)
	}
	// The manual-resolution guidance itself must be the real
	// conflict-resolve-cherry-pick-prompt.md template's own rendered
	// content -- a distinctive phrase from that file -- not a hand-written
	// string that happens to describe similar steps (issue #2060 review
	// finding).
	if !strings.Contains(state.WorkerFindings, "Reproduce the cherry-pick, resolve the conflicts") {
		t.Errorf("state.WorkerFindings = %q, want it to carry conflict-resolve-cherry-pick-prompt.md's own rendered guidance, not a hand-rolled string", state.WorkerFindings)
	}
	if !strings.Contains(state.WorkerFindings, "cherry-pick --continue") {
		t.Errorf("state.WorkerFindings = %q, want the rendered guidance's own cherry-pick --continue step", state.WorkerFindings)
	}
}

// TestDispatchManifestIfPresentSkipsBatchWhoseDependencyFailedToIntegrate
// verifies dispatchManifestIfPresent (issue #2060 review finding): a slice
// scheduled into a LATER batch, whose own DependsOn names a slice from an
// EARLIER batch that finished WorkerDone but whose own branch integration
// failed (never landed on HEAD), must never be dispatched at all -- its
// batch's own workers are created via `git worktree add ... HEAD`
// (workers.go), so dispatching it against a tree still missing its
// dependency's changes would silently produce a worker whose starting point
// doesn't match what its own DependsOn edge promised. repoRoot is dirtied
// up front so integrateSliceBranch's own leading `git status --porcelain`
// guard deterministically fails "done-fast"'s own integration attempt,
// independent of any git internals of the cherry-pick itself.
func TestDispatchManifestIfPresentSkipsBatchWhoseDependencyFailedToIntegrate(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)

	// Dirty repoRoot so integrateSliceBranch's own `git status --porcelain`
	// guard refuses to integrate at all -- deterministically forcing
	// "done-fast"'s own integration outcome to integrateFailed.
	if err := os.WriteFile(filepath.Join(repoRoot, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	// "done-fast" has no FileLeases, so scheduleSlices places it alone in
	// batch 0. "dependent" DependsOn "done-fast", also has no FileLeases, so
	// it lands alone in batch 1 -- exactly the cross-batch dependency shape
	// this test targets.
	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "done-fast", Task: "implement seam a"},
		{Name: "dependent", Task: "implement seam b", DependsOn: []string{"done-fast"}},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine(strings.TrimSpace(line))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    2 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	if strings.Contains(string(calls), "dependent") {
		t.Errorf("callLog = %q, want no invocation for \"dependent\" -- its own dependency's integration never landed on HEAD, so its batch must never be dispatched", calls)
	}
	if !strings.Contains(string(calls), "done-fast") {
		t.Errorf("callLog = %q, want an invocation for \"done-fast\" (its own batch runs regardless of its later integration outcome)", calls)
	}

	if !containsSlice(state.DoneSlices, "done-fast") {
		t.Errorf("state.DoneSlices = %v, want it to contain done-fast (the worker itself succeeded)", state.DoneSlices)
	}
	if containsSlice(state.DoneSlices, "dependent") {
		t.Errorf("state.DoneSlices = %v, want it to NOT contain dependent (it was never dispatched)", state.DoneSlices)
	}
	if containsSlice(state.RemainingSlices, "dependent") {
		t.Errorf("state.RemainingSlices = %v, want it to NOT contain dependent (a skipped-for-later-retry slice is left exactly as-is, not moved into RemainingSlices)", state.RemainingSlices)
	}

	if !strings.Contains(state.WorkerFindings, "dependent") {
		t.Errorf("state.WorkerFindings = %q, want a skip finding naming dependent", state.WorkerFindings)
	}
	if !strings.Contains(state.WorkerFindings, "done-fast") {
		t.Errorf("state.WorkerFindings = %q, want the skip finding to name done-fast as the dependency whose integration didn't land", state.WorkerFindings)
	}
}

// TestDispatchManifestIfPresentReportsIntegrationFailedWithoutLosingDoneSlice
// verifies dispatchManifestIfPresent's `case integrateFailed:` findings arm
// (issue #2060 review finding: no test previously drove a WorkerDone result
// whose integration outcome is integrateFailed through
// dispatchManifestIfPresent). It dirties repoRoot with an untracked file
// before dispatch, deterministically tripping integrateSliceBranch's own
// leading `git status --porcelain` guard (integrate.go) for every
// integration attempt regardless of what the worker itself did -- a
// reliable, non-flaky way to force integrateFailed without depending on any
// git internals of the cherry-pick itself. The worker still reports
// WorkerDone (its own job succeeded), so state.DoneSlices must still list
// it, but state.WorkerFindings must report the integration failure as a
// short outcome line, matching the `integrateConflict` case's shape: the
// underlying error text (which, per integrate.go's dirty-tree guard,
// embeds a full `git status --porcelain` dump that can be arbitrarily
// long) must land in a SEPARATE, indented, truncated block -- never
// appended raw and un-indented onto the one-line outcome, where it could
// blow past maxWorkerResultInFindings and read like additional top-level
// findings lines in the block seeded into the next coordinator prompt
// (issue #2060 review finding: a prompt-injection-shaped bug).
func TestDispatchManifestIfPresentReportsIntegrationFailedWithoutLosingDoneSlice(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)

	// Dirty repoRoot with an untracked file so integrateSliceBranch's own
	// `git status --porcelain` guard refuses to integrate at all --
	// deterministic and independent of the worker's own branch content.
	if err := os.WriteFile(filepath.Join(repoRoot, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "done-fast", Task: "implement seam a"}}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine(strings.TrimSpace(line))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    2 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	if !containsSlice(state.DoneSlices, "done-fast") {
		t.Errorf("state.DoneSlices = %v, want it to contain done-fast (the worker itself succeeded regardless of integration outcome)", state.DoneSlices)
	}

	const wantLine = "- done-fast: done, but integration failed\n"
	idx := strings.Index(state.WorkerFindings, wantLine)
	if idx == -1 {
		t.Fatalf("state.WorkerFindings = %q, want it to contain the short outcome line %q, with no raw error appended inline", state.WorkerFindings, wantLine)
	}

	rest := state.WorkerFindings[idx+len(wantLine):]
	if !strings.HasPrefix(rest, "  ") {
		t.Fatalf("state.WorkerFindings = %q, want a separate indented block immediately after the outcome line, got %q", state.WorkerFindings, rest)
	}
	const wantErrSnippet = "integrate slice done-fast"
	if !strings.Contains(rest, wantErrSnippet) {
		t.Errorf("state.WorkerFindings = %q, want the indented block to contain the underlying error text %q", state.WorkerFindings, wantErrSnippet)
	}
}

// TestDispatchManifestIfPresentSkipsLaterSliceWhoseLeaseOverlapsFailedSlice
// verifies dispatchManifestIfPresent (issue #2060 review finding: Bug A) --
// two slices declaring the SAME FileLeases, and NO DependsOn edge between
// them at all, are sequenced into two separate batches purely by
// scheduleSlices' own lease-disjointness rule (schedule.go). When the
// first slice's own branch integration fails to land on HEAD, the second
// slice -- which never named the first as a dependency -- must still be
// skipped: its overlapping lease makes it very likely to edit the same
// file(s), exactly the case a later worktree built from stale HEAD must not
// silently dispatch against. repoRoot is dirtied up front so
// integrateSliceBranch's own `git status --porcelain` guard deterministically
// fails "done-fast"'s own integration attempt.
func TestDispatchManifestIfPresentSkipsLaterSliceWhoseLeaseOverlapsFailedSlice(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)

	// Dirty repoRoot so integrateSliceBranch's own `git status --porcelain`
	// guard refuses to integrate at all -- deterministically forcing
	// "done-fast"'s own integration outcome to integrateFailed.
	if err := os.WriteFile(filepath.Join(repoRoot, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	// "done-fast" and "late" declare the SAME FileLeases (overlapping) and
	// have no DependsOn edge between them -- scheduleSlices still sequences
	// "late" into a LATER batch than "done-fast" purely because their leases
	// overlap.
	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "done-fast", Task: "implement seam a", FileLeases: []string{"shared.txt"}},
		{Name: "late", Task: "implement seam b", FileLeases: []string{"shared.txt"}},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine(strings.TrimSpace(line))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	workerPromptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(workerPromptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: workerPromptFile,
		workerWorkDir:    t.TempDir(),
		workerTimeout:    2 * time.Second,
	}
	state := runstate.RunState{}

	var stdout strings.Builder
	got, _, _ := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	if strings.Contains(string(calls), "late") {
		t.Errorf("callLog = %q, want no invocation for \"late\" -- its file lease overlaps \"done-fast\", whose own integration never landed on HEAD", calls)
	}
	if !strings.Contains(string(calls), "done-fast") {
		t.Errorf("callLog = %q, want an invocation for \"done-fast\" (its own batch runs regardless of its later integration outcome)", calls)
	}

	if !containsSlice(state.DoneSlices, "done-fast") {
		t.Errorf("state.DoneSlices = %v, want it to contain done-fast (the worker itself succeeded)", state.DoneSlices)
	}
	if containsSlice(state.DoneSlices, "late") {
		t.Errorf("state.DoneSlices = %v, want it to NOT contain late (it was never dispatched)", state.DoneSlices)
	}

	if !strings.Contains(state.WorkerFindings, "late: skipped -- file lease overlaps done-fast, whose own integration did not land on HEAD") {
		t.Errorf("state.WorkerFindings = %q, want a lease-overlap skip finding naming late and done-fast", state.WorkerFindings)
	}
}

// TestDispatchManifestIfPresentSkipsSliceDependingOnPersistedUnlandedSlice
// verifies dispatchManifestIfPresent (issue #2060 review finding: Bug B) --
// a slice name recorded in state.UnlandedSlices by an EARLIER pass (its own
// branch integration ended in conflict/failure, and it's already filtered
// out of any future manifest by state.DoneSlices) still gates a brand-new
// manifest's slice that DependsOn it, even though the local notLanded map
// starts empty on every call: dispatchManifestIfPresent must seed it from
// state.UnlandedSlices up front. LaunchWorkers must never run at all, since
// the manifest's only slice is skipped before ever being batched.
func TestDispatchManifestIfPresentSkipsSliceDependingOnPersistedUnlandedSlice(t *testing.T) {
	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "dependent", Task: "implement seam b", DependsOn: []string{"long-gone"}},
	}}
	line, err := manifest.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine(strings.TrimSpace(line))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	workerWorkDir := filepath.Join(dir, "workdir-that-should-not-be-created")
	cfg := config{
		driver:           "claude",
		logPath:          logPath,
		workerPromptFile: filepath.Join(dir, "worker-prompt.txt"),
		workerWorkDir:    workerWorkDir,
		workerTimeout:    2 * time.Second,
	}
	// "long-gone" was WorkerDone in an earlier pass, so it's already in
	// state.DoneSlices (and therefore filtered out of any future manifest's
	// own dispatchSlices before ever reaching the batch loop) -- but its own
	// branch integration ended in conflict/failure, recorded only in
	// state.UnlandedSlices, since a filtered-out DoneSlice's ManifestSlice
	// (and therefore its FileLeases) is never seen by this pass again.
	state := runstate.RunState{
		DoneSlices:     []string{"long-gone"},
		UnlandedSlices: []string{"long-gone"},
	}

	got, _, _ := dispatchManifestIfPresent(cfg, &state, io.Discard)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	if containsSlice(state.DoneSlices, "dependent") {
		t.Errorf("state.DoneSlices = %v, want it to NOT contain dependent (it was never dispatched)", state.DoneSlices)
	}
	if !strings.Contains(state.WorkerFindings, "dependent: skipped -- depends on long-gone, whose own integration did not land on HEAD") {
		t.Errorf("state.WorkerFindings = %q, want a skip finding naming dependent and long-gone", state.WorkerFindings)
	}
	if _, err := os.Stat(workerWorkDir); !os.IsNotExist(err) {
		t.Errorf("workerWorkDir exists (stat err = %v), want it never created -- LaunchWorkers must never run when the only manifest slice is skipped for depending on a persisted not-landed slice", err)
	}
}
