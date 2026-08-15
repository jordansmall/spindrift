package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/driver/claude"
)

// WorkerStatus is the terminal state the orchestrator's own join code
// assigns to one worker's dispatch (issue #2059) -- never inferred from
// model memory: a worker is Done only once its own done-sentinel file is
// observed, TimedOut when the per-worker timeout elapses first, and Crashed
// when its process exits before ever writing a sentinel.
type WorkerStatus string

const (
	WorkerDone     WorkerStatus = "done"
	WorkerTimedOut WorkerStatus = "timed_out"
	WorkerCrashed  WorkerStatus = "crashed"
)

// WorkerResult is one worker's outcome from a parallel dispatch: its slice
// name, the status the join assigned it, its process exit code (meaningful
// only when the process actually exited -- always 0 on WorkerTimedOut,
// since the process is still running or was just killed), the contents of
// its own result file if it wrote one before finishing, and any
// orchestrator-side error encountered launching or joining it (distinct
// from the worker's own process exit code).
type WorkerResult struct {
	Slice    string
	Status   WorkerStatus
	ExitCode int
	Result   string
	Err      error
}

// defaultWorkerTimeout bounds a worker's join when no explicit timeout is
// configured -- AC2 requires every worker to have SOME bound, so unlike
// maxReviewRounds/maxSlices (where zero legitimately means "no cap"), a
// worker join is never allowed to wait forever.
const defaultWorkerTimeout = 20 * time.Minute

