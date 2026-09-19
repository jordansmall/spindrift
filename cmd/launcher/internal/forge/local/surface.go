package local

import (
	"fmt"
	"os/exec"
	"strings"
)

// NeverLandedSkip is the skipped reason SurfaceIntegrationBranch reports when
// parent's Integration branch does not exist yet in the Accumulation repo.
// Callers tell this permanent, expected reason apart from the transient
// checked-out and diverged ones by comparing against it (issue #1739).
func NeverLandedSkip(parent SanitizedParent) string {
	return "no seam of " + parent.String() + " has landed yet"
}

// SurfaceIntegrationBranch fetches parent's Integration branch from the Accumulation
// repo at repoPath into pwd as branch branchName (ADR 0033, issue #1730), without
// switching pwd's branch or touching origin. branchName is independent of parent
// (issue #1811). A never-landed parent and the two ways this could clobber the
// operator's work report through skipped, so a missing surface never blocks reconcile.
func SurfaceIntegrationBranch(repoPath, pwd string, parent SanitizedParent, branchName string) (surfaced bool, skipped string, err error) {
	integrationBranch := IntegrationBranch(parent)
	if exists := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+integrationBranch).Run() == nil; !exists {
		return false, NeverLandedSkip(parent), nil
	}

	current, err := exec.Command("git", "-C", pwd, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		return false, "", fmt.Errorf("local: surface %s: current branch: %w: %s", branchName, err, current)
	}
	if strings.TrimSpace(string(current)) == branchName {
		return false, branchName + " is currently checked out", nil
	}

	// Output, not CombinedOutput: rev-parse's stderr for a not-yet-existing
	// branch would otherwise land in before and make the before != after
	// comparison work only by coincidence.
	before, _ := exec.Command("git", "-C", pwd, "rev-parse", "refs/heads/"+branchName).Output()

	refspec := "refs/heads/" + integrationBranch + ":refs/heads/" + branchName
	if out, err := exec.Command("git", "-C", pwd, "fetch", repoPath, refspec).CombinedOutput(); err != nil {
		// A non-fast-forward rejection means the operator's local branch has commits
		// of its own, so it is a skip. Anything else (a corrupt repoPath, or the
		// Integration branch vanishing in the TOCTOU window after the check above) is
		// a real failure the caller should see. git reports the rejection only in that
		// text, which is stable across git versions unlike the exit code.
		if strings.Contains(string(out), "non-fast-forward") {
			return false, "local branch " + branchName + " has diverged from " + integrationBranch, nil
		}
		return false, "", fmt.Errorf("local: surface %s: fetch %s: %w: %s", branchName, integrationBranch, err, out)
	}

	after, err := exec.Command("git", "-C", pwd, "rev-parse", "refs/heads/"+branchName).Output()
	if err != nil {
		return false, "", fmt.Errorf("local: surface %s: resolve surfaced branch: %w", branchName, err)
	}
	return string(before) != string(after), "", nil
}
