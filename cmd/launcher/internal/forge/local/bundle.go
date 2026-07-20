package local

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BundleFileName is the fixed name the Box writes its code-out bundle under
// in the writable outbox mount (ADR 0033) — a single well-known name, since
// the outbox holds exactly one seam's bundle per dispatch.
const BundleFileName = "seam.bundle"

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
	bundlePath := filepath.Join(outboxDir, BundleFileName)
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
