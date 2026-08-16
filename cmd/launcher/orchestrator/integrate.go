package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// integrateStatus is the outcome integrateSliceBranch reports for one
// slice's branch integration attempt (issue #2060).
type integrateStatus string

const (
	// integrateOK means branch's own commits past its merge-base with HEAD
	// landed cleanly as one new commit on HEAD.
	integrateOK integrateStatus = "integrated"
	// integrateEmpty means branch carried no commits past its merge-base
	// with HEAD -- there was nothing to integrate, and HEAD is untouched.
	integrateEmpty integrateStatus = "empty"
	// integrateConflict means the cherry-pick hit a conflict; it was
	// aborted, leaving the working tree exactly as it was before this call,
	// for a human/coordinator to resolve manually.
	integrateConflict integrateStatus = "conflict"
	// integrateFailed means some other git failure occurred (merge-base,
	// rev-list, or the final commit itself erroring) -- distinct from
	// integrateConflict, which is an expected outcome, not a failure.
	integrateFailed integrateStatus = "failed"
)

// integrateSliceBranch cherry-picks branch's own commits past its
// merge-base with HEAD onto the orchestrator's own current HEAD in
// repoRoot, landing them as one real commit when they apply cleanly. This
// automates -- for the clean, no-conflict case -- the exact manual git
// sequence this repo's own coordinator-facing instructions already
// describe for cherry-picking a completed worker's branch (issue #2060):
//
//   - `git merge-base HEAD <branch>` finds the common ancestor.
//   - `git rev-list <merge-base>..<branch>` checks whether branch actually
//     carries any commits past that point; an empty result (mirroring the
//     documented `git rev-list ... | fatal: empty commit set` guard) means
//     there is nothing to integrate -- returns (integrateEmpty, "", nil)
//     without ever attempting a cherry-pick.
//   - Otherwise `git cherry-pick --no-commit <merge-base>..<branch>` stages
//     every commit in that range without committing any of them
//     individually, so a single "chore(orchestrator): integrate slice
//     <sliceName>" commit lands the whole range as one real commit on
//     HEAD -- a real commit, not just a staged/uncommitted change, because
//     a later batch's own `git worktree add ... HEAD` (workers.go) needs to
//     see it: worktrees share the repository's objects/refs but not an
//     uncommitted index.
//   - A cherry-pick conflict -- diagnosed by unmerged paths in `git status
//     --porcelain` after the cherry-pick fails, not merely a non-zero exit
//     -- is an expected outcome, not a Go error: the in-progress cherry-pick
//     is aborted (leaving the working tree clean) and the conflict's own
//     combined output is returned as the second value, for a caller to fold
//     into a human/coordinator-facing finding -- exactly what today's
//     manual-cherry-pick instructions already tell a human to do by hand.
//     A cherry-pick can also fail for reasons that are NOT a content
//     conflict -- e.g. revRange contains a merge commit, which git rejects
//     without ever entering a real conflict state -- and those are treated
//     as a real Go error instead (see below).
//   - Any other unexpected git failure (merge-base/rev-list/a non-conflict
//     cherry-pick failure/diff --cached/commit itself erroring) is a real Go
//     error: returns (integrateFailed, "", err). Any of these that leaves
//     the cherry-pick staged/mid-sequence in repoRoot is cleaned up (aborted)
//     first, so the failure never cascades into a later slice's own
//     dirty-tree guard below.
//
// Before doing anything else, integrateSliceBranch refuses to run at all if
// repoRoot already has pre-existing uncommitted/staged changes: `git
// cherry-pick --abort` on the conflict path resets the index/tree to
// whatever they were when the cherry-pick started, which is only "exactly
// as it was before this call" if that starting point was clean -- on a
// dirty repoRoot, --abort can silently discard pre-existing staged work,
// and a successful cherry-pick's own `git commit` would sweep that
// unrelated staged work into the integration commit. Both failure modes
// are avoided by simply never starting on a dirty tree.
//
// Every git invocation runs with its working directory set to repoRoot.
func integrateSliceBranch(repoRoot, sliceName, branch string) (integrateStatus, string, error) {
	statusOut, err := runGitIn(repoRoot, "status", "--porcelain")
	if err != nil {
		return integrateFailed, "", fmt.Errorf("integrate slice %s: status: %w: %s", sliceName, err, strings.TrimSpace(statusOut))
	}
	if strings.TrimSpace(statusOut) != "" {
		return integrateFailed, "", fmt.Errorf("integrate slice %s: repoRoot %s has pre-existing uncommitted changes, refusing to integrate to avoid discarding them:\n%s", sliceName, repoRoot, strings.TrimSpace(statusOut))
	}

	mergeBaseOut, err := runGitIn(repoRoot, "merge-base", "HEAD", branch)
	if err != nil {
		return integrateFailed, "", fmt.Errorf("integrate slice %s: merge-base: %w: %s", sliceName, err, strings.TrimSpace(mergeBaseOut))
	}
	mergeBase := strings.TrimSpace(mergeBaseOut)
	revRange := mergeBase + ".." + branch

	revListOut, err := runGitIn(repoRoot, "rev-list", revRange)
	if err != nil {
		return integrateFailed, "", fmt.Errorf("integrate slice %s: rev-list: %w: %s", sliceName, err, strings.TrimSpace(revListOut))
	}
	if strings.TrimSpace(revListOut) == "" {
		// branch made no commits past the merge-base -- nothing to
		// integrate, and no cherry-pick is ever attempted.
		return integrateEmpty, "", nil
	}

	cherryOut, err := runGitIn(repoRoot, "cherry-pick", "--no-commit", revRange)
	if err != nil {
		// A non-zero exit here is not always a content conflict -- e.g. if
		// revRange includes a merge commit, git errors with "is a merge
		// but no -m option was given" without ever entering a real
		// conflict/sequencer state. Only unmerged paths in `git status
		// --porcelain` (any line starting with "U", or "AA"/"DD" for
		// both-added/both-deleted) mean this is a genuine conflict; any
		// other failure is misdiagnosed as one (issue #2060 review
		// finding), which sends the coordinator "resolve manually" guidance
		// that's literally the command that just failed.
		conflictStatusOut, statusErr := runGitIn(repoRoot, "status", "--porcelain")
		if statusErr == nil && hasUnmergedPaths(conflictStatusOut) {
			// Expected outcome, not a Go error -- abort the in-progress
			// cherry-pick so the working tree is left exactly as it was
			// before this call, then report the conflict's own output for
			// a human/coordinator to resolve manually.
			if abortErr := abortCherryPick(repoRoot); abortErr != nil {
				return integrateFailed, "", fmt.Errorf("integrate slice %s: cherry-pick --abort after conflict: %w", sliceName, abortErr)
			}
			return integrateConflict, strings.TrimSpace(cherryOut), nil
		}

		// Not a genuine conflict -- a real Go error. Defensively clean up
		// first: if the failure left anything dirty/mid-sequence behind
		// (e.g. the merge-commit case, which leaves the merge's own
		// changes staged), abort it the same way the conflict path does,
		// folding any abort failure into the returned error rather than
		// losing it silently.
		cherryErr := fmt.Errorf("integrate slice %s: cherry-pick --no-commit: %w: %s", sliceName, err, strings.TrimSpace(cherryOut))
		if leftoverOut, leftoverErr := runGitIn(repoRoot, "status", "--porcelain"); leftoverErr == nil && strings.TrimSpace(leftoverOut) != "" {
			if abortErr := abortCherryPick(repoRoot); abortErr != nil {
				return integrateFailed, "", fmt.Errorf("%w; cherry-pick --abort after non-conflict failure: %v", cherryErr, abortErr)
			}
		}
		return integrateFailed, "", cherryErr
	}

	// The cherry-pick applied cleanly, but if branch's net diff was
	// already present on HEAD (e.g. an earlier slice already landed an
	// identical change), nothing actually got staged -- `git commit` would
	// exit 1 ("nothing to commit"), which is a benign no-op, not a
	// failure. Check the index rather than let commit fail and try to
	// pattern-match its message.
	stagedOut, err := runGitIn(repoRoot, "diff", "--cached", "--name-only")
	if err != nil {
		diffErr := fmt.Errorf("integrate slice %s: diff --cached: %w: %s", sliceName, err, strings.TrimSpace(stagedOut))
		// The cherry-pick itself applied cleanly, so it left the cherry-pick
		// staged/mid-sequence in repoRoot; clean that up before returning,
		// or a later slice's own integration trips the dirty-tree guard
		// above (issue #2060 review finding).
		if abortErr := abortCherryPick(repoRoot); abortErr != nil {
			return integrateFailed, "", fmt.Errorf("%w; cherry-pick --abort after diff --cached failure: %v", diffErr, abortErr)
		}
		return integrateFailed, "", diffErr
	}
	if strings.TrimSpace(stagedOut) == "" {
		// Nothing staged -- mirror the empty-rev-list guard above and
		// report integrateEmpty without ever calling commit. A cherry-pick
		// that stages nothing still leaves no sequencer state behind (no
		// CHERRY_PICK_HEAD, clean `git status --porcelain`), but guard
		// against that changing underfoot by cleaning up defensively the
		// same way the conflict path does.
		if leftoverOut, leftoverErr := runGitIn(repoRoot, "status", "--porcelain"); leftoverErr == nil && strings.TrimSpace(leftoverOut) != "" {
			if abortErr := abortCherryPick(repoRoot); abortErr != nil {
				return integrateFailed, "", fmt.Errorf("integrate slice %s: cherry-pick --abort after no-op apply: %w", sliceName, abortErr)
			}
		}
		return integrateEmpty, "", nil
	}

	msg := fmt.Sprintf("chore(orchestrator): integrate slice %s\n\nCherry-picked from %s.", sliceName, branch)
	if commitOut, err := runGitIn(repoRoot, "commit", "-m", msg); err != nil {
		commitErr := fmt.Errorf("integrate slice %s: commit: %w: %s", sliceName, err, strings.TrimSpace(commitOut))
		// The cherry-pick's staged changes are still sitting in the index
		// after a failed commit (e.g. a rejecting commit-msg hook) -- clean
		// them up so they don't cascade into a later slice's own
		// dirty-tree refusal (issue #2060 review finding).
		if abortErr := abortCherryPick(repoRoot); abortErr != nil {
			return integrateFailed, "", fmt.Errorf("%w; cherry-pick --abort after commit failure: %v", commitErr, abortErr)
		}
		return integrateFailed, "", commitErr
	}

	return integrateOK, "", nil
}