// seedWorkerPrompt composes a fresh prompt file for one worker: promptFile's
// own content, with a "## Parallel worker dispatch" section prepended
// naming slice.Name and instructing the worker to write its final report to
// resultPath and then create sentinelPath as its very last action -- the
// deterministic, file-based completion signal the orchestrator's join polls
// for (issue #2059 AC3/AC4), replacing the "return your report as your
// final message" contract a subagent invoked via the Agent tool would use.
// Mirrors seedPromptFromState's own temp-file composition
// (run.go:423-475): reads promptFile, writes the composed text to a fresh
// os.CreateTemp file, and returns its path. Returns an error if promptFile
// can't be read or the temp file can't be created/written.
func seedWorkerPrompt(promptFile string, slice ManifestSlice, resultPath, sentinelPath string) (string, error) {
	original, err := os.ReadFile(promptFile)
	if err != nil {
		return "", fmt.Errorf("seed worker prompt: %w", err)
	}

	var b strings.Builder
	b.WriteString("## Parallel worker dispatch\n\n")
	fmt.Fprintf(&b, "You are the worker for slice %q, running fully concurrently\n", slice.Name)
	b.WriteString("alongside other workers on other slices of the same manifest -- never\n")
	b.WriteString("touch the orchestrator's own run-state file.\n\n")
	fmt.Fprintf(&b, "When you finish, write your final report to %s, then\n", resultPath)
	fmt.Fprintf(&b, "create %s as your very last action -- this is the only\n", sentinelPath)
	b.WriteString("signal the orchestrator's join waits for.\n")
	b.WriteString("\n---\n\n")
	b.Write(original)

	f, err := os.CreateTemp("", "orchestrator-worker-prompt-*.txt")
	if err != nil {
		return "", fmt.Errorf("seed worker prompt: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return "", fmt.Errorf("seed worker prompt: %w", err)
	}
	return f.Name(), nil
}

// waitForSentinel polls for path to exist, checking every pollInterval,
// until it appears (returns true) or ctx is done first (returns false) --
// the bounded-wait primitive behind a worker's per-worker timeout (issue
// #2059 AC2). Never busy-loops: each iteration either finds the file or
// sleeps pollInterval (via a timer that also respects ctx.Done(), so a
// cancelled ctx returns promptly rather than waiting out a long
// pollInterval first).
func waitForSentinel(ctx context.Context, path string, pollInterval time.Duration) bool {
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

// defaultWorkerPollInterval is LaunchWorkers' own default sentinel-poll
// cadence when WorkerOptions.PollInterval is unset.
const defaultWorkerPollInterval = 500 * time.Millisecond

// workerSentinelGrace is how long LaunchWorkers waits for a worker process
// to actually exit after its done-sentinel appears, before giving up on
// capturing its exit code -- the sentinel write and the process's own exit
// are not perfectly synchronized, but the sentinel itself is the
// authoritative completion signal (issue #2059 AC3), so a process that
// lingers past this grace period is still reported WorkerDone.
const workerSentinelGrace = 2 * time.Second

// WorkerOptions configures LaunchWorkers. Every path here is independent of
// the coordinator's own cfg.logPath/cfg.stateFile, so a worker structurally
// cannot write either (issue #2059 AC4/AC6).
type WorkerOptions struct {
	// PromptFile is the base worker prompt seedWorkerPrompt composes each
	// slice's own addendum onto.
	PromptFile string
	// WorkDir holds every worker's own quarantined log, heartbeat log,
	// result, and done-sentinel files, namespaced by slice name -- never
	// cfg.logPath/cfg.heartbeatLog, so a worker's raw driver stream can
	// never reach the log the orchestrator's own loop scans for outcome/
	// verdict markers (AC6), and concurrent workers never race on the same
	// file.
	WorkDir string
	// Timeout bounds each worker's own join; <= 0 falls back to
	// defaultWorkerTimeout (AC2: a worker join is never allowed to wait
	// forever).
	Timeout time.Duration
	// PollInterval is how often LaunchWorkers checks each worker's
	// sentinel; <= 0 falls back to defaultWorkerPollInterval.
	PollInterval time.Duration
}

// LaunchWorkers dispatches one driver-exec subprocess per manifest.Slices
// entry, all running fully concurrently (issue #2059): no model/Claude
// session is ever invoked to perform the join itself -- the join is this
// Go function, waiting on OS-level process/file-system signals only. Each
// worker gets its own fresh, sessionless config copy (own seeded prompt,
// own quarantined --log-path/--heartbeat-log under opts.WorkDir, no
// --state-file at all) built via buildDriverExecCmd, so a worker
// structurally cannot write run-state or pollute the coordinator's own log.
//
// A worker's terminal status is decided purely by which of two OS-level
// signals arrives first, bounded by opts.Timeout: its own done-sentinel
// file appearing (WorkerDone), or its process exiting without ever writing
// one (WorkerCrashed) -- with the timeout itself, when neither happens in
// time, forcibly ending the wait and killing the process (WorkerTimedOut).
// This is why no worker can ever leave the join unaccounted for (AC2): every
// slice in manifest.Slices gets exactly one WorkerResult, always.
//
// worker_start and worker_finish SpindriftOp lines are written to stdout for
// every worker (AC5), guarded by a shared mutex since goroutines write
// concurrently.
func LaunchWorkers(cfg config, manifest SliceManifest, opts WorkerOptions, stdout io.Writer) []WorkerResult {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultWorkerTimeout
	}
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultWorkerPollInterval
	}

	results := make([]WorkerResult, len(manifest.Slices))

	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		for i, s := range manifest.Slices {
			results[i] = WorkerResult{Slice: s.Name, Status: WorkerCrashed, Err: fmt.Errorf("create worker workdir: %w", err)}
		}
		return results
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, slice := range manifest.Slices {
		wg.Add(1)
		go func(i int, slice ManifestSlice) {
			defer wg.Done()
			results[i] = launchOneWorker(cfg, slice, opts.PromptFile, opts.WorkDir, timeout, pollInterval, stdout, &mu)
		}(i, slice)
	}
	wg.Wait()
	return results
}

