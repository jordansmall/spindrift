package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runstate"
)

// TestDispatchManifestIfPresentNoopWhenWorkerPromptFileUnset verifies
// dispatchManifestIfPresent is a no-op -- returning false and leaving state
// untouched -- when cfg.workerPromptFile is unset, matching every other
// "empty disables this feature" field on config (issue #2059).
func TestDispatchManifestIfPresentNoopWhenWorkerPromptFileUnset(t *testing.T) {
	cfg := config{workerPromptFile: "", logPath: filepath.Join(t.TempDir(), "nonexistent.log")}
	state := runstate.RunState{}
	want := runstate.RunState{}

	got := dispatchManifestIfPresent(cfg, &state, io.Discard)
	if got {
		t.Errorf("dispatchManifestIfPresent() = true, want false")
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

	got := dispatchManifestIfPresent(cfg, &state, io.Discard)
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
	if got := dispatchManifestIfPresent(cfg, &state, &stdout); !got {
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

	got := dispatchManifestIfPresent(cfg, &state, &stdout)
	if got {
		t.Errorf("dispatchManifestIfPresent() = true, want false (no manifest in second pass's log)")
	}
	if state.WorkerFindings != "" {
		t.Errorf("state.WorkerFindings = %q after a no-manifest pass, want empty (stale findings must not survive a pass that dispatched nothing)", state.WorkerFindings)
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
	got := dispatchManifestIfPresent(cfg, &state, &stdout)
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
	if got := dispatchManifestIfPresent(cfg, &state, &stdout); !got {
		t.Fatalf("dispatchManifestIfPresent() (first call) = false, want true")
	}
	if got := dispatchManifestIfPresent(cfg, &state, &stdout); !got {
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
	got := dispatchManifestIfPresent(cfg, &state, &stdout)
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
	got := dispatchManifestIfPresent(cfg, &state, &stdout)
	if !got {
		t.Fatalf("dispatchManifestIfPresent() = false, want true")
	}

	want := "REPORT: slice-a implemented the parser fix."
	if !strings.Contains(state.WorkerFindings, want) {
		t.Errorf("state.WorkerFindings = %q, want it to contain the worker's own reported result %q", state.WorkerFindings, want)
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
	got := dispatchManifestIfPresent(cfg, &state, &stdout)
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
	got := dispatchManifestIfPresent(cfg, &state, &stdout)
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