// hasUnmergedPaths reports whether porcelainOut -- the output of `git
// status --porcelain` -- shows any path left unmerged by an in-progress
// cherry-pick/merge: any line starting with "U", or "AA"/"DD"/"AU"/"DU" for
// both-added/both-deleted/add-vs-unmerged/delete-vs-unmerged, per
// git-status(1)'s "Unmerged" table -- the full set of porcelain conflict
// codes, not just the two an add/delete-shaped conflict never produces.
// Used to distinguish a genuine cherry-pick content conflict from any other
// cherry-pick failure (e.g. a merge commit in the picked range), which
// never enters this state at all.
func hasUnmergedPaths(porcelainOut string) bool {
	for _, line := range strings.Split(porcelainOut, "\n") {
		if len(line) < 2 {
			continue
		}
		code := line[:2]
		if strings.HasPrefix(code, "U") || code == "AA" || code == "DD" || code == "AU" || code == "DU" {
			return true
		}
	}
	return false
}

// abortCherryPick cleans up an in-progress or already-applied
// `cherry-pick --no-commit` in dir, returning an error that folds in
// whatever failed if cleanup doesn't succeed -- the shared "abort and fold
// failure into an error" pattern integrateSliceBranch uses at every point
// it needs to clean up a cherry-pick before returning a failure, so a
// cleanup failure is never silently lost.
//
// It tries `git cherry-pick --abort` first, which is enough whenever the
// cherry-pick left sequencer state behind (e.g. a multi-commit revRange).
// A cherry-pick of a *single*-commit revRange applies and stages directly
// without ever touching the sequencer, though, so `--abort` on that case
// fails with "no cherry-pick or revert in progress" despite there still
// being a staged apply to undo; abortCherryPick falls back to `git reset
// --hard HEAD` for that case. That fallback is safe precisely because of
// integrateSliceBranch's own invariants: it never moves HEAD before a
// successful commit, and it already refused to run at all on a dirty
// repoRoot, so resetting to HEAD can only discard this call's own
// cherry-pick apply -- never pre-existing, unrelated work.
func abortCherryPick(dir string) error {
	abortOut, abortErr := runGitIn(dir, "cherry-pick", "--abort")
	if abortErr == nil {
		return nil
	}
	if resetOut, resetErr := runGitIn(dir, "reset", "--hard", "HEAD"); resetErr != nil {
		return fmt.Errorf("cherry-pick --abort: %w: %s (fallback git reset --hard HEAD also failed: %v: %s)", abortErr, strings.TrimSpace(abortOut), resetErr, strings.TrimSpace(resetOut))
	}
	return nil
}

// runGitIn runs `git <args...>` with its working directory set to dir,
// returning its combined stdout+stderr output -- mirrors the
// git-invocation convention launchOneWorker already uses in workers.go
// (CombinedOutput plus cmd.Dir).
func runGitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
