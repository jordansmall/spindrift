package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// chdirToFreshWorkerRepo creates a fresh temp git repo with one commit (so
// HEAD resolves) and chdirs the test into it via t.Chdir (restored
// automatically on test completion) -- launchOneWorker now shells out to
// `git worktree add`/`git worktree remove` against os.Getwd() as the repo
// root (issue #2059 review finding: real per-worker git worktree
// isolation), so every test that reaches that code path needs a real repo
// to exercise against, isolated from the orchestrator package's own
// checkout so a test run never mutates the actual spindrift repo or
// collides with a stray branch/worktree left by a prior run.
func chdirToFreshWorkerRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "worker-test@example.com")
	run("config", "user.name", "Worker Test")
	run("commit", "--allow-empty", "-m", "init")
	t.Chdir(dir)
	return dir
}

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

	seeded, err := seedWorkerPrompt(promptPath, ManifestSlice{Name: "slice-a", Task: "implement seam a"}, "/tmp/slice-a.result", "/tmp/slice-a.done")
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
	// "implement seam a" (slice.Task) proves the worker's own scoped task
	// description reaches its prompt, not just the bare slice name (issue
	// #2059 code-review finding).
	for _, want := range []string{"ORIGINAL WORKER PROMPT", "slice-a", "implement seam a", "/tmp/slice-a.result", "/tmp/slice-a.done"} {
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
	_, err := seedWorkerPrompt("/nonexistent/prompt.txt", ManifestSlice{Name: "slice-a", Task: "implement seam a"}, "/tmp/slice-a.result", "/tmp/slice-a.done")
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
// preamble into $DRIVER_LOG_PATH) to play one of six worker behaviors: a
// "done-fast" slice writes to its log then creates its own sentinel (derived
// from DRIVER_LOG_PATH by replacing ".log" with ".done", matching
// launchOneWorker's own sentinel/log naming convention) and exits 0; a
// "crash-now" slice exits 1 immediately without ever touching a sentinel;
// a "hang-forever" slice sleeps well past any test's own short timeout,
// also without touching a sentinel; an "orphan-check" slice spawns its own
// background child (a loop, standing in for the real claude subprocess
// driver-exec spawns), records that child's pid to
// "<slice>.childpid" (derived the same way as the sentinel path), and then
// itself sleeps well past any test's own short timeout without ever
// touching a sentinel -- letting a test prove a timeout kill reaches the
// whole process group, not just this top-level driver-exec process (issue
// #2059 code-review finding); a "lingers-after-sentinel" slice creates its
// sentinel immediately but then, like "orphan-check", spawns and records a
// background child and sleeps well past the timeout -- letting a test prove
// a worker that already signaled completion still gets its process group
// reaped rather than left running; an "emits-stray-outcome" slice writes a
// bare SPINDRIFT_OUTCOME-shaped line to its own log (standing in for a
// misbehaving or parroting worker that ignores its own prompt's grammar-free
// contract) then, like "done-fast", creates its own sentinel and exits 0 --
// letting a test prove that line, despite genuinely existing on disk in the
// worker's own quarantined log, never reaches the coordinator's own pass log
// and so is never seen by the launcher's outcome scanner or backstop (issue
// #2491 AC2).
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
  orphan-check.log)
    ( while true; do sleep 0.05; done ) &
    echo $! > "${DRIVER_LOG_PATH%.log}.childpid"
    sleep 30
    ;;
  lingers-after-sentinel.log)
    : > "${DRIVER_LOG_PATH%.log}.done"
    ( while true; do sleep 0.05; done ) &
    echo $! > "${DRIVER_LOG_PATH%.log}.childpid"
    sleep 30
    ;;
  emits-stray-outcome.log)
    echo "SPINDRIFT_OUTCOME issue=9999 landing=bogus status=ready note=stray" >> "$DRIVER_LOG_PATH"
    sentinel="${DRIVER_LOG_PATH%.log}.done"
    : > "$sentinel"
    exit 0
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
	chdirToFreshWorkerRepo(t)

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
		{Name: "done-fast", Task: "implement seam a"},
		{Name: "crash-now", Task: "implement seam b"},
		{Name: "hang-forever", Task: "implement seam c"},
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

