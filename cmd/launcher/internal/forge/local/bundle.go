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

// relayBundle imports ref from the Box's git bundle in outboxDir into repoPath,
// the bare Accumulation repo, so a later Merge(ref) finds it. A missing bundle
// wraps forge.ErrBundleNotFound, the benign "Box wrote nothing" case; one that
// fails verify, corrupt or built against commits unreachable from repoPath, is a
// generic error. The refspec is forced so a retry can overwrite a diverged ref.
func relayBundle(repoPath, outboxDir, ref string) error {
	// settle derives ref host-side (issue #1949) and never forwards the outcome
	// line's landing= field, but ref interpolates straight into a refspec, so
	// guard it regardless of that guarantee holding upstream.
	if ref == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("local: invalid ref %q", ref)
	}
	bundlePath := filepath.Join(outboxDir, seambundle.FileName)
	if _, err := os.Stat(bundlePath); err != nil {
		// A missing outbox directory also yields os.IsNotExist and means the
		// same thing as a missing bundle file: nothing to relay.
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

// ensureIntegrationBranch creates integrationBranch at baseBranch's tip in
// repoPath when it does not already exist. The first seam of a broad ticket
// lands before any earlier seam has created integration/<parent>, and Merge
// assumes its base branch exists, which holds for real remotes but not for a
// freshly seeded Accumulation repo: SeedAccumulationRepo seeds only baseBranch.
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

// rebaseLand rebases branch onto integrationBranch's tip in repoPath and
// fast-forwards integrationBranch to the result, keeping Integration linear
// with no merge commits (ADR 0033, issue #1889). It works through a throwaway
// clone because a rebase needs a working tree the bare repo lacks. Any rebase
// failure returns forge.ErrMergeConflict and leaves integrationBranch untouched.
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

	// Cloning a repo past the loose-object threshold can fork a detached
	// `git maintenance --auto` that is still repacking when the deferred
	// os.RemoveAll (or a caller's t.TempDir cleanup) runs.
	if out, err := exec.Command("git", "-c", "maintenance.auto=false", "clone", repoPath, dir).CombinedOutput(); err != nil {
		return fmt.Errorf("local: clone %s: %w: %s", repoPath, err, out)
	}
	gitIn := func(args ...string) *exec.Cmd {
		return exec.Command("git", append([]string{"-C", dir, "-c", "maintenance.auto=false"}, args...)...)
	}
	// A rebase re-commits each replayed commit under the current committer, so a
	// clone with no ambient git config fails with "please tell me who you are".
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

	// Fetch rather than push from the clone: a push runs receive-pack on
	// repoPath, which does not reliably honor the pushing command's
	// `-c maintenance.auto=false`. The refspec is forced because a retry may
	// diverge from what this branch left there before, and the update-ref is a
	// compare-and-swap against oldTip, so a concurrent land is refused.
	branchRefspec := "+refs/heads/" + branch + ":refs/heads/" + branch
	if out, err := exec.Command("git", "-C", repoPath, "-c", "maintenance.auto=false", "fetch", dir, branchRefspec).CombinedOutput(); err != nil {
		return fmt.Errorf("local: fetch rebased %s: %w: %s", branch, err, out)
	}
	if out, err := exec.Command("git", "-C", repoPath, "update-ref", integrationRef, "refs/heads/"+branch, oldTip).CombinedOutput(); err != nil {
		return fmt.Errorf("local: fast-forward %s: %w: %s", integrationBranch, err, out)
	}
	return nil
}

// landingRef resolves branch's tip in repoPath as "<branch>@<sha>", the
// immutable `landing:` reference ADR 0029/0033 expects once a merge has landed.
func landingRef(repoPath, branch string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "refs/heads/"+branch).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("local: resolve %s sha: %w: %s", branch, err, out)
	}
	return branch + "@" + strings.TrimSpace(string(out)), nil
}

// parseLandingRef splits landingRef's "<branch>@<sha>" back into its parts. ok
// is false for the raw agent-branch name settle records before a merge is
// attempted (gate.go's early recordLanding call), which has no "@", and for a
// sha starting with "-", which git could otherwise misread as an option.
func parseLandingRef(landing string) (branch, sha string, ok bool) {
	branch, sha, found := strings.Cut(landing, "@")
	if !found || branch == "" || sha == "" || strings.HasPrefix(sha, "-") {
		return "", "", false
	}
	return branch, sha, true
}

// branchTipSHA resolves branch's tip in repoPath. ok is false with a nil error
// when branch does not exist there, since `git rev-parse --verify --quiet`
// exits non-zero with no output for a missing ref. A failure that is not itself
// a verdict, such as git not running at all, returns a real error.
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
// integrationBranch's tip in repoPath, the no-network merge observation
// LandingContained relies on (ADR 0029, ADR 0033). It checks ancestry rather
// than tip equality because a sibling seam landing later moves the tip without
// un-merging it. An unknown or unmerged sha is false with a nil error.
func isMergedIntoIntegration(repoPath, sha, integrationBranch string) (bool, error) {
	// sha comes from a parsed landing ref, so "--" keeps git from reading a
	// leading "-" as an option.
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

// patchEquivalentToIntegration reports whether every commit reachable from sha
// is already present patch-for-patch on integrationBranch, the fallback for a
// rebased-and-landed sha whose replay gave every commit a new sha ancestry can
// never match (issue #1890). `git cherry` prefixes "+" to a commit with no
// equivalent upstream, so a single "+" means the seam has not landed.
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
