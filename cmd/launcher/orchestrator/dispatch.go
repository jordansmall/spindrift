package main

import (
	"fmt"
	"io"
	"strings"

	"spindrift.dev/launcher/internal/runstate"
)

// maxWorkerResultInFindings caps how much of a single worker's own
// WorkerResult.Result text is folded into state.WorkerFindings -- one
// runaway worker report must never blow out the next coordinator pass's
// own seeded prompt size (issue #2059 review finding).
const maxWorkerResultInFindings = 4000

// dispatchManifestIfPresent scans cfg.logPath (the pass that just ran) for a
// slice manifest; if cfg.workerPromptFile is unset, it is a no-op returning
// false without touching state (a static run-wide "worker dispatch
// disabled" setting can never follow a pass that set state.WorkerFindings).
// If no manifest is present, it clears state.WorkerFindings and returns
// false -- a pass that dispatched nothing must not leave a stale findings
// report from an earlier dispatch sitting in state for a later pass to seed
// as though still fresh (issue #2058 review finding A). Otherwise it
// dispatches LaunchWorkers and merges every WorkerResult into state:
// WorkerDone slices move from RemainingSlices (if present) into DoneSlices,
// WorkerTimedOut/WorkerCrashed slices move the other way, and each move is
// deduped so a slice name never appears twice in the same list nor in both
// lists at once (issue #2058 review finding B). It composes
// state.WorkerFindings as one line (plus, for a WorkerDone result, its own
// indented result block, capped at maxWorkerResultInFindings) per slice, and
// returns true -- letting the caller's own loop treat this pass as "more
// work to do" regardless of what its own verdict/outcome scan found, since
// the coordinator's only job on a manifest-emitting pass is to declare the
// manifest and stop (issue #2059 AC1).
func dispatchManifestIfPresent(cfg config, state *runstate.RunState, stdout io.Writer) bool {
	if cfg.workerPromptFile == "" {
		return false
	}
	manifest, ok := scanForManifest(cfg.logPath, cfg.driver)
	if !ok {
		// A pass that dispatches nothing must clear any WorkerFindings left
		// over from an earlier dispatch, mirroring run.go's own
		// unconditional per-pass state.ReviewFindings reassignment --
		// otherwise a stale worker report would keep being seeded into
		// every later pass's prompt as though it were still fresh (issue
		// #2058 review finding A).
		state.WorkerFindings = ""
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
			// A slice may already sit in RemainingSlices from a prior
			// dispatch that timed out or crashed it -- remove it there
			// before recording success, and dedup the append, so a
			// retried-then-successful slice never ends up listed in both
			// DoneSlices and RemainingSlices at once, nor duplicated within
			// DoneSlices itself (issue #2058 review finding B).
			state.RemainingSlices = removeSlice(state.RemainingSlices, r.Slice)
			state.DoneSlices = appendUnique(state.DoneSlices, r.Slice)
			result := strings.TrimSpace(r.Result)
			if result == "" {
				fmt.Fprintf(&findings, "- %s: done (no result reported)\n", r.Slice)
			} else {
				if len(result) > maxWorkerResultInFindings {
					result = result[:maxWorkerResultInFindings] + "... (truncated)"
				}
				fmt.Fprintf(&findings, "- %s: done\n  %s\n", r.Slice, strings.ReplaceAll(result, "\n", "\n  "))
			}
		case WorkerTimedOut, WorkerCrashed:
			// Defensive mirror of the WorkerDone case above -- this
			// direction (a slice already recorded done regressing to
			// timed-out/crashed) shouldn't normally occur, but keeping the
			// same-invariant removal here means DoneSlices/RemainingSlices
			// never disagree regardless of dispatch order (issue #2058
			// review finding B).
			state.DoneSlices = removeSlice(state.DoneSlices, r.Slice)
			state.RemainingSlices = appendUnique(state.RemainingSlices, r.Slice)
			if r.Status == WorkerTimedOut {
				fmt.Fprintf(&findings, "- %s: timed out\n", r.Slice)
			} else if r.Err != nil {
				fmt.Fprintf(&findings, "- %s: crashed (%s)\n", r.Slice, r.Err)
			} else {
				fmt.Fprintf(&findings, "- %s: crashed (exit %d)\n", r.Slice, r.ExitCode)
			}
		}
	}
	state.WorkerFindings = strings.TrimSpace(findings.String())
	return true
}

// appendUnique appends name to slices only if it is not already present,
// keeping a slice name listed at most once per merge -- otherwise
// dispatching the same slice with the same outcome across two passes would
// duplicate its entry (issue #2058 review finding B).
func appendUnique(slices []string, name string) []string {
	for _, s := range slices {
		if s == name {
			return slices
		}
	}
	return append(slices, name)
}

// removeSlice returns slices with every occurrence of name removed, so a
// slice's name never appears in both state.DoneSlices and
// state.RemainingSlices at once when a merge moves it from one list to the
// other (issue #2058 review finding B).
func removeSlice(slices []string, name string) []string {
	out := slices[:0]
	for _, s := range slices {
		if s != name {
			out = append(out, s)
		}
	}
	return out
}
