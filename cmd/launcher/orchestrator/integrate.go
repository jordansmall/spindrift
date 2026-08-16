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
//   - A cherry-pick conflict is an expected outcome, not a Go error: the
//     in-progress cherry-pick is aborted (leaving the working tree clean)
//     and the conflict's own combined output is returned as the second
//     value, for a caller to fold into a human/coordinator-facing finding
//     -- exactly what today's manual-cherry-pick instructions already tell
//     a human to do by hand.
//   - Any other unexpected git failure (merge-base/rev-list/commit itself
//     erroring) is a real Go error: returns (integrateFailed, "", err).
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
		// Expected outcome, not a Go error -- abort the in-progress
		// cherry-pick so the working tree is left exactly as it was
		// before this call, then report the conflict's own output for a
		// human/coordinator to resolve manually.
		if abortOut, abortErr := runGitIn(repoRoot, "cherry-pick", "--abort"); abortErr != nil {
			// The abort itself failing is genuinely unexpected -- the repo
			// may now be left mid-cherry-pick, so this is a hard failure
			// rather than a reportable-as-conflict outcome.
			return integrateFailed, "", fmt.Errorf("integrate slice %s: cherry-pick --abort after conflict: %w: %s", sliceName, abortErr, strings.TrimSpace(abortOut))
		}
		return integrateConflict, strings.TrimSpace(cherryOut), nil
	}

	// The cherry-pick applied cleanly, but if branch's net diff was
	// already present on HEAD (e.g. an earlier slice already landed an
	// identical change), nothing actually got staged -- `git commit` would
	// exit 1 ("nothing to commit"), which is a benign no-op, not a
	// failure. Check the index rather than let commit fail and try to
	// pattern-match its message.
	stagedOut, err := runGitIn(repoRoot, "diff", "--cached", "--name-only")
	if err != nil {
		return integrateFailed, "", fmt.Errorf("integrate slice %s: diff --cached: %w: %s", sliceName, err, strings.TrimSpace(stagedOut))
	}
	if strings.TrimSpace(stagedOut) == "" {
		// Nothing staged -- mirror the empty-rev-list guard above and
		// report integrateEmpty without ever calling commit. A cherry-pick
		// that stages nothing still leaves no sequencer state behind (no
		// CHERRY_PICK_HEAD, clean `git status --porcelain`), but guard
		// against that changing underfoot by cleaning up defensively the
		// same way the conflict path does.
		if leftoverOut, leftoverErr := runGitIn(repoRoot, "status", "--porcelain"); leftoverErr == nil && strings.TrimSpace(leftoverOut) != "" {
			if abortOut, abortErr := runGitIn(repoRoot, "cherry-pick", "--abort"); abortErr != nil {
				return integrateFailed, "", fmt.Errorf("integrate slice %s: cherry-pick --abort after no-op apply: %w: %s", sliceName, abortErr, strings.TrimSpace(abortOut))
			}
		}
		return integrateEmpty, "", nil
	}

	msg := fmt.Sprintf("chore(orchestrator): integrate slice %s\n\nCherry-picked from %s.", sliceName, branch)
	if commitOut, err := runGitIn(repoRoot, "commit", "-m", msg); err != nil {
		return integrateFailed, "", fmt.Errorf("integrate slice %s: commit: %w: %s", sliceName, err, strings.TrimSpace(commitOut))
	}

	return integrateOK, "", nil
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
