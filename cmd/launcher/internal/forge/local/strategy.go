package local

import (
	"fmt"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
)

// Strategy is the local backend's Go half (ADR 0068).
var Strategy = backendkit.Strategy{
	Descriptor: backend.Local,
	LandedBanner: func(backendkit.Shared) string {
		return "seams landed host-side into their own Integration branches in the Accumulation repo"
	},
	ValidateCodeForge: func(s backendkit.Shared, _ backendkit.Lookup) error {
		if s.MergeMode != "immediate" {
			return fmt.Errorf(
				"CODE_FORGE=local requires MERGE_MODE=immediate (got %q) — "+
					"only immediate relays the seam bundle into the Accumulation "+
					"repo; manual/auto strand it in the outbox", s.MergeMode)
		}
		return nil
	},
	NewIssueTracker: func(s backendkit.Shared, l backendkit.Lookup) (forge.IssueTracker, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return nil, err
		}
		return NewLocalTracker(k.LocalIssuesDir, s.Labels, s.VerdictLabels), nil
	},
	NewCodeForge: func(s backendkit.Shared, l backendkit.Lookup, parent forge.SanitizedParent, _ forge.IssueTracker) (forge.CodeForge, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return nil, err
		}
		return NewLocalCodeForge(k.CodeForgeAccumulationRepoDir, s.BaseBranch, parent, s.GitUserName, s.GitUserEmail, s.BranchPrefix), nil
	},
	NewLedger: func(_ backendkit.Shared, l backendkit.Lookup) (backendkit.LedgerSite, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return backendkit.LedgerSite{}, err
		}
		return backendkit.LedgerSite{Repo: k.CodeForgeAccumulationRepoDir}, nil
	},
}
