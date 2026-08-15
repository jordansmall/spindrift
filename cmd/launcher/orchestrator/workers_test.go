package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSeedWorkerPromptComposesAddendumOverOriginal verifies seedWorkerPrompt
// writes a fresh temp file carrying the original prompt content plus an
// addendum naming the slice and the result/sentinel paths, leaving the
// original prompt file untouched (issue #2059).
func TestSeedWorkerPromptComposesAddendumOverOriginal(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte("ORIGINAL WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	seeded, err := seedWorkerPrompt(promptPath, ManifestSlice{Name: "slice-a"}, "/tmp/slice-a.result", "/tmp/slice-a.done")
	if err != nil {
		t.Fatalf("seedWorkerPrompt() error = %v", err)
	}

	if seeded == promptPath {
		t.Fatalf("seedWorkerPrompt() returned original path %q, want a fresh temp file", promptPath)
	}

	seededContent, err := os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("ReadFile(seeded): %v", err)
	}
	got := string(seededContent)
	for _, want := range []string{"ORIGINAL WORKER PROMPT", "slice-a", "/tmp/slice-a.result", "/tmp/slice-a.done"} {
		if !strings.Contains(got, want) {
			t.Errorf("seeded prompt missing substring %q; got:\n%s", want, got)
		}
	}

	originalContent, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("ReadFile(original): %v", err)
	}
	if string(originalContent) != "ORIGINAL WORKER PROMPT" {
		t.Errorf("original prompt file mutated: got %q", string(originalContent))
	}
}

// TestSeedWorkerPromptErrorsOnMissingPromptFile verifies seedWorkerPrompt
// surfaces a non-nil error when promptFile can't be read (issue #2059).
func TestSeedWorkerPromptErrorsOnMissingPromptFile(t *testing.T) {
	_, err := seedWorkerPrompt("/nonexistent/prompt.txt", ManifestSlice{Name: "slice-a"}, "/tmp/slice-a.result", "/tmp/slice-a.done")
	if err == nil {
		t.Fatal("seedWorkerPrompt() error = nil, want non-nil")
	}
}

// TestWaitForSentinelReturnsTrueWhenFileAppears verifies waitForSentinel
// notices a sentinel file that appears mid-poll, returning well before the
// context's own generous deadline (issue #2059 AC2/AC3).
func TestWaitForSentinelReturnsTrueWhenFileAppears(t *testing.T) {
	dir := t.TempDir()
	sentinelPath := filepath.Join(dir, "slice-a.done")

	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = os.WriteFile(sentinelPath, []byte(""), 0o644)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	ok := waitForSentinel(ctx, sentinelPath, 5*time.Millisecond)
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("waitForSentinel() = false, want true")
	}
	if elapsed >= 200*time.Millisecond {
		t.Errorf("waitForSentinel() took %v, want well under the 500ms context deadline", elapsed)
	}
}

// TestWaitForSentinelReturnsFalseOnContextTimeout verifies waitForSentinel
// gives up promptly when ctx expires before the sentinel ever appears
// (issue #2059 AC2), rather than busy-looping or ignoring ctx.
func TestWaitForSentinelReturnsFalseOnContextTimeout(t *testing.T) {
	dir := t.TempDir()
	sentinelPath := filepath.Join(dir, "slice-a.done")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	ok := waitForSentinel(ctx, sentinelPath, 5*time.Millisecond)
	elapsed := time.Since(start)

	if ok {
		t.Fatal("waitForSentinel() = true, want false")
	}
	if elapsed > 2*time.Second {
		t.Errorf("waitForSentinel() took %v, want close to the 30ms context deadline", elapsed)
	}
}

// TestWaitForSentinelReturnsTrueImmediatelyWhenFileAlreadyExists verifies
// waitForSentinel checks immediately before ever sleeping, so a
// pre-existing sentinel returns true without waiting out a full
// pollInterval (issue #2059 AC2/AC3).
func TestWaitForSentinelReturnsTrueImmediatelyWhenFileAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	sentinelPath := filepath.Join(dir, "slice-a.done")
	if err := os.WriteFile(sentinelPath, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	ok := waitForSentinel(ctx, sentinelPath, 200*time.Millisecond)
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("waitForSentinel() = false, want true")
	}
	if elapsed >= 200*time.Millisecond {
		t.Errorf("waitForSentinel() took %v, want under the 200ms pollInterval", elapsed)
	}
}

// writeFakeWorkerDriverExec writes a fake driver-exec that branches on the
// basename of its own --log-path flag (derived by writeFakeDriverExec's
// preamble into $DRIVER_LOG_PATH) to play one of three worker behaviors: a
// "done-fast" slice writes to its log then creates its own sentinel (derived
// from DRIVER_LOG_PATH by replacing ".log" with ".done", matching
// launchOneWorker's own sentinel/log naming convention) and exits 0; a
// "crash-now" slice exits 1 immediately without ever touching a sentinel;
// a "hang-forever" slice sleeps well past any test's own short timeout,
// also without touching a sentinel.
func writeFakeWorkerDriverExec(t *testing.T, dir, callLog string) string {
	t.Helper()
	body := `echo "worker log" > "$DRIVER_LOG_PATH"
base=$(basename "$DRIVER_LOG_PATH")
case "$base" in
  done-fast.log)
    sentinel="${DRIVER_LOG_PATH%.log}.done"
    : > "$sentinel"
    exit 0
    ;;
  crash-now.log)
    exit 1
    ;;
  hang-forever.log)
    sleep 30
    ;;
esac
`
	return writeFakeDriverExec(t, dir, callLog, body)
}

