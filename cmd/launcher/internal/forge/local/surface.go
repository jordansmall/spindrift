package local

import (
	"fmt"
	"os/exec"
	"strings"
)

// NeverLandedSkip is the skipped reason SurfaceIntegrationBranch reports
// when parent's Integration branch doesn't exist yet in the Accumulation
// repo — exported so callers that need to tell this permanent, expected
// reason apart from the transient checked-out/diverged ones (issue #1739)
// don't reconstruct the literal string themselves.
func NeverLandedSkip(parent SanitizedParent) string {
	return "no seam of " + parent.String() + " has landed yet"
}

// SurfaceIntegrationBranch fetches parent's Integration branch from the
// Accumulation repo at repoPath into pwd as a local branch named
// branchName — CODE_FORGE=local's auto-surface exit (ADR 0033, issue
// #1730), the counterpart to Merge's landing: it makes a completed broad
// ticket's assembled work visible in the operator's checkout without ever
// switching pwd off its current branch or touching origin. branchName is
// independent of parent (the Integration-branch key, issue #1811): equal to
// it for a parented ticket, but a parentless ticket's own sanitized title
// when the caller wants the human-facing branch decoupled from the stable
// slug identity. Idempotent: fetching an already-current branch is a no-op
// (surfaced=false), and fetching a fast-forwardable one advances it
// (surfaced=true).
//
// Two ways an operator's own work could be clobbered are refused rather than
// forced, both reported through skipped instead of an error — a missing
// surface never blocks reconcile, and the operator can resolve either by
// hand and let a later run pick it back up: branchName is currently checked
// out in pwd (mirrors console_freshness.go's checkCheckoutSafe), or the
// local branch has diverged from the Integration branch (non-fast-forward).
// The Integration branch not existing yet in repoPath (no seam of parent has
// landed) is also reported through skipped, not an error.
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

	// Output (not CombinedOutput): before/after must reflect stdout alone —
	// rev-parse's failure text on stderr for a not-yet-existing branch would
	// otherwise land in before and make the before != after comparison work
	// only by coincidence.
	before, _ := exec.Command("git", "-C", pwd, "rev-parse", "refs/heads/"+branchName).Output()

	refspec := "refs/heads/" + integrationBranch + ":refs/heads/" + branchName
	if out, err := exec.Command("git", "-C", pwd, "fetch", repoPath, refspec).CombinedOutput(); err != nil {
		// A non-fast-forward rejection is the expected shape of "the
		// operator's local branch has commits of its own" and is reported
		// through skipped, not an error. Anything else (a corrupt repoPath,
		// or the Integration branch vanishing in the TOCTOU window after the
		// existence check above) is a genuine failure the caller should see,
		// not a silently swallowed skip. Detected by matching git's own
		// "(non-fast-forward)" rejection text (stable across git versions,
		// unlike relying on a specific exit code) rather than a structured
		// signal — git's fetch has no other way to report it.
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
