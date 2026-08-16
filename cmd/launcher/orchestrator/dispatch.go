package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/runstate"
)

// maxWorkerResultInFindings caps how much of a single worker's own
// WorkerResult.Result text is folded into state.WorkerFindings -- one
// runaway worker report must never blow out the next coordinator pass's
// own seeded prompt size (issue #2059 review finding).
const maxWorkerResultInFindings = 4000

// truncateRunes caps s at max runes, appending "... (truncated)" when it
// does. Slices on the rune boundary rather than the byte index a bare
// s[:max] would use, so a multi-byte UTF-8 rune straddling that cutoff is
// never split into an invalid U+FFFD-producing partial encoding (issue
// #2059 review finding).
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "... (truncated)"
}

// dispatchManifestIfPresent scans cfg.logPath (the pass that just ran) for a
// slice manifest unconditionally, even when cfg.workerPromptFile is unset --
// the runtime analog of mkHarness.nix's eval-time
// maxParallelWorkersCoherenceOk assert (issue #2495): cfg.workerPromptFile
// is only ever empty on a pass that structurally cannot have asked for a
// manifest (a fix-pass or review-only invocation, promptassembly/
// assemble.go only renders worker-prompt.md on the fresh-work-dispatch
// path), so scanning costs nothing on the routine case, but the rare case
// where a manifest appears anyway (e.g. a rogue/hallucinated marker) gets an
// attributed reason instead of a silent drop.
//
// If no manifest is present, it clears state.WorkerFindings and returns
// false -- a pass that dispatched nothing must not leave a stale findings
// report from an earlier dispatch sitting in state for a later pass to seed
// as though still fresh (issue #2059 review finding A). But when
// cfg.workerPromptFile is empty AND no manifest is present -- the routine
// case on a fix-pass/review-only run, which structurally never emits its own
// manifest -- state is left untouched instead: a static run-wide "worker
// dispatch disabled" pass must never wipe out state.WorkerFindings a PRIOR
// pass (in this or an earlier Box invocation) already set, which a later
// pass's own seedPromptFromState still needs to seed (issue #2495 review
// finding: this branch previously cleared it unconditionally, silently
// losing an earlier dispatch's results on every subsequent fix-pass write of
// state to disk). Only a manifest that genuinely appears despite
// cfg.workerPromptFile being empty clears state.WorkerFindings, via the
// attributed-discard branch below.
//
// Otherwise, every slice already present in state.DoneSlices is filtered out
// before
// LaunchWorkers is ever called: launchOneWorker's own `git worktree add -B
// <branch>` (workers.go) unconditionally force-resets that slice's branch to
// the orchestrator's current HEAD, which is correct for retrying a
// genuinely timed-out/crashed slice but would silently destroy a completed
// worker's commits if the coordinator re-declares an already-done slice
// name before cherry-picking its branch (issue #2059 review finding). Each
// skipped slice gets its own "already done, skipped redispatch" line in
// state.WorkerFindings instead, so the next coordinator pass's seeded
// prompt tells it the slice's branch is still waiting to be cherry-picked.
// If every slice in the manifest is filtered out this way, LaunchWorkers is
// never called at all.
//
// For whatever slices remain, it dispatches LaunchWorkers and merges every
// WorkerResult into state: WorkerDone slices move from RemainingSlices (if
// present) into DoneSlices, WorkerTimedOut/WorkerCrashed slices move the
// other way, and each move is deduped so a slice name never appears twice in
// the same list nor in both lists at once (issue #2059 review finding B). It
// composes state.WorkerFindings as one line (plus, for a WorkerDone result,
// its own indented result block, capped at maxWorkerResultInFindings) per
// dispatched slice, plus one skip-notice line per already-done slice, and
// returns true -- letting the caller's own loop treat this pass as "more
// work to do" regardless of what its own verdict/outcome scan found, since
// the coordinator's only job on a manifest-emitting pass is to declare the
// manifest and stop (issue #2059 AC1).
func dispatchManifestIfPresent(cfg config, state *runstate.RunState, stdout io.Writer) bool {
	manifest, ok := scanForManifest(cfg.logPath, cfg.driver)
	if !ok {
		if cfg.workerPromptFile == "" {
			// The routine fix-pass/review-only case: this run was never
			// configured to dispatch anything of its own, so this pass
			// finding no manifest says nothing about whether an earlier
			// pass's WorkerFindings are stale -- leave state untouched
			// (issue #2495 review finding).
			return false
		}
		// A pass that dispatches nothing must clear any WorkerFindings left
		// over from an earlier dispatch, mirroring run.go's own
		// unconditional per-pass state.ReviewFindings reassignment --
		// otherwise a stale worker report would keep being seeded into
		// every later pass's prompt as though it were still fresh (issue
		// #2059 review finding A).
		state.WorkerFindings = ""
		return false
	}
	if cfg.workerPromptFile == "" {
		// Genuinely runtime-decidable (issue #2495 AC3, review finding): only
		// knowable once this pass's own log is scanned, unlike the
		// eval-time-decidable "no worker provisioned" case
		// maxParallelWorkersCoherenceOk already rejects at build time. A
		// manifest reaching here despite no configured worker prompt file
		// means MAX_PARALLEL_WORKERS has nothing to act on this pass -- log
		// why instead of silently discarding it.
		fmt.Fprintln(os.Stderr, "orchestrator: this pass emitted a slice manifest, but no -worker-prompt-file is configured for this run (e.g. a fix-pass or review-only invocation); MAX_PARALLEL_WORKERS has nothing to dispatch and the manifest is discarded")
		state.WorkerFindings = ""
		return false
	}

	var findings strings.Builder

	dispatchSlices := make([]ManifestSlice, 0, len(manifest.Slices))
	for _, s := range manifest.Slices {
		if containsSlice(state.DoneSlices, s.Name) {
			fmt.Fprintf(&findings, "- %s: already done, skipped redispatch (branch %s still needs cherry-picking)\n", s.Name, workerBranchName(s.Name))
			continue
		}
		dispatchSlices = append(dispatchSlices, s)
	}

	var results []WorkerResult
	if len(dispatchSlices) > 0 {
		results = LaunchWorkers(cfg, SliceManifest{Slices: dispatchSlices}, WorkerOptions{
			PromptFile:  cfg.workerPromptFile,
			WorkDir:     cfg.workerWorkDir,
			Timeout:     cfg.workerTimeout,
			MaxParallel: cfg.maxParallelWorkers,
		}, stdout)
	}

	for _, r := range results {
		switch r.Status {
		case WorkerDone:
			// A slice may already sit in RemainingSlices from a prior
			// dispatch that timed out or crashed it -- remove it there
			// before recording success, and dedup the append, so a
			// retried-then-successful slice never ends up listed in both
			// DoneSlices and RemainingSlices at once, nor duplicated within
			// DoneSlices itself (issue #2059 review finding B).
			state.RemainingSlices = removeSlice(state.RemainingSlices, r.Slice)
			state.DoneSlices = appendUnique(state.DoneSlices, r.Slice)
			result := strings.TrimSpace(r.Result)
			if result == "" {
				fmt.Fprintf(&findings, "- %s: done (no result reported; branch %s ready to cherry-pick)\n", r.Slice, workerBranchName(r.Slice))
			} else {
				result = truncateRunes(result, maxWorkerResultInFindings)
				fmt.Fprintf(&findings, "- %s: done (branch %s ready to cherry-pick)\n  %s\n", r.Slice, workerBranchName(r.Slice), strings.ReplaceAll(result, "\n", "\n  "))
			}
		case WorkerTimedOut, WorkerCrashed:
			// Defensive mirror of the WorkerDone case above -- this
			// direction (a slice already recorded done regressing to
			// timed-out/crashed) shouldn't normally occur, but keeping the
			// same-invariant removal here means DoneSlices/RemainingSlices
			// never disagree regardless of dispatch order (issue #2059
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

// containsSlice reports whether name is present anywhere in slices -- used
// by dispatchManifestIfPresent to check state.DoneSlices before ever
// dispatching a manifest slice, so an already-done slice's branch is never
// touched by a redispatch (issue #2059 review finding).
func containsSlice(slices []string, name string) bool {
	for _, s := range slices {
		if s == name {
			return true
		}
	}
	return false
}

// appendUnique appends name to slices only if it is not already present,
// keeping a slice name listed at most once per merge -- otherwise
// dispatching the same slice with the same outcome across two passes would
// duplicate its entry (issue #2059 review finding B).
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
// other (issue #2059 review finding B).
func removeSlice(slices []string, name string) []string {
	out := slices[:0]
	for _, s := range slices {
		if s != name {
			out = append(out, s)
		}
	}
	return out
}
