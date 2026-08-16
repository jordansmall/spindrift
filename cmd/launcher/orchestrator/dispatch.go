package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/runstate"
)

// integrationOutcome bundles integrateSliceBranch's own three return values
// so dispatchManifestIfPresent can record one WorkerDone slice's
// integration result immediately (right after its batch's LaunchWorkers
// call), then compose the corresponding findings line later, once every
// batch has run (issue #2060).
type integrationOutcome struct {
	status   integrateStatus
	output   string
	err      error
	revRange string
}

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
// slice manifest, but only once cfg.workerPromptFile is confirmed set.
// cfg.workerPromptFile is empty exactly when this pass was never configured
// to dispatch anything of its own (a fix-pass or review-only invocation --
// promptassembly/assemble.go only renders worker-prompt.md on the
// fresh-work-dispatch path), so that case is handled first, uniformly, and
// unconditionally: return false and leave state entirely untouched, without
// ever scanning the log for a manifest. That covers a manifest appearing in
// this pass's own log anyway -- e.g. a custom SPINDRIFT_PROMPT_DIR
// fix-prompt.md that still carries the manifest-emitting step, a
// legitimate, supported shape, not just a rogue/hallucinated marker -- the
// same as no manifest appearing at all: there is nowhere to dispatch it
// either way, and a static run-wide "worker dispatch disabled" pass must
// never wipe out state.WorkerFindings a PRIOR pass (in this or an earlier
// Box invocation) already set, which a later pass's own
// seedPromptFromState still needs to seed (issue #2495 review finding: this
// function previously scanned for and discarded such a manifest here,
// unconditionally clearing state.WorkerFindings and destroying an earlier
// pass's dispatch results). Runtime coherence checks for this knob belong
// at orchestrator startup (issue #2495 AC3), not as mid-run handling in
// this per-pass function.
//
// Once cfg.workerPromptFile is confirmed set, the routine case is scanning
// the log and finding no manifest: it clears state.WorkerFindings and
// returns false -- a pass that dispatched nothing must not leave a stale
// findings report from an earlier dispatch sitting in state for a later
// pass to seed as though still fresh (issue #2059 review finding A).
//
// Otherwise, every slice already present in state.DoneSlices is filtered out
// before
// LaunchWorkers is ever called: launchOneWorker's own `git worktree add -B
// <branch>` (workers.go) unconditionally force-resets that slice's branch to
// the orchestrator's current HEAD, which is correct for retrying a
// genuinely timed-out/crashed slice but would silently destroy a completed
// worker's commits if the coordinator re-declares an already-done slice
// name before its branch was ever integrated (issue #2059 review finding).
// Each skipped slice gets its own "already done, skipped redispatch" line in
// state.WorkerFindings instead, pointing back at that slice's own earlier
// integration result rather than claiming a manual cherry-pick is still
// owed -- by the time a slice reaches state.DoneSlices, this function has
// already run integrateSliceBranch against it (successfully or not, see
// below), so the common case is that its branch is already on HEAD, not
// still waiting to be cherry-picked (issue #2060 review finding: this line
// previously claimed the branch "still needs cherry-picking" even after
// automatic integration had already landed it). If every slice in the
// manifest is filtered out this way, LaunchWorkers is never called at all.
//
// For whatever slices remain, it partitions them into ordered batches via
// scheduleSlices (schedule.go, issue #2060) -- provably-disjoint-lease
// slices within one batch dispatch fully concurrently via one LaunchWorkers
// call, while batches themselves run strictly in sequence, batch N+1 only
// starting once every slice in batch N has both joined AND (for a
// WorkerDone slice) had its branch integrated. That integration step
// (integrateSliceBranch, integrate.go) runs immediately after each batch's
// LaunchWorkers call, before the next batch is ever dispatched: a later
// batch's own workers are created via `git worktree add ... HEAD`
// (workers.go), so a slice that DependsOn an earlier batch's slice must see
// that dependency's integrated changes already on HEAD when its own
// worktree is created. The same not-landed gate also covers two extensions
// (issue #2060 review findings): a later-batch slice is skipped not only
// when its own DependsOn names a not-landed slice, but also when its own
// FileLeases overlap (or are themselves undeclared) a not-landed slice's
// leases, even with no DependsOn edge between them at all -- scheduleSlices
// can sequence two lease-overlapping slices into different batches purely
// by lease rules, and a not-landed earlier slice's overlap is exactly the
// case a later worktree built from stale HEAD must not silently dispatch
// against. And the gate is seeded, not just built fresh each call: any
// slice name already recorded in state.UnlandedSlices from an EARLIER pass
// is treated as not-landed from this pass's very first batch, so a brand
// new manifest that DependsOn a slice whose integration failed in a prior
// pass (and therefore was filtered out of dispatchSlices entirely, since it
// already sits in state.DoneSlices) still gets gated correctly.
//
// Every WorkerResult across every batch is merged into state exactly as
// before: WorkerDone slices move from RemainingSlices (if present) into
// DoneSlices, WorkerTimedOut/WorkerCrashed slices move the other way, and
// each move is deduped so a slice name never appears twice in the same list
// nor in both lists at once (issue #2059 review finding B). A slice stays in
// DoneSlices once WorkerDone regardless of whether its own branch
// integration succeeded -- the worker's own job is done either way, and its
// branch is never deleted, so a conflicted/failed integration can still be
// resolved manually later from the same branch name. It composes
// state.WorkerFindings as one line per dispatched slice (naming the
// integration outcome for a WorkerDone slice -- integrated, nothing to
// integrate, conflicted with manual-resolution guidance, or failed -- plus
// its own indented result block, capped at maxWorkerResultInFindings), plus
// one skip-notice line per already-done slice, and returns true -- letting
// the caller's own loop treat this pass as "more work to do" regardless of
// what its own verdict/outcome scan found, since the coordinator's only job
// on a manifest-emitting pass is to declare the manifest and stop (issue
// #2059 AC1).
func dispatchManifestIfPresent(cfg config, state *runstate.RunState, stdout io.Writer) bool {
	if cfg.workerPromptFile == "" {
		// This pass was never configured to dispatch anything of its own
		// (a fix-pass or review-only invocation never renders
		// worker-prompt.md -- promptassembly/assemble.go only renders it on
		// a fresh-work-dispatch pass with FixPass == 0). Leave
		// state.WorkerFindings untouched regardless of whether this pass's
		// own log happens to carry a manifest marker anyway (e.g. a custom
		// SPINDRIFT_PROMPT_DIR fix-prompt.md that still carries the
		// manifest step is a legitimate shape, not just a rogue/
		// hallucinated marker) -- an earlier pass's dispatch results must
		// survive for a later pass's seedPromptFromState to still seed
		// (issue #2495 review finding). Runtime coherence checks for this
		// knob belong at orchestrator startup (AC3), not as mid-run
		// handling here.
		return false
	}
	manifest, ok := scanForManifest(cfg.logPath, cfg.driver)
	if !ok {
		// A pass that dispatches nothing must clear any WorkerFindings left
		// over from an earlier dispatch, mirroring run.go's own
		// unconditional per-pass state.ReviewFindings reassignment --
		// otherwise a stale worker report would keep being seeded into
		// every later pass's prompt as though it were still fresh (issue
		// #2059 review finding A).
		state.WorkerFindings = ""
		return false
	}

	var findings strings.Builder

	dispatchSlices := make([]ManifestSlice, 0, len(manifest.Slices))
	for _, s := range manifest.Slices {
		if containsSlice(state.DoneSlices, s.Name) {
			fmt.Fprintf(&findings, "- %s: already done, skipped redispatch (see this slice's earlier integration result for branch %s)\n", s.Name, workerBranchName(s.Name))
			continue
		}
		dispatchSlices = append(dispatchSlices, s)
	}

	var results []WorkerResult
	// integrations records, per slice name, the outcome of integrating that
	// slice's own branch into repoRoot -- populated only for WorkerDone
	// results, immediately after each batch's own LaunchWorkers call
	// returns and before the next batch is dispatched (see the function's
	// own doc comment above).
	integrations := make(map[string]integrationOutcome)
	if len(dispatchSlices) > 0 {
		repoRoot, repoRootErr := os.Getwd()

		// startHead records repoRoot's HEAD before this manifest's own batch
		// loop below lands any per-slice integration commits, so the whole
		// pass can be squashed into one commit once every batch has run (see
		// the squash step after the loop, and squashIntegrationCommits'
		// own doc comment, issue #2060 review finding: without this, an
		// N-slice manifest lands N separate "chore(orchestrator): integrate
		// slice <name>" commits instead of one coherent change). Left empty
		// when repoRootErr != nil -- every WorkerDone slice in that case
		// already degrades to integrateFailed below, so no commit ever
		// lands and there is nothing to squash.
		var startHead string
		if repoRootErr == nil {
			if headOut, headErr := runGitIn(repoRoot, "rev-parse", "HEAD"); headErr == nil {
				startHead = strings.TrimSpace(headOut)
			} else {
				fmt.Fprintln(os.Stderr, "orchestrator: rev-parse HEAD before manifest dispatch:", headErr, strings.TrimSpace(headOut))
			}
		}
		// integratedNames records, in manifest-processing order, every slice
		// name whose own integrateSliceBranch call returned integrateOK this
		// pass -- exactly the set squashIntegrationCommits below needs to
		// name in the final squashed commit's message. integrateEmpty/
		// integrateConflict/integrateFailed slices contributed no commit at
		// all, so they are deliberately never added here.
		var integratedNames []string

		opts := WorkerOptions{
			PromptFile:  cfg.workerPromptFile,
			WorkDir:     cfg.workerWorkDir,
			Timeout:     cfg.workerTimeout,
			MaxParallel: cfg.maxParallelWorkers,
		}

		// notLanded records every slice name whose own commits did NOT end
		// up on repoRoot's HEAD -- either because its own integration
		// outcome was integrateConflict/integrateFailed, because it was
		// itself skipped here for depending on (or, below, file-lease
		// overlapping) such a slice, or because a PRIOR pass already
		// recorded it in state.UnlandedSlices (seeded below). batch N+1's
		// own workers are created via `git worktree add ... HEAD`
		// (workers.go), so a slice that DependsOn a name in this set must
		// never be dispatched: its worktree would be created against a
		// tree still missing that dependency's changes, defeating
		// scheduleSlices' own batch-ordering guarantee in effect even
		// though dispatch ORDER still honored it (issue #2060 review
		// finding). integrateEmpty is deliberately excluded -- it means
		// there was nothing to integrate, so HEAD already carries whatever
		// the branch would have contributed (or the branch contributed
		// nothing at all), not that anything failed to land.
		notLanded := make(map[string]bool, len(state.UnlandedSlices))
		for _, name := range state.UnlandedSlices {
			notLanded[name] = true
		}

		// sliceLeases records, per dispatchSlices name, its own normalized
		// FileLeases (nil/empty for an undeclared slice) -- built once, up
		// front, from every slice about to be dispatched this pass, so a
		// slice skipped later (by DependsOn or by lease overlap) still has
		// its own leases on hand for a THIRD slice's lease-overlap check
		// against it (issue #2060 review finding: Bug A). A not-landed name
		// seeded above from state.UnlandedSlices (a prior pass) has no
		// entry here -- this pass never saw its FileLeases, so lease-overlap
		// gating against it is out of scope; only the DependsOn-name check
		// covers that cross-pass case, matching this function's pre-Bug-A
		// behavior.
		sliceLeases := make(map[string][]string, len(dispatchSlices))
		for _, s := range dispatchSlices {
			sliceLeases[s.Name] = claimLeases(s, nil)
		}

		for _, batch := range scheduleSlices(dispatchSlices) {
			safe := make([]ManifestSlice, 0, len(batch))
			for _, s := range batch {
				blockingDep := ""
				for _, dep := range s.DependsOn {
					if notLanded[dep] {
						blockingDep = dep
						break
					}
				}
				if blockingDep != "" {
					// This slice's own dependency never landed on HEAD --
					// skip it entirely (never dispatched), and propagate
					// the same fate to it so anything depending on IT
					// skips too. state.DoneSlices/RemainingSlices are left
					// exactly as they were: this slice was never
					// dispatched, so there is nothing to move, and a
					// future manifest can still retry it.
					notLanded[s.Name] = true
					fmt.Fprintf(&findings, "- %s: skipped -- depends on %s, whose own integration did not land on HEAD\n", s.Name, blockingDep)
					continue
				}

				if blockingLease := notLandedLeaseOverlap(s, dispatchSlices, sliceLeases, notLanded); blockingLease != "" {
					// scheduleSlices can sequence two slices with
					// overlapping (or undeclared) FileLeases into
					// different batches with no DependsOn edge between
					// them at all -- lease overlap makes it very likely
					// this slice edits the same files as blockingLease,
					// so its own worktree (built from HEAD) must not be
					// created until blockingLease's changes have actually
					// landed there. Propagate the same fate exactly like
					// the DependsOn case above.
					notLanded[s.Name] = true
					fmt.Fprintf(&findings, "- %s: skipped -- file lease overlaps %s, whose own integration did not land on HEAD\n", s.Name, blockingLease)
					continue
				}

				safe = append(safe, s)
			}
			if len(safe) == 0 {
				continue
			}

			batchResults := LaunchWorkers(cfg, SliceManifest{Slices: safe}, opts, stdout)
			for _, r := range batchResults {
				if r.Status != WorkerDone {
					continue
				}
				if repoRootErr != nil {
					// Determining repoRoot failed once, up front -- every
					// WorkerDone slice across every batch degrades to a
					// failed integration, but dispatch itself still
					// proceeds (workers already ran and succeeded on their
					// own terms).
					integrations[r.Slice] = integrationOutcome{status: integrateFailed, err: repoRootErr}
					notLanded[r.Slice] = true
					state.UnlandedSlices = appendUnique(state.UnlandedSlices, r.Slice)
					continue
				}
				branch := workerBranchName(r.Slice)
				status, out, err := integrateSliceBranch(repoRoot, r.Slice, branch)
				outcome := integrationOutcome{status: status, output: out, err: err}
				if status == integrateConflict {
					// Freeze the merge-base-vs-HEAD range for the
					// human/coordinator-facing guidance NOW, while
					// repoRoot's HEAD is still whatever this slice's own
					// worktree was actually rooted on -- integrateSliceBranch
					// aborts its own cherry-pick on conflict, so HEAD is
					// untouched by this call, but squashIntegrationCommits
					// (below, once every batch has run) rewrites HEAD via
					// `git reset --soft`, orphaning any interim per-slice
					// integration commit a LATER batch's worker branch was
					// itself rooted on. Recomputing this merge-base after
					// that rewrite would silently walk past the orphaned
					// commit to an earlier, shared ancestor and hand the
					// coordinator a range that replays an already-squashed
					// slice's own diff a second time -- an empty cherry-pick
					// git refuses outright (issue #2060 review finding).
					if mergeBaseOut, mbErr := runGitIn(repoRoot, "merge-base", "HEAD", branch); mbErr == nil {
						outcome.revRange = strings.TrimSpace(mergeBaseOut) + ".." + branch
					}
				}
				integrations[r.Slice] = outcome
				if status == integrateOK {
					integratedNames = append(integratedNames, r.Slice)
				}
				if status == integrateConflict || status == integrateFailed {
					notLanded[r.Slice] = true
					state.UnlandedSlices = appendUnique(state.UnlandedSlices, r.Slice)
				}
			}
			results = append(results, batchResults...)
		}

		// Every batch in this manifest has now been dispatched and
		// integrated -- squash whatever per-slice integration commits
		// landed on repoRoot's HEAD during the loop above into one final
		// commit, so this pass lands as a single coherent change rather
		// than one commit per slice (issue #2060). A squash failure is
		// orchestrator-internal plumbing, not a per-slice outcome, so it is
		// logged rather than folded into any one slice's own finding.
		if repoRootErr == nil && startHead != "" {
			if err := squashIntegrationCommits(repoRoot, startHead, integratedNames); err != nil {
				fmt.Fprintln(os.Stderr, "orchestrator: squash manifest integration commits:", err)
			}
		}
	}

	for _, r := range results {
		switch r.Status {
		case WorkerDone:
			// A slice may already sit in RemainingSlices from a prior
			// dispatch that timed out or crashed it -- remove it there
			// before recording success, and dedup the append, so a
			// retried-then-successful slice never ends up listed in both
			// DoneSlices and RemainingSlices at once, nor duplicated within
			// DoneSlices itself (issue #2059 review finding B). This move
			// happens regardless of the slice's own integration outcome
			// below -- the worker's own job is done either way, and its
			// branch is never deleted, so a conflicted/failed integration
			// can still be resolved manually later from the same branch
			// name (issue #2060).
			state.RemainingSlices = removeSlice(state.RemainingSlices, r.Slice)
			state.DoneSlices = appendUnique(state.DoneSlices, r.Slice)

			branch := workerBranchName(r.Slice)
			outcome := integrations[r.Slice]
			switch outcome.status {
			case integrateEmpty:
				fmt.Fprintf(&findings, "- %s: done, nothing to integrate (branch %s had no new commits)\n", r.Slice, branch)
			case integrateConflict:
				// The guidance itself is sourced from
				// templates/default/prompts/conflict-resolve-cherry-pick-
				// prompt.md (conflictResolveGuidance, issue #2060 review
				// finding) rather than hand-written here, so a
				// human/coordinator reading this finding sees the exact same
				// conflict-resolution steps this repo's own rebase-conflict
				// path already gives (conflict-resolve-prompt.md), adapted
				// for cherry-pick. revRange is the batch loop's own frozen
				// outcome.revRange (a concrete merge-base SHA computed
				// before squashIntegrationCommits rewrote HEAD), not a
				// literal `$(git merge-base HEAD <branch>)..<branch>`
				// shell expression re-evaluated here: re-evaluating it
				// against THIS pass's post-squash HEAD would walk past the
				// orphaned interim commit this slice's branch was rooted on
				// to a shared ancestor further back, hand the coordinator a
				// range that replays an already-squashed slice a second
				// time, and halt on git's own empty-cherry-pick guard
				// (issue #2060 review finding). Fall back to the live
				// shell-expression form only in the unexpected case the
				// batch loop's own merge-base call itself failed and left
				// outcome.revRange empty.
				revRange := outcome.revRange
				if revRange == "" {
					revRange = fmt.Sprintf("$(git merge-base HEAD %s)..%s", branch, branch)
				}
				fmt.Fprintf(&findings, "- %s: done, but integration conflicted (branch %s)\n", r.Slice, branch)
				guidance := conflictResolveGuidance(branch, revRange)
				fmt.Fprintf(&findings, "  %s\n", strings.ReplaceAll(guidance, "\n", "\n  "))
				if out := truncateRunes(strings.TrimSpace(outcome.output), maxWorkerResultInFindings); out != "" {
					fmt.Fprintf(&findings, "  %s\n", strings.ReplaceAll(out, "\n", "\n  "))
				}
			case integrateFailed:
				// Mirror the integrateConflict case's shape exactly: a
				// short, one-line outcome, then -- separately -- an
				// indented, truncated block for the actual error text.
				// integrateSliceBranch's own dirty-tree error
				// (integrate.go) embeds a full `git status --porcelain`
				// dump, which can be arbitrarily long; appending it raw
				// and un-indented onto this one line could blow past
				// maxWorkerResultInFindings and inject content that reads
				// like additional top-level findings lines into the block
				// seeded into the next coordinator pass's own prompt
				// (issue #2060 review finding).
				fmt.Fprintf(&findings, "- %s: done, but integration failed\n", r.Slice)
				errMsg := ""
				if outcome.err != nil {
					errMsg = outcome.err.Error()
				}
				if errMsg = strings.TrimSpace(errMsg); errMsg != "" {
					errMsg = truncateRunes(errMsg, maxWorkerResultInFindings)
					fmt.Fprintf(&findings, "  %s\n", strings.ReplaceAll(errMsg, "\n", "\n  "))
				}
			case integrateOK:
				fmt.Fprintf(&findings, "- %s: done, integrated (branch %s)\n", r.Slice, branch)
			default:
				fmt.Fprintf(&findings, "- %s: done, integration outcome %q unrecognized\n", r.Slice, outcome.status)
			}

			if result := strings.TrimSpace(r.Result); result != "" {
				result = truncateRunes(result, maxWorkerResultInFindings)
				fmt.Fprintf(&findings, "  %s\n", strings.ReplaceAll(result, "\n", "\n  "))
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

// notLandedLeaseOverlap reports the name of the first not-landed slice
// (per notLanded) in dispatchSlices, in manifest-processing order, whose own
// normalized FileLeases (per sliceLeases) are not provably disjoint from
// s's own -- or the empty string if none overlap. Matching canJoin's own
// conservative rule (schedule.go), an undeclared-lease slice on either side
// is treated as touching everything, so it always counts as overlapping.
// Only names present in sliceLeases are considered: a not-landed name
// seeded from a prior pass's state.UnlandedSlices has no known leases this
// pass, so lease-overlap gating against it is deliberately out of scope
// (issue #2060 review finding: Bug B) -- the DependsOn-name check above
// already covers that cross-pass case.
func notLandedLeaseOverlap(s ManifestSlice, dispatchSlices []ManifestSlice, sliceLeases map[string][]string, notLanded map[string]bool) string {
	sLeases := sliceLeases[s.Name]
	for _, other := range dispatchSlices {
		if other.Name == s.Name || !notLanded[other.Name] {
			continue
		}
		otherLeases, known := sliceLeases[other.Name]
		if !known {
			continue
		}
		if len(sLeases) == 0 || len(otherLeases) == 0 {
			return other.Name
		}
		for _, lease := range sLeases {
			for _, otherLease := range otherLeases {
				if leasesOverlap(lease, otherLease) {
					return other.Name
				}
			}
		}
	}
	return ""
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

// squashIntegrationCommits collapses every per-slice integration commit
// dispatchManifestIfPresent landed on repoRoot's HEAD since startHead into
// one final commit, naming every slice in integratedNames -- issue #2060
// review finding: without this, an N-slice manifest lands N separate
// "chore(orchestrator): integrate slice <name>" commits instead of the one
// coherent change a human/coordinator-driven integration is documented to
// produce (templates/default/prompts/fragments/coordinator.md).
//
// Those per-slice commits still MUST land as real commits during the batch
// loop itself, though, not merely staged/uncommitted -- squashing can only
// happen here, once, after every batch in the manifest has been dispatched
// AND integrated. A later batch's own workers are created via `git worktree
// add ... HEAD` (workers.go), and a new worktree only ever sees committed
// history on its shared repository's refs, never another worktree's
// uncommitted index -- so deferring ALL commits to the very end of the
// manifest would leave batch N+1's worktree unable to see batch N's
// changes at all. squashIntegrationCommits is safe to call only after the
// last batch has already integrated, exactly where
// dispatchManifestIfPresent calls it.
//
// If HEAD is still at startHead (nothing integrated this pass -- every
// slice was integrateEmpty/integrateConflict/integrateFailed, or
// dispatchSlices was empty), this is a deliberate no-op: no squash commit
// is created, since `git reset --soft` followed immediately by `git
// commit` with nothing to commit would either fail or, worse, silently
// walk HEAD forward via an empty commit. Otherwise it runs `git reset
// --soft startHead` (safe because integrateSliceBranch never leaves
// repoRoot's working tree dirty, either mid-loop or once the last batch
// has finished) followed by one `git commit` naming every slice in
// integratedNames, in order, so the squashed commit is traceable back to
// exactly which slices contributed to it.
func squashIntegrationCommits(repoRoot, startHead string, integratedNames []string) error {
	headOut, err := runGitIn(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("squash integration commits: rev-parse HEAD: %w: %s", err, strings.TrimSpace(headOut))
	}
	if strings.TrimSpace(headOut) == startHead {
		// Nothing landed on HEAD this pass -- no squash commit to make.
		return nil
	}

	if resetOut, err := runGitIn(repoRoot, "reset", "--soft", startHead); err != nil {
		return fmt.Errorf("squash integration commits: reset --soft %s: %w: %s", startHead, err, strings.TrimSpace(resetOut))
	}

	msg := fmt.Sprintf("chore(orchestrator): integrate manifest slices\n\nIntegrated: %s", strings.Join(integratedNames, ", "))
	if commitOut, err := runGitIn(repoRoot, "commit", "-m", msg); err != nil {
		return fmt.Errorf("squash integration commits: commit: %w: %s", err, strings.TrimSpace(commitOut))
	}
	return nil
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