// TestLaunchOneWorkerKillsOrphanedChildProcessOnTimeout verifies the timeout
// branch of launchOneWorker kills the timed-out worker's entire process
// GROUP, not just the driver-exec process itself (issue #2059 code-review
// finding): driver-exec spawns the real claude/driver subprocess as its own
// child, so killing only the parent leaves that child running -- still free
// to mutate the worker's own git worktree -- even after launchOneWorker has
// already returned WorkerTimedOut. The fake driver here plays the
// "orphan-check" behavior (see writeFakeWorkerDriverExec): it spawns its own
// background child and records that child's pid, then hangs past the
// configured timeout without ever writing a sentinel. Once launchOneWorker
// returns WorkerTimedOut, the recorded child pid must no longer be alive.
func TestLaunchOneWorkerKillsOrphanedChildProcessOnTimeout(t *testing.T) {
	chdirToFreshWorkerRepo(t)

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
	var mu sync.Mutex

	cfg := config{driver: "claude"}
	slice := ManifestSlice{Name: "orphan-check", Task: "implement seam a"}

	result := launchOneWorker(cfg, slice, promptFile, workDir, 80*time.Millisecond, 5*time.Millisecond, &stdout, &mu)

	if result.Status != WorkerTimedOut {
		t.Fatalf("result.Status = %q, want WorkerTimedOut", result.Status)
	}

	childPIDPath := filepath.Join(workDir, "orphan-check.childpid")
	pidBytes, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatalf("ReadFile(childPIDPath): %v -- the fake driver never recorded its child's pid", err)
	}
	childPID := strings.TrimSpace(string(pidBytes))
	if childPID == "" {
		t.Fatal("childpid file was empty")
	}

	// SIGKILL is immediate, but the kernel's own bookkeeping (and this
	// test's own polling) can lag a hair behind -- poll briefly rather than
	// asserting instantaneously.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat("/proc/" + childPID); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %s (spawned by the timed-out worker) is still alive under /proc after launchOneWorker returned -- want the whole process GROUP killed, not just the driver-exec parent", childPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLaunchOneWorkerKillsLingeringProcessAfterSentinelGrace verifies that
// once a worker's own done-sentinel appears but its process doesn't exit
// within workerSentinelGrace, launchOneWorker kills the process's whole
// group before returning -- rather than leaving it running while the
// deferred worktree-removal cleanup runs against its still-live cwd (issue
// #2059 review finding, workers.go:397): the same race
// killWorkerProcessGroup was added to close on the WorkerTimedOut path, but
// left open on this one. The fake driver here writes its sentinel
// immediately, then spawns an orphan child (recording its pid) and hangs --
// well past workerSentinelGrace, but comfortably under the timeout passed
// to launchOneWorker, so the join takes the sentinel-then-grace-expiry path,
// not the WorkerTimedOut path.
func TestLaunchOneWorkerKillsLingeringProcessAfterSentinelGrace(t *testing.T) {
	chdirToFreshWorkerRepo(t)

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
	var mu sync.Mutex

	cfg := config{driver: "claude"}
	slice := ManifestSlice{Name: "lingers-after-sentinel", Task: "implement seam a"}

	result := launchOneWorker(cfg, slice, promptFile, workDir, 30*time.Second, 5*time.Millisecond, &stdout, &mu)

	if result.Status != WorkerDone {
		t.Fatalf("result.Status = %q, want WorkerDone (sentinel is authoritative even though the process lingered)", result.Status)
	}

	childPIDPath := filepath.Join(workDir, "lingers-after-sentinel.childpid")
	pidBytes, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatalf("ReadFile(childPIDPath): %v -- the fake driver never recorded its child's pid", err)
	}
	childPID := strings.TrimSpace(string(pidBytes))
	if childPID == "" {
		t.Fatal("childpid file was empty")
	}

	if _, err := os.Stat("/proc/" + childPID); !os.IsNotExist(err) {
		t.Fatalf("child process %s (spawned by the lingering worker) is still alive under /proc after launchOneWorker returned -- want the whole process GROUP killed once sentinel grace expired, not left running under the worktree-removal cleanup", childPID)
	}
}

