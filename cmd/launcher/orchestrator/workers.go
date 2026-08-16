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
	"syscall"
	"time"

	"spindrift.dev/launcher/internal/driver/claude"
)

// workerBranchName returns the deterministic branch name launchOneWorker
// commits a slice's work onto: "orchestrator-worker/<name>". Shared with
// dispatch.go so a coordinator pass's seeded findings can name the exact
// branch to cherry-pick, rather than the coordinator having to reconstruct
// or guess it (issue #2059 review finding).
func workerBranchName(sliceName string) string {
	return "orchestrator-worker/" + sliceName
}

// workerLogPath returns one slice's own quarantined --log-path
// (launchOneWorker's own logPath convention, line ~332 below). Shared with
// dispatch.go's own budget-usage sum (issue #2694 review finding) so both
// call sites derive the same path from the same workDir value, rather than
// dispatch.go re-deriving it against cfg.workerWorkDir directly while
// launchOneWorker's own caller (LaunchWorkers) first absolutizes it --
// today those two resolve identically (nothing in this package ever calls
// os.Chdir), but a shared helper keeps that true by construction instead of
// by the absence of a future os.Chdir call elsewhere in the package.
func workerLogPath(workDir, sliceName string) string {
	return filepath.Join(workDir, sliceName+".log")
}

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

// defaultMaxParallelWorkers bounds how many manifest-dispatched workers
// LaunchWorkers runs concurrently when no explicit cap is configured --
// matches the maxParallelWorkers env-schema default (MAX_PARALLEL_WORKERS,
// issue #2495): enough to capture most of the wall-clock win on
// small-slice-count issues while staying clear of the Box's memory-kill
// regime.
const defaultMaxParallelWorkers = 2

// seedWorkerPrompt composes a fresh prompt file for one worker: promptFile's
// own content, with a "## Parallel worker dispatch" section prepended
// naming slice.Name, stating slice.Task as the worker's own scoped
// delegated work (issue #2059 code-review finding -- previously only the
// bare slice name reached the worker, leaving it with nothing to
// implement), and instructing the worker to write its final report to
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
	fmt.Fprintf(&b, "Your slice of work:\n\n%s\n\n", slice.Task)
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

// workerKillGrace bounds how long killWorkerProcessGroup waits for a killed
// worker's process group to actually be reaped before giving up -- SIGKILL
// itself is unmaskable and near-instant, but the caller's own goroutine
// (doneCh) still needs to observe cmd.Wait() return before the caller's
// worktree-removal cleanup is safe to run (issue #2059 review finding,
// workers.go:374): the previous code sent the kill and returned immediately,
// never confirming the group had actually exited first.
const workerKillGrace = 2 * time.Second

// killWorkerProcessGroup sends SIGKILL to cmd's own process group (Setpgid
// above makes -cmd.Process.Pid reach every child the group leader spawned,
// not just cmd itself) and waits, bounded by workerKillGrace, for doneCh to
// report the process has been reaped -- so a caller's own subsequent
// cleanup (removing this worker's worktree) never races a process the
// kernel hasn't finished tearing down yet. Best-effort past the grace
// period: if the wait times out, the function still returns rather than
// blocking the join forever, matching this file's existing
// best-effort-cleanup convention.
func killWorkerProcessGroup(cmd *exec.Cmd, doneCh <-chan error) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: kill timed-out worker process group:", err)
		return
	}
	select {
	case <-doneCh:
	case <-time.After(workerKillGrace):
		fmt.Fprintln(os.Stderr, "orchestrator: timed-out worker process group did not exit within grace period")
	}
}

