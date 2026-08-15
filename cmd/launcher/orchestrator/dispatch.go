package main

import (
	"fmt"
	"io"
	"strings"
)

// maxWorkerResultInFindings caps how much of a single worker's own
// WorkerResult.Result text is folded into state.WorkerFindings -- one
// runaway worker report must never blow out the next coordinator pass's
// own seeded prompt size (issue #2059 review finding).
const maxWorkerResultInFindings = 4000

// dispatchManifestIfPresent scans cfg.logPath (the pass that just ran) for a
// slice manifest; if cfg.workerPromptFile is unset, or no manifest is
// present, it is a no-op returning false. Otherwise it dispatches
// LaunchWorkers, merges every WorkerResult into state (WorkerDone slices
// appended to state.DoneSlices, WorkerTimedOut/WorkerCrashed slices
// appended to state.RemainingSlices so a later pass can decide whether to
// retry them), composes state.WorkerFindings as one line (plus, for a
// WorkerDone result, its own indented result block, capped at
// maxWorkerResultInFindings) per slice, and returns true -- letting the
// caller's own loop treat this pass as "more work to do" regardless of what
// its own verdict/outcome scan found, since the coordinator's only job on a
// manifest-emitting pass is to declare the manifest and stop (issue #2059
// AC1).
func dispatchManifestIfPresent(cfg config, state *RunState, stdout io.Writer) bool {
	if cfg.workerPromptFile == "" {
		return false
	}
	manifest, ok := scanForManifest(cfg.logPath, cfg.driver)
	if !ok {
		return false
	}

	results := LaunchWorkers(cfg, manifest, WorkerOptions{
		PromptFile: cfg.workerPromptFile,
		WorkDir:    cfg.workerWorkDir,
		Timeout:    cfg.workerTimeout,
	}, stdout)

	var findings strings.Builder
	for _, r := range results {
		switch r.Status {
		case WorkerDone:
			state.DoneSlices = append(state.DoneSlices, r.Slice)
			result := strings.TrimSpace(r.Result)
			if result == "" {
				fmt.Fprintf(&findings, "- %s: done (no result reported)\n", r.Slice)
			} else {
				if len(result) > maxWorkerResultInFindings {
					result = result[:maxWorkerResultInFindings] + "... (truncated)"
				}
				fmt.Fprintf(&findings, "- %s: done\n  %s\n", r.Slice, strings.ReplaceAll(result, "\n", "\n  "))
			}
		case WorkerTimedOut:
			state.RemainingSlices = append(state.RemainingSlices, r.Slice)
			fmt.Fprintf(&findings, "- %s: timed out\n", r.Slice)
		case WorkerCrashed:
			state.RemainingSlices = append(state.RemainingSlices, r.Slice)
			if r.Err != nil {
				fmt.Fprintf(&findings, "- %s: crashed (%s)\n", r.Slice, r.Err)
			} else {
				fmt.Fprintf(&findings, "- %s: crashed (exit %d)\n", r.Slice, r.ExitCode)
			}
		}
	}
	state.WorkerFindings = strings.TrimSpace(findings.String())
	return true
}
