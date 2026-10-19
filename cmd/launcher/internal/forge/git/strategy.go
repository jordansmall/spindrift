package git

import (
	"fmt"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
)

// Strategy is the git backend's Go half (ADR 0068).
var Strategy = backendkit.Strategy{
	Descriptor: backend.Git,
	LandedBanner: func(s backendkit.Shared) string {
		return "branches pushed on " + s.RepoSlug
	},
	ValidateCodeForge: func(_ backendkit.Shared, l backendkit.Lookup) error {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return err
		}
		if k.CodeForgeRemoteURL == "" {
			return fmt.Errorf("set CODE_FORGE_REMOTE_URL (the plain git remote to clone from and push to) when CODE_FORGE=git")
		}
		return nil
	},
	NewCodeForge: func(s backendkit.Shared, l backendkit.Lookup, _ forge.SanitizedParent, _ forge.IssueTracker) (forge.CodeForge, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return nil, err
		}
		return NewGitClient(k.CodeForgeRemoteURL, s.BaseBranch, s.GitUserName, s.GitUserEmail, s.BranchPrefix), nil
	},
}