// TestKillWorkerProcessGroupWaitsForReapBeforeReturning verifies
// killWorkerProcessGroup does not return until cmd.Wait() has actually
// observed the killed process exit (issue #2059 review finding,
// workers.go:374): the previous inline code sent SIGKILL and fell straight
// through to the caller's own worktree-removal cleanup without ever
// confirming the kernel had finished reaping the process group, so a
// caller could race a not-yet-dead process. SIGKILL reaping is too fast to
// observe reliably through the full LaunchWorkers integration path, so this
// unit-tests killWorkerProcessGroup directly against a real `sleep 5`
// subprocess, using the exact doneCh pattern launchOneWorker itself uses.
func TestKillWorkerProcessGroupWaitsForReapBeforeReturning(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start(): %v", err)
	}

	if cmd.ProcessState != nil {
		t.Fatalf("cmd.ProcessState = %+v, want nil (process still running)", cmd.ProcessState)
	}

	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	killWorkerProcessGroup(cmd, doneCh)

	if cmd.ProcessState == nil {
		t.Fatal("cmd.ProcessState is nil after killWorkerProcessGroup returned -- want it to block until cmd.Wait() completes")
	}
	// ProcessState.Exited() is specifically false for a signal-terminated
	// process (it reports true only for a program that called exit itself)
	// -- so a killed process's own terminal state is confirmed via its
	// String() representation instead ("signal: killed").
	if !strings.Contains(cmd.ProcessState.String(), "killed") {
		t.Errorf("cmd.ProcessState.String() = %q, want it to mention \"killed\"", cmd.ProcessState.String())
	}
}

// TestLaunchWorkersEachWorkerGetsOwnQuarantinedLogAndFreshSession verifies
// LaunchWorkers never forwards the coordinator's own cfg.logPath to a
// worker's driver-exec invocation, and never forwards a non-empty
// --state-file, so a worker structurally cannot pollute the coordinator's
// own log or run-state (issue #2059 AC4/AC6).
func TestLaunchWorkersEachWorkerGetsOwnQuarantinedLogAndFreshSession(t *testing.T) {
	chdirToFreshWorkerRepo(t)

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
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "done-fast", Task: "implement seam a"}}}

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

// TestLaunchWorkersIgnoresStaleSentinelFromPriorRun verifies a leftover
// done-sentinel file already sitting in workDir before a worker is even
// launched (e.g. from a prior run, or another slice that reused the same
// workDir) is never mistaken for the just-started worker's own completion
// signal (issue #2059 review finding, workers.go:238): waitForSentinel must
// only ever observe a sentinel the worker itself wrote after starting. The
// fake worker here never writes a real sentinel at all (it just hangs), so
// a join that's fooled by the stale file would report WorkerDone for a
// worker that never did any real work; the correct outcome is
// WorkerTimedOut once opts.Timeout elapses.
func TestLaunchWorkersIgnoresStaleSentinelFromPriorRun(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	// A worker that hangs well past the test's own timeout without ever
	// touching a real sentinel file.
	writeFakeDriverExec(t, fakeDir, callLog, "sleep 30\n")
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "worker-prompt.txt")
	if err := os.WriteFile(promptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile(promptFile): %v", err)
	}

	workDir := t.TempDir()
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(workDir): %v", err)
	}
	// Plant a stale sentinel before the worker is ever launched.
	stalePath := filepath.Join(workDir, "delayed-worker.done")
	if err := os.WriteFile(stalePath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(stalePath): %v", err)
	}

	var stdout bytes.Buffer
	cfg := config{driver: "claude"}
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "delayed-worker", Task: "implement seam a"}}}

	const workerTimeout = 100 * time.Millisecond
	start := time.Now()
	results := LaunchWorkers(cfg, manifest, WorkerOptions{
		PromptFile:   promptFile,
		WorkDir:      workDir,
		Timeout:      workerTimeout,
		PollInterval: 5 * time.Millisecond,
	}, &stdout)
	elapsed := time.Since(start)

	if elapsed >= time.Second {
		t.Fatalf("LaunchWorkers took %v, want well under 1s (fake worker sleeps 30s)", elapsed)
	}

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Status != WorkerTimedOut {
		t.Errorf("results[0].Status = %q, want WorkerTimedOut -- a stale pre-existing sentinel must never cause WorkerDone to be reported for a worker that hasn't done any real work", results[0].Status)
	}
}

