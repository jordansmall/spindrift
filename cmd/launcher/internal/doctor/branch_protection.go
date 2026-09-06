package doctor

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

// BranchProtectionCheckName is BranchProtectionCheck's Name field, exported
// for the same reason RuntimeCheckName is: a caller matching on the row
// (e.g. to filter it out of a larger slice) does so against this constant
// rather than the bare string literal "branch-protection".
const BranchProtectionCheckName = "branch-protection"

// BranchProtectionCheck builds the "branch-protection" Check row (issue
// #2570): it probes the code forge's base branch for protection and reports
// severity by merge policy.
//
// Tier is Required unless mergePolicy is exactly "manual", in which case it
// is Advisory. Any other value -- "immediate", "auto", or an unrecognized/
// empty string -- is treated as Required: under immediate/auto there is no
// human merge gate, so an unprotected base branch is unsafe to deploy
// (README/SECURITY); an unrecognized or unset merge policy fails closed
// toward the safer, more protective tier rather than silently going
// advisory, since a typo'd MERGE_MODE value is far more likely to mean "not
// actually configured as manual" than "operator deliberately relaxed this
// check".
//
// Probe reads caps.BranchProtectionForge (issue #2946), the code forge's
// resolved typed handle for the optional branch-protection interface, rather
// than asserting cf itself. A forge with no branch-protection API (push-only
// git, CODE_FORGE=local) resolves that handle nil — the not-applicable case:
// Probe succeeds unconditionally and SuccessMsg reports "not applicable"
// instead of a real protection state. A forge that does implement the
// interface is queried via BranchProtected(baseBranch):
//   - a non-nil error means the probe itself could not determine the
//     answer (e.g. a permission error) -- this is wrapped with ErrDegraded so
//     a Required-tier row still doesn't block Run (AC3: a probe failure must
//     never present as a false required failure), while still being reported
//     (advisory line + Remedy) for visibility;
//   - protected == false is a definitive, non-degraded failure;
//   - protected == true is success.
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
			if bp == nil {
				successMsg = "not applicable (code forge has no branch-protection API)"
				return nil, nil
			}
			protected, err := bp.BranchProtected(baseBranch)
			if err != nil {
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
