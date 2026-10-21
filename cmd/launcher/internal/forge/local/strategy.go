package local

import (
	"fmt"
	"path/filepath"

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
		k, err := ReadKnobs(l)
		if err != nil {
			return nil, err
		}
		return NewLocalTracker(k.LocalIssuesDir, s.Labels, s.VerdictLabels), nil
	},
	NewCodeForge: func(s backendkit.Shared, l backendkit.Lookup, parent forge.SanitizedParent, _ forge.IssueTracker) (forge.CodeForge, error) {
		dir, err := resolveAccumulationRepoDir(l)
		if err != nil {
			return nil, err
		}
		return NewLocalCodeForge(dir, s.BaseBranch, parent, s.GitUserName, s.GitUserEmail, s.BranchPrefix), nil
	},
	NewLedger: func(_ backendkit.Shared, l backendkit.Lookup) (backendkit.LedgerSite, error) {
		dir, err := resolveAccumulationRepoDir(l)
		if err != nil {
			return backendkit.LedgerSite{}, err
		}
		return backendkit.LedgerSite{Repo: dir}, nil
	},
	CloneURL: func(_ backendkit.Shared, _ backendkit.Lookup, repoMountDir string) (string, error) {
		return repoMountDir, nil
	},
	HostRepoDir: func(_ backendkit.Shared, l backendkit.Lookup) (string, error) {
		return resolveAccumulationRepoDir(l)
	},
}

// resolveAccumulationRepoDir resolves the Accumulation repo dir (ADR 0033) to
// an absolute host path, defaulting an unset knob to .spindrift/accum.git under
// the process cwd (issue #1726). The read-only /repo Box mount and the landing
// forge's git subprocesses both need the same path, so every consumer goes
// through here.
func resolveAccumulationRepoDir(l backendkit.Lookup) (string, error) {
	k, err := ReadKnobs(l)
	if err != nil {
		return "", err
	}
	dir := k.CodeForgeAccumulationRepoDir
	if dir == "" {
		dir = filepath.Join(".spindrift", "accum.git")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir, nil
	}
	return abs, nil
}