// TestLaunchWorkersRemovesSeededPromptTempFile verifies launchOneWorker
// cleans up the temp file seedWorkerPrompt wrote for it once the worker is
// done, mirroring run.go's own prevSeededPromptFile cleanup convention
// (issue #2059 review finding, workers.go:206) -- otherwise every worker
// dispatched, across every pass, leaks an unbounded "orchestrator-worker-
// prompt-*.txt" file into the OS temp dir.
func TestLaunchWorkersRemovesSeededPromptTempFile(t *testing.T) {
	chdirToFreshWorkerRepo(t)

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

	pattern := filepath.Join(os.TempDir(), "orchestrator-worker-prompt-*.txt")
	before, _ := filepath.Glob(pattern)
	beforeSet := make(map[string]bool, len(before))
	for _, f := range before {
		beforeSet[f] = true
	}

	cfg := config{driver: "claude"}
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "done-fast", Task: "implement seam a"}, {Name: "crash-now", Task: "implement seam b"}}}

	LaunchWorkers(cfg, manifest, WorkerOptions{
		PromptFile:   promptFile,
		WorkDir:      workDir,
		Timeout:      time.Second,
		PollInterval: 5 * time.Millisecond,
	}, &stdout)

	after, _ := filepath.Glob(pattern)
	for _, f := range after {
		if !beforeSet[f] {
			t.Errorf("seeded worker prompt temp file leaked: %s", f)
		}
	}
}