// WorkerOptions configures LaunchWorkers. Every path here is independent of
// the coordinator's own cfg.logPath/cfg.stateFile (AC4/AC6) -- but note the
// structural guarantee behind AC4 lives in buildDriverExecCmd itself
// (run.go:728-753), not here: it never emits a --state-file or
// --review-prompt-file flag for ANY cfg, worker or coordinator pass, so no
// driver-exec invocation is ever told the run-state path in the first
// place. See the passCfg comment in launchOneWorker below (issue #2059
// review finding).
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
	// MaxParallel bounds how many workers LaunchWorkers runs concurrently;
	// <= 0 falls back to defaultMaxParallelWorkers.
	MaxParallel int
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
	maxParallel := opts.MaxParallel
	if maxParallel <= 0 {
		maxParallel = defaultMaxParallelWorkers
	}

	var mu sync.Mutex

	// Absolutize WorkDir up front so every path launchOneWorker derives from
	// it (result/sentinel/log files, the worktree dir) is unambiguous
	// regardless of cwd -- the orchestrator polls resultPath/sentinelPath
	// from its own process cwd, while the worker's driver-exec subprocess
	// runs with cmd.Dir set to its own worktree; a relative WORKER_WORK_DIR
	// would make the two sides resolve the same string to two different
	// files, silently burning the worker's full timeout waiting for a
	// sentinel that already exists, just not where it's looking (issue
	// #2059 review finding, workers.go:248).
	absWorkDir, err := filepath.Abs(opts.WorkDir)
	if err != nil {
		return crashAllSlices(stdout, &mu, manifest, fmt.Errorf("resolve worker workdir: %w", err))
	}

	if err := os.MkdirAll(absWorkDir, 0o755); err != nil {
		return crashAllSlices(stdout, &mu, manifest, fmt.Errorf("create worker workdir: %w", err))
	}

	results := make([]WorkerResult, len(manifest.Slices))
	tasks := make([]func(), len(manifest.Slices))
	for i, slice := range manifest.Slices {
		i, slice := i, slice
		tasks[i] = func() {
			results[i] = launchOneWorker(cfg, slice, opts.PromptFile, absWorkDir, timeout, pollInterval, stdout, &mu)
		}
	}
	runBounded(maxParallel, tasks)
	return results
}

// runBounded runs every fn in tasks, each on its own goroutine, but never
// lets more than maxParallel of them run at the same instant (issue #2495:
// LaunchWorkers previously fanned out every manifest slice's worker
// goroutine at once, uncapped). Blocks until every task has returned. Every
// task is invoked exactly once, regardless of maxParallel -- callers (like
// LaunchWorkers, whose own AC2 promises exactly one WorkerResult per slice)
// rely on that. maxParallel <= 0 falls back to serial (1 at a time) rather
// than an unbuffered semaphore: a zero-capacity channel's send blocks
// forever, since the receiving goroutine is only spawned after the send
// completes -- LaunchWorkers already normalizes its own opts.MaxParallel
// before calling this, but that's a caller convention, not something this
// function's own doc comment can rely on to keep its "invoked exactly once"
// promise true.
func runBounded(maxParallel int, tasks []func()) {
	if maxParallel <= 0 {
		maxParallel = 1
	}
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(task func()) {
			defer wg.Done()
			defer func() { <-sem }()
			task()
		}(task)
	}
	wg.Wait()
}

// crashAllSlices reports every slice in manifest as WorkerCrashed with err,
// emitting a worker_start/worker_finish pair for each (AC5) -- used when
// LaunchWorkers fails before any worker can even be dispatched, so every
// slice in manifest.Slices still gets exactly one WorkerResult (AC2).
func crashAllSlices(stdout io.Writer, mu *sync.Mutex, manifest SliceManifest, err error) []WorkerResult {
	results := make([]WorkerResult, len(manifest.Slices))
	for i, s := range manifest.Slices {
		writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_start", Worker: s.Name})
		writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_finish", Worker: s.Name, WorkerStatus: string(WorkerCrashed), Reason: err.Error()})
		results[i] = WorkerResult{Slice: s.Name, Status: WorkerCrashed, Err: err}
	}
	return results
}

