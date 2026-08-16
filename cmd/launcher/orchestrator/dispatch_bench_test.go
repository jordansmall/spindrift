package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestScheduleSlicesConcurrentBatchDispatchBeatsSequential is the AC5
// verification a review pass found missing (issue #2058/#2060): "A
// parallelizable issue completes in less wall-clock than the sequential
// coordinator, with results fully reconciled" was previously asserted only
// via batch *ordering* (see e.g.
// TestDispatchManifestIfPresentDispatchesSeparateBatchesSequentially), never
// via an actual wall-clock measurement. This test builds a 3-slice manifest
// with mutually disjoint FileLeases and no DependsOn edges -- confirmed via
// scheduleSlices to land in a single batch, i.e. exactly the shape
// dispatchManifestIfPresent's own batch loop would dispatch concurrently via
// one LaunchWorkers call -- then rigs a fake driver-exec that sleeps a
// small, fixed, noticeable delay before signaling done, and measures real
// wall-clock time for two live LaunchWorkers passes over the identical
// batch: one with MaxParallel wide enough to run every slice at once, one
// with MaxParallel: 1 (forced sequential). Both sides are measured live in
// the same test run (rather than comparing a live measurement against a
// hardcoded threshold) specifically to stay robust against CI timing
// jitter: the concurrent pass must finish in well under the sequential
// pass's own elapsed time, "results fully reconciled" pinned by asserting
// every slice comes back WorkerDone on both passes.
func TestScheduleSlicesConcurrentBatchDispatchBeatsSequential(t *testing.T) {
	repoRoot := chdirToFreshWorkerRepo(t)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.log")
	if err := os.WriteFile(callLog, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(callLog): %v", err)
	}
	// Sleeps a small, fixed, deliberately-noticeable delay -- long enough to
	// dominate git-worktree/process overhead noise, short enough this test
	// doesn't slow the suite down painfully -- before signaling done, the
	// same way writeFakeWorkerDriverExec's "done-fast" behavior does but
	// with a controllable runtime standing in for real worker "thinking"
	// time.
	const perSliceDelay = 250 * time.Millisecond
	body := `sleep 0.25
: > "$DRIVER_LOG_PATH"
: > "${DRIVER_LOG_PATH%.log}.done"
`
	writeFakeDriverExec(t, fakeDir, callLog, body)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "worker-prompt.txt")
	if err := os.WriteFile(promptFile, []byte("BASE WORKER PROMPT"), 0o644); err != nil {
		t.Fatalf("WriteFile(promptFile): %v", err)
	}

	slices := []ManifestSlice{
		{Name: "alpha", Task: "implement seam alpha", FileLeases: []string{"alpha.txt"}},
		{Name: "beta", Task: "implement seam beta", FileLeases: []string{"beta.txt"}},
		{Name: "gamma", Task: "implement seam gamma", FileLeases: []string{"gamma.txt"}},
	}
	manifest := SliceManifest{Slices: slices}

	// Criterion 1: mutually disjoint FileLeases and no DependsOn edges must
	// actually batch all three slices together -- scheduleSlices is what
	// dispatchManifestIfPresent's own batch loop calls to decide what one
	// LaunchWorkers call dispatches concurrently, so if this ever regressed
	// to splitting the slices into solo batches, the rest of this test
	// would silently stop proving anything about concurrent dispatch.
	batches := scheduleSlices(slices)
	if len(batches) != 1 || len(batches[0]) != len(slices) {
		t.Fatalf("scheduleSlices(slices) = %d batch(es) (sizes %v), want exactly 1 batch of %d disjoint-lease slices", len(batches), func() []int {
			sizes := make([]int, len(batches))
			for i, b := range batches {
				sizes[i] = len(b)
			}
			return sizes
		}(), len(slices))
	}

	cfg := config{driver: "claude"}

	runLaunchWorkers := func(t *testing.T, maxParallel int) time.Duration {
		t.Helper()
		workDir := t.TempDir()
		var stdout bytes.Buffer

		start := time.Now()
		results := LaunchWorkers(cfg, manifest, WorkerOptions{
			PromptFile:   promptFile,
			WorkDir:      workDir,
			Timeout:      10 * time.Second,
			PollInterval: 5 * time.Millisecond,
			MaxParallel:  maxParallel,
		}, &stdout)
		elapsed := time.Since(start)

		if len(results) != len(slices) {
			t.Fatalf("len(results) = %d, want %d", len(results), len(slices))
		}
		for i, r := range results {
			if r.Status != WorkerDone {
				t.Errorf("results[%d] = %+v, want WorkerDone -- results must be fully reconciled on both the concurrent and sequential pass", i, r)
			}
		}
		return elapsed
	}

	concurrentElapsed := runLaunchWorkers(t, len(slices))
	sequentialElapsed := runLaunchWorkers(t, 1)

	t.Logf("concurrent elapsed = %v, sequential elapsed = %v (per-slice delay = %v, repo = %s)", concurrentElapsed, sequentialElapsed, perSliceDelay, repoRoot)

	// Loose, jitter-tolerant bound: the concurrent pass, dispatching all
	// three slices at once, must finish well under the sequential pass's
	// own live measurement -- 0.6x leaves generous headroom for git
	// worktree/process overhead noise on a loaded CI box while still
	// clearly distinguishing "ran concurrently" from "ran sequentially"
	// (which would land close to 1.0x, not under it).
	if maxWant := time.Duration(float64(sequentialElapsed) * 0.6); concurrentElapsed >= maxWant {
		t.Errorf("concurrent elapsed = %v, want < %v (0.6 * sequential elapsed %v) -- concurrent batch dispatch should be meaningfully faster than dispatching the same slices one at a time", concurrentElapsed, maxWant, sequentialElapsed)
	}
}