// TestLaunchWorkersResolvesRelativeWorkDirAbsolutely verifies a relative
// opts.WorkDir is resolved to the same absolute location by both sides of
// the join: the orchestrator, which stats resultPath/sentinelPath from its
// own process cwd, and the worker's driver-exec subprocess, which runs with
// cmd.Dir set to its own dedicated worktree -- a different cwd. Before this
// fix, a relative WorkDir made the two sides resolve the identical path
// string to two different files, so the orchestrator would poll forever
// against a sentinel the worker had actually written elsewhere, silently
// burning the full timeout (issue #2059 review finding, workers.go:248).
func TestLaunchWorkersResolvesRelativeWorkDirAbsolutely(t *testing.T) {
	repoDir := chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	body := `: > "$DRIVER_LOG_PATH"
: > "${DRIVER_LOG_PATH%.log}.done"
`
	writeFakeDriverExec(t, fakeDir, callLog, body)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "worker-prompt.txt")
	if err := os.WriteFile(promptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile(promptFile): %v", err)
	}

	var stdout bytes.Buffer
	cfg := config{driver: "claude"}
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "relative-slice", Task: "implement seam a"}}}

	results := LaunchWorkers(cfg, manifest, WorkerOptions{
		PromptFile:   promptFile,
		WorkDir:      "relative-workdir",
		Timeout:      2 * time.Second,
		PollInterval: 5 * time.Millisecond,
	}, &stdout)

	if len(results) != 1 || results[0].Status != WorkerDone {
		t.Fatalf("results = %+v, want a single WorkerDone result", results)
	}

	wantLogFile := filepath.Join(repoDir, "relative-workdir", "relative-slice.log")
	if _, err := os.Stat(wantLogFile); err != nil {
		t.Errorf("Stat(%s): %v -- want the relative WorkDir resolved against the orchestrator's own cwd (%s)", wantLogFile, err, repoDir)
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
	manifest := SliceManifest{Slices: []ManifestSlice{{Name: "slice-a", Task: "implement seam a"}, {Name: "slice-b", Task: "implement seam b"}}}

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

	// AC5: worker_start/worker_finish are still logged for every slice even
	// though none of them ever got a chance to launch (issue #2059 review
	// finding) -- WorkDir failing to create must not silently skip the
	// heartbeat stream.
	out := stdout.String()
	for _, want := range []string{"worker_start", "worker_finish", "slice-a", "slice-b"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
	if got := strings.Count(out, "worker_start"); got != 2 {
		t.Errorf("worker_start count = %d, want 2", got)
	}
	if got := strings.Count(out, "worker_finish"); got != 2 {
		t.Errorf("worker_finish count = %d, want 2", got)
	}
}

// TestLaunchOneWorkerEmitsStartAndFinishOpsOnBuildDriverExecCmdFailure
// verifies launchOneWorker emits both worker_start and worker_finish
// SpindriftOp lines even when the worker never actually launches --
// buildDriverExecCmd fails here because "driver-exec" isn't on PATH -- so
// AC5's "worker start/finish/timeout events are logged" holds for every
// early-return failure path, not just the success/crash/timeout paths
// reached after cmd.Start() succeeds (issue #2059 review finding).
func TestLaunchOneWorkerEmitsStartAndFinishOpsOnBuildDriverExecCmdFailure(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	emptyPathDir := t.TempDir()
	t.Setenv("PATH", emptyPathDir)

	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "worker-prompt.txt")
	if err := os.WriteFile(promptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile(promptFile): %v", err)
	}

	workDir := t.TempDir()
	var stdout bytes.Buffer
	var mu sync.Mutex

	cfg := config{driver: "claude"}
	slice := ManifestSlice{Name: "no-driver-exec", Task: "implement seam a"}

	result := launchOneWorker(cfg, slice, promptFile, workDir, time.Second, 5*time.Millisecond, &stdout, &mu)

	if result.Status != WorkerCrashed {
		t.Errorf("result.Status = %q, want WorkerCrashed", result.Status)
	}
	if result.Err == nil {
		t.Error("result.Err = nil, want non-nil (driver-exec not on PATH)")
	}

	out := stdout.String()
	for _, want := range []string{"worker_start", "worker_finish", "no-driver-exec"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q for a launch failure before cmd.Start(); got:\n%s", want, out)
		}
	}
}

