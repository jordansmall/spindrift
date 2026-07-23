package local

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/seambundle"
)

// relayBundle imports ref from the git bundle the Box left in outboxDir into
// repoPath (the bare Accumulation repo), so a subsequent Merge(ref) — which
// fetches ref from repoPath itself — finds it. Returns an error, leaving the
// seam unlanded, when the bundle is missing or fails `git bundle verify`
// (the prerequisite commit(s) it was built against aren't reachable from
// repoPath, or its contents are corrupt). The fetch refspec is forced: a
// retried seam (the Box crashed and re-dispatched, rebuilding its bundle from
// a rebased branch) must be able to overwrite whatever an earlier, abandoned
// attempt already left at the same ref, even when the new history diverges
// from it.
func relayBundle(repoPath, outboxDir, ref string) error {
	// Defense in depth, matching the git adapter's own validateGitRef: ref is
	// launcher-controlled today (AgentBranch's own naming), but this method
	// interpolates it directly into a refspec, so guard it the same way
	// regardless.
	if ref == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("local: invalid ref %q", ref)
	}
	bundlePath := filepath.Join(outboxDir, seambundle.FileName)
	if _, err := os.Stat(bundlePath); err != nil {
		return fmt.Errorf("local: bundle relay: %w", err)
	}
	if out, err := exec.Command("git", "-C", repoPath, "bundle", "verify", bundlePath).CombinedOutput(); err != nil {
		return fmt.Errorf("local: malformed bundle %s: %w: %s", bundlePath, err, out)
	}
	refspec := "+" + ref + ":refs/heads/" + ref
	if out, err := exec.Command("git", "-C", repoPath, "-c", "maintenance.auto=false", "fetch", bundlePath, refspec).CombinedOutput(); err != nil {
		return fmt.Errorf("local: fetch bundle %s: %w: %s", bundlePath, err, out)
	}
	return nil
}

// ensureIntegrationBranch creates integrationBranch in repoPath, pointing at
// baseBranch's current tip, when it doesn't already exist — the very first
// seam of a broad ticket lands before any earlier seam has created
// integration/<parent>, and Merge assumes its base branch already exists
// (a safe assumption for git/github's real remotes, not for a freshly seeded
// Accumulation repo, which SeedAccumulationRepo only ever seeds baseBranch
// into). A no-op once some seam has landed and the branch exists.
func ensureIntegrationBranch(repoPath, baseBranch, integrationBranch string) error {
	verify := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+integrationBranch)
	if err := verify.Run(); err == nil {
		return nil
	}
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "refs/heads/"+baseBranch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("local: resolve base branch %s: %w: %s", baseBranch, err, out)
	}
	sha := strings.TrimSpace(string(out))
	if out, err := exec.Command("git", "-C", repoPath, "update-ref", "refs/heads/"+integrationBranch, sha).CombinedOutput(); err != nil {
		return fmt.Errorf("local: create integration branch %s: %w: %s", integrationBranch, err, out)
	}
	return nil
}

// rebaseLand rebases branch onto integrationBranch's current tip inside
// repoPath (the bare Accumulation repo) and fast-forwards integrationBranch
// to the rebased result — localCodeForge's Merge override (ADR 0033, issue
// #1889): unlike the shared git adapter's `git merge --no-ff`, this keeps
// the Integration branch linear with zero merge commits. Works through a
// throwaway clone rather than operating on repoPath directly, since a
// rebase needs a working tree a bare repo doesn't have.
//
// Returns forge.ErrMergeConflict, leaving integrationBranch untouched, when
// the rebase itself cannot complete automatically — every rebase failure is
// treated as a conflict, matching forge/git.Rebase's own precedent for a
// fetched, well-formed ref, rather than pattern-matching stderr. The
// fast-forward push after a successful rebase is deliberately non-forced: it
// only ever succeeds when HEAD is genuinely a descendant of
// integrationBranch's tip, so a would-be non-fast-forward (another seam
// landed onto integrationBranch between this rebase's start and its push) is
// refused outright rather than silently overwritten.
//
// userName/userEmail configure the clone's commit identity: rebase re-commits
// each replayed commit under the current committer, so a clone with no
// ambient git config would otherwise fail outright with "please tell me who
// you are" rather than landing cleanly.
func rebaseLand(repoPath, branch, integrationBranch, userName, userEmail string) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("local: invalid ref %q", branch)
	}
	dir, err := os.MkdirTemp("", "spindrift-local-forge-land-*")
	if err != nil {
		return fmt.Errorf("local: mkdtemp: %w", err)
	}
	defer os.RemoveAll(dir)

	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir}, args...)...)
	}
	if out, err := exec.Command("git", "clone", repoPath, dir).CombinedOutput(); err != nil {
		return fmt.Errorf("local: clone %s: %w: %s", repoPath, err, out)
	}
	if out, err := gitIn("config", "user.name", userName).CombinedOutput(); err != nil {
		return fmt.Errorf("local: config user.name: %w: %s", err, out)
	}
	if out, err := gitIn("config", "user.email", userEmail).CombinedOutput(); err != nil {
		return fmt.Errorf("local: config user.email: %w: %s", err, out)
	}
	if out, err := gitIn("checkout", branch).CombinedOutput(); err != nil {
		return fmt.Errorf("local: checkout %s: %w: %s", branch, err, out)
	}
	if err := gitIn("rebase", "origin/"+integrationBranch).Run(); err != nil {
		_ = gitIn("rebase", "--abort").Run()
		return forge.ErrMergeConflict
	}
	if out, err := gitIn("push", "origin", "HEAD:refs/heads/"+integrationBranch).CombinedOutput(); err != nil {
		return fmt.Errorf("local: fast-forward %s: %w: %s", integrationBranch, err, out)
	}
	return nil
}

