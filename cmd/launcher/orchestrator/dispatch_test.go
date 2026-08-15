package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestDispatchManifestIfPresentNoopWhenWorkerPromptFileUnset verifies
// dispatchManifestIfPresent is a no-op -- returning false and leaving state
// untouched -- when cfg.workerPromptFile is unset, matching every other
// "empty disables this feature" field on config (issue #2059).
func TestDispatchManifestIfPresentNoopWhenWorkerPromptFileUnset(t *testing.T) {
	cfg := config{workerPromptFile: "", logPath: filepath.Join(t.TempDir(), "nonexistent.log")}
	state := RunState{}
	want := RunState{}

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
	state := RunState{}
	want := RunState{}

	got := dispatchManifestIfPresent(cfg, &state, io.Discard)
	if got {
		t.Errorf("dispatchManifestIfPresent() = true, want false")
	}
	if !reflect.DeepEqual(state, want) {
		t.Errorf("state = %+v, want untouched %+v", state, want)
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
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	coordCountFile := filepath.Join(dir, "coord-count")
	workerWorkDir := filepath.Join(dir, "worker-work-dir")
	if err := os.MkdirAll(workerWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKER_WORK_DIR", workerWorkDir)
	t.Setenv("COORD_COUNT_FILE", coordCountFile)

	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "slice-a"}}}
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

// TestDispatchManifestIfPresentDispatchesAndMergesResults verifies
// dispatchManifestIfPresent, when the pass's own log carries a manifest,
// dispatches LaunchWorkers and merges every WorkerResult into state: done
// slices into state.DoneSlices, timed-out/crashed slices into
// state.RemainingSlices, and a per-slice summary into state.WorkerFindings
// (issue #2059).
func TestDispatchManifestIfPresentDispatchesAndMergesResults(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "done-fast"},
		{Name: "crash-now"},
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
	state := RunState{}

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
