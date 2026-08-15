package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
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
