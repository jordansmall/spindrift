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
// fetches ref from repoPath itself — finds it. On failure the seam is left
// unlanded, and the two failure cases are distinct: a missing bundle wraps
// forge.ErrBundleNotFound, the benign "Box wrote nothing" case, while a
// bundle that fails `git bundle verify` is a generic error. The fetch refspec
// is forced: a retried seam rebuilt from a rebased branch must overwrite
// whatever an earlier, abandoned attempt left at the same ref.
func relayBundle(repoPath, outboxDir, ref string) error {
	// Defense in depth: settle derives ref host-side and never forwards the
	// outcome line's own landing= field, but it interpolates directly into a
	// refspec, so guard it regardless of that holding upstream.
	if ref == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("local: invalid ref %q", ref)
	}
	bundlePath := filepath.Join(outboxDir, seambundle.FileName)
	if _, err := os.Stat(bundlePath); err != nil {
		// An absent outbox dir collapses into this same case: "no dir" means
		// "nothing to relay" just as "no bundle file" does.
		if os.IsNotExist(err) {
			return fmt.Errorf("local: bundle relay: %w: %s", forge.ErrBundleNotFound, bundlePath)
		}
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

// ensureIntegrationBranch creates integrationBranch in repoPath at
// baseBranch's current tip when it doesn't already exist. Merge assumes its
// base branch exists — safe for git/github's real remotes, but not for a
// freshly seeded Accumulation repo, which SeedAccumulationRepo only seeds
// baseBranch into, so the first seam of a broad ticket would find nothing.
// A no-op once some seam has landed.
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
// to the rebased result — localCodeForge's Merge override (ADR 0033), which
// keeps the Integration branch linear with zero merge commits. It works
// through a throwaway clone because a rebase needs a working tree a bare repo
// doesn't have, but every command touching repoPath runs directly against it
// rather than via `git push` from the clone: a push's receiving side is a
// separate `git-receive-pack` process that doesn't reliably honor the pushing
// command's `-c maintenance.auto=false`, leaving repoPath open to the
// detached `git maintenance --auto` race.
//
// Returns forge.ErrMergeConflict, leaving integrationBranch untouched, when
// the rebase cannot complete automatically — every rebase failure is treated
// as a conflict rather than pattern-matching stderr. The final
// integrationBranch update is an atomic compare-and-swap against the tip this
// call started from, so a concurrent land in between is refused outright
// rather than silently overwritten.
//
// userName/userEmail configure the clone's commit identity: rebase re-commits
// each replayed commit under the current committer, so a clone with no
// ambient git config would fail with "please tell me who you are".
func rebaseLand(repoPath, branch, integrationBranch, userName, userEmail string) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("local: invalid ref %q", branch)
	}
	integrationRef := "refs/heads/" + integrationBranch
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", integrationRef).CombinedOutput()
	if err != nil {
		return fmt.Errorf("local: resolve %s: %w: %s", integrationBranch, err, out)
	}
	oldTip := strings.TrimSpace(string(out))

	dir, err := os.MkdirTemp("", "spindrift-local-forge-land-*")
	if err != nil {
		return fmt.Errorf("local: mkdtemp: %w", err)
	}
	defer os.RemoveAll(dir)

	// Cloning a repo that's crossed the loose-object threshold can fork a
	// detached `git maintenance --auto` still repacking when the deferred
	// os.RemoveAll(dir) (or a caller's t.TempDir cleanup) runs.
	if out, err := exec.Command("git", "-c", "maintenance.auto=false", "clone", repoPath, dir).CombinedOutput(); err != nil {
		return fmt.Errorf("local: clone %s: %w: %s", repoPath, err, out)
	}
	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir, "-c", "maintenance.auto=false"}, args...)...)
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

	// Forced, like relayBundle's refspec: a retry may diverge from whatever
	// this same branch left there before.
	branchRefspec := "+refs/heads/" + branch + ":refs/heads/" + branch
	if out, err := exec.Command("git", "-C", repoPath, "-c", "maintenance.auto=false", "fetch", dir, branchRefspec).CombinedOutput(); err != nil {
		return fmt.Errorf("local: fetch rebased %s: %w: %s", branch, err, out)
	}
	if out, err := exec.Command("git", "-C", repoPath, "update-ref", integrationRef, "refs/heads/"+branch, oldTip).CombinedOutput(); err != nil {
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

// parseLandingRef splits landingRef's "<branch>@<sha>" output back into its
// parts. ok is false for anything that doesn't match that shape — notably the
// raw agent-branch name settle records before a merge is attempted, which
// never contains "@" — and for a sha starting with "-", rejected here rather
// than trusted to isMergedIntoIntegration's "--" guard.
func parseLandingRef(landing string) (branch, sha string, ok bool) {
	branch, sha, found := strings.Cut(landing, "@")
	if !found || branch == "" || sha == "" || strings.HasPrefix(sha, "-") {
		return "", "", false
	}
	return branch, sha, true
}

// branchTipSHA resolves branch's current tip commit sha inside repoPath. ok
// is false with a nil error when branch doesn't exist there, which
// LandingContained treats as contained=false rather than a hard error. A
// failure that isn't itself a verdict (git can't even run) returns a real
// error instead.
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
// observation LandingContained relies on (ADR 0029, ADR 0033). Ancestry, not
// tip equality, because a sibling seam landing after this one moves
// integrationBranch's tip forward without un-merging this commit. A
// non-ancestor result (unknown sha, or genuinely not merged) reports false
// with a nil error; only a git invocation failure that isn't itself a verdict
// is a real error.
func isMergedIntoIntegration(repoPath, sha, integrationBranch string) (bool, error) {
	// sha comes from a parsed landing ref, so "--" guards it against being
	// misread as a git option.
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

// patchEquivalentToIntegration reports whether every one of sha's commits is
// already present, patch-for-patch, on integrationBranch's current tip inside
// repoPath — LandingContained's fallback for a rebased-and-landed sha, whose
// replay gives every commit a new sha raw ancestry can never see again.
// A bundle relays a branch's entire base..branch range, so sha routinely
// carries more than one commit, and a single `git cherry` "+" anywhere in
// that list means the seam as a whole hasn't landed even if an earlier commit
// has. An unknown sha yields false rather than a real error.
func patchEquivalentToIntegration(repoPath, sha, integrationBranch string) (bool, error) {
	out, err := exec.Command("git", "-C", repoPath, "cherry", "--", "refs/heads/"+integrationBranch, sha).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return false, fmt.Errorf("local: cherry %s %s: %w", integrationBranch, sha, err)
		}
		return false, nil
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return true, nil
	}
	for _, line := range strings.Split(trimmed, "\n") {
		if strings.HasPrefix(line, "+") {
			return false, nil
		}
	}
	return true, nil
}