// TestLaunchOneWorkerRunsInDedicatedGitWorktree verifies launchOneWorker's
// core isolation contract (issue #2059 code-review finding): the worker's
// driver-exec subprocess runs inside a dedicated `git worktree add`
// checkout under opts.WorkDir, not the orchestrator's own repo root, so
// concurrently-dispatched workers never share (and mutate) one working
// tree -- the promise worker-prompt.md already makes but the Go code never
// kept. Once the worker finishes, the worktree directory itself is removed
// but its branch (orchestrator-worker/<slice>) survives for inspection.
func TestLaunchOneWorkerRunsInDedicatedGitWorktree(t *testing.T) {
	repoDir := chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	// Records its own $PWD (the worktree launchOneWorker set as cmd.Dir,
	// if the fix is in place -- the repo root otherwise) alongside the
	// quarantined log, then signals done.
	body := `pwd > "${DRIVER_LOG_PATH%.log}.pwd"
: > "${DRIVER_LOG_PATH%.log}.done"
`
	writeFakeDriverExec(t, fakeDir, callLog, body)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "worker-prompt.txt")
	if err := os.WriteFile(promptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile(promptFile): %v", err)
	}

	workDir := t.TempDir()
	var stdout bytes.Buffer
	var mu sync.Mutex

	cfg := config{driver: "claude"}
	slice := ManifestSlice{Name: "worker-a", Task: "implement seam a"}

	result := launchOneWorker(cfg, slice, promptFile, workDir, time.Second, 5*time.Millisecond, &stdout, &mu)

	if result.Status != WorkerDone {
		t.Fatalf("result.Status = %q, want WorkerDone (err=%v)", result.Status, result.Err)
	}

	wantWorktree := filepath.Join(workDir, "worker-a.worktree")
	pwdBytes, err := os.ReadFile(filepath.Join(workDir, "worker-a.pwd"))
	if err != nil {
		t.Fatalf("ReadFile(worker-a.pwd): %v", err)
	}
	gotPWD := strings.TrimSpace(string(pwdBytes))
	wantResolved, err := filepath.EvalSymlinks(wantWorktree)
	if err != nil {
		wantResolved = wantWorktree
	}
	if gotPWD != wantWorktree && gotPWD != wantResolved {
		t.Errorf("fake driver-exec ran with PWD = %q, want the dedicated worker worktree %q (not the repo root %q)", gotPWD, wantWorktree, repoDir)
	}

	// (a) the worktree directory existed under workDir during dispatch --
	// already proven above (the fake driver-exec wrote its .pwd file from
	// inside it) -- and (c) it's gone again once the worker has finished.
	if _, err := os.Stat(wantWorktree); !os.IsNotExist(err) {
		t.Errorf("worktree dir %s still exists after launchOneWorker returned (stat err=%v), want removed", wantWorktree, err)
	}

	listCmd := exec.Command("git", "worktree", "list")
	listCmd.Dir = repoDir
	listOut, err := listCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v\n%s", err, listOut)
	}
	if strings.Contains(string(listOut), "worker-a.worktree") {
		t.Errorf("git worktree list still shows worker-a.worktree:\n%s", listOut)
	}

	// The branch itself must survive removal -- left around for a human/
	// reviewer to inspect after a crash.
	branchCmd := exec.Command("git", "branch", "--list", "orchestrator-worker/worker-a")
	branchCmd.Dir = repoDir
	branchOut, err := branchCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list: %v\n%s", err, branchOut)
	}
	if !strings.Contains(string(branchOut), "orchestrator-worker/worker-a") {
		t.Errorf("git branch --list orchestrator-worker/worker-a = %q, want the branch to still exist", string(branchOut))
	}
}