// TestLaunchWorkersJoinsDoneCrashedAndTimedOutDeterministically verifies
// LaunchWorkers' core join contract (issue #2059 AC2): a done worker, a
// worker whose process crashes before ever writing a sentinel, and a worker
// that hangs past its own timeout, are all joined deterministically and
// bounded well under their own hang duration -- and every worker gets its
// own quarantined --log-path, distinct from any other worker's.
func TestLaunchWorkersJoinsDoneCrashedAndTimedOutDeterministically(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "worker-prompt.txt")
	if err := os.WriteFile(promptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile(promptFile): %v", err)
	}

	workDir := t.TempDir()
	var stdout bytes.Buffer

	cfg := config{driver: "claude"}
	manifest := SliceManifest{Slices: []ManifestSlice{
		{Name: "done-fast"},
		{Name: "crash-now"},
		{Name: "hang-forever"},
	}}

	start := time.Now()
	results := LaunchWorkers(cfg, manifest, WorkerOptions{
		PromptFile:   promptFile,
		WorkDir:      workDir,
		Timeout:      80 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	}, &stdout)
	elapsed := time.Since(start)

	if elapsed >= time.Second {
		t.Fatalf("LaunchWorkers took %v, want well under 1s (hang-forever sleeps 30s)", elapsed)
	}

	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}

	if results[0].Slice != "done-fast" || results[0].Status != WorkerDone || results[0].ExitCode != 0 {
		t.Errorf("results[0] = %+v, want done-fast/WorkerDone/exit 0", results[0])
	}
	if results[1].Slice != "crash-now" || results[1].Status != WorkerCrashed {
		t.Errorf("results[1] = %+v, want crash-now/WorkerCrashed", results[1])
	}
	if results[2].Slice != "hang-forever" || results[2].Status != WorkerTimedOut {
		t.Errorf("results[2] = %+v, want hang-forever/WorkerTimedOut", results[2])
	}

	out := stdout.String()
	for _, want := range []string{"done-fast", "crash-now", "hang-forever", "worker_start", "worker_finish"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}

	for _, slice := range []string{"done-fast", "crash-now", "hang-forever"} {
		logPath := filepath.Join(workDir, slice+".log")
		if _, err := os.Stat(logPath); err != nil {
			t.Errorf("expected quarantined log file %s to exist: %v", logPath, err)
		}
	}
}

// TestLaunchWorkersEachWorkerGetsOwnQuarantinedLogAndFreshSession verifies
// LaunchWorkers never forwards the coordinator's own cfg.logPath to a
// worker's driver-exec invocation, and never forwards a non-empty
// --state-file, so a worker structurally cannot pollute the coordinator's
// own log or run-state (issue #2059 AC4/AC6).
func TestLaunchWorkersEachWorkerGetsOwnQuarantinedLogAndFreshSession(t *testing.T) {
	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	writeFakeWorkerDriverExec(t, fakeDir, callLog)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "worker-prompt.txt")
	if err := os.WriteFile(promptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile(promptFile): %v", err)
	}

	coordinatorDir := t.TempDir()
	coordinatorLogPath := filepath.Join(coordinatorDir, "coordinator.log")
	coordinatorStateFile := filepath.Join(coordinatorDir, "run-state.json")

	workDir := t.TempDir()
	var stdout bytes.Buffer

	cfg := config{
		driver:    "claude",
		logPath:   coordinatorLogPath,
		stateFile: coordinatorStateFile,
	}
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "done-fast"}}}

	LaunchWorkers(cfg, manifest, WorkerOptions{
		PromptFile:   promptFile,
		WorkDir:      workDir,
		Timeout:      time.Second,
		PollInterval: 5 * time.Millisecond,
	}, &stdout)

	callBytes, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("ReadFile(callLog): %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(callBytes), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("callLog has %d lines, want 1: %q", len(lines), string(callBytes))
	}

	if got := flagValue(lines[0], "--log-path"); got == coordinatorLogPath {
		t.Errorf("worker --log-path = %q, want anything other than the coordinator's own cfg.logPath", got)
	}
	if !strings.Contains(lines[0], filepath.Join(workDir, "done-fast.log")) {
		t.Errorf("worker argv = %q, want it to carry the quarantined log path under WorkDir", lines[0])
	}
	if strings.Contains(lines[0], "--state-file") {
		t.Errorf("worker argv = %q, want no --state-file flag forwarded at all", lines[0])
	}
}

// TestLaunchWorkersReturnsOneResultPerSliceEvenWhenWorkDirUncreatable
// verifies LaunchWorkers degrades to one WorkerCrashed result per slice,
// each carrying a non-nil Err, rather than panicking, when opts.WorkDir
// can't be created (issue #2059 AC2: every slice always gets exactly one
// WorkerResult).
func TestLaunchWorkersReturnsOneResultPerSliceEvenWhenWorkDirUncreatable(t *testing.T) {
	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(blockingFile): %v", err)
	}
	uncreatableWorkDir := filepath.Join(blockingFile, "sub")

	promptFile := filepath.Join(dir, "worker-prompt.txt")
	if err := os.WriteFile(promptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile(promptFile): %v", err)
	}

	var stdout bytes.Buffer
	cfg := config{driver: "claude"}
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "slice-a"}, {Name: "slice-b"}}}

	results := LaunchWorkers(cfg, manifest, WorkerOptions{
		PromptFile: promptFile,
		WorkDir:    uncreatableWorkDir,
	}, &stdout)

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	for i, r := range results {
		if r.Status != WorkerCrashed {
			t.Errorf("results[%d].Status = %q, want WorkerCrashed", i, r.Status)
		}
		if r.Err == nil {
			t.Errorf("results[%d].Err = nil, want non-nil", i)
		}
	}
}