func launchOneWorker(cfg config, slice ManifestSlice, promptFile, workDir string, timeout, pollInterval time.Duration, stdout io.Writer, mu *sync.Mutex) WorkerResult {
	// Emitted before anything else can fail, so every slice gets both a
	// worker_start and a worker_finish op regardless of which return path
	// this call takes (issue #2059 AC5 review finding) -- previously the
	// three launch-failure paths below (seedWorkerPrompt,
	// buildDriverExecCmd, cmd.Start) all returned before worker_start was
	// ever written, so a launch failure was logged as neither started nor
	// finished.
	writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_start", Worker: slice.Name})

	resultPath := filepath.Join(workDir, slice.Name+".result")
	sentinelPath := filepath.Join(workDir, slice.Name+".done")
	logPath := filepath.Join(workDir, slice.Name+".log")
	heartbeatLogPath := filepath.Join(workDir, slice.Name+".heartbeat.log")

	// Remove any pre-existing result/sentinel files before the worker is
	// ever launched -- a leftover <slice>.done from a prior run, or another
	// slice that reused the same workDir, must never be mistaken for this
	// worker's own completion signal (issue #2059 AC3 review finding).
	// os.IsNotExist is expected and ignored; these usually don't exist.
	if err := os.Remove(resultPath); err != nil && !os.IsNotExist(err) {
		err = fmt.Errorf("remove stale result file: %w", err)
		writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_finish", Worker: slice.Name, WorkerStatus: string(WorkerCrashed), Reason: err.Error()})
		return WorkerResult{Slice: slice.Name, Status: WorkerCrashed, Err: err}
	}
	if err := os.Remove(sentinelPath); err != nil && !os.IsNotExist(err) {
		err = fmt.Errorf("remove stale sentinel file: %w", err)
		writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_finish", Worker: slice.Name, WorkerStatus: string(WorkerCrashed), Reason: err.Error()})
		return WorkerResult{Slice: slice.Name, Status: WorkerCrashed, Err: err}
	}

	// Give this worker its own dedicated git worktree, checked out onto its
	// own branch off the orchestrator's own HEAD, so its driver-exec
	// subprocess never shares (and mutates) the orchestrator's own working
	// tree with any other concurrently-dispatched worker -- the isolation
	// worker-prompt.md already promises ("You run in your own isolated git
	// worktree") but the Go code never enforced (issue #2059 code-review
	// finding). The repo root is the orchestrator process's own cwd, the
	// same assumption buildDriverExecCmd already makes implicitly today
	// (it never sets cmd.Dir either); there's no separate repo-root config
	// field to thread through instead.
	repoRoot, err := os.Getwd()
	if err != nil {
		err = fmt.Errorf("determine repo root for worker worktree: %w", err)
		writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_finish", Worker: slice.Name, WorkerStatus: string(WorkerCrashed), Reason: err.Error()})
		return WorkerResult{Slice: slice.Name, Status: WorkerCrashed, Err: err}
	}
	worktreePath := filepath.Join(workDir, slice.Name+".worktree")
	worktreeBranch := "orchestrator-worker/" + slice.Name

	// A coordinator can legitimately dispatch the same slice name again on
	// a later pass (e.g. a manifest re-emitted across passes) -- since the
	// branch itself is deliberately left behind after a prior dispatch's
	// worktree is removed (below), that recurrence must reuse the
	// existing branch rather than fail: only "git worktree add -b" (create
	// a fresh branch) errors on an already-existing branch name, so check
	// for it first and drop -b when it's already there.
	addArgs := []string{"worktree", "add", worktreePath}
	verifyCmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+worktreeBranch)
	verifyCmd.Dir = repoRoot
	if verifyCmd.Run() == nil {
		addArgs = append(addArgs, worktreeBranch)
	} else {
		addArgs = append(addArgs, "-b", worktreeBranch, "HEAD")
	}

	addCmd := exec.Command("git", addArgs...)
	addCmd.Dir = repoRoot
	if out, err := addCmd.CombinedOutput(); err != nil {
		err = fmt.Errorf("create worker worktree: %w: %s", err, strings.TrimSpace(string(out)))
		writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_finish", Worker: slice.Name, WorkerStatus: string(WorkerCrashed), Reason: err.Error()})
		return WorkerResult{Slice: slice.Name, Status: WorkerCrashed, Err: err}
	}
	// Best-effort cleanup: the worktree directory is removed once this
	// worker's join is fully decided (success, timeout, or crash), on
	// every return path from here on -- but the branch itself is left
	// alone for a human/reviewer to inspect after a crash. A cleanup
	// failure never overrides the join's own already-decided status,
	// matching this file's existing convention (the seeded-prompt-file
	// cleanup above ignores its own error too).
	defer func() {
		rmCmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		rmCmd.Dir = repoRoot
		if err := rmCmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "orchestrator: remove worker worktree:", err)
		}
	}()

	seededPromptFile, err := seedWorkerPrompt(promptFile, slice, resultPath, sentinelPath)
	if err != nil {
		writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_finish", Worker: slice.Name, WorkerStatus: string(WorkerCrashed), Reason: err.Error()})
		return WorkerResult{Slice: slice.Name, Status: WorkerCrashed, Err: err}
	}
	// Mirrors run.go's own prevSeededPromptFile cleanup: the file is fully
	// written by the time seedWorkerPrompt returns, so it's safe to remove
	// on any return path once this worker is done with it (issue #2059
	// review finding, workers.go:206) -- otherwise every worker dispatched
	// leaks an unbounded temp file.
	defer os.Remove(seededPromptFile)

	passCfg := cfg
	passCfg.promptFile = seededPromptFile
	passCfg.sessionFile = ""
	passCfg.logPath = logPath
	passCfg.heartbeatLog = heartbeatLogPath
	passCfg.stateFile = ""
	passCfg.reviewPromptFile = ""

	cmd, err := buildDriverExecCmd(passCfg)
	if err != nil {
		writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_finish", Worker: slice.Name, WorkerStatus: string(WorkerCrashed), Reason: err.Error()})
		return WorkerResult{Slice: slice.Name, Status: WorkerCrashed, Err: err}
	}
	// Run the worker's driver-exec subprocess (and everything it spawns)
	// inside its own dedicated worktree, never the orchestrator's own tree.
	cmd.Dir = worktreePath
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_finish", Worker: slice.Name, WorkerStatus: string(WorkerCrashed), Reason: err.Error()})
		return WorkerResult{Slice: slice.Name, Status: WorkerCrashed, Err: err}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	sentinelCh := make(chan bool, 1)
	go func() { sentinelCh <- waitForSentinel(ctx, sentinelPath, pollInterval) }()

	var status WorkerStatus
	var exitCode int
	var procErr error

	select {
	case procErr = <-doneCh:
		cancel() // let the sentinel-poll goroutine return promptly
		if _, statErr := os.Stat(sentinelPath); statErr == nil {
			status = WorkerDone
		} else {
			status = WorkerCrashed
		}
		exitCode = workerExitCode(procErr)
	case sentinelOK := <-sentinelCh:
		if sentinelOK {
			status = WorkerDone
			select {
			case procErr = <-doneCh:
				exitCode = workerExitCode(procErr)
			case <-time.After(workerSentinelGrace):
				// sentinel is authoritative; the process lingering past
				// grace doesn't change the outcome (see workerSentinelGrace).
			}
		} else {
			status = WorkerTimedOut
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		}
	}

	resultBytes, _ := os.ReadFile(resultPath)

	reason := ""
	switch status {
	case WorkerTimedOut:
		reason = fmt.Sprintf("timeout after %s", timeout)
	case WorkerCrashed:
		if procErr != nil {
			reason = procErr.Error()
		} else {
			reason = fmt.Sprintf("exit %d", exitCode)
		}
	}
	writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_finish", Worker: slice.Name, WorkerStatus: string(status), Reason: reason})

	return WorkerResult{Slice: slice.Name, Status: status, ExitCode: exitCode, Result: string(resultBytes)}
}

// workerExitCode mirrors invokeDriverExec's own *exec.ExitError translation
// (run.go:634-650): a clean exit or a non-ExitError is 0.
func workerExitCode(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 0
}

// writeWorkerOp writes op to stdout under mu -- worker goroutines write
// concurrently, and most io.Writers (including the *bytes.Buffer the test
// suite uses) are not safe for concurrent Write calls.
func writeWorkerOp(stdout io.Writer, mu *sync.Mutex, op claude.SpindriftOp) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprint(stdout, claude.EncodeSpindriftOp(op))
}
