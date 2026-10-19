package forgejo

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
)

// Strategy is the forgejo backend's Go half (ADR 0068).
var Strategy = backendkit.Strategy{
	Descriptor:        backend.Forgejo,
	ValidateTracker:   validate("ISSUE_TRACKER"),
	ValidateCodeForge: validate("CODE_FORGE"),
	NewIssueTracker: func(s backendkit.Shared, l backendkit.Lookup) (forge.IssueTracker, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return nil, err
		}
		return NewForgejoClient(ForgejoConfig{
			BaseURL:       k.ForgejoBaseURL,
			Repo:          s.RepoSlug,
			Token:         k.ForgejoToken,
			Labels:        s.Labels,
			VerdictLabels: s.VerdictLabels,
		}), nil
	},
	NewCodeForge: func(s backendkit.Shared, l backendkit.Lookup, _ forge.SanitizedParent, it forge.IssueTracker) (forge.CodeForge, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return nil, err
		}
		return NewForgejoCodeForge(codeForgeConfig(s, k), it), nil
	},
	NewReadOnlyCodeForge: func(s backendkit.Shared, l backendkit.Lookup, _ forge.SanitizedParent, it forge.IssueTracker) (forge.CodeForge, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return nil, err
		}
		return NewReadOnlyForgejoCodeForge(codeForgeConfig(s, k), it), nil
	},
	NewLedger: func(s backendkit.Shared, l backendkit.Lookup) (backendkit.LedgerSite, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return backendkit.LedgerSite{}, err
		}
		return backendkit.LedgerSite{URL: GitRemoteURL(k.ForgejoBaseURL, s.RepoSlug, k.ForgejoToken)}, nil
	},
}

// validate checks the forgejo knobs for one axis (ISSUE_TRACKER or
// CODE_FORGE); the axes differ only in the error text.
func validate(axis string) func(backendkit.Shared, backendkit.Lookup) error {
	return func(_ backendkit.Shared, l backendkit.Lookup) error {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return err
		}
		return ValidateForgejoEnv(axis, k.ForgejoBaseURL, k.ForgejoToken)
	}
}

func codeForgeConfig(s backendkit.Shared, k Knobs) ForgejoCodeForgeConfig {
	return ForgejoCodeForgeConfig{
		BaseURL:      k.ForgejoBaseURL,
		Repo:         s.RepoSlug,
		Token:        k.ForgejoToken,
		BaseBranch:   s.BaseBranch,
		UserName:     s.GitUserName,
		UserEmail:    s.GitUserEmail,
		BranchPrefix: s.BranchPrefix,
		MergeMethod:  s.MergeMethod,
	}
}
