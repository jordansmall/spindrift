package main

import (
	"fmt"
	"io"
	"strings"
)

// dispatchManifestIfPresent scans cfg.logPath (the pass that just ran) for a
// slice manifest; if cfg.workerPromptFile is unset, or no manifest is
// present, it is a no-op returning false. Otherwise it dispatches
// LaunchWorkers, merges every WorkerResult into state (WorkerDone slices
// appended to state.DoneSlices, WorkerTimedOut/WorkerCrashed slices
// appended to state.RemainingSlices so a later pass can decide whether to
// retry them), composes state.WorkerFindings as one line per slice, and
// returns true -- letting the caller's own loop treat this pass as "more
// work to do" regardless of what its own verdict/outcome scan found, since
// the coordinator's only job on a manifest-emitting pass is to declare the
// manifest and stop (issue #2059 AC1).
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
			fmt.Fprintf(&findings, "- %s: done\n", r.Slice)
		case WorkerTimedOut:
			state.RemainingSlices = append(state.RemainingSlices, r.Slice)
			fmt.Fprintf(&findings, "- %s: timed out\n", r.Slice)
		case WorkerCrashed:
			state.RemainingSlices = append(state.RemainingSlices, r.Slice)
			fmt.Fprintf(&findings, "- %s: crashed (exit %d)\n", r.Slice, r.ExitCode)
		}
	}
	state.WorkerFindings = strings.TrimSpace(findings.String())
	return true
}