func launchOneWorker(cfg config, slice ManifestSlice, promptFile, workDir string, timeout, pollInterval time.Duration, stdout io.Writer, mu *sync.Mutex) WorkerResult {
	// Emitted before anything else can fail, so every slice gets both a
	// worker_start and a worker_finish op regardless of which return path
	// this call takes -- including the seedWorkerPrompt, buildDriverExecCmd,
	// and cmd.Start launch-failure paths below, which would otherwise log a
	// launch failure as neither started nor finished (issue #2059 AC5).
	writeWorkerOp(stdout, mu, claude.SpindriftOp{Op: "worker_start", Worker: slice.Name})

	resultPath := filepath.Join(workDir, slice.Name+".result")
	sentinelPath := filepath.Join(workDir, slice.Name+".done")
	logPath := workerLogPath(workDir, slice.Name)
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
	worktreeBranch := workerBranchName(slice.Name)

	// A coordinator can legitimately dispatch the same slice name again on
	// a later pass (e.g. a manifest re-emitted across passes) -- since the
	// branch itself is deliberately left behind after a prior dispatch's
	// worktree is removed (below), that recurrence must reuse the branch
	// name rather than fail. "git worktree add -B <branch>" both creates
	// the branch on first use and, on reuse, forcibly resets it to HEAD --
	// unlike a plain reuse of the existing branch tip, this guarantees a
	// retried (e.g. previously timed-out, partially-committed) slice always
	// starts fresh off the orchestrator's own current HEAD, never off
	// whatever commit a prior, possibly-killed worker left the branch at
	// (issue #2059 review finding).
	addCmd := exec.Command("git", "worktree", "add", "-B", worktreeBranch, worktreePath, "HEAD")
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
	// Belt-and-suspenders, not the actual enforcement mechanism:
	// buildDriverExecCmd (run.go:728-753) never reads cfg.stateFile or
	// cfg.reviewPromptFile into argv for ANY cfg, so clearing them here only
	// guards against a future buildDriverExecCmd change starting to wire one
	// of them in -- see TestBuildDriverExecCmdNeverForwardsStateOrReviewPromptFile
	// in run_test.go, which pins the real invariant. This does NOT make a
	// worker structurally unable to discover the run-state path: it lives at
	// the fixed literal "/tmp/run-state.json" (main.go's own --state-file
	// default, entrypoint.sh:1159's --run-state-file), and a worker holds
	// Bash/Write like any other pass -- AC4/AC6 rest on the worker prompt's
	// own instruction not to touch it, not on any structural barrier (issue
	// #2059 review finding).
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
	// Start this worker as its own process group leader, so a timeout kill
	// (below) can signal the whole group -- driver-exec spawns the actual
	// claude/driver subprocess as its own child, and killing only the
	// driver-exec parent would leave that child orphaned and still mutating
	// this worker's own worktree after the join has already moved on (issue
	// #2059 code-review finding).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
				// The sentinel is authoritative, so status stays WorkerDone
				// regardless -- but the process itself is still running,
				// and the worktree-removal defer above is about to run
				// against its cwd. Kill the group so that removal never
				// races a process the kernel hasn't torn down yet, the
				// same race killWorkerProcessGroup exists to close on the
				// WorkerTimedOut path below (issue #2059 review finding,
				// workers.go:397). doneCh is consumed inside
				// killWorkerProcessGroup, so it must not be read again on
				// this return path.
				killWorkerProcessGroup(cmd, doneCh)
			}
		} else {
			status = WorkerTimedOut
			// Kill the whole process GROUP (negative pid), not just
			// driver-exec itself -- Setpgid above made this process its own
			// group leader, so this also reaches any subprocess (e.g. the
			// real claude driver) it spawned. Best-effort: the status is
			// already decided as WorkerTimedOut regardless of whether the
			// kill itself succeeds, matching this file's existing
			// best-effort-cleanup convention (e.g. the worktree-removal
			// defer above). killWorkerProcessGroup also blocks (bounded by
			// workerKillGrace) until doneCh confirms the group has been
			// reaped, so the worktree-removal defer above never races a
			// process the kernel hasn't finished tearing down yet (issue
			// #2059 review finding, workers.go:374) -- doneCh is consumed
			// here, so it must not be read again on this return path.
			killWorkerProcessGroup(cmd, doneCh)
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