// TestLaunchOneWorkerEmitsStartAndFinishOpsOnWorktreeAddFailure verifies
// launchOneWorker treats a `git worktree add` failure (e.g. a leftover
// worktree destination from a stale prior run) exactly like the other
// early-failure paths: worker_start/worker_finish are both still emitted,
// the result is WorkerCrashed with a non-nil Err, and driver-exec is never
// invoked at all (issue #2059 code-review finding). This forces the
// failure via a blocking file already sitting at the worktree's own
// destination path, rather than a pre-existing branch -- launchOneWorker
// deliberately tolerates (reuses) an already-existing branch of the same
// name, since a coordinator legitimately re-dispatching the same slice
// name across passes must keep working, not crash the second time around.
func TestLaunchOneWorkerEmitsStartAndFinishOpsOnWorktreeAddFailure(t *testing.T) {
	chdirToFreshWorkerRepo(t)

	// driver-exec deliberately absent from PATH: if launchOneWorker ever
	// reached buildDriverExecCmd, this test would pass for the wrong
	// reason (LookPath failure), masking a regression where the worktree
	// failure no longer short-circuits before it.
	t.Setenv("PATH", t.TempDir())

	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "worker-prompt.txt")
	if err := os.WriteFile(promptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile(promptFile): %v", err)
	}

	workDir := t.TempDir()
	// Pre-create the worktree's own destination path as a regular file --
	// `git worktree add` refuses to check out onto an existing non-empty
	// path, exactly the class of failure a leftover directory from a
	// stale/crashed prior run would produce.
	blockedWorktreePath := filepath.Join(workDir, "blocked-worktree.worktree")
	if err := os.WriteFile(blockedWorktreePath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(blockedWorktreePath): %v", err)
	}

	var stdout bytes.Buffer
	var mu sync.Mutex

	cfg := config{driver: "claude"}
	slice := ManifestSlice{Name: "blocked-worktree", Task: "implement seam a"}

	result := launchOneWorker(cfg, slice, promptFile, workDir, time.Second, 5*time.Millisecond, &stdout, &mu)

	if result.Status != WorkerCrashed {
		t.Errorf("result.Status = %q, want WorkerCrashed", result.Status)
	}
	if result.Err == nil {
		t.Error("result.Err = nil, want non-nil (git worktree add should fail: destination path already exists)")
	}

	out := stdout.String()
	for _, want := range []string{"worker_start", "worker_finish", "blocked-worktree"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

// TestLaunchOneWorkerReusesExistingWorktreeBranchAcrossDispatches verifies
// a second launchOneWorker call for the very same slice name -- e.g. a
// coordinator re-dispatching the same slice on a later pass -- succeeds by
// reusing the branch name the first dispatch's own worktree left behind
// (workers.go never deletes the branch itself, only the worktree
// directory), rather than failing the second time around because `git
// worktree add -b` refuses to recreate an already-existing branch name
// (issue #2059 code-review finding). It also pins that the reused branch is
// reset to the orchestrator's own current HEAD on every dispatch, not left
// at whatever commit the prior dispatch's own worktree tip happened to
// reach -- a retried slice must always start fresh off orchestrator HEAD
// (issue #2059 review finding, workers.go:299).
func TestLaunchOneWorkerReusesExistingWorktreeBranchAcrossDispatches(t *testing.T) {
	repoDir := chdirToFreshWorkerRepo(t)
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	body := `: > "$DRIVER_LOG_PATH"
: > "${DRIVER_LOG_PATH%.log}.done"
`
	writeFakeDriverExec(t, fakeDir, callLog, body)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "worker-prompt.txt")
	if err := os.WriteFile(promptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile(promptFile): %v", err)
	}

	workDir := t.TempDir()
	var mu sync.Mutex
	cfg := config{driver: "claude"}
	slice := ManifestSlice{Name: "repeat-slice", Task: "implement seam a"}
	branchRef := "refs/heads/orchestrator-worker/repeat-slice"

	for i := 0; i < 2; i++ {
		if i == 1 {
			// Advance orchestrator HEAD between dispatches, simulating a
			// retry after other work landed -- the second dispatch must
			// rebase the branch onto this new tip, not the first
			// dispatch's now-stale one.
			runGit("commit", "--allow-empty", "-m", "advance orchestrator HEAD")
		}
		wantHEAD := runGit("rev-parse", "HEAD")

		var stdout bytes.Buffer
		result := launchOneWorker(cfg, slice, promptFile, workDir, time.Second, 5*time.Millisecond, &stdout, &mu)
		if result.Status != WorkerDone {
			t.Fatalf("dispatch %d: result.Status = %q, want WorkerDone (err=%v)", i, result.Status, result.Err)
		}

		gotBranchTip := runGit("rev-parse", branchRef)
		if gotBranchTip != wantHEAD {
			t.Errorf("dispatch %d: branch tip = %s, want %s (orchestrator HEAD)", i, gotBranchTip, wantHEAD)
		}
	}
}