// landingRef resolves branch's current tip commit sha inside repoPath,
// returning "<branch>@<sha>" — the immutable landing: reference ADR
// 0029/0033 expects once a merge has landed onto branch.
func landingRef(repoPath, branch string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "refs/heads/"+branch).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("local: resolve %s sha: %w: %s", branch, err, out)
	}
	return branch + "@" + strings.TrimSpace(string(out)), nil
}

// parseLandingRef splits landing (landingRef's own "<branch>@<sha>" output)
// back into its branch and sha parts. ok is false for anything that doesn't
// match that shape — notably the raw agent-branch name settle records before
// a merge is even attempted (gate.go's early recordLanding call), which never
// contains "@" and so is never mistaken for a landed ref — and for a sha
// starting with "-", rejected outright rather than trusted to isMergedIntoIntegration's
// own "--" guard against it being misread as a git option.
func parseLandingRef(landing string) (branch, sha string, ok bool) {
	branch, sha, found := strings.Cut(landing, "@")
	if !found || branch == "" || sha == "" || strings.HasPrefix(sha, "-") {
		return "", "", false
	}
	return branch, sha, true
}

// branchTipSHA resolves branch's current tip commit sha inside repoPath. ok
// is false, with a nil error, when branch doesn't exist there — `git
// rev-parse --verify --quiet` exits non-zero (via *exec.ExitError) with no
// output for a missing ref, the same "nothing to report" result
// BranchMergedIntoIntegration treats as merged=false rather than a hard
// error. A failure that isn't itself a verdict (git can't even run) is
// distinct — the same distinction isMergedIntoIntegration's own exec.ExitError
// check draws — and returns a real error instead.
func branchTipSHA(repoPath, branch string) (sha string, ok bool, err error) {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output()
	if err == nil {
		return strings.TrimSpace(string(out)), true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "", false, nil
	}
	return "", false, fmt.Errorf("local: rev-parse %s: %w", branch, err)
}

// isMergedIntoIntegration reports whether sha is an ancestor of
// integrationBranch's current tip inside repoPath — the no-network merge
// observation VerifyLanding relies on (ADR 0029, ADR 0033). Ancestry, not
// tip equality, because a sibling seam landing after this one moves
// integrationBranch's tip forward without ever un-merging this commit. A
// non-ancestor result (sha unknown to the repo, or genuinely not merged —
// e.g. the merge that was supposed to record it in fact conflicted) reports
// false with a nil error, the same "not merged" posture as any other
// not-yet-landed seam; only a git invocation failure that isn't itself a
// verdict (the repo path is unreadable, git itself can't run) is a real
// error.
func isMergedIntoIntegration(repoPath, sha, integrationBranch string) (bool, error) {
	// The "--" guard matches relayBundle's own defense in depth: sha comes
	// from a parsed landing ref, so treat it as untrusted input rather than
	// assume it can never start with "-" and be misread as a git option.
	cmd := exec.Command("git", "-C", repoPath, "merge-base", "--is-ancestor", "--", sha, "refs/heads/"+integrationBranch)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, fmt.Errorf("local: merge-base --is-ancestor %s %s: %w", sha, integrationBranch, err)
}
