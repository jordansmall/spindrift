package doctor

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

// BranchProtectionCheckName is the Name of the branch-protection Check row, so
// callers match on the constant rather than the bare string literal.
const BranchProtectionCheckName = "branch-protection"

// BranchProtectionCheck builds the "branch-protection" Check row (issue #2570).
// Only mergePolicy "manual" is Advisory; every other value, including an
// unrecognized or empty one, is Required, because immediate and auto merging
// have no human gate and a typo'd MERGE_MODE more likely means "not configured
// as manual" than a deliberate relaxation.
func BranchProtectionCheck(caps forge.Capabilities, mergePolicy, baseBranch string) Check {
	tier := Required
	if mergePolicy == "manual" {
		tier = Advisory
	}

	var successMsg string
	return Check{
		Name: BranchProtectionCheckName,
		Tier: tier,
		Remedy: fmt.Sprintf(
			"protect %s: block direct pushes and require CI status checks (see README.md/SECURITY.md — running without branch protection is not safe to deploy)",
			baseBranch,
		),
		Probe: func() (any, error) {
			bp := caps.BranchProtectionForge
			// A forge with no branch-protection API (push-only git,
			// CODE_FORGE=local) resolves this handle nil (issue #2946).
			if bp == nil {
				successMsg = "not applicable (code forge has no branch-protection API)"
				return nil, nil
			}
			protected, err := bp.BranchProtected(baseBranch)
			if err != nil {
				// ErrDegraded stops an indeterminate probe (e.g. a permission
				// error) from blocking Run as a false Required failure (AC3).
				return nil, fmt.Errorf("branch protection probe for %q failed: %w: %w", baseBranch, err, ErrDegraded)
			}
			if !protected {
				return nil, fmt.Errorf("base branch %q is not protected", baseBranch)
			}
			successMsg = fmt.Sprintf("base branch %q is protected", baseBranch)
			return nil, nil
		},
		SuccessMsg: func(output any) string {
			return successMsg
		},
	}
}
